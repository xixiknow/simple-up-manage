package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/middleware"
	"simple-up-manage/internal/ops"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/upstream"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var hopByHop = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"host":                {},
	"content-length":      {},
}

const (
	proxyOverallTimeout = 300 * time.Second
	proxyFirstTokenWait = 30 * time.Second
)

type Gateway struct {
	DB     *gorm.DB
	Enc    *crypto.AESGCM
	Ops    *ops.Service
	Picker picker.Picker
	Client *upstream.Client
	rpm    *rpmLimiter
	conc   *concLimiter
}

func NewGateway(db *gorm.DB, enc *crypto.AESGCM, opsSvc *ops.Service, p picker.Picker) *Gateway {
	return &Gateway{
		DB:     db,
		Enc:    enc,
		Ops:    opsSvc,
		Picker: p,
		Client: upstream.NewStreamingClient(),
		rpm:    newRPMLimiter(),
		conc:   newConcLimiter(),
	}
}

type consumerCtx struct {
	Key *domain.ConsumerKey
}

func (h *Gateway) authConsumer(c *gin.Context, enforceLimits bool) (*domain.ConsumerKey, bool) {
	raw := middleware.ExtractAPIKey(c)
	if raw == "" {
		gatewayError(c, http.StatusUnauthorized, "authentication_error", "Missing API key")
		return nil, false
	}
	var k domain.ConsumerKey
	if err := h.DB.Where("key = ?", raw).First(&k).Error; err != nil {
		gatewayError(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return nil, false
	}
	if k.Status != domain.StatusEnabled {
		gatewayError(c, http.StatusForbidden, "permission_error", "API key disabled")
		return nil, false
	}
	if enforceLimits {
		if k.QuotaUSD > 0 && k.QuotaUsed >= k.QuotaUSD {
			gatewayError(c, http.StatusTooManyRequests, "rate_limit_error", "Quota exhausted")
			return nil, false
		}
		if k.RPM > 0 && !h.rpm.Allow(k.ID, k.RPM) {
			gatewayError(c, http.StatusTooManyRequests, "rate_limit_error", "RPM exceeded")
			return nil, false
		}
	}
	return &k, true
}

func (h *Gateway) Messages(c *gin.Context) {
	h.proxy(c, domain.ProtocolAnthropic)
}

func (h *Gateway) ChatCompletions(c *gin.Context) {
	h.proxy(c, domain.ProtocolOpenAI)
}

func (h *Gateway) Responses(c *gin.Context) {
	h.proxy(c, domain.ProtocolOpenAI)
}

// Images serves /v1/images/{generations,edits,variations}. Generations is JSON;
// edits and variations are multipart/form-data with image uploads.
func (h *Gateway) Images(c *gin.Context) {
	h.proxy(c, domain.ProtocolOpenAI)
}

// maxImageRequestBytes bounds multipart uploads on image endpoints. Regular JSON
// endpoints keep the previous unbounded behaviour.
const maxImageRequestBytes = 64 << 20

func isImagePath(path string) bool {
	return strings.HasPrefix(path, "/v1/images/")
}

func (h *Gateway) proxy(c *gin.Context, protocol string) {
	reqStart := time.Now()
	ck, ok := h.authConsumer(c, true)
	if !ok {
		return
	}
	reader := c.Request.Body
	if isImagePath(c.Request.URL.Path) {
		reader = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageRequestBytes)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			gatewayError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
			return
		}
		gatewayError(c, http.StatusBadRequest, "invalid_request_error", "failed to read body")
		return
	}
	model := peekModelFrom(c.GetHeader("Content-Type"), body)
	session := picker.SessionFromRequest(c.GetHeader("X-Session-Id"), body)
	reqID := strings.TrimSpace(c.GetHeader("X-Request-Id"))
	if reqID == "" {
		reqID = uuid.NewString()
	}
	reqSnap := captureInbound(c.Request, body)
	reqSnap.StartedAt = reqStart
	path := c.Request.URL.Path
	clientIP := requestClientIP(c)
	lg := h.beginLog(ck, protocol, model, path, reqID, clientIP, reqSnap)
	lastMsg := "no enabled upstream for protocol"
	var settled bool
	defer func() {
		if settled {
			return
		}
		msg := lastMsg
		if msg == "" {
			msg = "request ended"
		}
		h.finishLog(lg, ck, nil, nil, protocol, model, path, reqID, clientIP, c.Writer.Status(), false, upstream.TokenUsage{}, 0, int(time.Since(reqStart).Milliseconds()), msg, reqSnap)
	}()

	attempts := 2
	retries := 1
	if h.Picker != nil {
		s := h.Picker.Settings()
		if n := s.FailoverMax; n > 0 {
			attempts = n
		}
		if s.RetryMax >= 0 {
			retries = s.RetryMax
		}
	}
	allow, drift, bound, err := ops.ResolveAllowKeys(c.Request.Context(), h.DB, ck.ID, protocol, model)
	if err != nil {
		lastMsg = err.Error()
		gatewayError(c, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	if bound && len(allow) == 0 {
		lastMsg = "route group has no available key for this protocol/model"
		if len(drift) > 0 {
			lastMsg = "route group members' rate multipliers are all outside the group's rate range"
		}
		gatewayError(c, http.StatusServiceUnavailable, "api_error", lastMsg)
		return
	}
	if !bound {
		allow, drift = nil, nil
	}

	var exclude, excludeProviders, excludeKeyModels []uint
	for attempt := 0; attempt < attempts; attempt++ {
		pk, up, err := h.Picker.Pick(c.Request.Context(), picker.Request{
			Protocol:         protocol,
			Model:            model,
			Session:          session,
			Exclude:          exclude,
			ExcludeProviders: excludeProviders,
			ExcludeKeyModels: excludeKeyModels,
			AllowKeys:        allow,
			DriftKeys:        drift,
		})
		if err != nil {
			if errors.Is(err, picker.ErrNoUpstream) {
				if attempt == 0 {
					lastMsg = "no enabled upstream for protocol"
					if bound {
						lastMsg = "no healthy key in bound route groups"
					}
					gatewayError(c, http.StatusServiceUnavailable, "api_error", lastMsg)
					return
				}
				gatewayError(c, http.StatusBadGateway, "api_error", lastMsg)
				return
			}
			lastMsg = err.Error()
			gatewayError(c, http.StatusInternalServerError, "api_error", err.Error())
			return
		}
		var outcome forwardOutcome
		for r := 0; r <= retries; r++ {
			outcome = h.forwardOnce(c, ck, pk, up, protocol, model, session, reqID, body, reqSnap, lg, reqStart)
			if outcome.ok {
				settled = true
				return
			}
			lastMsg = outcome.msg
			if outcome.retrySame && r < retries {
				if err := sleepCtx(c.Request.Context(), sameKeyRetryDelay); err != nil {
					if !c.Writer.Written() {
						gatewayError(c, http.StatusBadGateway, "api_error", lastMsg)
					}
					return
				}
				continue
			}
			break
		}
		if outcome.failOver && !outcome.capacityBusy {
			h.applyFailure(c.Request.Context(), pk, up, model, outcome)
		}
		switch outcome.scope {
		case failureScopeProvider:
			excludeProviders = append(excludeProviders, up.ID)
		case failureScopeKeyModel:
			excludeKeyModels = append(excludeKeyModels, pk.ID)
		case failureScopeKey:
			exclude = append(exclude, pk.ID)
		}
		if !outcome.failOver {
			if !c.Writer.Written() {
				gatewayError(c, http.StatusBadGateway, "api_error", lastMsg)
			}
			return
		}
	}
	if !c.Writer.Written() {
		gatewayError(c, http.StatusBadGateway, "api_error", lastMsg)
	}
}

const sameKeyRetryDelay = 250 * time.Millisecond

type forwardOutcome struct {
	ok           bool
	failOver     bool
	retrySame    bool
	lowBalance   bool
	capacityBusy bool
	status       int
	scope        string
	action       string
	msg          string
}

const (
	failureScopeKey      = "key"
	failureScopeKeyModel = "key_model"
	failureScopeProvider = "provider"
)

func (h *Gateway) applyFailure(ctx context.Context, key *domain.PlatformKey, up *domain.Upstream, model string, outcome forwardOutcome) {
	if h.Picker == nil || key == nil || up == nil {
		return
	}
	if outcome.lowBalance {
		h.Picker.MarkLowBalance(ctx, key.ID)
		return
	}
	runtime, ok := h.Picker.(picker.RuntimeController)
	if !ok {
		if outcome.scope == failureScopeKey {
			h.Picker.Cooldown(ctx, key.ID)
		}
		return
	}
	switch outcome.scope {
	case failureScopeProvider:
		runtime.CooldownProvider(ctx, up.ID, outcome.msg)
	case failureScopeKeyModel:
		runtime.CooldownKeyModel(ctx, key.ID, model, outcome.msg, outcome.status)
	case failureScopeKey:
		h.Picker.Cooldown(ctx, key.ID)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h *Gateway) forwardOnce(c *gin.Context, ck *domain.ConsumerKey, pk *domain.PlatformKey, up *domain.Upstream, protocol, model, session, reqID string, body []byte, reqSnap ioCapture, lg *liveLog, reqStart time.Time) forwardOutcome {
	if runtime, ok := h.Picker.(picker.RuntimeController); ok {
		acquired, scope := runtime.TryAcquire(pk, up)
		if !acquired {
			msg := "key capacity exceeded"
			if scope == failureScopeProvider {
				msg = "upstream concurrency exceeded"
			}
			outcome := forwardOutcome{failOver: true, capacityBusy: true, scope: scope, action: "exclude_busy_resource", msg: msg}
			lg.markFailure(h, outcome.scope, outcome.action)
			return outcome
		}
		defer runtime.Release(pk, up)
	} else if up != nil {
		if !h.conc.Acquire(up.ID, up.Concurrency) {
			outcome := forwardOutcome{failOver: true, capacityBusy: true, scope: failureScopeProvider, action: "exclude_busy_resource", msg: "upstream concurrency exceeded"}
			lg.markFailure(h, outcome.scope, outcome.action)
			return outcome
		}
		defer h.conc.Release(up.ID, up.Concurrency)
	}
	lg.attachRoute(h, pk, up)

	apiKey, err := h.Enc.Decrypt(pk.EncryptedKey)
	if err != nil {
		return forwardOutcome{msg: "failed to decrypt platform key"}
	}
	started := reqStart
	if started.IsZero() {
		started = time.Now()
	}
	deadline := started.Add(proxyOverallTimeout)
	attemptCtx, cancelAttempt := context.WithDeadline(c.Request.Context(), deadline)
	defer cancelAttempt()

	target := upstream.JoinEndpoint(up.BaseURL, c.Request.URL.Path)
	if c.Request.URL.RawQuery != "" {
		target += "?" + c.Request.URL.RawQuery
	}
	upReq, err := http.NewRequestWithContext(attemptCtx, c.Request.Method, target, bytes.NewReader(body))
	if err != nil {
		return forwardOutcome{msg: err.Error()}
	}
	copyForwardHeaders(c.Request.Header, upReq.Header)
	upReq.Header.Set("Authorization", "Bearer "+apiKey)
	if protocol == domain.ProtocolAnthropic {
		if upReq.Header.Get("x-api-key") == "" {
			upReq.Header.Set("x-api-key", apiKey)
		}
		if upReq.Header.Get("anthropic-version") == "" && c.GetHeader("anthropic-version") == "" {
			upReq.Header.Set("anthropic-version", "2023-06-01")
		}
	}

	path := c.Request.URL.Path
	clientIP := requestClientIP(c)
	wantStream := reqSnap.ReqStream
	firstWatch := &firstTokenWatch{}
	if wantStream {
		firstWatch.start(cancelAttempt, proxyFirstTokenWait, deadline)
	}
	defer firstWatch.stop()

	resp, err := h.Client.DoRaw(upReq)
	if err != nil {
		dur := int(time.Since(started).Milliseconds())
		msg := proxyTimeoutMessage(err, firstWatch.timedOut())
		h.observeAttempt(pk, model, false, upstream.TokenUsage{}, 0)
		h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, 0, false, upstream.TokenUsage{}, 0, dur, msg, reqSnap, true, true)
		outcome := forwardOutcome{failOver: true, retrySame: !firstWatch.timedOut() && !isTimeoutErr(err), scope: failureScopeProvider, action: "cooldown_provider", msg: msg}
		lg.markFailure(h, outcome.scope, outcome.action)
		return outcome
	}

	if failure := classifyHTTPFailure(resp.StatusCode, nil); failure.failOver && resp.StatusCode != http.StatusForbidden && !c.Writer.Written() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, maxLogBodyBytes+1))
		_ = resp.Body.Close()
		failure = classifyHTTPFailure(resp.StatusCode, peek)
		snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), peek, len(peek))
		h.observeAttempt(pk, model, false, upstream.TokenUsage{}, 0)
		h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, 0, int(time.Since(started).Milliseconds()), http.StatusText(resp.StatusCode), snap, true, true)
		failure.msg = http.StatusText(resp.StatusCode)
		lg.markFailure(h, failure.scope, failure.action)
		return failure
	}

	// new-api / one-api report an exhausted token as 403 with a quota error
	// code instead of 402. Treat it as low balance and try the next key.
	if resp.StatusCode == http.StatusForbidden && !c.Writer.Written() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if isQuotaExhaustedBody(peek) {
			_ = resp.Body.Close()
			msg := "upstream quota exhausted"
			snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), peek, len(peek))
			h.observeAttempt(pk, model, false, upstream.TokenUsage{}, 0)
			h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, 0, int(time.Since(started).Milliseconds()), msg, snap, true, true)
			outcome := forwardOutcome{failOver: true, lowBalance: true, status: resp.StatusCode, scope: failureScopeProvider, action: "mark_low_balance", msg: msg}
			lg.markFailure(h, outcome.scope, outcome.action)
			return outcome
		}
		_ = resp.Body.Close()
		msg := http.StatusText(resp.StatusCode)
		snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), peek, len(peek))
		h.observeAttempt(pk, model, false, upstream.TokenUsage{}, 0)
		h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, 0, int(time.Since(started).Milliseconds()), msg, snap, true, true)
		outcome := forwardOutcome{failOver: true, status: resp.StatusCode, scope: failureScopeKey, action: "cooldown_key", msg: msg}
		lg.markFailure(h, outcome.scope, outcome.action)
		return outcome
	}

	defer resp.Body.Close()

	collector := &streamCollector{start: started, onProgress: func(ttft, dur int, usage upstream.TokenUsage) {
		h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, usage, ttft, dur, "", ioCapture{}, true, false)
	}}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	streaming := isSSEContentType(ct)
	if streaming {
		elapsed := time.Since(started)
		if !firstWatch.running() {
			remain := proxyFirstTokenWait - elapsed
			firstWatch.start(cancelAttempt, remain, deadline)
		}
	} else {
		firstWatch.stop()
	}

	var committed bool
	commitHeaders := func() {
		if committed || c.Writer.Written() {
			return
		}
		committed = true
		firstWatch.stop()
		for k, vs := range resp.Header {
			if _, skip := hopByHop[strings.ToLower(k)]; skip {
				continue
			}
			for _, v := range vs {
				c.Writer.Header().Add(k, v)
			}
		}
		c.Writer.Header().Set("X-Request-Id", reqID)
		c.Status(resp.StatusCode)
	}

	var respPrefix []byte
	var respTotal int
	if streaming {
		err = copySSE(c.Writer, resp.Body, collector, true, commitHeaders)
		respPrefix = collector.prefix
		respTotal = collector.total
		if err != nil && !c.Writer.Written() {
			dur := int(time.Since(started).Milliseconds())
			msg := proxyTimeoutMessage(err, firstWatch.timedOut())
			snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), respPrefix, respTotal)
			h.observeAttempt(pk, model, false, upstream.TokenUsage{}, 0)
			h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, collector.ttftMs, dur, msg, snap, true, true)
			outcome := forwardOutcome{failOver: true, scope: failureScopeProvider, action: "cooldown_provider", msg: msg}
			lg.markFailure(h, outcome.scope, outcome.action)
			return outcome
		}
	} else {
		commitHeaders()
		// Stream the body through untouched (image responses with b64_json can be
		// tens of MB) while capturing a bounded prefix for usage parsing.
		capture := &usageCapture{limit: 16 << 20, onBytes: collector.noteBytes}
		_, copyErr := io.Copy(c.Writer, io.TeeReader(resp.Body, capture))
		respPrefix = capture.Prefix()
		respTotal = capture.Total()
		if copyErr != nil {
			err = copyErr
		} else if raw := capture.Bytes(); len(raw) > 0 {
			upstream.MergeUsage(&collector.usage, upstream.ParseUsageJSON(raw))
		}
		if f, ok := c.Writer.(http.Flusher); ok {
			f.Flush()
		}
		if err != nil && !c.Writer.Written() {
			dur := int(time.Since(started).Milliseconds())
			msg := proxyTimeoutMessage(err, false)
			snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), respPrefix, respTotal)
			h.observeAttempt(pk, model, false, upstream.TokenUsage{}, 0)
			h.patchLog(lg, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, 0, dur, msg, snap, true, true)
			outcome := forwardOutcome{failOver: true, retrySame: !isTimeoutErr(err), scope: failureScopeProvider, action: "cooldown_provider", msg: msg}
			lg.markFailure(h, outcome.scope, outcome.action)
			return outcome
		}
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 400 && err == nil
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	} else if !success {
		errMsg = http.StatusText(resp.StatusCode)
	}
	dur := int(time.Since(started).Milliseconds())
	snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), respPrefix, respTotal)
	if collector.usage.CostUSD == nil {
		collector.usage.CostUSD = estimateRequestCost(h.DB, model, pk, collector.usage)
	}
	if success {
		h.observeAttempt(pk, model, true, collector.usage, collector.ttftMs)
	}
	h.finishLog(lg, ck, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, success, collector.usage, collector.ttftMs, dur, errMsg, snap)
	if success {
		if runtime, ok := h.Picker.(picker.RuntimeController); ok {
			runtime.RecordProviderSuccess(c.Request.Context(), up.ID)
		}
		h.Picker.SetSticky(c.Request.Context(), protocol, session, pk.ID)
		if collector.usage.CostUSD != nil {
			h.addQuotaUsed(ck.ID, *collector.usage.CostUSD)
		}
	}
	return forwardOutcome{ok: true, msg: errMsg}
}

// quotaErrorCodes are error codes new-api / one-api / OpenAI use when the
// caller's quota (not a rate limit) is exhausted.
var quotaErrorCodes = []string{
	"insufficient_user_quota",
	"pre_consume_token_quota_failed",
	"insufficient_quota",
}

func isQuotaExhaustedBody(body []byte) bool {
	var top struct {
		Error struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return false
	}
	code, _ := top.Error.Code.(string)
	for _, want := range quotaErrorCodes {
		if code == want || top.Error.Type == want {
			return true
		}
	}
	return false
}

func classifyHTTPFailure(code int, body []byte) forwardOutcome {
	switch {
	case code == http.StatusPaymentRequired || code == http.StatusForbidden && isQuotaExhaustedBody(body):
		return forwardOutcome{failOver: true, lowBalance: true, status: code, scope: failureScopeProvider, action: "mark_low_balance"}
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return forwardOutcome{failOver: true, status: code, scope: failureScopeKey, action: "cooldown_key"}
	case code == http.StatusTooManyRequests:
		return forwardOutcome{failOver: true, status: code, scope: failureScopeKeyModel, action: "cooldown_key_model"}
	case code == http.StatusNotFound || code == 529 || code >= 500:
		return forwardOutcome{failOver: true, retrySame: true, status: code, scope: failureScopeProvider, action: "cooldown_provider"}
	default:
		return forwardOutcome{status: code}
	}
}

func shouldFailoverStatus(code int) bool {
	return classifyHTTPFailure(code, nil).failOver
}

func (h *Gateway) Models(c *gin.Context) {
	ck, ok := h.authConsumer(c, false)
	if !ok {
		return
	}
	var groups []domain.RouteGroup
	var links []domain.ConsumerRouteGroup
	_ = h.DB.Where("consumer_key_id = ?", ck.ID).Find(&links).Error
	bound := len(links) > 0
	memberOf := map[uint][]uint{}
	if bound {
		ids := make([]uint, 0, len(links))
		for _, l := range links {
			ids = append(ids, l.RouteGroupID)
		}
		_ = h.DB.Where("id IN ? AND status = ?", ids, domain.StatusEnabled).Find(&groups).Error
		var members []domain.RouteGroupKey
		_ = h.DB.Where("route_group_id IN ?", ids).Find(&members).Error
		for _, m := range members {
			memberOf[m.PlatformKeyID] = append(memberOf[m.PlatformKeyID], m.RouteGroupID)
		}
	}

	var keys []domain.PlatformKey
	_ = h.DB.Preload("Upstream").Where("status = ?", domain.StatusEnabled).Find(&keys).Error
	ids := ops.ListVisibleModels(bound, groups, keys, memberOf)
	type modelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	data := make([]modelItem, 0, len(ids))
	for _, id := range ids {
		data = append(data, modelItem{ID: id, Object: "model", OwnedBy: "simple-up-manage"})
	}
	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}

func (h *Gateway) Usage(c *gin.Context) {
	ck, ok := h.authConsumer(c, false)
	if !ok {
		return
	}
	if ck.QuotaUSD > 0 {
		remaining := ck.QuotaUSD - ck.QuotaUsed
		if remaining < 0 {
			remaining = 0
		}
		c.JSON(http.StatusOK, gin.H{
			"mode":    "quota_limited",
			"isValid": true,
			"status":  ck.Status,
			"quota": gin.H{
				"limit":     ck.QuotaUSD,
				"used":      ck.QuotaUsed,
				"remaining": remaining,
				"unit":      "USD",
			},
			"remaining": remaining,
			"unit":      "USD",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"mode":      "unrestricted",
		"isValid":   true,
		"status":    ck.Status,
		"remaining": nil,
		"unit":      "USD",
	})
}

func (h *Gateway) observeAttempt(pk *domain.PlatformKey, model string, success bool, usage upstream.TokenUsage, ttft int) {
	if pk == nil {
		return
	}
	if h.Picker != nil {
		h.Picker.Observe(context.Background(), pk.ID, model, success, usage.InputTokens, usage.CacheReadTokens, usage.CacheCreationTokens, ttft)
	}
	if h.Ops != nil && success {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			h.Ops.ObserveRequest(ctx, pk.ID, success, 0, "")
		}()
	}
}

type liveLog struct {
	id        uint
	mu        sync.Mutex
	lastFlush time.Time
	ttftSent  bool
	revision  uint64
	closed    bool
	updates   map[string]any
}

func (lg *liveLog) markFailure(h *Gateway, scope, action string) {
	if lg == nil || lg.id == 0 || (scope == "" && action == "") {
		return
	}
	h.writeLog(lg, map[string]any{
		"failure_scope":  scope,
		"failure_action": action,
	}, false)
}

func (h *Gateway) beginLog(ck *domain.ConsumerKey, protocol, model, path, reqID, clientIP string, snap ioCapture) *liveLog {
	row := domain.RequestLog{
		RequestID:        reqID,
		Protocol:         protocol,
		Model:            model,
		Path:             path,
		ClientIP:         clientIP,
		InFlight:         true,
		Stream:           snap.ReqStream,
		StreamKnown:      snap.StreamKnown,
		CreatedAt:        snap.StartedAt,
		RequestHeaders:   snap.ReqHeaders,
		RequestBody:      snap.ReqBody,
		RequestBodyTrunc: snap.ReqTrunc,
	}
	if ck != nil {
		id := ck.ID
		row.ConsumerKeyID = &id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.DB.WithContext(ctx).Create(&row).Error; err != nil {
		slog.Error("create request log", "request_id", reqID, "error", err)
		return nil
	}
	return &liveLog{id: row.ID}
}

func (lg *liveLog) attachRoute(h *Gateway, pk *domain.PlatformKey, up *domain.Upstream) {
	if lg == nil || lg.id == 0 {
		return
	}
	updates := map[string]any{}
	if pk != nil {
		updates["platform_key_id"] = pk.ID
	}
	if up != nil {
		updates["upstream_id"] = up.ID
	}
	if len(updates) == 0 {
		return
	}
	h.writeLog(lg, updates, false)
}

func (h *Gateway) patchLog(lg *liveLog, pk *domain.PlatformKey, up *domain.Upstream, protocol, model, path, reqID, clientIP string, status int, success bool, usage upstream.TokenUsage, ttft, dur int, errMsg string, snap ioCapture, inFlight, force bool) {
	if lg == nil || lg.id == 0 {
		return
	}
	lg.mu.Lock()
	if ttft > 0 && !lg.ttftSent {
		force = true
	}
	if !force && !lg.lastFlush.IsZero() && time.Since(lg.lastFlush) < 500*time.Millisecond {
		lg.mu.Unlock()
		return
	}
	if ttft > 0 {
		lg.ttftSent = true
	}
	lg.lastFlush = time.Now()
	lg.mu.Unlock()
	h.writeLog(lg, logUpdates(ckIDs{pk: pk, up: up}, protocol, model, path, reqID, clientIP, status, success, usage, ttft, dur, errMsg, snap, inFlight), false)
}

type ckIDs struct {
	pk *domain.PlatformKey
	up *domain.Upstream
}

func (h *Gateway) finishLog(lg *liveLog, ck *domain.ConsumerKey, pk *domain.PlatformKey, up *domain.Upstream, protocol, model, path, reqID, clientIP string, status int, success bool, usage upstream.TokenUsage, ttft, dur int, errMsg string, snap ioCapture) {
	completedAt := time.Now().UTC()
	updates := logUpdates(ckIDs{pk: pk, up: up}, protocol, model, path, reqID, clientIP, status, success, usage, ttft, dur, errMsg, snap, false)
	updates["completed_at"] = completedAt
	updates["stream"] = snap.ReqStream
	updates["stream_known"] = snap.StreamKnown
	if success {
		updates["failure_scope"] = ""
		updates["failure_action"] = ""
	}
	if lg == nil || lg.id == 0 {
		row := domain.RequestLog{
			RequestID:           reqID,
			Protocol:            protocol,
			Model:               model,
			Path:                path,
			ClientIP:            clientIP,
			StatusCode:          status,
			Success:             success,
			InputTokens:         usage.InputTokens,
			OutputTokens:        usage.OutputTokens,
			CacheReadTokens:     usage.CacheReadTokens,
			CacheCreationTokens: usage.CacheCreationTokens,
			TTFTMs:              ttft,
			DurationMs:          dur,
			InFlight:            false,
			Stream:              snap.ReqStream,
			StreamKnown:         snap.StreamKnown,
			CompletedAt:         &completedAt,
			CreatedAt:           snap.StartedAt,
			CostUSD:             usage.CostUSD,
			ErrorMessage:        truncateErr(errMsg),
			RequestHeaders:      snap.ReqHeaders,
			RequestBody:         snap.ReqBody,
			RequestBodyTrunc:    snap.ReqTrunc,
			ResponseHeaders:     snap.RespHeaders,
			ResponseBody:        snap.RespBody,
			ResponseBodyTrunc:   snap.RespTrunc,
		}
		if ck != nil {
			id := ck.ID
			row.ConsumerKeyID = &id
		}
		if pk != nil {
			id := pk.ID
			row.PlatformKeyID = &id
		}
		if up != nil {
			id := up.ID
			row.UpstreamID = &id
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.DB.WithContext(ctx).Create(&row).Error; err != nil {
				slog.Error("finish request log", "request_id", reqID, "error", err)
			}
			h.touchConsumer(ctx, ck)
		}()
		return
	}
	h.writeLog(lg, updates, true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.touchConsumer(ctx, ck)
	}()
}

func logUpdates(ids ckIDs, protocol, model, path, reqID, clientIP string, status int, success bool, usage upstream.TokenUsage, ttft, dur int, errMsg string, snap ioCapture, inFlight bool) map[string]any {
	updates := map[string]any{
		"protocol":              protocol,
		"model":                 model,
		"path":                  path,
		"request_id":            reqID,
		"client_ip":             clientIP,
		"status_code":           status,
		"success":               success,
		"input_tokens":          usage.InputTokens,
		"output_tokens":         usage.OutputTokens,
		"cache_read_tokens":     usage.CacheReadTokens,
		"cache_creation_tokens": usage.CacheCreationTokens,
		"duration_ms":           dur,
		"ttft_ms":               ttft,
		"cost_usd":              usage.CostUSD,
		"in_flight":             inFlight,
		"error_message":         truncateErr(errMsg),
	}
	if snap.ReqHeaders != "" {
		updates["request_headers"] = snap.ReqHeaders
		updates["request_body"] = snap.ReqBody
		updates["request_body_trunc"] = snap.ReqTrunc
	}
	if snap.RespHeaders != "" || snap.RespBody != "" {
		updates["response_headers"] = snap.RespHeaders
		updates["response_body"] = snap.RespBody
		updates["response_body_trunc"] = snap.RespTrunc
	}
	if ids.pk != nil {
		updates["platform_key_id"] = ids.pk.ID
	}
	if ids.up != nil {
		updates["upstream_id"] = ids.up.ID
	}
	return updates
}

func (h *Gateway) writeLog(lg *liveLog, updates map[string]any, final bool) {
	if lg == nil || lg.id == 0 || len(updates) == 0 {
		return
	}
	lg.mu.Lock()
	if lg.closed {
		lg.mu.Unlock()
		return
	}
	lg.closed = final
	lg.revision++
	if lg.updates == nil {
		lg.updates = make(map[string]any)
	}
	for k, v := range updates {
		lg.updates[k] = v
	}
	// Each version includes earlier route/body updates, even if writes arrive out of order.
	snapshot := make(map[string]any, len(lg.updates)+1)
	for k, v := range lg.updates {
		snapshot[k] = v
	}
	revision := lg.revision
	snapshot["log_revision"] = revision
	lg.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			err = h.persistLog(ctx, lg.id, revision, snapshot)
			if err == nil || !final || ctx.Err() != nil {
				break
			}
			if sleepCtx(ctx, 50*time.Millisecond) != nil {
				break
			}
		}
		if err != nil {
			slog.Error("update request log", "log_id", lg.id, "final", final, "error", err)
		}
	}()
}

func (h *Gateway) persistLog(ctx context.Context, id uint, revision uint64, updates map[string]any) error {
	return h.DB.WithContext(ctx).Model(&domain.RequestLog{}).
		Where("id = ? AND in_flight = ? AND log_revision < ?", id, true, revision).
		Updates(updates).Error
}

func (h *Gateway) touchConsumer(ctx context.Context, ck *domain.ConsumerKey) {
	if ck == nil {
		return
	}
	now := time.Now()
	_ = h.DB.WithContext(ctx).Model(&domain.ConsumerKey{}).Where("id = ?", ck.ID).
		Update("last_used_at", now).Error
}

func (h *Gateway) addQuotaUsed(id uint, delta float64) {
	if delta <= 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.DB.WithContext(ctx).Model(&domain.ConsumerKey{}).Where("id = ?", id).
			UpdateColumn("quota_used", gorm.Expr("quota_used + ?", delta)).Error
	}()
}

func requestClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if ip := strings.TrimSpace(c.ClientIP()); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(c.Request.RemoteAddr)
	}
	return host
}

func copyForwardHeaders(src, dst http.Header) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if _, skip := hopByHop[lk]; skip {
			continue
		}
		if lk == "authorization" || lk == "x-api-key" {
			continue
		}
		if lk == "content-type" || lk == "accept" || strings.HasPrefix(lk, "anthropic-") || strings.HasPrefix(lk, "openai-") || strings.HasPrefix(lk, "x-") {
			for _, v := range vs {
				dst.Add(k, v)
			}
		}
	}
	if dst.Get("Content-Type") == "" {
		dst.Set("Content-Type", "application/json")
	}
}

func peekModel(body []byte) string {
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &peek)
	return strings.TrimSpace(peek.Model)
}

func peekStream(body []byte) bool {
	stream, _ := domain.RequestStream("application/json", body)
	return stream
}

func isSSEContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(strings.ToLower(ct))
	return err == nil && (mt == "text/event-stream" || mt == "text/stream")
}

// peekModelFrom extracts the model id from either a JSON body or a
// multipart/form-data body (image edits / variations). File parts are skipped.
func peekModelFrom(contentType string, body []byte) string {
	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		return peekModel(body)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return ""
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err != nil {
			return ""
		}
		if part.FormName() != "model" || part.FileName() != "" {
			_ = part.Close()
			continue
		}
		val, _ := io.ReadAll(io.LimitReader(part, 512))
		_ = part.Close()
		return strings.TrimSpace(string(val))
	}
}

// usageCapture keeps the first `limit` bytes of a passthrough response so usage
// can be parsed after the body has been streamed to the client unmodified.
type usageCapture struct {
	buf     bytes.Buffer
	limit   int
	total   int
	onBytes func(int)
}

func (u *usageCapture) Prefix() []byte {
	return u.buf.Bytes()
}

func (u *usageCapture) Total() int {
	return u.total
}

func (u *usageCapture) Write(p []byte) (int, error) {
	n := len(p)
	if u.onBytes != nil {
		u.onBytes(n)
	}
	u.total += n
	if room := u.limit - u.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		u.buf.Write(p)
	}
	return n, nil
}

func (u *usageCapture) Bytes() []byte {
	if u.total > u.limit {
		// Truncated payloads are not valid JSON; skip usage parsing.
		return nil
	}
	return u.buf.Bytes()
}

type streamCollector struct {
	start          time.Time
	firstAt        time.Time
	ttftMs         int
	buf            []byte
	prefix         []byte
	total          int
	usage          upstream.TokenUsage
	onProgress     func(ttft, dur int, usage upstream.TokenUsage)
	eventName      string
	eventData      []string
	eventBytes     int
	eventOversized bool
	terminal       bool
	terminalErr    error
}

func (s *streamCollector) markTTFT() {
	if s.ttftMs > 0 {
		return
	}
	s.firstAt = time.Now()
	s.ttftMs = int(s.firstAt.Sub(s.start).Milliseconds())
	if s.ttftMs <= 0 {
		s.ttftMs = 1
	}
	s.emitProgress()
}

func (s *streamCollector) emitProgress() {
	if s.onProgress == nil {
		return
	}
	s.onProgress(s.ttftMs, int(time.Since(s.start).Milliseconds()), s.usage)
}

func (s *streamCollector) noteBytes(n int) {
	if n > 0 && s.ttftMs == 0 {
		s.markTTFT()
	}
}

func (s *streamCollector) feed(p []byte) int {
	consumed := len(p)
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) > 1<<20 {
				s.buf = s.buf[len(s.buf)-64:]
				s.eventOversized = true
				s.eventData = nil
			}
			break
		}
		line := strings.TrimSuffix(string(s.buf[:i]), "\r")
		s.buf = s.buf[i+1:]
		if line == "" {
			s.finishEvent()
			if s.terminal {
				consumed -= len(s.buf)
				s.buf = nil
				break
			}
		} else if strings.HasPrefix(line, "event:") {
			s.eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			s.eventBytes += len(line)
			if s.eventBytes > 1<<20 {
				s.eventOversized = true
				s.eventData = nil
			}
			if !s.eventOversized {
				s.eventData = append(s.eventData, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if s.ttftMs == 0 && upstream.SSELineHasText(line) {
			s.markTTFT()
		}
		u := upstream.ParseSSEUsageLine(line)
		upstream.MergeUsage(&s.usage, u)
	}
	s.total += consumed
	if room := maxLogBodyBytes - len(s.prefix); room > 0 {
		s.prefix = append(s.prefix, p[:min(room, consumed)]...)
	}
	s.emitProgress()
	return consumed
}

func (s *streamCollector) finishEvent() {
	defer func() { s.eventBytes = 0; s.eventOversized = false; s.eventData = nil; s.eventName = "" }()
	if s.eventOversized {
		return
	}
	data := strings.Join(s.eventData, "\n")
	name := s.eventName
	s.eventData = nil
	s.eventName = ""
	if strings.TrimSpace(data) == "[DONE]" {
		s.terminal = true
		return
	}
	if upstream.SSELineHasText("data: "+data) && s.ttftMs == 0 {
		s.markTTFT()
	}
	upstream.MergeUsage(&s.usage, upstream.ParseSSEUsageLine("data: "+data))
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(data), &event) == nil && event.Type != "" {
		name = event.Type
	}
	switch name {
	case "message_stop", "response.completed":
		s.terminal = true
	case "error", "response.failed", "response.incomplete":
		s.terminal = true
		s.terminalErr = errors.New("upstream stream ended: " + name)
	}
}

func copySSE(w gin.ResponseWriter, r io.Reader, col *streamCollector, hold bool, onRelease func()) error {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(r, 32*1024)
	buf := make([]byte, 32*1024)
	var pending []byte
	released := !hold
	var releaseErr error
	release := func() {
		if released {
			return
		}
		released = true
		if onRelease != nil {
			onRelease()
		}
		if len(pending) == 0 {
			return
		}
		_, releaseErr = w.Write(pending)
		pending = nil
		if releaseErr == nil && flusher != nil {
			flusher.Flush()
		}
	}
	for {
		n, err := br.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			chunk = chunk[:col.feed(chunk)]
			if !released {
				if len(pending)+len(chunk) > 1<<20 {
					return errors.New("stream prefix exceeded limit before first token")
				}
				pending = append(pending, chunk...)
				if col.ttftMs > 0 {
					release()
					if releaseErr != nil {
						return releaseErr
					}
				}
			} else {
				if _, werr := w.Write(chunk); werr != nil {
					return werr
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if col.terminal {
				release()
				if releaseErr != nil {
					return releaseErr
				}
				return col.terminalErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				release()
				return releaseErr
			}
			return err
		}
	}
}

type firstTokenWatch struct {
	mu      sync.Mutex
	timer   *time.Timer
	fired   bool
	stopped bool
	started bool
}

func (w *firstTokenWatch) start(cancel context.CancelFunc, wait time.Duration, deadline time.Time) {
	if w == nil || cancel == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.stopped {
		return
	}
	if rem := time.Until(deadline); wait > rem {
		wait = rem
	}
	w.started = true
	if wait <= 0 {
		w.fired = true
		go cancel()
		return
	}
	w.timer = time.AfterFunc(wait, func() {
		w.mu.Lock()
		if w.stopped {
			w.mu.Unlock()
			return
		}
		w.fired = true
		w.mu.Unlock()
		cancel()
	})
}

func (w *firstTokenWatch) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *firstTokenWatch) running() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started && !w.stopped
}

func (w *firstTokenWatch) timedOut() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func proxyTimeoutMessage(err error, firstToken bool) string {
	if firstToken && isTimeoutErr(err) {
		return "first token timeout"
	}
	if isTimeoutErr(err) {
		return "upstream timeout"
	}
	if err != nil {
		return err.Error()
	}
	return "upstream request failed"
}

func gatewayError(c *gin.Context, status int, typ, message string) {
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v1/messages") {
		c.JSON(status, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    typ,
				"message": message,
			},
		})
		return
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    typ,
			"code":    typ,
		},
	})
}

func truncateErr(s string) string {
	if len(s) > 1000 {
		return s[:1000]
	}
	return s
}

type rpmLimiter struct {
	mu      sync.Mutex
	windows map[uint][]time.Time
}

func newRPMLimiter() *rpmLimiter {
	return &rpmLimiter{windows: map[uint][]time.Time{}}
}

func (r *rpmLimiter) Allow(id uint, rpm int) bool {
	if rpm <= 0 {
		return true
	}
	now := time.Now()
	cut := now.Add(-time.Minute)
	r.mu.Lock()
	defer r.mu.Unlock()
	hits := r.windows[id]
	alive := hits[:0]
	for _, t := range hits {
		if t.After(cut) {
			alive = append(alive, t)
		}
	}
	if len(alive) >= rpm {
		r.windows[id] = alive
		return false
	}
	r.windows[id] = append(alive, now)
	return true
}

func (h *Gateway) upstreamKeyIDs(ctx context.Context, upstreamID uint) []uint {
	var ids []uint
	_ = h.DB.WithContext(ctx).Model(&domain.PlatformKey{}).Where("upstream_id = ?", upstreamID).Pluck("id", &ids).Error
	return ids
}

type concLimiter struct {
	mu       sync.Mutex
	inflight map[uint]int
}

func newConcLimiter() *concLimiter {
	return &concLimiter{inflight: map[uint]int{}}
}

func (l *concLimiter) Acquire(id uint, limit int) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[id] >= limit {
		return false
	}
	l.inflight[id]++
	return true
}

func (l *concLimiter) Release(id uint, limit int) {
	if limit <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.inflight[id] - 1
	if n <= 0 {
		delete(l.inflight, id)
		return
	}
	l.inflight[id] = n
}

package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
		gatewayError(c, http.StatusInternalServerError, "api_error", err.Error())
		return
	}
	if bound && len(allow) == 0 {
		msg := "route group has no available key for this protocol/model"
		if len(drift) > 0 {
			msg = "route group members' rate multipliers are all outside the group's rate range"
		}
		gatewayError(c, http.StatusServiceUnavailable, "api_error", msg)
		return
	}
	if !bound {
		allow, drift = nil, nil
	}

	var exclude []uint
	var lastMsg = "no enabled upstream for protocol"
	for attempt := 0; attempt < attempts; attempt++ {
		pk, up, err := h.Picker.Pick(c.Request.Context(), picker.Request{
			Protocol:  protocol,
			Model:     model,
			Session:   session,
			Exclude:   exclude,
			AllowKeys: allow,
			DriftKeys: drift,
		})
		if err != nil {
			if errors.Is(err, picker.ErrNoUpstream) {
				if attempt == 0 {
					msg := "no enabled upstream for protocol"
					if bound {
						msg = "no healthy key in bound route groups"
					}
					gatewayError(c, http.StatusServiceUnavailable, "api_error", msg)
					return
				}
				gatewayError(c, http.StatusBadGateway, "api_error", lastMsg)
				return
			}
			gatewayError(c, http.StatusInternalServerError, "api_error", err.Error())
			return
		}
		var outcome forwardOutcome
		for r := 0; r <= retries; r++ {
			outcome = h.forwardOnce(c, ck, pk, up, protocol, model, session, reqID, body, reqSnap)
			if outcome.ok {
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
		h.penalizeKey(c.Request.Context(), pk.ID, outcome.lowBalance, outcome.failOver && !outcome.concBusy)
		if outcome.concBusy || outcome.lowBalance {
			exclude = append(exclude, h.upstreamKeyIDs(c.Request.Context(), up.ID)...)
		} else {
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
	ok         bool
	failOver   bool
	retrySame  bool
	lowBalance bool
	concBusy   bool
	msg        string
}

func (h *Gateway) penalizeKey(ctx context.Context, keyID uint, lowBalance, failOver bool) {
	if h.Picker == nil || !failOver {
		return
	}
	if lowBalance {
		h.Picker.MarkLowBalance(ctx, keyID)
		return
	}
	h.Picker.Cooldown(ctx, keyID)
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

func (h *Gateway) forwardOnce(c *gin.Context, ck *domain.ConsumerKey, pk *domain.PlatformKey, up *domain.Upstream, protocol, model, session, reqID string, body []byte, reqSnap ioCapture) forwardOutcome {
	if up != nil {
		if !h.conc.Acquire(up.ID, up.Concurrency) {
			return forwardOutcome{failOver: true, concBusy: true, msg: "upstream concurrency exceeded"}
		}
		defer h.conc.Release(up.ID, up.Concurrency)
	}

	apiKey, err := h.Enc.Decrypt(pk.EncryptedKey)
	if err != nil {
		return forwardOutcome{msg: "failed to decrypt platform key"}
	}
	target := upstream.JoinEndpoint(up.BaseURL, c.Request.URL.Path)
	if c.Request.URL.RawQuery != "" {
		target += "?" + c.Request.URL.RawQuery
	}
	upReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, target, bytes.NewReader(body))
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

	started := time.Now()
	path := c.Request.URL.Path
	clientIP := requestClientIP(c)
	resp, err := h.Client.DoRaw(upReq)
	if err != nil {
		h.recordAttempt(ck, pk, up, protocol, model, path, reqID, clientIP, 0, false, upstream.TokenUsage{}, 0, int(time.Since(started).Milliseconds()), err.Error(), reqSnap)
		return forwardOutcome{failOver: true, retrySame: true, msg: "upstream request failed"}
	}

	if shouldFailoverStatus(resp.StatusCode) && !c.Writer.Written() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, maxLogBodyBytes+1))
		_ = resp.Body.Close()
		snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), peek, len(peek))
		h.recordAttempt(ck, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, 0, int(time.Since(started).Milliseconds()), http.StatusText(resp.StatusCode), snap)
		low := resp.StatusCode == http.StatusPaymentRequired
		return forwardOutcome{
			failOver:   true,
			retrySame:  !low,
			lowBalance: low,
			msg:        http.StatusText(resp.StatusCode),
		}
	}

	// new-api / one-api report an exhausted token as 403 with a quota error
	// code instead of 402. Treat it as low balance and try the next key.
	if resp.StatusCode == http.StatusForbidden && !c.Writer.Written() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if isQuotaExhaustedBody(peek) {
			_ = resp.Body.Close()
			msg := "upstream quota exhausted"
			snap := reqSnap.withResponse(resp.Header, resp.Header.Get("Content-Type"), peek, len(peek))
			h.recordAttempt(ck, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, false, upstream.TokenUsage{}, 0, int(time.Since(started).Milliseconds()), msg, snap)
			return forwardOutcome{failOver: true, lowBalance: true, msg: msg}
		}
		// Not a quota error: stitch the peeked prefix back and pass through as-is.
		orig := resp.Body
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(peek), orig), orig}
	}

	defer resp.Body.Close()
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

	collector := &streamCollector{start: started}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	streaming := strings.Contains(ct, "text/event-stream") || strings.Contains(ct, "text/stream")
	var respPrefix []byte
	var respTotal int
	if streaming {
		err = copySSE(c.Writer, resp.Body, collector)
		respPrefix = collector.prefix
		respTotal = collector.total
	} else {
		// Stream the body through untouched (image responses with b64_json can be
		// tens of MB) while capturing a bounded prefix for usage parsing.
		capture := &usageCapture{limit: 16 << 20}
		n, copyErr := io.Copy(c.Writer, io.TeeReader(resp.Body, capture))
		collector.noteBytes(int(n))
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
	h.recordAttempt(ck, pk, up, protocol, model, path, reqID, clientIP, resp.StatusCode, success, collector.usage, collector.ttftMs, dur, errMsg, snap)
	if success {
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

func shouldFailoverStatus(code int) bool {
	if code == http.StatusTooManyRequests || code == http.StatusPaymentRequired || code == 529 {
		return true
	}
	return code >= 500
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

func (h *Gateway) recordAttempt(ck *domain.ConsumerKey, pk *domain.PlatformKey, up *domain.Upstream, protocol, model, path, reqID, clientIP string, status int, success bool, usage upstream.TokenUsage, ttft, dur int, errMsg string, snap ioCapture) {
	if pk != nil && h.Picker != nil {
		h.Picker.Observe(context.Background(), pk.ID, model, success, usage.InputTokens, usage.CacheReadTokens, usage.CacheCreationTokens, ttft)
	}
	h.recordLog(ck, pk, up, protocol, model, path, reqID, clientIP, status, success, usage, ttft, dur, errMsg, snap)
}

func (h *Gateway) recordLog(ck *domain.ConsumerKey, pk *domain.PlatformKey, up *domain.Upstream, protocol, model, path, reqID, clientIP string, status int, success bool, usage upstream.TokenUsage, ttft, dur int, errMsg string, snap ioCapture) {
	logRow := domain.RequestLog{
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
		logRow.ConsumerKeyID = &id
	}
	if pk != nil {
		id := pk.ID
		logRow.PlatformKeyID = &id
	}
	if up != nil {
		id := up.ID
		logRow.UpstreamID = &id
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.DB.WithContext(ctx).Create(&logRow).Error
		if ck != nil {
			now := time.Now()
			_ = h.DB.WithContext(ctx).Model(&domain.ConsumerKey{}).Where("id = ?", ck.ID).
				Update("last_used_at", now).Error
		}
		if pk != nil && h.Ops != nil {
			h.Ops.ObserveRequest(ctx, pk.ID, success, status, errMsg)
		}
	}()
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
	buf   bytes.Buffer
	limit int
	total int
}

func (u *usageCapture) Prefix() []byte {
	return u.buf.Bytes()
}

func (u *usageCapture) Total() int {
	return u.total
}

func (u *usageCapture) Write(p []byte) (int, error) {
	n := len(p)
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
	start   time.Time
	firstAt time.Time
	ttftMs  int
	buf     []byte
	prefix  []byte
	total   int
	usage   upstream.TokenUsage
}

func (s *streamCollector) noteBytes(n int) {
	if n > 0 && s.firstAt.IsZero() {
		s.firstAt = time.Now()
		s.ttftMs = int(s.firstAt.Sub(s.start).Milliseconds())
	}
}

func (s *streamCollector) feed(p []byte) {
	s.noteBytes(len(p))
	s.total += len(p)
	if room := maxLogBodyBytes - len(s.prefix); room > 0 {
		if len(p) > room {
			s.prefix = append(s.prefix, p[:room]...)
		} else {
			s.prefix = append(s.prefix, p...)
		}
	}
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) > 1<<20 {
				s.buf = s.buf[len(s.buf)-64:]
			}
			return
		}
		line := string(s.buf[:i])
		s.buf = s.buf[i+1:]
		u := upstream.ParseSSEUsageLine(line)
		upstream.MergeUsage(&s.usage, u)
	}
}

func copySSE(w gin.ResponseWriter, r io.Reader, col *streamCollector) error {
	flusher, _ := w.(http.Flusher)
	br := bufio.NewReaderSize(r, 32*1024)
	buf := make([]byte, 32*1024)
	for {
		n, err := br.Read(buf)
		if n > 0 {
			col.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
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

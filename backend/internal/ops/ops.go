package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Service struct {
	DB     *gorm.DB
	Enc    *crypto.AESGCM
	Client *upstream.Client
	Redis  *redis.Client
}

func New(db *gorm.DB, enc *crypto.AESGCM, rdb *redis.Client) *Service {
	return &Service{
		DB:     db,
		Enc:    enc,
		Client: upstream.NewClient(),
		Redis:  rdb,
	}
}

func (s *Service) decrypt(key *domain.PlatformKey) (string, error) {
	return s.Enc.Decrypt(key.EncryptedKey)
}

func (s *Service) RecomputeHealth(key *domain.PlatformKey, up *domain.Upstream) string {
	if key.Status == domain.StatusDisabled || (up != nil && up.Status == domain.StatusDisabled) {
		return domain.HealthDisabled
	}
	now := time.Now()
	if key.CooldownUntil != nil && key.CooldownUntil.After(now) {
		return domain.HealthCooldown
	}
	if up != nil && up.LastBalance != nil && *up.LastBalance <= 0 {
		return domain.HealthLowBalance
	}
	switch key.HealthStatus {
	case domain.HealthHealthy, domain.HealthDegraded, domain.HealthDown:
		return key.HealthStatus
	default:
		return domain.HealthHealthy
	}
}

func MatchModel(pattern, model string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "*" || pattern == model {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func (s *Service) RefreshKeyHealth(ctx context.Context, keyID uint) error {
	var key domain.PlatformKey
	if err := s.DB.WithContext(ctx).Preload("Upstream").First(&key, keyID).Error; err != nil {
		return err
	}
	probe := key.HealthStatus
	switch probe {
	case domain.HealthHealthy, domain.HealthDegraded, domain.HealthDown:
	default:
		probe = domain.HealthHealthy
	}
	key.HealthStatus = probe
	key.HealthStatus = s.RecomputeHealth(&key, key.Upstream)
	return s.DB.WithContext(ctx).Model(&key).Update("health_status", key.HealthStatus).Error
}

func (s *Service) RefreshAllHealth(ctx context.Context) error {
	var keys []domain.PlatformKey
	if err := s.DB.WithContext(ctx).Preload("Upstream").Find(&keys).Error; err != nil {
		return err
	}
	for i := range keys {
		k := &keys[i]
		probe := k.HealthStatus
		switch probe {
		case domain.HealthHealthy, domain.HealthDegraded, domain.HealthDown:
		default:
			probe = domain.HealthHealthy
		}
		k.HealthStatus = probe
		next := s.RecomputeHealth(k, k.Upstream)
		_ = s.DB.WithContext(ctx).Model(k).Update("health_status", next).Error
	}
	return nil
}

func (s *Service) throttled(ctx context.Context, kind string, keyID uint, ttl time.Duration) bool {
	if s.Redis == nil || ttl <= 0 {
		return false
	}
	ok, err := s.Redis.SetNX(ctx, fmt.Sprintf("sum:%s:%d", kind, keyID), 1, ttl).Result()
	if err != nil {
		return false
	}
	return !ok
}

type ProbeOutcome struct {
	Success    bool   `json:"success"`
	StatusCode int    `json:"status_code"`
	LatencyMs  int    `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	Models     int    `json:"models,omitempty"`
	Message    string `json:"message,omitempty"`
	Model      string `json:"model,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
}

func (s *Service) ProbeKey(ctx context.Context, keyID uint, deep bool) (*ProbeOutcome, error) {
	var key domain.PlatformKey
	if err := s.DB.WithContext(ctx).Preload("Upstream").First(&key, keyID).Error; err != nil {
		return nil, err
	}
	if key.Upstream == nil {
		return nil, fmt.Errorf("upstream missing")
	}
	apiKey, err := s.decrypt(&key)
	if err != nil {
		return nil, err
	}

	kind := domain.ProbeLight
	if deep {
		kind = domain.ProbeDeep
	}
	start := time.Now()
	var outcome ProbeOutcome
	if deep {
		outcome = s.deepProbe(ctx, &key, apiKey)
	} else {
		outcome = s.lightProbe(ctx, &key, apiKey)
	}
	outcome.LatencyMs = int(time.Since(start).Milliseconds())

	health := domain.HealthDown
	if outcome.Success {
		if outcome.LatencyMs >= probeDegradedMs {
			health = domain.HealthDegraded
		} else {
			health = domain.HealthHealthy
		}
	}
	updates := map[string]any{
		"health_status": health,
		"last_error":    outcome.Error,
	}
	if err := s.DB.WithContext(ctx).Model(&domain.PlatformKey{}).Where("id = ?", key.ID).Updates(updates).Error; err != nil {
		return &outcome, err
	}
	extra := ""
	if outcome.Model != "" {
		payload, _ := json.Marshal(map[string]any{"model": outcome.Model, "vendor": outcome.Vendor})
		extra = string(payload)
	}
	_ = s.DB.WithContext(ctx).Create(&domain.ProbeLog{
		PlatformKeyID: key.ID,
		Kind:          kind,
		Success:       outcome.Success,
		StatusCode:    outcome.StatusCode,
		LatencyMs:     outcome.LatencyMs,
		ErrorMessage:  outcome.Error,
		Extra:         extra,
	}).Error
	_ = s.RefreshKeyHealth(ctx, key.ID)
	if outcome.Success {
		if health == domain.HealthDegraded {
			if outcome.Model != "" {
				outcome.Message = fmt.Sprintf("探测降级 %s 慢响应 (%dms)", outcome.Model, outcome.LatencyMs)
			} else {
				outcome.Message = fmt.Sprintf("探测降级 慢响应 (%dms)", outcome.LatencyMs)
			}
		} else if outcome.Model != "" {
			outcome.Message = fmt.Sprintf("探测成功 %s (%dms)", outcome.Model, outcome.LatencyMs)
		} else {
			outcome.Message = fmt.Sprintf("探测成功 (%dms)", outcome.LatencyMs)
		}
	} else if outcome.Error != "" {
		outcome.Message = outcome.Error
	} else {
		outcome.Message = "探测失败"
	}
	return &outcome, nil
}

// modelsHeaders returns extra headers for GET /v1/models. Anthropic-only
// upstreams need x-api-key + anthropic-version; everyone else is fine with the
// Bearer token GetJSON already sets.
func modelsHeaders(up *domain.Upstream, apiKey string) http.Header {
	if up != nil && up.Supports(domain.ProtocolAnthropic) && !up.Supports(domain.ProtocolOpenAI) {
		return upstream.AnthropicHeaders(apiKey)
	}
	return nil
}

// getModels performs GET /v1/models against the key's upstream.
func (s *Service) getModels(ctx context.Context, key *domain.PlatformKey, apiKey string) (*upstream.Result, error) {
	return s.Client.GetJSONWithHeaders(ctx, key.Upstream.BaseURL, "/v1/models", apiKey, modelsHeaders(key.Upstream, apiKey))
}

// storeModels persists a freshly fetched model list on the key.
func (s *Service) storeModels(ctx context.Context, keyID uint, ids []string) error {
	return s.DB.WithContext(ctx).Model(&domain.PlatformKey{}).Where("id = ?", keyID).Updates(map[string]any{
		"last_models":    domain.JSONStrings(ids),
		"last_models_at": time.Now(),
	}).Error
}

type ModelsOutcome struct {
	Success    bool      `json:"success"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int       `json:"latency_ms"`
	Models     []string  `json:"models"`
	Count      int       `json:"count"`
	FetchedAt  time.Time `json:"fetched_at"`
	Error      string    `json:"error,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// FetchModels pulls GET /v1/models for one key and stores the ids on the key.
// It is an explicit admin action, separate from the light probe (which only
// updates the list opportunistically).
func (s *Service) FetchModels(ctx context.Context, keyID uint) (*ModelsOutcome, error) {
	var key domain.PlatformKey
	if err := s.DB.WithContext(ctx).Preload("Upstream").First(&key, keyID).Error; err != nil {
		return nil, err
	}
	if key.Upstream == nil {
		return nil, fmt.Errorf("upstream missing")
	}
	apiKey, err := s.decrypt(&key)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	res, err := s.getModels(ctx, &key, apiKey)
	out := &ModelsOutcome{LatencyMs: int(time.Since(start).Milliseconds()), Models: []string{}}
	plog := domain.ProbeLog{PlatformKeyID: key.ID, Kind: domain.ProbeModels, LatencyMs: out.LatencyMs}
	if err != nil {
		out.Error = err.Error()
		out.Message = out.Error
		plog.ErrorMessage = out.Error
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return out, nil
	}
	out.StatusCode = res.Status
	plog.StatusCode = res.Status
	if res.Status < 200 || res.Status >= 300 {
		out.Error = truncate(string(res.Body), 500)
		out.Message = fmt.Sprintf("上游返回 %d", res.Status)
		plog.ErrorMessage = out.Error
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return out, nil
	}
	ids := upstream.ParseModelIDs(res.Body)
	if ids == nil {
		ids = []string{}
	}
	if err := s.storeModels(ctx, key.ID, ids); err != nil {
		return out, err
	}
	out.Success = true
	out.Models = ids
	out.Count = len(ids)
	out.FetchedAt = time.Now()
	out.Message = fmt.Sprintf("获取到 %d 个模型 (%dms)", out.Count, out.LatencyMs)
	plog.Success = true
	extra, _ := json.Marshal(map[string]any{"models": len(ids)})
	plog.Extra = string(extra)
	_ = s.DB.WithContext(ctx).Create(&plog).Error
	return out, nil
}

// FetchModelsForUpstream fetches models for every enabled key of one upstream
// and returns ok/fail counts plus the de-duplicated union of model ids.
func (s *Service) FetchModelsForUpstream(ctx context.Context, upstreamID uint) (ok, fail int, union []string) {
	var keys []domain.PlatformKey
	if err := s.DB.WithContext(ctx).Where("upstream_id = ? AND status = ?", upstreamID, domain.StatusEnabled).Order("id ASC").Find(&keys).Error; err != nil {
		log.Printf("fetch models upstream=%d: list keys: %v", upstreamID, err)
		return 0, 0, nil
	}
	seen := map[string]struct{}{}
	union = []string{}
	for _, k := range keys {
		out, err := s.FetchModels(ctx, k.ID)
		if err != nil || out == nil || !out.Success {
			fail++
			continue
		}
		ok++
		for _, id := range out.Models {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			union = append(union, id)
		}
	}
	return ok, fail, union
}

// usageProbePath is the cheap authenticated GET used when /v1/models is not
// available on an upstream.
func usageProbePath(up *domain.Upstream) string {
	if up != nil && up.Kind == domain.KindNewAPI {
		return "/v1/dashboard/billing/subscription"
	}
	return "/v1/usage"
}

func (s *Service) lightProbe(ctx context.Context, key *domain.PlatformKey, apiKey string) ProbeOutcome {
	usagePath := usageProbePath(key.Upstream)
	res, err := s.getModels(ctx, key, apiKey)
	if err != nil {
		res, err2 := s.Client.GetJSON(ctx, key.Upstream.BaseURL, usagePath, apiKey)
		if err2 != nil {
			return ProbeOutcome{Error: err.Error()}
		}
		if res.Status >= 200 && res.Status < 300 {
			return ProbeOutcome{Success: true, StatusCode: res.Status}
		}
		return ProbeOutcome{StatusCode: res.Status, Error: truncate(string(res.Body), 500)}
	}
	if res.Status >= 200 && res.Status < 300 {
		ids := upstream.ParseModelIDs(res.Body)
		if len(ids) > 0 {
			_ = s.storeModels(ctx, key.ID, ids)
		}
		return ProbeOutcome{Success: true, StatusCode: res.Status, Models: len(ids)}
	}
	if res.Status == 404 {
		u, err := s.Client.GetJSON(ctx, key.Upstream.BaseURL, usagePath, apiKey)
		if err != nil {
			return ProbeOutcome{StatusCode: res.Status, Error: truncate(string(res.Body), 500)}
		}
		ok := u.Status >= 200 && u.Status < 300
		errMsg := ""
		if !ok {
			errMsg = truncate(string(u.Body), 500)
		}
		return ProbeOutcome{Success: ok, StatusCode: u.Status, Error: errMsg}
	}
	return ProbeOutcome{StatusCode: res.Status, Error: truncate(string(res.Body), 500)}
}

func (s *Service) probeSettings(ctx context.Context) domain.SchedulerSettings {
	row := domain.DefaultSchedulerSettings()
	_ = s.DB.WithContext(ctx).First(&row, 1).Error
	row.Normalize()
	return row
}

func (s *Service) deepProbe(ctx context.Context, key *domain.PlatformKey, apiKey string) ProbeOutcome {
	cfg := s.probeSettings(ctx)
	var catalog []domain.CatalogModel
	_ = s.DB.WithContext(ctx).Find(&catalog).Error
	target := PickProbeTarget(key, cfg, catalog)
	if target.Protocol == domain.ProtocolAnthropic {
		body := map[string]any{
			"model":      target.Model,
			"max_tokens": 1,
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
			"stream":     false,
		}
		extra := map[string][]string{"Anthropic-Version": {"2023-06-01"}}
		res, err := s.Client.PostJSON(ctx, key.Upstream.BaseURL, "/v1/messages", apiKey, body, extra)
		return probeHTTPOutcome(res, err, target)
	}
	body := map[string]any{
		"model":      target.Model,
		"max_tokens": 1,
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"stream":     false,
	}
	res, err := s.Client.PostJSON(ctx, key.Upstream.BaseURL, "/v1/chat/completions", apiKey, body, nil)
	return probeHTTPOutcome(res, err, target)
}

func probeHTTPOutcome(res *upstream.Result, err error, target ProbeTarget) ProbeOutcome {
	if err != nil {
		return ProbeOutcome{Error: err.Error(), Model: target.Model, Vendor: target.Vendor}
	}
	ok := res.Status >= 200 && res.Status < 300
	errMsg := ""
	if !ok {
		errMsg = truncate(string(res.Body), 500)
	} else if strings.TrimSpace(probeResponseText(res.Body)) == "" {
		// V1 replace-mode: 2xx with empty extracted text is a failed check.
		ok = false
		errMsg = "upstream returned 2xx with empty text"
	}
	return ProbeOutcome{Success: ok, StatusCode: res.Status, Error: errMsg, Model: target.Model, Vendor: target.Vendor}
}

func probeResponseText(body []byte) string {
	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return ""
	}
	var openai struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &openai) == nil && len(openai.Choices) > 0 {
		return strings.TrimSpace(openaiContentText(openai.Choices[0].Message.Content))
	}
	var anth struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &anth) == nil && len(anth.Content) > 0 {
		var b strings.Builder
		for _, part := range anth.Content {
			b.WriteString(part.Text)
		}
		return strings.TrimSpace(b.String())
	}
	return raw
}

func openaiContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return ""
}

func (s *Service) RefreshBalance(ctx context.Context, keyID uint) error {
	var key domain.PlatformKey
	if err := s.DB.WithContext(ctx).Select("id", "upstream_id").First(&key, keyID).Error; err != nil {
		return err
	}
	return s.RefreshUpstreamBalance(ctx, key.UpstreamID, keyID)
}

// RefreshUpstreamBalance fetches remaining quota for a provider using any
// enabled key (preferKeyID first when set) and writes the result on the
// upstream. Every key of that provider then shares the same balance.
func (s *Service) RefreshUpstreamBalance(ctx context.Context, upstreamID, preferKeyID uint) error {
	var up domain.Upstream
	if err := s.DB.WithContext(ctx).First(&up, upstreamID).Error; err != nil {
		return err
	}
	var keys []domain.PlatformKey
	if err := s.DB.WithContext(ctx).
		Where("upstream_id = ? AND status = ?", upstreamID, domain.StatusEnabled).
		Order("id ASC").
		Find(&keys).Error; err != nil {
		return err
	}
	if len(keys) == 0 {
		return fmt.Errorf("no enabled key")
	}
	if preferKeyID != 0 {
		for i := range keys {
			if keys[i].ID == preferKeyID {
				keys[0], keys[i] = keys[i], keys[0]
				break
			}
		}
	}
	var lastErr error
	for i := range keys {
		k := &keys[i]
		k.Upstream = &up
		remaining, unlimited, err := s.fetchKeyBalance(ctx, k, &up)
		if err != nil {
			lastErr = err
			continue
		}
		if err := s.applyUpstreamBalance(ctx, up.ID, remaining, unlimited); err != nil {
			return err
		}
		return s.refreshKeysHealth(ctx, upstreamID)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("balance fetch failed")
	}
	_ = s.refreshKeysHealth(ctx, upstreamID)
	return lastErr
}

func (s *Service) applyUpstreamBalance(ctx context.Context, upstreamID uint, remaining *float64, unlimited bool) error {
	updates := map[string]any{
		"last_balance_at": time.Now(),
	}
	if unlimited {
		updates["last_balance"] = nil
	} else if remaining != nil {
		updates["last_balance"] = *remaining
	}
	return s.DB.WithContext(ctx).Model(&domain.Upstream{}).Where("id = ?", upstreamID).Updates(updates).Error
}

func (s *Service) refreshKeysHealth(ctx context.Context, upstreamID uint) error {
	var keys []domain.PlatformKey
	if err := s.DB.WithContext(ctx).Where("upstream_id = ?", upstreamID).Find(&keys).Error; err != nil {
		return err
	}
	for i := range keys {
		_ = s.RefreshKeyHealth(ctx, keys[i].ID)
	}
	return nil
}

func (s *Service) fetchKeyBalance(ctx context.Context, key *domain.PlatformKey, up *domain.Upstream) (remaining *float64, unlimited bool, err error) {
	apiKey, err := s.decrypt(key)
	if err != nil {
		return nil, false, err
	}
	start := time.Now()
	if up.Kind == domain.KindNewAPI {
		remaining, unlimited, source, status, err := s.newAPIBalance(ctx, up.BaseURL, apiKey)
		plog := domain.ProbeLog{PlatformKeyID: key.ID, Kind: domain.ProbeBalance, LatencyMs: int(time.Since(start).Milliseconds()), StatusCode: status}
		if err != nil {
			plog.ErrorMessage = truncate(err.Error(), 500)
			_ = s.DB.WithContext(ctx).Create(&plog).Error
			_ = s.DB.WithContext(ctx).Model(key).Updates(map[string]any{"last_error": plog.ErrorMessage}).Error
			return nil, false, err
		}
		plog.Success = true
		extra, _ := json.Marshal(map[string]any{"source": source, "unlimited": unlimited})
		plog.Extra = string(extra)
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		_ = s.DB.WithContext(ctx).Model(key).Updates(map[string]any{"last_error": ""}).Error
		return remaining, unlimited, nil
	}
	res, err := s.Client.GetJSON(ctx, up.BaseURL, "/v1/usage", apiKey)
	plog := domain.ProbeLog{PlatformKeyID: key.ID, Kind: domain.ProbeBalance, LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		plog.ErrorMessage = err.Error()
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		_ = s.DB.WithContext(ctx).Model(key).Updates(map[string]any{"last_error": err.Error()}).Error
		return nil, false, err
	}
	plog.StatusCode = res.Status
	if res.Status < 200 || res.Status >= 300 {
		plog.ErrorMessage = truncate(string(res.Body), 500)
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		_ = s.DB.WithContext(ctx).Model(key).Updates(map[string]any{"last_error": plog.ErrorMessage}).Error
		return nil, false, fmt.Errorf("usage status %d", res.Status)
	}
	bal := upstream.ParseBalance(res.Body)
	plog.Success = true
	extra, _ := json.Marshal(map[string]any{"source": bal.Source})
	plog.Extra = string(extra)
	_ = s.DB.WithContext(ctx).Create(&plog).Error
	_ = s.DB.WithContext(ctx).Model(key).Updates(map[string]any{"last_error": ""}).Error
	return bal.Remaining, false, nil
}

// newAPIBalance queries a new-api token's remaining quota. Primary path is the
// OpenAI-compatible /v1/dashboard/billing/{subscription,usage} pair (already in
// USD); fallback is new-api's /api/usage/token/ (raw quota units).
// remaining==nil with unlimited==true means the token has no cap.
func (s *Service) newAPIBalance(ctx context.Context, baseURL, apiKey string) (remaining *float64, unlimited bool, source string, status int, err error) {
	sub, err := s.Client.GetJSON(ctx, baseURL, "/v1/dashboard/billing/subscription", apiKey)
	if err != nil {
		return nil, false, "", 0, err
	}
	status = sub.Status
	if sub.Status >= 200 && sub.Status < 300 {
		limit, isUnlimited, ok := upstream.ParseSubscriptionLimit(sub.Body)
		if ok {
			if isUnlimited {
				return nil, true, "billing.subscription", sub.Status, nil
			}
			use, err := s.Client.GetJSON(ctx, baseURL, "/v1/dashboard/billing/usage", apiKey)
			if err != nil {
				return nil, false, "", sub.Status, err
			}
			status = use.Status
			if use.Status >= 200 && use.Status < 300 {
				if used, ok := upstream.ParseBillingUsage(use.Body); ok {
					rem := limit - used
					if rem < 0 {
						rem = 0
					}
					return &rem, false, "billing.subscription-usage", use.Status, nil
				}
			}
		}
	}
	tok, err := s.Client.GetJSON(ctx, baseURL, "/api/usage/token/", apiKey)
	if err != nil {
		return nil, false, "", status, err
	}
	if tok.Status < 200 || tok.Status >= 300 {
		return nil, false, "", tok.Status, fmt.Errorf("new-api balance status %d: %s", tok.Status, truncate(string(tok.Body), 200))
	}
	rem, isUnlimited, ok := upstream.ParseNewAPITokenUsage(tok.Body)
	if !ok {
		return nil, false, "", tok.Status, fmt.Errorf("new-api token usage: unexpected body")
	}
	if isUnlimited {
		return nil, true, "api.usage.token", tok.Status, nil
	}
	return &rem, false, "api.usage.token", tok.Status, nil
}

// refreshNewAPIBilling reads new-api's public /api/pricing and copies the
// group_ratio of the key's billing group ("default" when unset) into the key's
// rate_multiplier.
func (s *Service) refreshNewAPIBilling(ctx context.Context, key *domain.PlatformKey, apiKey string, now time.Time) error {
	start := time.Now()
	res, err := s.Client.GetJSON(ctx, key.Upstream.BaseURL, "/api/pricing", apiKey)
	plog := domain.ProbeLog{PlatformKeyID: key.ID, Kind: domain.ProbeBilling, LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		plog.ErrorMessage = err.Error()
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return err
	}
	plog.StatusCode = res.Status
	if res.Status == 404 || res.Status == 401 || res.Status == 403 {
		backoff := now.Add(24 * time.Hour)
		plog.ErrorMessage = fmt.Sprintf("pricing endpoint unavailable (%d)", res.Status)
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return s.DB.WithContext(ctx).Model(key).Updates(map[string]any{
			"billing_unsupported":   true,
			"billing_backoff_until": backoff,
			"last_error":            plog.ErrorMessage,
		}).Error
	}
	if res.Status < 200 || res.Status >= 300 {
		plog.ErrorMessage = truncate(string(res.Body), 500)
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		_ = s.DB.WithContext(ctx).Model(key).Update("last_error", plog.ErrorMessage).Error
		return fmt.Errorf("pricing status %d", res.Status)
	}
	ratios, ok := upstream.ParseNewAPIGroupRatio(res.Body)
	if !ok {
		plog.ErrorMessage = "group_ratio missing"
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return fmt.Errorf("group_ratio missing in /api/pricing")
	}
	name := key.EffectiveBillingGroup()
	mult, found := ratios[name]
	if !found {
		plog.ErrorMessage = fmt.Sprintf("group %q not in group_ratio", name)
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		_ = s.DB.WithContext(ctx).Model(key).Update("last_error", plog.ErrorMessage).Error
		return fmt.Errorf("new-api 分组 %q 不在 group_ratio 中，请在 Key 上填写令牌所属的 new-api 分组名", name)
	}
	plog.Success = true
	extra, _ := json.Marshal(map[string]any{"group": name, "group_ratio": mult})
	plog.Extra = string(extra)
	_ = s.DB.WithContext(ctx).Create(&plog).Error
	if err := s.DB.WithContext(ctx).Model(key).Updates(billingSyncedUpdates(key, mult, now)).Error; err != nil {
		return err
	}
	return s.RefreshKeyHealth(ctx, key.ID)
}

func (s *Service) RefreshBilling(ctx context.Context, keyID uint) error {
	var key domain.PlatformKey
	if err := s.DB.WithContext(ctx).Preload("Upstream").First(&key, keyID).Error; err != nil {
		return err
	}
	if key.Upstream == nil {
		return fmt.Errorf("upstream missing")
	}
	if !domain.KindHasBilling(key.Upstream.Kind) {
		return fmt.Errorf("该上游不是 sub2api / new-api，无法同步分组倍率")
	}
	now := time.Now()
	if key.BillingUnsupported && key.BillingBackoffUntil != nil && key.BillingBackoffUntil.After(now) {
		return fmt.Errorf("billing unsupported until %s", key.BillingBackoffUntil.Format(time.RFC3339))
	}
	apiKey, err := s.decrypt(&key)
	if err != nil {
		return err
	}
	if key.Upstream.Kind == domain.KindNewAPI {
		return s.refreshNewAPIBilling(ctx, &key, apiKey, now)
	}
	start := time.Now()
	res, err := s.Client.GetJSON(ctx, key.Upstream.BaseURL, "/v1/sub2api/billing", apiKey)
	lat := int(time.Since(start).Milliseconds())
	plog := domain.ProbeLog{PlatformKeyID: key.ID, Kind: domain.ProbeBilling, LatencyMs: lat}
	if err != nil {
		plog.ErrorMessage = err.Error()
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return err
	}
	plog.StatusCode = res.Status
	if res.Status == 404 {
		backoff := now.Add(24 * time.Hour)
		plog.ErrorMessage = "billing endpoint unsupported"
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return s.DB.WithContext(ctx).Model(&key).Updates(map[string]any{
			"billing_unsupported":   true,
			"billing_backoff_until": backoff,
			"last_error":            "billing unsupported (404)",
		}).Error
	}
	if res.Status < 200 || res.Status >= 300 {
		plog.ErrorMessage = truncate(string(res.Body), 500)
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		_ = s.DB.WithContext(ctx).Model(&key).Update("last_error", plog.ErrorMessage).Error
		return fmt.Errorf("billing status %d", res.Status)
	}
	mult, ok := upstream.ParseBillingMultiplier(res.Body)
	if !ok {
		plog.ErrorMessage = "effective_rate_multiplier missing"
		_ = s.DB.WithContext(ctx).Create(&plog).Error
		return fmt.Errorf("effective_rate_multiplier missing")
	}
	plog.Success = true
	_ = s.DB.WithContext(ctx).Create(&plog).Error
	if err := s.DB.WithContext(ctx).Model(&key).Updates(billingSyncedUpdates(&key, mult, now)).Error; err != nil {
		return err
	}
	return s.RefreshKeyHealth(ctx, key.ID)
}

func billingSyncedUpdates(key *domain.PlatformKey, rate float64, now time.Time) map[string]any {
	u := map[string]any{
		"rate_multiplier":       rate,
		"rate_synced_at":        now,
		"billing_unsupported":   false,
		"billing_backoff_until": nil,
		"last_error":            "",
	}
	if key != nil && key.Upstream != nil {
		if tag := strings.TrimSpace(key.NameTag); tag != "" {
			u["name"] = domain.ComposeKeyName(key.Upstream.Name, tag, rate)
		}
	}
	return u
}

func (s *Service) ProbeAllEnabled(ctx context.Context, skipRecent time.Duration) (int, int, int) {
	return s.ProbeFiltered(ctx, true, nil, skipRecent)
}

func (s *Service) ProbeFiltered(ctx context.Context, deep bool, upstreamID *uint, skipRecent time.Duration) (int, int, int) {
	q := s.DB.WithContext(ctx).Where("status = ?", domain.StatusEnabled)
	if upstreamID != nil && *upstreamID > 0 {
		q = q.Where("upstream_id = ?", *upstreamID)
	}
	var keys []domain.PlatformKey
	if err := q.Find(&keys).Error; err != nil {
		log.Printf("probe all: list keys: %v", err)
		return 0, 0, 0
	}
	win := skipRecent
	for i := range keys {
		if d := keys[i].ProbeEvery(skipRecent); d > win {
			win = d
		}
	}
	recent := s.recentRequestAt(ctx, keys, win)
	probed := s.lastProbeAt(ctx, keys)
	ok, fail, skipped := 0, 0, 0
	for _, k := range keys {
		every := k.ProbeEvery(skipRecent)
		if skipRecent > 0 && every > 0 {
			if t, hit := probed[k.ID]; hit && time.Since(t) < every {
				skipped++
				continue
			}
			if t, hit := recent[k.ID]; hit && time.Since(t) < every {
				skipped++
				continue
			}
			if k.LastRequestAt != nil && time.Since(*k.LastRequestAt) < every {
				skipped++
				continue
			}
		}
		if s.throttled(ctx, "probe", k.ID, 30*time.Second) {
			continue
		}
		out, err := s.ProbeKey(ctx, k.ID, deep)
		if err != nil || out == nil || !out.Success {
			fail++
			continue
		}
		ok++
	}
	return ok, fail, skipped
}

func (s *Service) lastProbeAt(ctx context.Context, keys []domain.PlatformKey) map[uint]time.Time {
	out := make(map[uint]time.Time)
	if len(keys) == 0 {
		return out
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	var rows []struct {
		PlatformKeyID uint             `gorm:"column:platform_key_id"`
		LastAt        domain.LooseTime `gorm:"column:last_at"`
	}
	// Balance / billing / models ticks must not suppress health probes, or
	// the 15-minute channel-score window goes empty.
	_ = s.DB.WithContext(ctx).Model(&domain.ProbeLog{}).
		Select("platform_key_id, MAX(created_at) as last_at").
		Where("platform_key_id IN ? AND kind IN ?", ids, []string{domain.ProbeLight, domain.ProbeDeep}).
		Group("platform_key_id").
		Scan(&rows).Error
	for _, r := range rows {
		if t := r.LastAt.Time(); !t.IsZero() {
			out[r.PlatformKeyID] = t
		}
	}
	return out
}

func (s *Service) recentRequestAt(ctx context.Context, keys []domain.PlatformKey, window time.Duration) map[uint]time.Time {
	out := make(map[uint]time.Time)
	if window <= 0 || len(keys) == 0 {
		return out
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	var rows []struct {
		PlatformKeyID uint             `gorm:"column:platform_key_id"`
		LastAt        domain.LooseTime `gorm:"column:last_at"`
	}
	_ = s.DB.WithContext(ctx).Model(&domain.RequestLog{}).
		Select("platform_key_id, MAX(created_at) as last_at").
		Where("platform_key_id IN ? AND created_at >= ?", ids, time.Now().Add(-window)).
		Group("platform_key_id").
		Scan(&rows).Error
	for _, r := range rows {
		if t := r.LastAt.Time(); !t.IsZero() {
			out[r.PlatformKeyID] = t
		}
	}
	return out
}

func (s *Service) ObserveRequest(ctx context.Context, keyID uint, success bool, status int, errMsg string) {
	if keyID == 0 {
		return
	}
	health := domain.HealthDown
	if success {
		health = domain.HealthHealthy
	} else if status >= 400 && status < 500 && status != 401 && status != 403 {
		health = domain.HealthDegraded
	}
	updates := map[string]any{
		"last_request_at": time.Now(),
		"health_status":   health,
	}
	if success {
		updates["last_error"] = ""
	} else if msg := truncate(errMsg, 500); msg != "" {
		updates["last_error"] = msg
	}
	_ = s.DB.WithContext(ctx).Model(&domain.PlatformKey{}).Where("id = ?", keyID).Updates(updates).Error
	_ = s.RefreshKeyHealth(ctx, keyID)
}

const PulseBuckets = 60
const CacheWindow = 15 * time.Minute

// probeDegradedMs matches sub2api V1: a successful check slower than 6s is degraded.
const probeDegradedMs = 6000

type PulseCell struct {
	Start         time.Time `json:"start"`
	State         string    `json:"state"`
	Ok            int       `json:"ok"`
	Fail          int       `json:"fail"`
	LastLatencyMs int       `json:"last_latency_ms,omitempty"`
	LatencyP50Ms  int       `json:"latency_p50_ms,omitempty"`
	Score         int       `json:"score"`
}

type KeyCache struct {
	Rate    float64 `json:"cache_rate"`
	Samples int     `json:"cache_samples"`
}

type pulseBucket struct {
	ok, fail int
	lats     []int
	lastAt   time.Time
	lastLat  int
}

func (s *Service) HealthPulses(ctx context.Context, keyIDs []uint) map[uint][]PulseCell {
	out := make(map[uint][]PulseCell, len(keyIDs))
	if len(keyIDs) == 0 {
		return out
	}
	end := time.Now().Truncate(time.Minute)
	start := end.Add(-time.Duration(PulseBuckets-1) * time.Minute)

	buckets := make(map[uint][]pulseBucket, len(keyIDs))
	for _, id := range keyIDs {
		buckets[id] = make([]pulseBucket, PulseBuckets)
	}

	add := func(keyID uint, at time.Time, success bool, latency int) {
		if at.Before(start) {
			return
		}
		b := buckets[keyID]
		if b == nil {
			b = make([]pulseBucket, PulseBuckets)
			buckets[keyID] = b
		}
		idx := int(at.Sub(start) / time.Minute)
		if idx < 0 {
			idx = 0
		}
		if idx >= PulseBuckets {
			idx = PulseBuckets - 1
		}
		cell := &b[idx]
		if success {
			cell.ok++
		} else {
			cell.fail++
		}
		if latency > 0 {
			cell.lats = append(cell.lats, latency)
		}
		if cell.lastAt.IsZero() || !at.Before(cell.lastAt) {
			cell.lastAt = at
			cell.lastLat = latency
		}
	}

	var probes []domain.ProbeLog
	_ = s.DB.WithContext(ctx).
		Select("platform_key_id, success, latency_ms, created_at").
		Where("platform_key_id IN ? AND created_at >= ? AND kind IN ?", keyIDs, start, []string{domain.ProbeLight, domain.ProbeDeep}).
		Find(&probes).Error
	for _, p := range probes {
		add(p.PlatformKeyID, p.CreatedAt, p.Success, p.LatencyMs)
	}

	var reqs []domain.RequestLog
	_ = s.DB.WithContext(ctx).
		Select("platform_key_id, success, duration_ms, created_at").
		Where("platform_key_id IN ? AND created_at >= ?", keyIDs, start).
		Find(&reqs).Error
	for _, r := range reqs {
		if r.PlatformKeyID == nil {
			continue
		}
		add(*r.PlatformKeyID, r.CreatedAt, r.Success, r.DurationMs)
	}

	for _, id := range keyIDs {
		cells := make([]PulseCell, PulseBuckets)
		b := buckets[id]
		for i := 0; i < PulseBuckets; i++ {
			src := pulseBucket{}
			if b != nil {
				src = b[i]
			}
			cell := PulseCell{
				Start:         start.Add(time.Duration(i) * time.Minute),
				Ok:            src.ok,
				Fail:          src.fail,
				LastLatencyMs: src.lastLat,
				LatencyP50Ms:  medianInt(src.lats),
			}
			cell.State, cell.Score = pulseScore(cell.Ok, cell.Fail, cell.LatencyP50Ms)
			cells[i] = cell
		}
		out[id] = cells
	}
	return out
}

func (s *Service) KeyCacheRates(ctx context.Context, keyIDs []uint) map[uint]KeyCache {
	out := make(map[uint]KeyCache, len(keyIDs))
	if len(keyIDs) == 0 {
		return out
	}
	var rows []domain.RequestLog
	_ = s.DB.WithContext(ctx).
		Select("platform_key_id, input_tokens, cache_read_tokens, cache_creation_tokens").
		Where("platform_key_id IN ? AND created_at >= ?", keyIDs, time.Now().Add(-CacheWindow)).
		Find(&rows).Error
	type acc struct {
		in, cr, cc int64
		n          int
	}
	byKey := make(map[uint]*acc, len(keyIDs))
	for _, r := range rows {
		if r.PlatformKeyID == nil {
			continue
		}
		a := byKey[*r.PlatformKeyID]
		if a == nil {
			a = &acc{}
			byKey[*r.PlatformKeyID] = a
		}
		a.in += r.InputTokens
		a.cr += r.CacheReadTokens
		a.cc += r.CacheCreationTokens
		a.n++
	}
	for id, a := range byKey {
		c := KeyCache{Samples: a.n}
		den := float64(a.in + a.cr + a.cc)
		if den > 0 {
			c.Rate = float64(a.cr) / den
		}
		out[id] = c
	}
	return out
}

func pulseScore(ok, fail, p50 int) (state string, score int) {
	if ok == 0 && fail == 0 {
		return "empty", 0
	}
	if fail > 0 {
		return "bad", 0
	}
	if p50 >= probeDegradedMs {
		return "degraded", 6
	}
	return "ok", 10
}

func medianInt(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int(nil), vals...)
	sort.Ints(cp)
	return cp[(len(cp)-1)/2]
}

func (s *Service) RefreshAllBalances(ctx context.Context) (int, int) {
	ids, err := s.enabledBillingUpstreamIDs(ctx)
	if err != nil {
		log.Printf("balance all: list upstreams: %v", err)
		return 0, 0
	}
	ok, fail := 0, 0
	for _, id := range ids {
		if err := s.RefreshUpstreamBalance(ctx, id, 0); err != nil {
			fail++
			continue
		}
		ok++
	}
	return ok, fail
}

func (s *Service) enabledBillingUpstreamIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := s.DB.WithContext(ctx).
		Table("upstreams").
		Select("DISTINCT upstreams.id").
		Joins("JOIN platform_keys ON platform_keys.upstream_id = upstreams.id").
		Where("upstreams.kind IN ?", []string{domain.KindSub2API, domain.KindNewAPI}).
		Where("platform_keys.status = ?", domain.StatusEnabled).
		Pluck("upstreams.id", &ids).Error
	return ids, err
}

// enabledSub2APIKeys returns enabled keys whose upstream kind exposes
// balance / billing endpoints (sub2api and new-api).
func (s *Service) enabledSub2APIKeys(ctx context.Context) ([]domain.PlatformKey, error) {
	var keys []domain.PlatformKey
	err := s.DB.WithContext(ctx).
		Table("platform_keys").
		Select("platform_keys.*").
		Joins("JOIN upstreams ON upstreams.id = platform_keys.upstream_id").
		Where("platform_keys.status = ?", domain.StatusEnabled).
		Where("upstreams.kind IN ?", []string{domain.KindSub2API, domain.KindNewAPI}).
		Find(&keys).Error
	return keys, err
}

func (s *Service) RefreshAllBilling(ctx context.Context) (int, int) {
	keys, err := s.enabledSub2APIKeys(ctx)
	if err != nil {
		log.Printf("billing all: list keys: %v", err)
		return 0, 0
	}
	ok, fail := 0, 0
	now := time.Now()
	for _, k := range keys {
		if k.BillingUnsupported && k.BillingBackoffUntil != nil && k.BillingBackoffUntil.After(now) {
			continue
		}
		if err := s.RefreshBilling(ctx, k.ID); err != nil {
			fail++
			continue
		}
		ok++
	}
	return ok, fail
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}

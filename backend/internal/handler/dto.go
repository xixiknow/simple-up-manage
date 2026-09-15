package handler

import (
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"time"
)

type upstreamDTO struct {
	Summary       *upstreamSummary `json:"summary,omitempty"`
	ID            uint             `json:"id"`
	Name          string           `json:"name"`
	BaseURL       string           `json:"base_url"`
	Kind          string           `json:"kind"`
	Protocols     []string         `json:"protocols"`
	Status        string           `json:"status"`
	Note          string           `json:"note"`
	NewAPIUserID  int              `json:"new_api_user_id"`
	Concurrency   int              `json:"concurrency"`
	HealthStatus  string           `json:"health_status"`
	CooldownUntil *time.Time       `json:"cooldown_until"`
	LastError     string           `json:"last_error"`
	LastBalance   *float64         `json:"last_balance"`
	LastBalanceAt *time.Time       `json:"last_balance_at"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type upstreamSummary struct {
	KeyCount      int            `json:"key_count"`
	AbnormalCount int            `json:"abnormal_count"`
	HealthCounts  map[string]int `json:"health_counts"`
	LastRequestAt *time.Time     `json:"last_request_at"`
}

func toUpstreamDTO(u domain.Upstream) upstreamDTO {
	if u.HealthStatus == domain.HealthCooldown && (u.CooldownUntil == nil || !u.CooldownUntil.After(time.Now())) {
		u.HealthStatus = domain.HealthHealthy
		u.CooldownUntil = nil
	}
	return upstreamDTO{
		ID:            u.ID,
		Name:          u.Name,
		BaseURL:       u.BaseURL,
		Kind:          u.Kind,
		Protocols:     u.ProtocolList(),
		Status:        u.Status,
		Note:          u.Note,
		NewAPIUserID:  u.NewAPIUserID,
		Concurrency:   u.Concurrency,
		HealthStatus:  u.HealthStatus,
		CooldownUntil: u.CooldownUntil,
		LastError:     u.LastError,
		LastBalance:   u.LastBalance,
		LastBalanceAt: u.LastBalanceAt,
		CreatedAt:     u.CreatedAt,
		UpdatedAt:     u.UpdatedAt,
	}
}

type pulseCell struct {
	Start         time.Time `json:"start"`
	State         string    `json:"state"`
	Ok            int       `json:"ok"`
	Fail          int       `json:"fail"`
	LastLatencyMs int       `json:"last_latency_ms,omitempty"`
	LatencyP50Ms  int       `json:"latency_p50_ms,omitempty"`
	Score         int       `json:"score"`
}

type scoreMeta struct {
	Samples    int      `json:"samples"`
	Success    float64  `json:"success"`
	LatencyP50 int      `json:"latency_p50,omitempty"`
	Cache      *float64 `json:"cache,omitempty"`
	LowSample  bool     `json:"low_sample"`
	Terms      []string `json:"terms"`
}

type keyDTO struct {
	Protocols           []string    `json:"protocols"`
	EffectiveProtocols  []string    `json:"effective_protocols"`
	ID                  uint        `json:"id"`
	UpstreamID          uint        `json:"upstream_id"`
	UpstreamName        string      `json:"upstream_name,omitempty"`
	UpstreamKind        string      `json:"upstream_kind,omitempty"`
	Name                string      `json:"name"`
	NameTag             string      `json:"name_tag"`
	KeyPreview          string      `json:"key_preview"`
	Status              string      `json:"status"`
	Concurrency         int         `json:"concurrency"`
	RPMLimit            int         `json:"rpm_limit"`
	MaxConcurrency      int         `json:"max_concurrency"`
	LastBalance         *float64    `json:"last_balance"`
	LastBalanceAt       *time.Time  `json:"last_balance_at"`
	LastRequestAt       *time.Time  `json:"last_request_at,omitempty"`
	LastError           string      `json:"last_error"`
	CooldownUntil       *time.Time  `json:"cooldown_until"`
	HealthStatus        string      `json:"health_status"`
	LastProbeAt         *time.Time  `json:"last_probe_at,omitempty"`
	ProbeIntervalSec    int         `json:"probe_interval_sec"`
	HealthPulse         []pulseCell `json:"health_pulse,omitempty"`
	CacheRate           *float64    `json:"cache_rate,omitempty"`
	CacheSamples        int         `json:"cache_samples,omitempty"`
	BillingUnsupported  bool        `json:"billing_unsupported"`
	BillingBackoffUntil *time.Time  `json:"billing_backoff_until,omitempty"`
	LastModels          []string    `json:"last_models,omitempty"`
	LastModelsAt        *time.Time  `json:"last_models_at,omitempty"`
	ModelsCount         int         `json:"models_count"`
	RateMultiplier      float64     `json:"rate_multiplier"`
	RateSyncedAt        *time.Time  `json:"rate_synced_at,omitempty"`
	BillingGroup        string      `json:"billing_group,omitempty"`
	ChannelScore        *int        `json:"channel_score,omitempty"`
	ChannelScoreMeta    *scoreMeta  `json:"channel_score_meta,omitempty"`
	RouteGroups         []refDTO    `json:"route_groups"`
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"updated_at"`
}

type keyRateDTO struct {
	ID             uint    `json:"id"`
	UpstreamID     uint    `json:"upstream_id"`
	UpstreamName   string  `json:"upstream_name,omitempty"`
	Name           string  `json:"name"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

// refDTO is a compact {id,name} reference used for tags in list views.
type refDTO struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol,omitempty"`
}

func toRouteGroupRefs(groups []domain.RouteGroup) []refDTO {
	out := make([]refDTO, 0, len(groups))
	for _, g := range groups {
		out = append(out, refDTO{ID: g.ID, Name: g.Name, Protocol: g.Protocol})
	}
	return out
}

func toKeyDTO(k domain.PlatformKey) keyDTO {
	d := keyDTO{
		Protocols:           append([]string{}, domain.SplitCSV(k.Protocols)...),
		EffectiveProtocols:  k.EffectiveProtocols(),
		ID:                  k.ID,
		UpstreamID:          k.UpstreamID,
		Name:                k.Name,
		NameTag:             k.NameTag,
		KeyPreview:          k.KeyPreview,
		Status:              k.Status,
		Concurrency:         k.Concurrency,
		RPMLimit:            k.RPMLimit,
		MaxConcurrency:      k.MaxConcurrency,
		LastBalance:         k.LastBalance,
		LastBalanceAt:       k.LastBalanceAt,
		LastRequestAt:       k.LastRequestAt,
		LastError:           k.LastError,
		CooldownUntil:       k.CooldownUntil,
		HealthStatus:        k.HealthStatus,
		ProbeIntervalSec:    k.ProbeIntervalSec,
		BillingUnsupported:  k.BillingUnsupported,
		BillingBackoffUntil: k.BillingBackoffUntil,
		LastModels:          []string(k.LastModels),
		LastModelsAt:        k.LastModelsAt,
		ModelsCount:         len(k.LastModels),
		RateMultiplier:      k.RateMultiplier,
		RateSyncedAt:        k.RateSyncedAt,
		BillingGroup:        k.BillingGroup,
		RouteGroups:         []refDTO{},
		CreatedAt:           k.CreatedAt,
		UpdatedAt:           k.UpdatedAt,
	}
	if k.Upstream != nil {
		d.UpstreamName = k.Upstream.Name
		d.UpstreamKind = k.Upstream.Kind
		d.Concurrency = k.Upstream.Concurrency
		d.LastBalance = k.Upstream.LastBalance
		d.LastBalanceAt = k.Upstream.LastBalanceAt
	}
	return d
}

type consumerDTO struct {
	ID          uint       `json:"id"`
	Name        string     `json:"name"`
	Key         string     `json:"key,omitempty"`
	KeyPreview  string     `json:"key_preview"`
	Status      string     `json:"status"`
	QuotaUSD    float64    `json:"quota_usd"`
	QuotaUsed   float64    `json:"quota_used"`
	RPM         int        `json:"rpm"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	RouteGroups []refDTO   `json:"route_groups"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func toConsumerDTO(k domain.ConsumerKey, includeRaw bool) consumerDTO {
	d := consumerDTO{
		ID:          k.ID,
		Name:        k.Name,
		KeyPreview:  k.KeyPrefix,
		Status:      k.Status,
		QuotaUSD:    k.QuotaUSD,
		QuotaUsed:   k.QuotaUsed,
		RPM:         k.RPM,
		LastUsedAt:  k.LastUsedAt,
		RouteGroups: []refDTO{},
		CreatedAt:   k.CreatedAt,
		UpdatedAt:   k.UpdatedAt,
	}
	if d.KeyPreview == "" {
		d.KeyPreview = crypto.KeyPreview(k.Key)
	}
	if includeRaw {
		d.Key = k.Key
	}
	return d
}

type logDTO struct {
	ID                  uint      `json:"id"`
	RequestID           string    `json:"request_id"`
	ConsumerKeyID       *uint     `json:"consumer_key_id"`
	UpstreamID          *uint     `json:"upstream_id"`
	PlatformKeyID       *uint     `json:"platform_key_id"`
	Protocol            string    `json:"protocol"`
	Model               string    `json:"model"`
	Path                string    `json:"path"`
	ClientIP            string    `json:"client_ip"`
	StatusCode          int       `json:"status_code"`
	Success             bool      `json:"success"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	TTFTMs              int       `json:"ttft_ms"`
	DurationMs          int       `json:"duration_ms"`
	InFlight            bool      `json:"in_flight"`
	Stream              bool      `json:"stream"`
	StreamKnown         bool      `json:"stream_known"`
	CostUSD             *float64  `json:"cost_usd"`
	ErrorMessage        string    `json:"error_message"`
	FailureScope        string    `json:"failure_scope"`
	FailureAction       string    `json:"failure_action"`
	SelectionTrace      string    `json:"selection_trace"`
	CreatedAt           time.Time `json:"created_at"`
	UpstreamName        string    `json:"upstream_name,omitempty"`
	ConsumerName        string    `json:"consumer_name,omitempty"`
}

type logDetailDTO struct {
	logDTO
	Attempts          []domain.RequestAttempt `json:"attempts"`
	RequestHeaders    string                  `json:"request_headers"`
	RequestBody       string                  `json:"request_body"`
	RequestBodyTrunc  bool                    `json:"request_body_truncated"`
	ResponseHeaders   string                  `json:"response_headers"`
	ResponseBody      string                  `json:"response_body"`
	ResponseBodyTrunc bool                    `json:"response_body_truncated"`
}

func toLogDTO(l domain.RequestLog, upstreamName, consumerName string) logDTO {
	return logDTO{
		ID:                  l.ID,
		RequestID:           l.RequestID,
		ConsumerKeyID:       l.ConsumerKeyID,
		UpstreamID:          l.UpstreamID,
		PlatformKeyID:       l.PlatformKeyID,
		Protocol:            l.Protocol,
		Model:               l.Model,
		Path:                l.Path,
		ClientIP:            l.ClientIP,
		StatusCode:          l.StatusCode,
		Success:             l.Success,
		InputTokens:         l.InputTokens,
		OutputTokens:        l.OutputTokens,
		CacheReadTokens:     l.CacheReadTokens,
		CacheCreationTokens: l.CacheCreationTokens,
		TTFTMs:              l.TTFTMs,
		DurationMs:          l.DurationMs,
		InFlight:            l.InFlight,
		Stream:              l.Stream,
		StreamKnown:         l.StreamKnown,
		CostUSD:             l.CostUSD,
		ErrorMessage:        l.ErrorMessage,
		FailureScope:        l.FailureScope,
		FailureAction:       l.FailureAction,
		SelectionTrace:      l.SelectionTrace,
		CreatedAt:           l.CreatedAt,
		UpstreamName:        upstreamName,
		ConsumerName:        consumerName,
	}
}

func toLogDetailDTO(l domain.RequestLog, upstreamName, consumerName string) logDetailDTO {
	return logDetailDTO{
		logDTO:            toLogDTO(l, upstreamName, consumerName),
		RequestHeaders:    l.RequestHeaders,
		RequestBody:       l.RequestBody,
		RequestBodyTrunc:  l.RequestBodyTrunc,
		ResponseHeaders:   l.ResponseHeaders,
		ResponseBody:      l.ResponseBody,
		ResponseBodyTrunc: l.ResponseBodyTrunc,
	}
}

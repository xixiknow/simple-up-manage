package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	KindSub2API         = "sub2api"
	KindNewAPI          = "new_api"
	KindOpenAICompat    = "openai_compat"
	KindAnthropicCompat = "anthropic_compat"
	ProtocolOpenAI      = "openai"
	ProtocolAnthropic   = "anthropic"
	StatusEnabled       = "enabled"
	StatusDisabled      = "disabled"
	HealthHealthy       = "healthy"
	HealthDegraded      = "degraded"
	HealthDown          = "down"
	HealthCooldown      = "cooldown"
	HealthLowBalance    = "low_balance"
	HealthDisabled      = "disabled"
	ProbeLight          = "light"
	ProbeDeep           = "deep"
	ProbeBalance        = "balance"
	ProbeBilling        = "billing"
	ProbeModels         = "models"
	RateChangeUp        = "up"
	RateChangeDown      = "down"
	RateChangeBilling   = "billing"
	RateChangeManual    = "manual"
)

type Upstream struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Name      string `gorm:"size:128;not null" json:"name"`
	BaseURL   string `gorm:"size:512;not null" json:"base_url"`
	Kind      string `gorm:"size:32;not null" json:"kind"`
	Protocols string `gorm:"size:128;not null" json:"-"`
	Status    string `gorm:"size:16;not null;default:enabled" json:"status"`
	Note      string `gorm:"size:1024" json:"note"`
	// Concurrency is the shared in-flight request cap for every key of this
	// provider. 0 means unlimited. Upstream APIs do not expose a reliable value,
	// so this is operator-set.
	Concurrency   int        `gorm:"not null;default:0" json:"concurrency"`
	HealthStatus  string     `gorm:"size:32;not null;default:healthy" json:"health_status"`
	CooldownUntil *time.Time `json:"cooldown_until"`
	LastError     string     `gorm:"type:text" json:"last_error"`
	LastBalance   *float64   `gorm:"type:decimal(20,8)" json:"last_balance"`
	LastBalanceAt *time.Time `json:"last_balance_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`

	Keys []PlatformKey `gorm:"constraint:OnDelete:CASCADE" json:"-"`
}

func (u *Upstream) ProtocolList() []string {
	return SplitCSV(u.Protocols)
}

func (u *Upstream) Supports(protocol string) bool {
	for _, p := range u.ProtocolList() {
		if p == protocol {
			return true
		}
	}
	return false
}

type PlatformKey struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UpstreamID uint   `gorm:"index;not null" json:"upstream_id"`
	Name       string `gorm:"size:256;not null" json:"name"`
	// NameTag is the operator-chosen middle segment of the display name
	// `{provider}-{tag}-{rate}`.
	NameTag      string `gorm:"size:64" json:"name_tag"`
	EncryptedKey string `gorm:"type:text;not null" json:"-"`
	KeyPreview   string `gorm:"size:64" json:"key_preview"`
	// RateMultiplier is the upstream's price multiplier for this key. Synced from
	// sub2api / new-api when available, otherwise operator-entered. Defaults to 1.
	RateMultiplier float64    `gorm:"type:decimal(12,6);not null;default:1" json:"rate_multiplier"`
	RateSyncedAt   *time.Time `json:"rate_synced_at"`
	// BillingGroup is the new-api group the token belongs to (e.g. "default",
	// "vip"); used to look up group_ratio when syncing. Empty means "default".
	BillingGroup string `gorm:"size:128" json:"billing_group"`
	Status       string `gorm:"size:16;not null;default:enabled" json:"status"`
	// Concurrency / LastBalance / LastBalanceAt remain on the table for older
	// databases; live values live on Upstream. All keys of a provider share one
	// concurrency cap and one balance.
	Concurrency    int        `gorm:"not null;default:0" json:"concurrency"`
	RPMLimit       int        `gorm:"not null;default:0" json:"rpm_limit"`
	MaxConcurrency int        `gorm:"not null;default:0" json:"max_concurrency"`
	LastBalance    *float64   `gorm:"type:decimal(20,8)" json:"last_balance"`
	LastBalanceAt  *time.Time `json:"last_balance_at"`
	LastRequestAt  *time.Time `json:"last_request_at"`
	LastError      string     `gorm:"type:text" json:"last_error"`
	CooldownUntil  *time.Time `json:"cooldown_until"`
	HealthStatus   string     `gorm:"size:32;not null;default:healthy" json:"health_status"`
	// ProbeIntervalSec is this key's scheduled probe cadence. 0 follows the
	// global jobs.probe_interval.
	ProbeIntervalSec    int         `gorm:"not null;default:0" json:"probe_interval_sec"`
	BillingUnsupported  bool        `gorm:"not null;default:false" json:"billing_unsupported"`
	BillingBackoffUntil *time.Time  `json:"billing_backoff_until"`
	LastModels          JSONStrings `gorm:"type:text" json:"last_models"`
	LastModelsAt        *time.Time  `json:"last_models_at"`
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"updated_at"`

	Upstream *Upstream `gorm:"constraint:OnDelete:CASCADE" json:"-"`
}

// EffectiveBillingGroup returns the new-api group name to look up, defaulting
// to "default" when unset.
func (k *PlatformKey) EffectiveBillingGroup() string {
	if g := strings.TrimSpace(k.BillingGroup); g != "" {
		return g
	}
	return "default"
}

// ProbeEvery is how often this key should be scheduled-probed. 0 on the key
// means follow the global jobs.probe_interval.
func (k *PlatformKey) ProbeEvery(fallback time.Duration) time.Duration {
	if k != nil && k.ProbeIntervalSec > 0 {
		return time.Duration(k.ProbeIntervalSec) * time.Second
	}
	return fallback
}

// FormatRate formats a multiplier for display names: 1 → "1", 0.08 → "0.08".
func FormatRate(rate float64) string {
	s := strconv.FormatFloat(rate, 'f', 4, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

// ComposeKeyName builds `{provider}-{tag}-{rate}`.
func ComposeKeyName(provider, tag string, rate float64) string {
	provider = strings.TrimSpace(provider)
	tag = strings.TrimSpace(tag)
	return provider + "-" + tag + "-" + FormatRate(rate)
}

// InferNameTag recovers the middle segment from a display name
// `{provider}-{tag}-{rate}` when name_tag was never stored.
func InferNameTag(fullName, provider string) string {
	name := strings.TrimSpace(fullName)
	p := strings.TrimSpace(provider)
	if p != "" {
		if prefix := p + "-"; strings.HasPrefix(name, prefix) {
			name = strings.TrimPrefix(name, prefix)
		}
	}
	if i := strings.LastIndex(name, "-"); i > 0 {
		if _, err := strconv.ParseFloat(name[i+1:], 64); err == nil {
			name = name[:i]
		}
	}
	return strings.TrimSpace(name)
}

// EffectiveNameTag returns the stored tag, or infers it from Name.
func (k *PlatformKey) EffectiveNameTag(provider string) string {
	if t := strings.TrimSpace(k.NameTag); t != "" {
		return t
	}
	return InferNameTag(k.Name, provider)
}

// SupportsModel reports whether this key's last fetched model list contains the
// requested model. An empty list (never fetched / upstream has no /v1/models)
// or an empty model means "no restriction". Matching is case-insensitive.
func (k *PlatformKey) SupportsModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || len(k.LastModels) == 0 {
		return true
	}
	for _, id := range k.LastModels {
		if strings.ToLower(strings.TrimSpace(id)) == model {
			return true
		}
	}
	return false
}

type ConsumerKey struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Name       string     `gorm:"size:128;not null" json:"name"`
	Key        string     `gorm:"size:128;uniqueIndex;not null" json:"-"`
	KeyPrefix  string     `gorm:"size:64" json:"key_preview"`
	Status     string     `gorm:"size:16;not null;default:enabled" json:"status"`
	QuotaUSD   float64    `gorm:"type:decimal(20,8);not null;default:0" json:"quota_usd"`
	QuotaUsed  float64    `gorm:"type:decimal(20,8);not null;default:0" json:"quota_used"`
	RPM        int        `gorm:"not null;default:0" json:"rpm"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// RouteGroup is an operator-defined pool of platform keys. Consumer keys bind to
// route groups to restrict which platform keys may serve their traffic.
type RouteGroup struct {
	ID          uint        `gorm:"primaryKey" json:"id"`
	Name        string      `gorm:"size:128;uniqueIndex;not null" json:"name"`
	Protocol    string      `gorm:"size:16" json:"protocol"`
	Models      JSONStrings `gorm:"type:text" json:"models"`
	RateMin     *float64    `gorm:"type:decimal(12,6)" json:"rate_min"`
	RateMax     *float64    `gorm:"type:decimal(12,6)" json:"rate_max"`
	Description string      `gorm:"size:1024" json:"description"`
	Status      string      `gorm:"size:16;not null;default:enabled" json:"status"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// MatchesProtocol reports whether the group accepts the given protocol.
// An empty protocol on the group means "any".
func (g *RouteGroup) MatchesProtocol(protocol string) bool {
	return g.Protocol == "" || g.Protocol == protocol
}

// RouteGroupKey links a platform key into a route group.
type RouteGroupKey struct {
	RouteGroupID  uint      `gorm:"primaryKey;autoIncrement:false" json:"route_group_id"`
	PlatformKeyID uint      `gorm:"primaryKey;autoIncrement:false;index" json:"platform_key_id"`
	CreatedAt     time.Time `json:"created_at"`
}

// ConsumerRouteGroup binds a consumer key to a route group.
type ConsumerRouteGroup struct {
	ConsumerKeyID uint      `gorm:"primaryKey;autoIncrement:false" json:"consumer_key_id"`
	RouteGroupID  uint      `gorm:"primaryKey;autoIncrement:false;index" json:"route_group_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type RequestLog struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	RequestID           string     `gorm:"size:64;index" json:"request_id"`
	ConsumerKeyID       *uint      `gorm:"index" json:"consumer_key_id"`
	UpstreamID          *uint      `gorm:"index" json:"upstream_id"`
	PlatformKeyID       *uint      `gorm:"index" json:"platform_key_id"`
	Protocol            string     `gorm:"size:16;index" json:"protocol"`
	Model               string     `gorm:"size:128;index" json:"model"`
	Path                string     `gorm:"size:256" json:"path"`
	ClientIP            string     `gorm:"size:64;index" json:"client_ip"`
	StatusCode          int        `json:"status_code"`
	Success             bool       `gorm:"index" json:"success"`
	InputTokens         int64      `json:"input_tokens"`
	OutputTokens        int64      `json:"output_tokens"`
	CacheReadTokens     int64      `json:"cache_read_tokens"`
	CacheCreationTokens int64      `json:"cache_creation_tokens"`
	TTFTMs              int        `json:"ttft_ms"`
	DurationMs          int        `json:"duration_ms"`
	InFlight            bool       `gorm:"index" json:"in_flight"`
	Stream              bool       `json:"stream"`
	StreamKnown         bool       `gorm:"not null;default:false" json:"stream_known"`
	LogRevision         uint64     `gorm:"not null;default:0" json:"-"`
	CompletedAt         *time.Time `gorm:"index" json:"completed_at"`
	CostUSD             *float64   `gorm:"type:decimal(20,8)" json:"cost_usd"`
	ErrorMessage        string     `gorm:"type:text" json:"error_message"`
	FailureScope        string     `gorm:"size:32;index" json:"failure_scope"`
	FailureAction       string     `gorm:"size:32" json:"failure_action"`
	RequestHeaders      string     `gorm:"type:text" json:"request_headers"`
	RequestBody         string     `gorm:"type:text" json:"request_body"`
	RequestBodyTrunc    bool       `json:"request_body_truncated"`
	ResponseHeaders     string     `gorm:"type:text" json:"response_headers"`
	ResponseBody        string     `gorm:"type:text" json:"response_body"`
	ResponseBodyTrunc   bool       `json:"response_body_truncated"`
	CreatedAt           time.Time  `gorm:"index" json:"created_at"`
}

// KeyModelCooldown isolates model-specific throttling without disabling the
// same credential for unrelated models.
type KeyModelCooldown struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	PlatformKeyID uint      `gorm:"uniqueIndex:idx_key_model_cooldown;not null" json:"platform_key_id"`
	Model         string    `gorm:"size:128;uniqueIndex:idx_key_model_cooldown;not null" json:"model"`
	CooldownUntil time.Time `gorm:"index;not null" json:"cooldown_until"`
	Reason        string    `gorm:"size:128" json:"reason"`
	StatusCode    int       `json:"status_code"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ProbeLog struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	PlatformKeyID uint      `gorm:"index;not null" json:"platform_key_id"`
	Kind          string    `gorm:"size:16;not null" json:"kind"`
	Success       bool      `json:"success"`
	StatusCode    int       `json:"status_code"`
	LatencyMs     int       `json:"latency_ms"`
	ErrorMessage  string    `gorm:"type:text" json:"error_message"`
	Extra         string    `gorm:"type:text" json:"extra"`
	CreatedAt     time.Time `gorm:"index" json:"created_at"`
}

// RateChangeNotice is a durable record of a key's rate_multiplier going up or
// down, so operators can review price moves after the console toast is gone.
type RateChangeNotice struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	PlatformKeyID uint       `gorm:"index;not null" json:"platform_key_id"`
	UpstreamID    uint       `gorm:"index;not null" json:"upstream_id"`
	KeyName       string     `gorm:"size:256;not null" json:"key_name"`
	UpstreamName  string     `gorm:"size:128;not null" json:"upstream_name"`
	OldRate       float64    `gorm:"type:decimal(12,6);not null" json:"old_rate"`
	NewRate       float64    `gorm:"type:decimal(12,6);not null" json:"new_rate"`
	Direction     string     `gorm:"size:8;not null" json:"direction"`
	Source        string     `gorm:"size:16;not null;index" json:"source"`
	ReadAt        *time.Time `json:"read_at"`
	CreatedAt     time.Time  `gorm:"index" json:"created_at"`
}

type JSONStrings []string

func (j JSONStrings) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal([]string(j))
}

func (j *JSONStrings) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported JSONStrings type %T", value)
	}
	if len(raw) == 0 {
		*j = nil
		return nil
	}
	return json.Unmarshal(raw, j)
}

func SplitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return NormalizeStrings(arr)
		}
	}
	parts := strings.Split(s, ",")
	return NormalizeStrings(parts)
}

func JoinCSV(items []string) string {
	return strings.Join(NormalizeStrings(items), ",")
}

func NormalizeStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, it := range items {
		it = strings.ToLower(strings.TrimSpace(it))
		if it == "" {
			continue
		}
		if _, ok := seen[it]; ok {
			continue
		}
		seen[it] = struct{}{}
		out = append(out, it)
	}
	return out
}

func ValidKind(kind string) bool {
	switch kind {
	case KindSub2API, KindNewAPI, KindOpenAICompat, KindAnthropicCompat:
		return true
	default:
		return false
	}
}

// KindHasBilling reports whether the upstream kind exposes balance / rate
// endpoints that the background jobs should poll.
func KindHasBilling(kind string) bool {
	return kind == KindSub2API || kind == KindNewAPI
}

func ValidStatus(status string) bool {
	return status == StatusEnabled || status == StatusDisabled
}

func ValidProtocol(p string) bool {
	return p == ProtocolOpenAI || p == ProtocolAnthropic
}

func ValidHealth(h string) bool {
	switch h {
	case HealthHealthy, HealthDegraded, HealthDown, HealthCooldown, HealthLowBalance, HealthDisabled:
		return true
	default:
		return false
	}
}

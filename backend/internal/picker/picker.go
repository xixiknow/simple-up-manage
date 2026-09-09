package picker

import (
	"context"
	"errors"

	"simple-up-manage/internal/domain"
)

var ErrNoUpstream = errors.New("no enabled upstream matching protocol")

type Request struct {
	Protocol string
	Model    string
	Session  string
	Exclude  []uint
	// AllowKeys restricts candidates to this set of platform key IDs. nil means
	// unrestricted; an empty non-nil map rejects every key.
	AllowKeys map[uint]struct{}
	// DriftKeys are route-group members whose rate_multiplier has drifted out of
	// every matching group's reference range. They are never picked, but are
	// reported with skip reason "route_rate_drift" instead of the generic
	// "not_in_route_group" so operators can tell the two apart.
	DriftKeys map[uint]struct{}
}

type Candidate struct {
	Key           *domain.PlatformKey `json:"-"`
	Upstream      *domain.Upstream    `json:"-"`
	KeyID         uint                `json:"key_id"`
	KeyName       string              `json:"key_name"`
	KeyPreview    string              `json:"key_preview"`
	UpstreamID    uint                `json:"upstream_id"`
	UpstreamName  string              `json:"upstream_name"`
	Eligible      bool                `json:"eligible"`
	SkipReason    string              `json:"skip_reason,omitempty"`
	Quality       float64             `json:"quality"`
	InBand        bool                `json:"in_band"`
	Selected      bool                `json:"selected"`
	Rate          float64             `json:"rate"`
	EffectiveCost float64             `json:"effective_cost"`
	SuccessRate   float64             `json:"success_rate"`
	CacheRate     float64             `json:"cache_rate"`
	TTFTp50       int                 `json:"ttft_p50"`
	Samples       int                 `json:"samples"`
	HealthStatus  string              `json:"health_status"`
	LastBalance   *float64            `json:"last_balance,omitempty"`
}

type Picker interface {
	Pick(ctx context.Context, req Request) (*domain.PlatformKey, *domain.Upstream, error)
	Explain(ctx context.Context, req Request) ([]Candidate, error)
	Settings() domain.SchedulerSettings
	UpdateSettings(ctx context.Context, next domain.SchedulerSettings) error
	Reload()
	Observe(ctx context.Context, keyID uint, model string, success bool, input, cacheRead, cacheCreate int64, ttftMs int)
	SetSticky(ctx context.Context, protocol, session string, keyID uint)
	Cooldown(ctx context.Context, keyID uint)
	MarkLowBalance(ctx context.Context, keyID uint)
}

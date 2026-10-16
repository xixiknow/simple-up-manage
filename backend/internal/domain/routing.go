package domain

import "time"

// FailureFirstTokenTimeout marks attempts that never produced output within
// the first-token wait. They are weighted heavier in circuit accounting.
const FailureFirstTokenTimeout = "first_token_timeout"

// Availability failure actions: the key was unreachable because of billing or
// credential state (arrears, disabled key, quota-capped rate limit), not
// because of upstream quality. Circuits and cooldowns already punish these
// and release them on recharge; score windows must not count them, or a
// previously good key stays unpickable long after the balance is restored.
const (
	ActionKeyQuotaExhausted  = "key_quota_exhausted"
	ActionCredentialDisabled = "credential_disabled"
	ActionQuotaRateLimited   = "quota_rate_limited"
)

// IsAvailabilityFailureAction reports whether a failure action is a billing or
// credential state rather than a quality signal. Plain authentication
// failures (cooldown_key) are deliberately excluded: a broken key must look
// bad in the score.
func IsAvailabilityFailureAction(action string) bool {
	switch action {
	case ActionKeyQuotaExhausted, ActionCredentialDisabled, ActionQuotaRateLimited:
		return true
	}
	return false
}

// AvailabilityFailureActions lists every excluded action, for SQL filters.
func AvailabilityFailureActions() []string {
	return []string{ActionKeyQuotaExhausted, ActionCredentialDisabled, ActionQuotaRateLimited}
}

// RoutingCircuit is independent of diagnostic probe health. Scope is either a
// key-wide authentication/legacy gate or a single business request dimension.
type RoutingCircuit struct {
	Generation    uint64  `gorm:"not null;default:0" json:"-"`
	Scope         string  `gorm:"primaryKey;size:80" json:"scope"`
	PlatformKeyID uint    `gorm:"index" json:"platform_key_id"`
	Open          bool    `json:"open"`
	Failures      int     `json:"failures"`
	FailureTimes  []int64 `gorm:"serializer:json;type:text" json:"-"`
	// AttemptTimes records every non-neutral outcome in the rolling window
	// (successes included) so the open threshold can scale with traffic.
	AttemptTimes []int64    `gorm:"serializer:json;type:text" json:"-"`
	OpenedAt     time.Time  `json:"opened_at"`
	Until        time.Time  `json:"until"`
	BackoffSec   int        `json:"backoff_sec"`
	Reason       string     `gorm:"size:128" json:"reason"`
	Lease        string     `gorm:"size:36" json:"-"`
	LeaseUntil   time.Time  `json:"lease_until"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Protocol     string     `json:"protocol"`
	Model        string     `json:"model"`
	Path         string     `json:"path"`
	Stream       bool       `json:"stream"`
	RecoveryAt   *time.Time `json:"recovery_at,omitempty"`
	CheckLease   string     `gorm:"size:36" json:"-"`
	CheckUntil   *time.Time `json:"-"`
	CheckAt      *time.Time `json:"check_at,omitempty"`
	CheckOK      bool       `json:"check_ok"`
	CheckError   string     `json:"check_error,omitempty"`
	NextCheckAt  *time.Time `json:"next_check_at,omitempty"`
	CheckBackoff int        `json:"-"`
}

type RoutingObservation struct {
	Scope     string    `gorm:"primaryKey;size:80"`
	RequestID string    `gorm:"primaryKey;size:36"`
	CreatedAt time.Time `gorm:"index"`
}

type RoutingBudget struct {
	Scope         string `gorm:"primaryKey;size:80"`
	Counter       int
	RecoveryAfter *time.Time
	UpdatedAt     time.Time
}

type RoutingMigration struct {
	ID uint `gorm:"primaryKey"`
}

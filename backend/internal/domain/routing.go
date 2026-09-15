package domain

import "time"

// RoutingCircuit is independent of diagnostic probe health. Scope is either a
// key-wide authentication/legacy gate or a single business request dimension.
type RoutingCircuit struct {
	Scope         string    `gorm:"primaryKey;size:80" json:"scope"`
	PlatformKeyID uint      `gorm:"index" json:"platform_key_id"`
	Open          bool      `json:"open"`
	Failures      int       `json:"failures"`
	FailureTimes  []int64   `gorm:"serializer:json;type:text" json:"-"`
	OpenedAt      time.Time `json:"opened_at"`
	Until         time.Time `json:"until"`
	BackoffSec    int       `json:"backoff_sec"`
	Reason        string    `gorm:"size:128" json:"reason"`
	Lease         string    `gorm:"size:36" json:"-"`
	LeaseUntil    time.Time `json:"lease_until"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type RoutingObservation struct {
	Scope     string    `gorm:"primaryKey;size:80"`
	RequestID string    `gorm:"primaryKey;size:36"`
	CreatedAt time.Time `gorm:"index"`
}

type RoutingBudget struct {
	Scope     string `gorm:"primaryKey;size:80"`
	Counter   int
	UpdatedAt time.Time
}

type RoutingMigration struct {
	ID uint `gorm:"primaryKey"`
}

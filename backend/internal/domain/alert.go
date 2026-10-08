package domain

import (
	"time"
)

// AlertSettings is the single-row (ID=1) configuration for the low-balance
// push alert. SendKey is stored AES-GCM encrypted, like platform keys.
type AlertSettings struct {
	ID      uint `gorm:"primaryKey" json:"id"`
	Enabled bool `gorm:"not null;default:false" json:"enabled"`
	// SendKey ciphertext; empty means no channel configured. The plaintext
	// never leaves the backend, only crypto.KeyPreview does.
	SendKey string `gorm:"size:512;not null;default:''" json:"-"`
	// HoursThreshold fires the alert when a provider's projected hours
	// (balance ÷ 24h burn rate) fall below it. 0 (depleted) is included.
	HoursThreshold float64 `gorm:"type:decimal(6,2);not null;default:5" json:"hours_threshold"`
	// SilenceHours is the per-provider repeat window: a provider already
	// alerted within the window is skipped; recovery resets it naturally
	// because the check only fires below the threshold.
	SilenceHours int `gorm:"not null;default:6" json:"silence_hours"`
	// SilenceState caches providerID → last alert time as JSON so the check
	// stays stateless across restarts without a dedicated table.
	SilenceState string    `gorm:"type:text;not null;default:''" json:"-"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func DefaultAlertSettings() AlertSettings {
	return AlertSettings{
		ID:             1,
		Enabled:        false,
		HoursThreshold: 5,
		SilenceHours:   6,
	}
}

func (s *AlertSettings) Normalize() {
	if s.HoursThreshold <= 0 || s.HoursThreshold > 72 {
		s.HoursThreshold = 5
	}
	if s.SilenceHours <= 0 || s.SilenceHours > 168 {
		s.SilenceHours = 6
	}
}

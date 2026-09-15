package domain

import "time"

const AttemptStatsVersion = 2

// RequestAttempt measures a single upstream attempt, never a request total.
type RequestAttempt struct {
	ID                  string    `gorm:"primaryKey;size:36" json:"id"`
	RequestLogID        uint      `gorm:"index" json:"request_log_id"`
	PlatformKeyID       uint      `gorm:"index:idx_attempt_window,priority:1" json:"platform_key_id"`
	Protocol            string    `gorm:"size:16" json:"protocol"`
	Model               string    `gorm:"size:128" json:"model"`
	Path                string    `gorm:"size:256" json:"path"`
	Stream              bool      `json:"stream"`
	StatsVersion        int       `json:"stats_version"`
	Result              string    `gorm:"size:32" json:"result"`
	StatusCode          int       `json:"status_code"`
	TTFTMs              int       `json:"ttft_ms"`
	DurationMs          int       `json:"duration_ms"`
	InputTokens         int64     `json:"input_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	StartedAt           time.Time `json:"started_at"`
	CompletedAt         time.Time `gorm:"index:idx_attempt_window,priority:2;index" json:"completed_at"`
}

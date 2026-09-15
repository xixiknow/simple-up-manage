package domain

import "time"

// LogBody describes an independent, bounded archive of received text.
type LogBody struct {
	ID            string    `gorm:"primaryKey;size:36" json:"id"`
	RequestLogID  uint      `gorm:"index;not null" json:"request_log_id"`
	AttemptID     string    `gorm:"size:36;index" json:"attempt_id,omitempty"`
	Direction     string    `gorm:"size:16" json:"direction"`
	ContentType   string    `json:"content_type"`
	Status        string    `gorm:"size:24" json:"status"`
	Reason        string    `gorm:"size:128" json:"reason,omitempty"`
	ReceivedBytes int64     `json:"received_bytes"`
	SavedBytes    int64     `json:"saved_bytes"`
	StoredBytes   int64     `json:"stored_bytes"`
	CreatedAt     time.Time `gorm:"index" json:"created_at"`
}

package domain

import "time"

// LiteLLMPrice is one parsed entry of the LiteLLM model price card (working
// table refreshed by the billing sync job). All prices are USD per token, so
// cheap cache-read prices keep full precision. Fields the source card omits
// stay nil: the billing layer falls back to the legacy cache coefficients.
type LiteLLMPrice struct {
	ID                  uint      `gorm:"primaryKey" json:"-"`
	ModelKey            string    `gorm:"size:192;uniqueIndex:idx_litellm_model;not null" json:"model_key"`
	Vendor              string    `gorm:"size:32;not null" json:"vendor"`
	Mode                string    `gorm:"size:32" json:"mode"`
	SyncedAt            time.Time `json:"synced_at"`
	InputPricePerToken  float64   `gorm:"type:decimal(20,12)" json:"input_price_per_token"`
	OutputPricePerToken float64   `gorm:"type:decimal(20,12)" json:"output_price_per_token"`

	CacheReadPricePerToken    *float64 `gorm:"type:decimal(20,12)" json:"cache_read_price_per_token,omitempty"`
	CacheWrite5mPricePerToken *float64 `gorm:"type:decimal(20,12)" json:"cache_write_5m_price_per_token,omitempty"`
	CacheWrite1hPricePerToken *float64 `gorm:"type:decimal(20,12)" json:"cache_write_1h_price_per_token,omitempty"`

	InputPriorityPerToken        *float64 `gorm:"type:decimal(20,12)" json:"input_priority_per_token,omitempty"`
	OutputPriorityPerToken       *float64 `gorm:"type:decimal(20,12)" json:"output_priority_per_token,omitempty"`
	CacheReadPriorityPerToken    *float64 `gorm:"type:decimal(20,12)" json:"cache_read_priority_per_token,omitempty"`
	CacheWrite5mPriorityPerToken *float64 `gorm:"type:decimal(20,12)" json:"cache_write_5m_priority_per_token,omitempty"`

	// Long-context ladder: total prompt tokens above the threshold multiply
	// input (and cache) prices by InputMult and output prices by OutputMult.
	LongCtxThreshold  int64   `gorm:"" json:"long_ctx_threshold,omitempty"`
	LongCtxInputMult  float64 `gorm:"type:decimal(8,4)" json:"long_ctx_input_mult,omitempty"`
	LongCtxOutputMult float64 `gorm:"type:decimal(8,4)" json:"long_ctx_output_mult,omitempty"`
}

func (LiteLLMPrice) TableName() string { return "catalog_litellm_prices" }

// LiteLLMMeta is the singleton row recording the last LiteLLM price sync.
// Hash anchors the downloaded card's content; ProbeHash remembers the remote
// change probe (commit sha) so unchanged rounds skip the multi-MB download.
type LiteLLMMeta struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Hash       string     `gorm:"size:80" json:"hash"`
	ProbeHash  string     `gorm:"size:80" json:"probe_hash"`
	ModelCount int        `json:"model_count"`
	SyncedAt   *time.Time `json:"synced_at"`
	LastError  string     `gorm:"type:text" json:"last_error,omitempty"`
}

func (LiteLLMMeta) TableName() string { return "catalog_litellm_meta" }

package dashboard

import (
	"encoding/json"
	"time"
)

const (
	FamilyRequest = "request"
	FamilyAttempt = "attempt"
	DimGlobal     = "global"
	DimGroup      = "group"
	DimProvider   = "provider"

	ReasonUnbound      = "unbound"
	ReasonMissingSale  = "missing_sale"
	ReasonMissingPrice = "missing_price"
	ReasonMissingUsage = "missing_usage"
	ReasonInterrupted  = "interrupted"
	ReasonLocalOnly    = "local_only"

	CostSourceReported  = "reported"
	CostSourceEstimated = "estimated"
	CostSourceNone      = "none"

	DefaultRenewalHorizonHours     = 48
	DefaultMinQualitySamples       = 100
	DefaultMinSuccessRate          = 0.98
	DefaultMinTTFTSamples          = 100
	DefaultMaxTTFTP95Ms            = 10000
	DefaultMinFinanceCoverage      = 0.90
	DefaultMinCommonDemandCoverage = 0.50

	FactRetention   = 30 * 24 * time.Hour
	MinuteRetention = 30 * 24 * time.Hour
	LedgerRetention = 31 * 24 * time.Hour
	ReplayRejectAge = 30 * 24 * time.Hour
	QueueLimit      = 10000
	WriteTimeout    = 5 * time.Second
)

var HistBoundsMS = []int{100, 250, 500, 1000, 2000, 3000, 5000, 7500, 10000, 15000, 20000, 30000, 60000, 120000, 300000}

type CatalogVersion struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Source      string    `gorm:"size:256" json:"source"`
	ModelCount  int       `json:"model_count"`
	PublishedAt time.Time `gorm:"index" json:"published_at"`
}

func (CatalogVersion) TableName() string { return "dash_catalog_versions" }

type CatalogVersionPrice struct {
	VersionID       uint    `gorm:"primaryKey;autoIncrement:false" json:"version_id"`
	Vendor          string  `gorm:"primaryKey;autoIncrement:false;size:32" json:"vendor"`
	ModelID         string  `gorm:"primaryKey;autoIncrement:false;size:128" json:"model_id"`
	InputCost       float64 `gorm:"type:decimal(16,8)" json:"input_cost"`
	OutputCost      float64 `gorm:"type:decimal(16,8)" json:"output_cost"`
	CacheReadCoeff  float64 `gorm:"type:decimal(8,4);not null;default:0.1" json:"cache_read_coeff"`
	CacheWriteCoeff float64 `gorm:"type:decimal(8,4);not null;default:1.25" json:"cache_write_coeff"`
}

func (CatalogVersionPrice) TableName() string { return "dash_catalog_prices" }

type RequestFact struct {
	UUID              string     `gorm:"primaryKey;size:36" json:"uuid"`
	ConsumerKeyID     uint       `gorm:"index" json:"consumer_key_id"`
	RouteGroupID      *uint      `gorm:"index" json:"route_group_id"`
	RouteGroupName    string     `gorm:"size:128" json:"route_group_name"`
	SaleMultiplier    *float64   `gorm:"type:decimal(12,6)" json:"sale_multiplier"`
	CatalogVersionID  uint       `json:"catalog_version_id"`
	Model             string     `gorm:"size:128;index" json:"model"`
	Protocol          string     `gorm:"size:16;index" json:"protocol"`
	Path              string     `gorm:"size:256" json:"path"`
	Endpoint          string     `gorm:"size:256" json:"endpoint"`
	Stream            bool       `json:"stream"`
	Source            string     `gorm:"size:16;index" json:"source"`
	StartedAt         time.Time  `gorm:"index" json:"started_at"`
	CompletedAt       *time.Time `gorm:"index" json:"completed_at"`
	Success           bool       `json:"success"`
	Interrupted       bool       `json:"interrupted"`
	HTTPAttempts      int        `json:"http_attempts"`
	InputTokens       int64      `json:"input_tokens"`
	OutputTokens      int64      `json:"output_tokens"`
	CacheReadTokens   int64      `json:"cache_read_tokens"`
	CacheWriteTokens  int64      `json:"cache_write_tokens"`
	UsageKnown        bool       `json:"usage_known"`
	InputPrice        *float64   `gorm:"type:decimal(16,8)" json:"input_price"`
	OutputPrice       *float64   `gorm:"type:decimal(16,8)" json:"output_price"`
	CacheReadCoeff    float64    `gorm:"type:decimal(8,4)" json:"cache_read_coeff"`
	CacheWriteCoeff   float64    `gorm:"type:decimal(8,4)" json:"cache_write_coeff"`
	BaseCostUSD       *float64   `gorm:"type:decimal(20,8)" json:"base_cost_usd"`
	RevenueUSD        *float64   `gorm:"type:decimal(20,8)" json:"revenue_usd"`
	Covered           bool       `json:"covered"`
	Reasons           string     `gorm:"type:text" json:"reasons"`
	FinalProviderID   *uint      `gorm:"index" json:"final_provider_id"`
	FinalProviderName string     `gorm:"size:128" json:"final_provider_name"`
	RequestLogID      uint       `json:"request_log_id"`
	TTFTMs            int        `json:"ttft_ms"`
	TTFTStatus        string     `gorm:"size:32" json:"ttft_status"`
	CreatedAt         time.Time  `json:"created_at"`
}

func (RequestFact) TableName() string { return "dash_request_facts" }

type AttemptFact struct {
	UUID             string    `gorm:"primaryKey;size:36" json:"uuid"`
	RequestUUID      string    `gorm:"index;size:36" json:"request_uuid"`
	ProviderID       uint      `gorm:"index" json:"provider_id"`
	ProviderName     string    `gorm:"size:128" json:"provider_name"`
	KeyID            uint      `gorm:"index" json:"key_id"`
	CostMultiplier   float64   `gorm:"type:decimal(12,6)" json:"cost_multiplier"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `gorm:"index" json:"completed_at"`
	Result           string    `gorm:"size:32" json:"result"`
	HTTPSent         bool      `json:"http_sent"`
	StatusCode       int       `json:"status_code"`
	TTFTMs           int       `json:"ttft_ms"`
	TTFTStatus       string    `gorm:"size:32" json:"ttft_status"`
	Protocol         string    `gorm:"size:16" json:"protocol"`
	Model            string    `gorm:"size:128" json:"model"`
	Path             string    `gorm:"size:256" json:"path"`
	Stream           bool      `json:"stream"`
	Source           string    `gorm:"size:16;index" json:"source"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	UsageKnown       bool      `json:"usage_known"`
	EstimatedCostUSD *float64  `gorm:"type:decimal(20,8)" json:"estimated_cost_usd"`
	ReportedCostUSD  *float64  `gorm:"type:decimal(20,8)" json:"reported_cost_usd"`
	ConsumptionUSD   *float64  `gorm:"type:decimal(20,8)" json:"consumption_usd"`
	CostSource       string    `gorm:"size:16" json:"cost_source"`
	BaseCostUSD      *float64  `gorm:"type:decimal(20,8)" json:"base_cost_usd"`
	CreatedAt        time.Time `json:"created_at"`
}

func (AttemptFact) TableName() string { return "dash_attempt_facts" }

type EventLedger struct {
	EventKey    string    `gorm:"primaryKey;size:80" json:"event_key"`
	Kind        string    `gorm:"size:32" json:"kind"`
	ProcessedAt time.Time `gorm:"index" json:"processed_at"`
}

func (EventLedger) TableName() string { return "dash_event_ledger" }

type MinuteAgg struct {
	ID                      uint      `gorm:"primaryKey"`
	Bucket                  time.Time `gorm:"uniqueIndex:idx_dash_min;not null"`
	Family                  string    `gorm:"size:16;uniqueIndex:idx_dash_min;not null"`
	Dim                     string    `gorm:"size:16;uniqueIndex:idx_dash_min;not null"`
	DimID                   uint      `gorm:"uniqueIndex:idx_dash_min;not null"`
	RequestsStarted         int64
	RequestsCompleted       int64
	RequestsSuccess         int64
	RequestsRetried         int64
	ProviderSuccess         int64
	ProviderFailure         int64
	InflightIntegralSec     float64 `gorm:"type:decimal(20,6)"`
	InflightPeak            int
	TTFTHistJSON            string `gorm:"type:text"`
	TTFTSamples             int64
	KnownRevenueUSD         float64 `gorm:"type:decimal(20,8)"`
	KnownCostUSD            float64 `gorm:"type:decimal(20,8)"`
	ConsumptionUSD          float64 `gorm:"type:decimal(20,8)"`
	ReportedConsumptionUSD  float64 `gorm:"type:decimal(20,8)"`
	EstimatedConsumptionUSD float64 `gorm:"type:decimal(20,8)"`
	UnknownConsumption      int64
	CoveredRevenueUSD       float64 `gorm:"type:decimal(20,8)"`
	CoveredCostUSD          float64 `gorm:"type:decimal(20,8)"`
	CoveredCount            int64
	UnboundCount            int64
	MissingSaleCount        int64
	MissingPriceCount       int64
	MissingUsageCount       int64
	InterruptedCount        int64
}

func (MinuteAgg) TableName() string { return "dash_minute_aggs" }

type DayAgg struct {
	ID                      uint      `gorm:"primaryKey"`
	Bucket                  time.Time `gorm:"uniqueIndex:idx_dash_day;not null"`
	Family                  string    `gorm:"size:16;uniqueIndex:idx_dash_day;not null"`
	Dim                     string    `gorm:"size:16;uniqueIndex:idx_dash_day;not null"`
	DimID                   uint      `gorm:"uniqueIndex:idx_dash_day;not null"`
	RequestsStarted         int64
	RequestsCompleted       int64
	RequestsSuccess         int64
	RequestsRetried         int64
	ProviderSuccess         int64
	ProviderFailure         int64
	InflightIntegralSec     float64 `gorm:"type:decimal(20,6)"`
	InflightPeak            int
	TTFTHistJSON            string `gorm:"type:text"`
	TTFTSamples             int64
	KnownRevenueUSD         float64 `gorm:"type:decimal(20,8)"`
	KnownCostUSD            float64 `gorm:"type:decimal(20,8)"`
	ConsumptionUSD          float64 `gorm:"type:decimal(20,8)"`
	ReportedConsumptionUSD  float64 `gorm:"type:decimal(20,8)"`
	EstimatedConsumptionUSD float64 `gorm:"type:decimal(20,8)"`
	UnknownConsumption      int64
	CoveredRevenueUSD       float64 `gorm:"type:decimal(20,8)"`
	CoveredCostUSD          float64 `gorm:"type:decimal(20,8)"`
	CoveredCount            int64
	UnboundCount            int64
	MissingSaleCount        int64
	MissingPriceCount       int64
	MissingUsageCount       int64
	InterruptedCount        int64
}

func (DayAgg) TableName() string { return "dash_day_aggs" }

type Gap struct {
	ID        uint      `gorm:"primaryKey"`
	StartedAt time.Time `gorm:"index"`
	EndedAt   time.Time
	Reason    string `gorm:"size:64"`
	CreatedAt time.Time
}

func (Gap) TableName() string { return "dash_gaps" }

type Settings struct {
	ID                      uint      `gorm:"primaryKey" json:"-"`
	RenewalHorizonHours     int       `gorm:"not null;default:48" json:"renewal_horizon_hours"`
	MinQualitySamples       int       `gorm:"not null;default:100" json:"min_quality_samples"`
	MinSuccessRate          float64   `gorm:"type:decimal(8,6);not null;default:0.98" json:"min_success_rate"`
	MinTTFTSamples          int       `gorm:"not null;default:100" json:"min_ttft_samples"`
	MaxTTFTP95Ms            int       `gorm:"not null;default:10000" json:"max_ttft_p95_ms"`
	MinFinanceCoverage      float64   `gorm:"type:decimal(8,6);not null;default:0.90" json:"min_finance_coverage"`
	MinCommonDemandCoverage float64   `gorm:"type:decimal(8,6);not null;default:0.50" json:"min_common_demand_coverage"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func (Settings) TableName() string { return "dash_settings" }

type Meta struct {
	ID            uint `gorm:"primaryKey"`
	AvailableFrom time.Time
	Overflow      bool
}

func (Meta) TableName() string { return "dash_meta" }

func DefaultSettings() Settings {
	return Settings{
		ID:                      1,
		RenewalHorizonHours:     DefaultRenewalHorizonHours,
		MinQualitySamples:       DefaultMinQualitySamples,
		MinSuccessRate:          DefaultMinSuccessRate,
		MinTTFTSamples:          DefaultMinTTFTSamples,
		MaxTTFTP95Ms:            DefaultMaxTTFTP95Ms,
		MinFinanceCoverage:      DefaultMinFinanceCoverage,
		MinCommonDemandCoverage: DefaultMinCommonDemandCoverage,
	}
}

func emptyHist() []int64 {
	return make([]int64, len(HistBoundsMS)+1)
}

func histJSON(h []int64) string {
	if h == nil {
		h = emptyHist()
	}
	b, _ := json.Marshal(h)
	return string(b)
}

func parseHist(s string) []int64 {
	h := emptyHist()
	if s == "" {
		return h
	}
	var raw []int64
	if json.Unmarshal([]byte(s), &raw) != nil {
		return h
	}
	for i := 0; i < len(h) && i < len(raw); i++ {
		h[i] = raw[i]
	}
	return h
}

func addHist(dst, src []int64) []int64 {
	if dst == nil {
		dst = emptyHist()
	}
	for i := 0; i < len(dst) && i < len(src); i++ {
		dst[i] += src[i]
	}
	return dst
}

func observeTTFT(ms int) []int64 {
	h := emptyHist()
	if ms < 0 {
		ms = 0
	}
	for i, bound := range HistBoundsMS {
		if ms <= bound {
			h[i]++
			return h
		}
	}
	h[len(HistBoundsMS)]++
	return h
}

func quantileMS(h []int64, q float64) (ms *float64, samples int64, overflow bool) {
	var total int64
	for _, n := range h {
		total += n
	}
	if total == 0 {
		return nil, 0, false
	}
	target := q * float64(total)
	var acc float64
	for i, n := range h {
		acc += float64(n)
		if acc >= target {
			if i >= len(HistBoundsMS) {
				v := float64(HistBoundsMS[len(HistBoundsMS)-1])
				return &v, total, true
			}
			v := float64(HistBoundsMS[i])
			return &v, total, false
		}
	}
	v := float64(HistBoundsMS[len(HistBoundsMS)-1])
	return &v, total, true
}

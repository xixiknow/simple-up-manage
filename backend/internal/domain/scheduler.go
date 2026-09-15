package domain

import (
	"strings"
	"time"
)

type SchedulerSettings struct {
	CircuitWindowSec        int     `gorm:"not null;default:60" json:"circuit_window_sec"`
	CircuitFailureThreshold int     `gorm:"not null;default:3" json:"circuit_failure_threshold"`
	CircuitCooldownSec      int     `gorm:"not null;default:30" json:"circuit_cooldown_sec"`
	CircuitMaxCooldownSec   int     `gorm:"not null;default:300" json:"circuit_max_cooldown_sec"`
	SwitchImprovementRatio  float64 `gorm:"not null;default:0.20" json:"switch_improvement_ratio"`
	SwitchImprovementMs     int     `gorm:"not null;default:2000" json:"switch_improvement_ms"`
	SwitchConfirmSec        int     `gorm:"not null;default:60" json:"switch_confirm_sec"`
	ExplorationRatio        float64 `gorm:"not null;default:0.05" json:"exploration_ratio"`
	ProbeTimeoutSec         int     `gorm:"not null;default:30" json:"probe_timeout_sec"`
	ID                      uint    `gorm:"primaryKey" json:"id"`
	WeightSuccess           float64 `gorm:"type:decimal(8,4);not null;default:0.45" json:"weight_success"`
	WeightCache             float64 `gorm:"type:decimal(8,4);not null;default:0.30" json:"weight_cache"`
	WeightTTFT              float64 `gorm:"type:decimal(8,4);not null;default:0.25" json:"weight_ttft"`
	Epsilon                 float64 `gorm:"type:decimal(8,4);not null;default:0.08" json:"epsilon"`
	WindowMinutes           int     `gorm:"not null;default:15" json:"window_minutes"`
	WindowMaxSamples        int     `gorm:"not null;default:50" json:"window_max_samples"`
	MinSamples              int     `gorm:"not null;default:5" json:"min_samples"`
	PriorSuccess            float64 `gorm:"type:decimal(8,4);not null;default:0.70" json:"prior_success"`
	TTFTCapMs               int     `gorm:"not null;default:8000" json:"ttft_cap_ms"`
	StickyAnthropic         bool    `gorm:"not null;default:true" json:"sticky_anthropic"`
	StickyOpenAI            bool    `gorm:"not null;default:false" json:"sticky_openai"`
	StickyTTLSec            int     `gorm:"not null;default:3600" json:"sticky_ttl_sec"`
	FailoverMax             int     `gorm:"not null;default:2" json:"failover_max"`
	// RetryMax is extra attempts on the same key after a retryable failure
	// (network / 5xx / 429 / 529) before switching keys. 0 means no same-key retry.
	RetryMax            int    `gorm:"not null;default:1" json:"retry_max"`
	CooldownSec         int    `gorm:"not null;default:30" json:"cooldown_sec"`
	FailureWindowSec    int    `gorm:"not null;default:60" json:"failure_window_sec"`
	FailureThreshold    int    `gorm:"not null;default:8" json:"failure_threshold"`
	RankingMode         string `gorm:"size:32;not null;default:adaptive" json:"ranking_mode"`
	ProbeOpenAIModel    string `gorm:"size:128;not null;default:gpt-4o-mini" json:"probe_openai_model"`
	ProbeAnthropicModel string `gorm:"size:128;not null;default:claude-3-haiku-20240307" json:"probe_anthropic_model"`
	ProbeGrokModel      string `gorm:"size:128;not null;default:grok-3-mini" json:"probe_grok_model"`
	ProbeZhipuModel     string `gorm:"size:128;not null;default:glm-4.5-flash" json:"probe_zhipu_model"`
	ProbeMoonshotModel  string `gorm:"size:128;not null;default:kimi-k2-turbo-preview" json:"probe_moonshot_model"`
	ProbeDeepseekModel  string `gorm:"size:128;not null;default:deepseek-chat" json:"probe_deepseek_model"`
	// FilterByModels skips keys whose fetched model list does not contain the
	// requested model. Keys with an empty list are never filtered.
	FilterByModels *bool     `gorm:"not null;default:true" json:"filter_by_models"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ModelFilterEnabled returns the effective FilterByModels value (default true).
func (s *SchedulerSettings) ModelFilterEnabled() bool {
	return s.FilterByModels == nil || *s.FilterByModels
}

func DefaultSchedulerSettings() SchedulerSettings {
	filter := true
	return SchedulerSettings{
		CircuitWindowSec: 60, CircuitFailureThreshold: 3, CircuitCooldownSec: 30, CircuitMaxCooldownSec: 300,
		SwitchImprovementRatio: 0.20,
		SwitchImprovementMs:    2000,
		SwitchConfirmSec:       60,
		ExplorationRatio:       0.05,
		ProbeTimeoutSec:        30,
		FilterByModels:         &filter,
		WeightSuccess:          0.45,
		WeightCache:            0.30,
		WeightTTFT:             0.25,
		Epsilon:                0.08,
		WindowMinutes:          15,
		WindowMaxSamples:       50,
		MinSamples:             5,
		PriorSuccess:           0.70,
		TTFTCapMs:              8000,
		StickyAnthropic:        true,
		StickyOpenAI:           false,
		StickyTTLSec:           3600,
		FailoverMax:            2,
		RetryMax:               1,
		CooldownSec:            30,
		FailureWindowSec:       60,
		FailureThreshold:       8,
		RankingMode:            "adaptive",
		ProbeOpenAIModel:       "gpt-4o-mini",
		ProbeAnthropicModel:    "claude-3-haiku-20240307",
		ProbeGrokModel:         "grok-3-mini",
		ProbeZhipuModel:        "glm-4.5-flash",
		ProbeMoonshotModel:     "kimi-k2-turbo-preview",
		ProbeDeepseekModel:     "deepseek-chat",
	}
}

func (s *SchedulerSettings) ProbeModel(vendor string) string {
	switch vendor {
	case VendorOpenAI:
		return strings.TrimSpace(s.ProbeOpenAIModel)
	case VendorAnthropic:
		return strings.TrimSpace(s.ProbeAnthropicModel)
	case VendorGrok:
		return strings.TrimSpace(s.ProbeGrokModel)
	case VendorZhipu:
		return strings.TrimSpace(s.ProbeZhipuModel)
	case VendorMoonshot:
		return strings.TrimSpace(s.ProbeMoonshotModel)
	case VendorDeepseek:
		return strings.TrimSpace(s.ProbeDeepseekModel)
	default:
		return ""
	}
}

func (s *SchedulerSettings) SetProbeModel(vendor, model string) {
	model = strings.TrimSpace(model)
	switch vendor {
	case VendorOpenAI:
		s.ProbeOpenAIModel = model
	case VendorAnthropic:
		s.ProbeAnthropicModel = model
	case VendorGrok:
		s.ProbeGrokModel = model
	case VendorZhipu:
		s.ProbeZhipuModel = model
	case VendorMoonshot:
		s.ProbeMoonshotModel = model
	case VendorDeepseek:
		s.ProbeDeepseekModel = model
	}
}

// ConfiguredProbeModels returns vendor → model for every non-empty probe slot.
func (s *SchedulerSettings) ConfiguredProbeModels() map[string]string {
	out := map[string]string{}
	for _, v := range ProbeVendors() {
		if m := s.ProbeModel(v.ID); m != "" {
			out[v.ID] = m
		}
	}
	return out
}

func (s *SchedulerSettings) Normalize() {
	if s.CircuitWindowSec <= 0 || s.CircuitWindowSec > 3600 {
		s.CircuitWindowSec = 60
	}
	if s.CircuitFailureThreshold <= 0 || s.CircuitFailureThreshold > 100 {
		s.CircuitFailureThreshold = 3
	}
	if s.CircuitCooldownSec <= 0 || s.CircuitCooldownSec > 600 {
		s.CircuitCooldownSec = 30
	}
	if s.CircuitMaxCooldownSec < s.CircuitCooldownSec || s.CircuitMaxCooldownSec > 3600 {
		s.CircuitMaxCooldownSec = max(300, s.CircuitCooldownSec)
	}
	if s.SwitchImprovementRatio <= 0 || s.SwitchImprovementRatio > 1 {
		s.SwitchImprovementRatio = 0.20
	}
	if s.SwitchImprovementMs <= 0 {
		s.SwitchImprovementMs = 2000
	}
	if s.SwitchConfirmSec <= 0 {
		s.SwitchConfirmSec = 60
	}
	if s.ExplorationRatio < 0 || s.ExplorationRatio > 0.05 {
		s.ExplorationRatio = 0.05
	}
	if s.ProbeTimeoutSec <= 0 {
		s.ProbeTimeoutSec = 30
	}
	if s.ProbeTimeoutSec > 300 {
		s.ProbeTimeoutSec = 300
	}
	d := DefaultSchedulerSettings()
	if s.WeightSuccess < 0 {
		s.WeightSuccess = 0
	}
	if s.WeightCache < 0 {
		s.WeightCache = 0
	}
	if s.WeightTTFT < 0 {
		s.WeightTTFT = 0
	}
	sum := s.WeightSuccess + s.WeightCache + s.WeightTTFT
	if sum <= 0 {
		s.WeightSuccess, s.WeightCache, s.WeightTTFT = d.WeightSuccess, d.WeightCache, d.WeightTTFT
	}
	if s.Epsilon <= 0 || s.Epsilon > 0.5 {
		s.Epsilon = d.Epsilon
	}
	if s.WindowMinutes <= 0 {
		s.WindowMinutes = d.WindowMinutes
	}
	if s.WindowMaxSamples <= 0 {
		s.WindowMaxSamples = d.WindowMaxSamples
	}
	if s.MinSamples <= 0 {
		s.MinSamples = d.MinSamples
	}
	if s.PriorSuccess <= 0 || s.PriorSuccess > 1 {
		s.PriorSuccess = d.PriorSuccess
	}
	if s.TTFTCapMs <= 0 {
		s.TTFTCapMs = d.TTFTCapMs
	}
	if s.StickyTTLSec <= 0 {
		s.StickyTTLSec = d.StickyTTLSec
	}
	if s.FailoverMax < 1 {
		s.FailoverMax = 1
	}
	if s.FailoverMax > 5 {
		s.FailoverMax = 5
	}
	if s.RetryMax < 0 {
		s.RetryMax = 0
	}
	if s.RetryMax > 5 {
		s.RetryMax = 5
	}
	if s.CooldownSec <= 0 {
		s.CooldownSec = d.CooldownSec
	}
	if s.FailureWindowSec <= 0 {
		s.FailureWindowSec = d.FailureWindowSec
	}
	if s.FailureWindowSec > 3600 {
		s.FailureWindowSec = 3600
	}
	if s.FailureThreshold <= 0 {
		s.FailureThreshold = d.FailureThreshold
	}
	if s.FailureThreshold > 100 {
		s.FailureThreshold = 100
	}
	switch s.RankingMode = strings.TrimSpace(s.RankingMode); s.RankingMode {
	case "adaptive", "fixed_order", "cache_affinity", "load_balance", "stable_latency":
	default:
		s.RankingMode = d.RankingMode
	}
	if s.RankingMode == "stable_latency" {
		s.MinSamples = max(s.MinSamples, 5)
		s.WindowMaxSamples = max(s.WindowMaxSamples, s.MinSamples)
	}
	if s.ProbeOpenAIModel = strings.TrimSpace(s.ProbeOpenAIModel); s.ProbeOpenAIModel == "" {
		s.ProbeOpenAIModel = d.ProbeOpenAIModel
	}
	if s.ProbeAnthropicModel = strings.TrimSpace(s.ProbeAnthropicModel); s.ProbeAnthropicModel == "" {
		s.ProbeAnthropicModel = d.ProbeAnthropicModel
	}
	if s.ProbeGrokModel = strings.TrimSpace(s.ProbeGrokModel); s.ProbeGrokModel == "" {
		s.ProbeGrokModel = d.ProbeGrokModel
	}
	if s.ProbeZhipuModel = strings.TrimSpace(s.ProbeZhipuModel); s.ProbeZhipuModel == "" {
		s.ProbeZhipuModel = d.ProbeZhipuModel
	}
	if s.ProbeMoonshotModel = strings.TrimSpace(s.ProbeMoonshotModel); s.ProbeMoonshotModel == "" {
		s.ProbeMoonshotModel = d.ProbeMoonshotModel
	}
	if s.ProbeDeepseekModel = strings.TrimSpace(s.ProbeDeepseekModel); s.ProbeDeepseekModel == "" {
		s.ProbeDeepseekModel = d.ProbeDeepseekModel
	}
	if s.FilterByModels == nil {
		v := true
		s.FilterByModels = &v
	}
}

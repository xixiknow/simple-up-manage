package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen            string  `yaml:"listen"`
	AdminToken        string  `yaml:"admin_token"`
	EncryptKey        string  `yaml:"encrypt_key"`
	DatabaseURL       string  `yaml:"database_url"`
	RedisURL          string  `yaml:"redis_url"`
	StaticDir         string  `yaml:"static_dir"`
	LogBodiesDir      string  `yaml:"log_bodies_dir"`
	LogBodyMaxBytes   int64   `yaml:"log_body_max_bytes"`
	LogBodiesMaxBytes int64   `yaml:"log_bodies_max_bytes"`
	Jobs              Jobs    `yaml:"jobs"`
	Billing           Billing `yaml:"billing"`
}

// Billing configures the LiteLLM price-card mirror that feeds the dashboard's
// consumption estimates, plus optional surcharge rules layered on top of it.
type Billing struct {
	RemoteURL         string        `yaml:"remote_url"`
	HashURL           string        `yaml:"hash_url"`
	FullSyncInterval  time.Duration `yaml:"full_sync_interval"`
	HashCheckInterval time.Duration `yaml:"hash_check_interval"`
	TimeRules         []TimeRule    `yaml:"time_rules"`
	EffortRules       []EffortRule  `yaml:"effort_rules"`
}

// TimeRule multiplies input/output/cache-read prices during configured time
// windows (e.g. DeepSeek peak hours). Windows are half-open [start, end) in TZ;
// end <= start wraps midnight. When no rule is configured, the built-in
// DeepSeek peak-valley rule applies.
type TimeRule struct {
	ModelMatch  string       `yaml:"match"`
	TZ          string       `yaml:"tz"`
	WeekendTZ   string       `yaml:"weekend_tz"`
	SkipWeekend bool         `yaml:"skip_weekend"`
	Windows     []TimeWindow `yaml:"windows"`
}

type TimeWindow struct {
	Days       []string `yaml:"days"`
	Start      string   `yaml:"start"`
	End        string   `yaml:"end"`
	Multiplier float64  `yaml:"multiplier"`
}

// EffortRule multiplies the whole cost when a request's reasoning effort
// (OpenAI protocol: reasoning_effort / reasoning.effort) matches a key.
type EffortRule struct {
	ModelMatch  string             `yaml:"match"`
	Multipliers map[string]float64 `yaml:"multipliers"`
}

type Jobs struct {
	RecoveryConcurrency  int           `yaml:"recovery_concurrency"`
	BalanceInterval      time.Duration `yaml:"balance_interval"`
	BillingInterval      time.Duration `yaml:"billing_interval"`
	ProbeInterval        time.Duration `yaml:"probe_interval"`
	CatalogInterval      time.Duration `yaml:"catalog_interval"`
	ModelsInterval       time.Duration `yaml:"models_interval"`
	LogRetention         time.Duration `yaml:"log_retention"`
	LogRetentionInterval time.Duration `yaml:"log_retention_interval"`
}

// Default LiteLLM price-card mirror and its change probe, kept inline so this
// package stays a leaf (catalog's test imports store, which reaches back here).
// The commits API answers with a few hundred bytes, keeping the 10-minute hash
// check well inside GitHub's unauthenticated rate limit.
const (
	DefaultBillingRemoteURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	DefaultBillingHashURL   = "https://api.github.com/repos/BerriAI/litellm/commits?path=model_prices_and_context_window.json&per_page=1"
)

func defaults() Config {
	return Config{
		Listen:            ":8080",
		LogBodiesDir:      "data/log-bodies",
		LogBodyMaxBytes:   64 << 20,
		LogBodiesMaxBytes: 10 << 30,
		Jobs: Jobs{
			RecoveryConcurrency:  8,
			BalanceInterval:      time.Minute,
			BillingInterval:      time.Minute,
			ProbeInterval:        time.Minute,
			CatalogInterval:      24 * time.Hour,
			ModelsInterval:       time.Hour,
			LogRetention:         24 * time.Hour,
			LogRetentionInterval: time.Hour,
		},
		Billing: Billing{
			RemoteURL:         DefaultBillingRemoteURL,
			HashURL:           DefaultBillingHashURL,
			FullSyncInterval:  24 * time.Hour,
			HashCheckInterval: 10 * time.Minute,
		},
	}
}

func Load() (*Config, error) {
	cfg := defaults()

	path := firstExisting(
		os.Getenv("CONFIG_FILE"),
		"config.local.yaml",
		"../config.local.yaml",
		"config.yaml",
		"../config.yaml",
		"deploy/config.yaml",
		"../deploy/config.yaml",
	)
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}

	if v := os.Getenv("LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("ADMIN_TOKEN"); v != "" {
		cfg.AdminToken = v
	}
	if v := os.Getenv("APP_ENCRYPT_KEY"); v != "" {
		cfg.EncryptKey = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := os.Getenv("REDIS_URL"); v != "" {
		cfg.RedisURL = v
	}
	if v := os.Getenv("STATIC_DIR"); v != "" {
		cfg.StaticDir = v
	}
	if v := os.Getenv("LOG_BODIES_DIR"); v != "" {
		cfg.LogBodiesDir = v
	}
	if v := os.Getenv("RECOVERY_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("RECOVERY_CONCURRENCY must be an integer between 1 and 64")
		}
		cfg.Jobs.RecoveryConcurrency = n
	}
	if cfg.Jobs.RecoveryConcurrency < 1 || cfg.Jobs.RecoveryConcurrency > 64 {
		return nil, fmt.Errorf("jobs.recovery_concurrency must be between 1 and 64")
	}
	if cfg.LogBodyMaxBytes <= 0 || cfg.LogBodiesMaxBytes <= 0 || cfg.LogBodiesDir == "" {
		return nil, fmt.Errorf("log body directory and positive limits are required")
	}

	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.Jobs.BalanceInterval <= 0 {
		cfg.Jobs.BalanceInterval = time.Minute
	}
	if cfg.Jobs.BillingInterval <= 0 {
		cfg.Jobs.BillingInterval = time.Minute
	}
	if cfg.Jobs.ProbeInterval <= 0 {
		cfg.Jobs.ProbeInterval = time.Minute
	}
	if cfg.Jobs.CatalogInterval <= 0 {
		cfg.Jobs.CatalogInterval = 24 * time.Hour
	}
	if cfg.Jobs.ModelsInterval <= 0 {
		cfg.Jobs.ModelsInterval = time.Hour
	}
	if cfg.Jobs.LogRetention <= 0 {
		cfg.Jobs.LogRetention = 24 * time.Hour
	}
	if cfg.Jobs.LogRetentionInterval <= 0 {
		cfg.Jobs.LogRetentionInterval = time.Hour
	}
	// Billing: an explicitly empty remote_url in YAML disables the LiteLLM
	// mirror (defaults are pre-filled in defaults(), so only a deliberate
	// override can blank it); sync cadence gets floors.
	cfg.Billing.RemoteURL = strings.TrimSpace(cfg.Billing.RemoteURL)
	cfg.Billing.HashURL = strings.TrimSpace(cfg.Billing.HashURL)
	if cfg.Billing.FullSyncInterval <= 0 {
		cfg.Billing.FullSyncInterval = 24 * time.Hour
	}
	if cfg.Billing.HashCheckInterval <= 0 {
		cfg.Billing.HashCheckInterval = 10 * time.Minute
	}
	if cfg.Billing.HashCheckInterval < time.Minute {
		cfg.Billing.HashCheckInterval = time.Minute
	}
	if strings.TrimSpace(cfg.AdminToken) == "" {
		return nil, fmt.Errorf("admin_token / ADMIN_TOKEN is required")
	}
	if strings.TrimSpace(cfg.EncryptKey) == "" {
		return nil, fmt.Errorf("encrypt_key / APP_ENCRYPT_KEY is required")
	}
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return nil, fmt.Errorf("database_url / DATABASE_URL is required")
	}
	return &cfg, nil
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

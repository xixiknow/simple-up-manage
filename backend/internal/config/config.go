package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen            string `yaml:"listen"`
	AdminToken        string `yaml:"admin_token"`
	EncryptKey        string `yaml:"encrypt_key"`
	DatabaseURL       string `yaml:"database_url"`
	RedisURL          string `yaml:"redis_url"`
	StaticDir         string `yaml:"static_dir"`
	LogBodiesDir      string `yaml:"log_bodies_dir"`
	LogBodyMaxBytes   int64  `yaml:"log_body_max_bytes"`
	LogBodiesMaxBytes int64  `yaml:"log_bodies_max_bytes"`
	Jobs              Jobs   `yaml:"jobs"`
}

type Jobs struct {
	BalanceInterval      time.Duration `yaml:"balance_interval"`
	BillingInterval      time.Duration `yaml:"billing_interval"`
	ProbeInterval        time.Duration `yaml:"probe_interval"`
	CatalogInterval      time.Duration `yaml:"catalog_interval"`
	LogRetention         time.Duration `yaml:"log_retention"`
	LogRetentionInterval time.Duration `yaml:"log_retention_interval"`
}

func defaults() Config {
	return Config{
		Listen:            ":8080",
		LogBodiesDir:      "data/log-bodies",
		LogBodyMaxBytes:   64 << 20,
		LogBodiesMaxBytes: 10 << 30,
		Jobs: Jobs{
			BalanceInterval:      time.Minute,
			BillingInterval:      time.Minute,
			ProbeInterval:        time.Minute,
			CatalogInterval:      24 * time.Hour,
			LogRetention:         24 * time.Hour,
			LogRetentionInterval: time.Hour,
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
	if cfg.Jobs.LogRetention <= 0 {
		cfg.Jobs.LogRetention = 24 * time.Hour
	}
	if cfg.Jobs.LogRetentionInterval <= 0 {
		cfg.Jobs.LogRetentionInterval = time.Hour
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

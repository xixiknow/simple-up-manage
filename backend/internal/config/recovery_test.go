package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryConcurrencyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("ADMIN_TOKEN", "test")
	t.Setenv("APP_ENCRYPT_KEY", "test")
	t.Setenv("DATABASE_URL", "test")
	for _, tc := range []struct {
		yaml, env string
		want      int
		invalid   bool
	}{
		{"", "", 8, false}, {"jobs:\n  recovery_concurrency: 12\n", "", 12, false}, {"jobs:\n  recovery_concurrency: 12\n", "16", 16, false},
		{"", "0", 0, true}, {"", "65", 0, true}, {"", "bad", 0, true}, {"jobs:\n  recovery_concurrency: -1\n", "", 0, true},
	} {
		if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RECOVERY_CONCURRENCY", tc.env)
		cfg, err := Load()
		if (err != nil) != tc.invalid {
			t.Fatalf("yaml=%q env=%q err=%v", tc.yaml, tc.env, err)
		}
		if err == nil && cfg.Jobs.RecoveryConcurrency != tc.want {
			t.Fatalf("got %d want %d", cfg.Jobs.RecoveryConcurrency, tc.want)
		}
	}
}

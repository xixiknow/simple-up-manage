package domain

import "testing"

func TestSchedulerRetryMaxNormalize(t *testing.T) {
	s := DefaultSchedulerSettings()
	if s.RetryMax != 1 {
		t.Fatalf("default retry_max want 1, got %d", s.RetryMax)
	}
	s.RetryMax = -3
	s.Normalize()
	if s.RetryMax != 0 {
		t.Fatalf("negative retry_max should clamp to 0, got %d", s.RetryMax)
	}
	s.RetryMax = 9
	s.Normalize()
	if s.RetryMax != 5 {
		t.Fatalf("retry_max should clamp to 5, got %d", s.RetryMax)
	}
}

func TestSchedulerRankingModeNormalize(t *testing.T) {
	s := DefaultSchedulerSettings()
	if s.RankingMode != "adaptive" {
		t.Fatalf("default ranking_mode want adaptive, got %q", s.RankingMode)
	}
	for _, mode := range []string{"adaptive", "fixed_order", "cache_affinity", "load_balance"} {
		s.RankingMode = mode
		s.Normalize()
		if s.RankingMode != mode {
			t.Fatalf("valid ranking_mode %q changed to %q", mode, s.RankingMode)
		}
	}
	s.RankingMode = "unknown"
	s.Normalize()
	if s.RankingMode != "adaptive" {
		t.Fatalf("invalid ranking_mode should fall back to adaptive, got %q", s.RankingMode)
	}
}

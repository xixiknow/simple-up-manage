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

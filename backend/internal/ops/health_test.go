package ops

import (
	"testing"

	"simple-up-manage/internal/domain"
)

func TestRecomputeHealthUsesUpstreamBalance(t *testing.T) {
	s := &Service{}
	zero := 0.0
	pos := 1.5
	key := &domain.PlatformKey{Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy, LastBalance: &zero}
	up := &domain.Upstream{Status: domain.StatusEnabled, LastBalance: &zero}
	if got := s.RecomputeHealth(key, up); got != domain.HealthLowBalance {
		t.Fatalf("provider balance 0: got %s", got)
	}
	up.LastBalance = &pos
	if got := s.RecomputeHealth(key, up); got != domain.HealthHealthy {
		t.Fatalf("provider balance >0 should ignore key balance, got %s", got)
	}
	up.LastBalance = nil
	if got := s.RecomputeHealth(key, up); got != domain.HealthHealthy {
		t.Fatalf("unlimited/nil balance should not be low_balance, got %s", got)
	}
}

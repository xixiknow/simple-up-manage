package ops

import (
	"errors"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

// seedDueQuarantine creates a quarantined state due for retest under the
// plan, mimicking what evaluateIntelQuarantine leaves behind.
func (s *Service) seedDueQuarantine(t *testing.T, plan *domain.IntelTestPlan, keyID uint) domain.IntelQuarantineState {
	t.Helper()
	now := time.Now().Add(-time.Minute)
	state := domain.IntelQuarantineState{
		PlanID: plan.ID, PlatformKeyID: keyID, UpstreamID: keyID,
		Status: domain.IntelQuarantineQuarantined, BackoffSec: intelQuarantineBaseSec,
		QuarantinedAt: now, NextTestAt: &now,
	}
	if err := s.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRestoreIntelKeyDropsStateWhenGroupMissing(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var key domain.PlatformKey
	if err := s.DB.Order("id ASC").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	state := s.seedDueQuarantine(t, plan, key.ID)
	// A system quarantine removes the key from the group; mimic it so the
	// pre-guard assertion (membership) matches real quarantine state.
	if err := s.DB.Where("route_group_id = ? AND platform_key_id = ?", group.ID, key.ID).
		Delete(&domain.RouteGroupKey{}).Error; err != nil {
		t.Fatal(err)
	}

	if err := s.DB.Delete(&domain.RouteGroup{}, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	s.restoreIntelKey(plan, &state)

	var stateCount int64
	if err := s.DB.Model(&domain.IntelQuarantineState{}).Where("plan_id = ?", plan.ID).Count(&stateCount).Error; err != nil {
		t.Fatal(err)
	}
	if stateCount != 0 {
		t.Fatalf("quarantine states left = %d, want 0 (state must be dropped, not restored)", stateCount)
	}
	if s.keyInGroup(t, group.ID, key.ID) {
		t.Fatalf("key must not be re-added to a deleted group")
	}
}

func TestMissingGroupBlocksAllIntelRuns(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var key domain.PlatformKey
	if err := s.DB.Order("id ASC").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	s.seedDueQuarantine(t, plan, key.ID)

	if err := s.DB.Delete(&domain.RouteGroup{}, group.ID).Error; err != nil {
		t.Fatal(err)
	}

	if ids := s.runIntelQuarantineDue(); len(ids) != 0 {
		t.Fatalf("runIntelQuarantineDue = %v, want no plans for a deleted group", ids)
	}
	if _, err := s.RunIntelQuarantine(plan.ID); !errors.Is(err, ErrIntelGroupMissing) {
		t.Fatalf("RunIntelQuarantine err = %v, want ErrIntelGroupMissing", err)
	}
	if _, err := s.RunIntelPlan(plan.ID); !errors.Is(err, ErrIntelGroupMissing) {
		t.Fatalf("RunIntelPlan err = %v, want ErrIntelGroupMissing", err)
	}
}

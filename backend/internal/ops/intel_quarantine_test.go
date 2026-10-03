package ops

import (
	"strings"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func (s *Service) intelQuarantinePlan(t *testing.T, groupID uint, model string) *domain.IntelTestPlan {
	t.Helper()
	plan := domain.IntelTestPlan{
		Name: "quarantine-plan", RouteGroupID: groupID, Model: model,
		QuestionKind: domain.IntelQuestionCandy, Parallel: 4, Enabled: true,
		QuarantineEnabled: true,
	}
	if err := s.DB.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return &plan
}

// intelQuarantineRule pins a plan's trigger rule (0 values = defaults 3/50).
func (s *Service) intelQuarantineRule(t *testing.T, plan *domain.IntelTestPlan, minSamples int, threshold float64) {
	t.Helper()
	if err := s.DB.Model(plan).Updates(map[string]any{
		"quarantine_min_samples": minSamples, "quarantine_threshold": threshold,
	}).Error; err != nil {
		t.Fatal(err)
	}
	plan.QuarantineMinSamples, plan.QuarantineThreshold = minSamples, threshold
}

// seedIntelVerdict inserts a judged result row directly, as if a past run
// produced it.
func (s *Service) seedIntelVerdict(t *testing.T, plan *domain.IntelTestPlan, keyID uint, verdict string) {
	t.Helper()
	res := domain.IntelTestResult{
		RunID: 1, PlanID: plan.ID, RouteGroupID: plan.RouteGroupID,
		PlatformKeyID: keyID, KeyName: "seeded", Model: plan.Model,
		QuestionKind: plan.QuestionKind, Verdict: verdict,
		AnswerPreview: "21", CreatedAt: time.Now(),
	}
	if err := s.DB.Create(&res).Error; err != nil {
		t.Fatal(err)
	}
}

func (s *Service) quarantineState(t *testing.T, planID, keyID uint) domain.IntelQuarantineState {
	t.Helper()
	var state domain.IntelQuarantineState
	if err := s.DB.Where("plan_id = ? AND platform_key_id = ?", planID, keyID).First(&state).Error; err != nil {
		t.Fatalf("quarantine state missing: %v", err)
	}
	return state
}

func (s *Service) keyInGroup(t *testing.T, groupID, keyID uint) bool {
	t.Helper()
	var count int64
	if err := s.DB.Model(&domain.RouteGroupKey{}).Where("route_group_id = ? AND platform_key_id = ?", groupID, keyID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return count > 0
}

func TestIntelQuarantineTriggerFromRun(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "42", 0) // wrong answer
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var key domain.PlatformKey
	if err := s.DB.First(&key).Error; err != nil {
		t.Fatal(err)
	}
	// Window so far: 3 correct / 6 wrong = 33%; the run adds a wrong answer.
	for i := 0; i < 3; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictCorrect)
	}
	for i := 0; i < 6; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
	}
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntelRun(t, s, plan.ID, run.ID)

	state := s.quarantineState(t, plan.ID, key.ID)
	if state.Status != domain.IntelQuarantineQuarantined {
		t.Fatalf("status = %q, want quarantined", state.Status)
	}
	if state.QuarantineCount != 1 || state.PassStreak != 0 {
		t.Fatalf("count=%d streak=%d, want 1/0", state.QuarantineCount, state.PassStreak)
	}
	if !strings.Contains(state.Reason, "低于") {
		t.Fatalf("reason = %q", state.Reason)
	}
	if state.NextTestAt == nil || !state.NextTestAt.After(time.Now().Add(30*time.Second)) {
		t.Fatalf("next_test_at = %v, want ~1 minute out", state.NextTestAt)
	}
	if state.BackoffSec != intelQuarantineBaseSec {
		t.Fatalf("backoff = %d, want %d", state.BackoffSec, intelQuarantineBaseSec)
	}
	if s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("key should have been removed from the group")
	}
}

func TestIntelQuarantineMinSamples(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "42", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	s.intelQuarantineRule(t, plan, 5, 50)
	var key domain.PlatformKey
	if err := s.DB.First(&key).Error; err != nil {
		t.Fatal(err)
	}
	// Only 3 effective samples + this run = 4 < 5 minimum.
	for i := 0; i < 3; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
	}
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntelRun(t, s, plan.ID, run.ID)
	var count int64
	if err := s.DB.Model(&domain.IntelQuarantineState{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("quarantined despite too few effective samples")
	}
	if !s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("key should stay in the group")
	}
}

func TestIntelQuarantineIgnoresErrors(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	s.intelQuarantineRule(t, plan, 5, 50)
	var key domain.PlatformKey
	if err := s.DB.Preload("Upstream").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	// 4 wrong answers plus a pile of transport errors: errors are not
	// intelligence evidence, so the window stays below the minimum even when
	// another error result arrives.
	for i := 0; i < 4; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
	}
	for i := 0; i < 8; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictError)
	}
	s.applyIntelOutcome(plan, &key, &domain.IntelTestResult{
		PlanID: plan.ID, PlatformKeyID: key.ID,
		QuestionKind: domain.IntelQuestionCandy, Verdict: domain.IntelVerdictError,
	})
	var count int64
	if err := s.DB.Model(&domain.IntelQuarantineState{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("transport errors must not drive quarantine")
	}
	if !s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("key should stay in the group")
	}
}

func TestIntelQuarantineRequiresMembership(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "42", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var key domain.PlatformKey
	if err := s.DB.First(&key).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
	}
	// Operator already removed the key: nothing to quarantine.
	if err := s.DB.Where("route_group_id = ? AND platform_key_id = ?", group.ID, key.ID).
		Delete(&domain.RouteGroupKey{}).Error; err != nil {
		t.Fatal(err)
	}
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntelRun(t, s, plan.ID, run.ID)
	var count int64
	if err := s.DB.Model(&domain.IntelQuarantineState{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("non-member key must not be quarantined")
	}
}

func TestIntelQuarantineBackoffAndRestore(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var key domain.PlatformKey
	if err := s.DB.Preload("Upstream").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	state := domain.IntelQuarantineState{
		PlanID: plan.ID, PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
		KeyName: key.Name, UpstreamName: key.Upstream.Name,
		Status: domain.IntelQuarantineQuarantined, BackoffSec: intelQuarantineBaseSec,
		QuarantinedAt: now, NextTestAt: &now,
	}
	if err := s.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	// A quarantined key is one the system already removed from the group.
	if err := s.DB.Where("route_group_id = ? AND platform_key_id = ?", group.ID, key.ID).
		Delete(&domain.RouteGroupKey{}).Error; err != nil {
		t.Fatal(err)
	}
	result := func(verdict string) *domain.IntelTestResult {
		return &domain.IntelTestResult{
			PlanID: plan.ID, PlatformKeyID: key.ID, QuestionKind: domain.IntelQuestionCandy, Verdict: verdict,
		}
	}

	// wrong: streak resets, backoff doubles 60 -> 120
	s.applyIntelOutcome(plan, &key, result(domain.IntelVerdictIncorrect))
	st := s.quarantineState(t, plan.ID, key.ID)
	if st.PassStreak != 0 || st.BackoffSec != 120 {
		t.Fatalf("after wrong: streak=%d backoff=%d, want 0/120", st.PassStreak, st.BackoffSec)
	}
	// wrong again: 120 -> 240
	s.applyIntelOutcome(plan, &key, result(domain.IntelVerdictIncorrect))
	st = s.quarantineState(t, plan.ID, key.ID)
	if st.BackoffSec != 240 {
		t.Fatalf("after second wrong: backoff=%d, want 240", st.BackoffSec)
	}
	// correct: fast confirm, backoff resets, streak 1, still out
	s.applyIntelOutcome(plan, &key, result(domain.IntelVerdictCorrect))
	st = s.quarantineState(t, plan.ID, key.ID)
	if st.PassStreak != 1 || st.BackoffSec != intelQuarantineBaseSec {
		t.Fatalf("after correct: streak=%d backoff=%d, want 1/60", st.PassStreak, st.BackoffSec)
	}
	if s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("one correct answer must not restore the key yet")
	}
	// transport error: streak preserved, but the next test waits longer
	s.applyIntelOutcome(plan, &key, result(domain.IntelVerdictError))
	st = s.quarantineState(t, plan.ID, key.ID)
	if st.PassStreak != 1 || st.BackoffSec != 120 {
		t.Fatalf("after error: streak=%d backoff=%d, want 1/120", st.PassStreak, st.BackoffSec)
	}
	// second correct: restored into the group
	s.applyIntelOutcome(plan, &key, result(domain.IntelVerdictCorrect))
	st = s.quarantineState(t, plan.ID, key.ID)
	if st.Status != domain.IntelQuarantineRestored || st.RestoredAt == nil {
		t.Fatalf("status=%q restored_at=%v, want restored", st.Status, st.RestoredAt)
	}
	if st.NextTestAt != nil {
		t.Fatalf("restored key should have no next_test_at, got %v", st.NextTestAt)
	}
	if !s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("restored key should be back in the group")
	}
}

func TestIntelQuarantineWindowResetsAfterRestore(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	s.intelQuarantineRule(t, plan, 5, 50)
	var key domain.PlatformKey
	if err := s.DB.Preload("Upstream").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	// A previously quarantined key that was restored two minutes ago.
	restoredAt := time.Now().Add(-2 * time.Minute)
	state := domain.IntelQuarantineState{
		PlanID: plan.ID, PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
		KeyName: key.Name, Status: domain.IntelQuarantineRestored,
		BackoffSec: intelQuarantineBaseSec, QuarantineCount: 1,
		QuarantinedAt: restoredAt.Add(-time.Hour), RestoredAt: &restoredAt,
	}
	if err := s.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	// Old failures from before the restore must not count.
	for i := 0; i < 8; i++ {
		s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
	}
	s.DB.Model(&domain.IntelTestResult{}).Where("plan_id = ?", plan.ID).
		Update("created_at", restoredAt.Add(-time.Minute))
	// feedWrong persists a fresh wrong answer (as intelSample would) and feeds
	// it into the state machine.
	feedWrong := func() {
		res := domain.IntelTestResult{
			RunID: 9, PlanID: plan.ID, RouteGroupID: plan.RouteGroupID,
			PlatformKeyID: key.ID, KeyName: key.Name, Model: plan.Model,
			QuestionKind: domain.IntelQuestionCandy, Verdict: domain.IntelVerdictIncorrect,
			AnswerPreview: "42", CreatedAt: time.Now(),
		}
		if err := s.DB.Create(&res).Error; err != nil {
			t.Fatal(err)
		}
		s.applyIntelOutcome(plan, &key, &res)
	}
	// Two fresh wrong answers after the restore: window has 2 < 5 samples.
	feedWrong()
	feedWrong()
	var count int64
	if err := s.DB.Model(&domain.IntelQuarantineState{}).Where("status = ?", domain.IntelQuarantineQuarantined).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("post-restore observation window must restart: 2 fresh samples cannot quarantine")
	}
	if !s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("key should stay in the group")
	}
	// Fifth fresh failure reaches the minimum: 0/5 -> quarantined again.
	feedWrong()
	feedWrong()
	feedWrong()
	st := s.quarantineState(t, plan.ID, key.ID)
	if st.Status != domain.IntelQuarantineQuarantined || st.QuarantineCount != 2 {
		t.Fatalf("status=%q count=%d, want re-quarantined as round 2", st.Status, st.QuarantineCount)
	}
	if s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("key should be removed from the group again")
	}
}

func TestIntelQuarantineCustomRule(t *testing.T) {
	for _, tc := range []struct {
		name           string
		minSamples     int
		threshold      float64
		seedCorrect    int
		seedWrong      int
		feedVerdict    string
		wantQuarantine bool
	}{
		// Two wrong answers reach the configured minimum of 2: quarantined,
		// although the old fixed rule (min 5) would not have fired yet.
		{name: "min2 threshold50 two wrongs", minSamples: 2, threshold: 50, seedWrong: 1, feedVerdict: domain.IntelVerdictIncorrect, wantQuarantine: true},
		// Strictest rule: a single wrong answer quarantines immediately.
		{name: "min1 single wrong", minSamples: 1, threshold: 50, feedVerdict: domain.IntelVerdictIncorrect, wantQuarantine: true},
		// Lenient threshold: 1/3 correct = 33% is below 80%.
		{name: "min3 threshold80 one correct", minSamples: 3, threshold: 80, seedCorrect: 1, seedWrong: 1, feedVerdict: domain.IntelVerdictIncorrect, wantQuarantine: true},
		// Standard rule: 2/3 correct = 67% stays above 50%.
		{name: "min3 threshold50 two correct", minSamples: 3, threshold: 50, seedCorrect: 2, feedVerdict: domain.IntelVerdictIncorrect, wantQuarantine: false},
		// Correct answers keep accuracy above the threshold.
		{name: "min3 threshold50 all correct", minSamples: 3, threshold: 50, seedCorrect: 2, feedVerdict: domain.IntelVerdictCorrect, wantQuarantine: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, group := newIntelEnv(t, "openai", "21", 0)
			plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
			s.intelQuarantineRule(t, plan, tc.minSamples, tc.threshold)
			var key domain.PlatformKey
			if err := s.DB.Preload("Upstream").First(&key).Error; err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.seedCorrect; i++ {
				s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictCorrect)
			}
			for i := 0; i < tc.seedWrong; i++ {
				s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
			}
			res := domain.IntelTestResult{
				RunID: 9, PlanID: plan.ID, RouteGroupID: plan.RouteGroupID,
				PlatformKeyID: key.ID, KeyName: key.Name, Model: plan.Model,
				QuestionKind: domain.IntelQuestionCandy, Verdict: tc.feedVerdict, CreatedAt: time.Now(),
			}
			if err := s.DB.Create(&res).Error; err != nil {
				t.Fatal(err)
			}
			s.applyIntelOutcome(plan, &key, &res)
			var st domain.IntelQuarantineState
			err := s.DB.Where("plan_id = ? AND platform_key_id = ?", plan.ID, key.ID).First(&st).Error
			if tc.wantQuarantine {
				if err != nil {
					t.Fatalf("quarantine state missing: %v", err)
				}
				if st.Status != domain.IntelQuarantineQuarantined {
					t.Fatalf("status = %q, want quarantined", st.Status)
				}
			} else if err == nil && st.Status == domain.IntelQuarantineQuarantined {
				t.Fatalf("unexpectedly quarantined")
			}
		})
	}
}

func TestIntelQuarantineSchedulerRunsDueOnly(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var keyA, keyB domain.PlatformKey
	if err := s.DB.Order("id ASC").First(&keyA).Error; err != nil {
		t.Fatal(err)
	}
	secret, err := s.Enc.Encrypt("fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	keyB = domain.PlatformKey{UpstreamID: keyA.UpstreamID, Name: "second-key", EncryptedKey: secret, Status: domain.StatusEnabled}
	if err := s.DB.Create(&keyB).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&domain.RouteGroupKey{RouteGroupID: group.ID, PlatformKeyID: keyB.ID}).Error; err != nil {
		t.Fatal(err)
	}
	// Key A: quarantined and due (already removed from the group). Key B: healthy member.
	now := time.Now().Add(-time.Minute)
	state := domain.IntelQuarantineState{
		PlanID: plan.ID, PlatformKeyID: keyA.ID, UpstreamID: keyA.UpstreamID,
		KeyName: keyA.Name, UpstreamName: "fixture-provider",
		Status: domain.IntelQuarantineQuarantined, BackoffSec: intelQuarantineBaseSec,
		QuarantinedAt: now, NextTestAt: &now,
	}
	if err := s.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Where("route_group_id = ? AND platform_key_id = ?", group.ID, keyA.ID).
		Delete(&domain.RouteGroupKey{}).Error; err != nil {
		t.Fatal(err)
	}

	s.RunDueIntelPlans()
	deadline := time.Now().Add(5 * time.Second)
	var runs []domain.IntelTestRun
	for {
		if err := s.DB.Where("plan_id = ?", plan.ID).Find(&runs).Error; err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Status == domain.IntelRunFinished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected one finished quarantine run, got %+v", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitIntelIdle(t, plan.ID)
	if runs[0].Scope != domain.IntelRunScopeQuarantine {
		t.Fatalf("scope = %q, want quarantine", runs[0].Scope)
	}
	if runs[0].Total != 1 {
		t.Fatalf("run total = %d, want 1 (only the due quarantined key)", runs[0].Total)
	}
	var results []domain.IntelTestResult
	if err := s.DB.Where("run_id = ?", runs[0].ID).Find(&results).Error; err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].PlatformKeyID != keyA.ID {
		t.Fatalf("results = %+v, want exactly key A", results)
	}
	// The mock answered correctly: streak 1, still quarantined, next test soon.
	st := s.quarantineState(t, plan.ID, keyA.ID)
	if st.PassStreak != 1 || st.Status != domain.IntelQuarantineQuarantined {
		t.Fatalf("streak=%d status=%q, want 1/quarantined", st.PassStreak, st.Status)
	}
	if st.NextTestAt == nil || !st.NextTestAt.After(time.Now()) {
		t.Fatalf("next_test_at = %v, want in the future", st.NextTestAt)
	}
	if !s.keyInGroup(t, group.ID, keyB.ID) {
		t.Fatal("healthy member key must be untouched")
	}
	if s.keyInGroup(t, group.ID, keyA.ID) {
		t.Fatal("quarantined key must stay out of the group")
	}
}

func TestIntelDeletePlanRestoresQuarantined(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelQuarantinePlan(t, group.ID, "gpt-fixture")
	var key domain.PlatformKey
	if err := s.DB.First(&key).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	state := domain.IntelQuarantineState{
		PlanID: plan.ID, PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
		KeyName: key.Name, Status: domain.IntelQuarantineQuarantined,
		BackoffSec: intelQuarantineBaseSec, QuarantinedAt: now, NextTestAt: &now,
	}
	if err := s.DB.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	s.seedIntelVerdict(t, plan, key.ID, domain.IntelVerdictIncorrect)
	if err := s.DeleteIntelPlanData(plan.ID); err != nil {
		t.Fatal(err)
	}
	if !s.keyInGroup(t, group.ID, key.ID) {
		t.Fatal("deleting the plan must re-add quarantined keys")
	}
	for _, table := range []string{"intel_quarantine_states", "intel_test_results", "intel_test_runs"} {
		var count int64
		if err := s.DB.Table(table).Where("plan_id = ?", plan.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s still has %d rows", table, count)
		}
	}
}

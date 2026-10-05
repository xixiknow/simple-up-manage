package ops

import (
	"errors"
	"fmt"
	"log"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Quarantine policy constants. Candy-only: the last N (the plan's minimum
// sample count) effective (judged) results decide; when accuracy drops to the
// plan's threshold or below — a perfect score is never quarantined — the key
// leaves the group, then retests on a per-key exponential backoff (1min,
// doubling to a 30min cap) until it answers correctly twice in a row.
const (
	intelQuarantineBaseSec   = 60
	intelQuarantineMaxSec    = 1800
	intelQuarantineRestoreAt = 2
)

// nextIntelBackoff doubles the current interval, capped at the maximum.
func nextIntelBackoff(cur int) int {
	if cur < intelQuarantineBaseSec {
		cur = intelQuarantineBaseSec
	}
	cur *= 2
	if cur > intelQuarantineMaxSec {
		cur = intelQuarantineMaxSec
	}
	return cur
}

// intelQuarantinedStates returns the quarantined states of a plan, optionally
// only those whose retest is due.
func (s *Service) intelQuarantinedStates(planID uint, dueOnly bool) ([]domain.IntelQuarantineState, error) {
	q := s.DB.Where("plan_id = ? AND status = ?", planID, domain.IntelQuarantineQuarantined)
	if dueOnly {
		q = q.Where("next_test_at IS NULL OR next_test_at <= ?", time.Now())
	}
	var states []domain.IntelQuarantineState
	err := q.Find(&states).Error
	return states, err
}

// intelPlanGroupExists reports whether the plan's route group still exists.
// Once the operator deletes the group there is nothing left to protect: no
// full runs and no quarantine retests may run for the plan.
func (s *Service) intelPlanGroupExists(plan *domain.IntelTestPlan) bool {
	var groupExists int64
	if err := s.DB.Model(&domain.RouteGroup{}).Where("id = ?", plan.RouteGroupID).Count(&groupExists).Error; err != nil {
		return false
	}
	return groupExists > 0
}

// intelQuarantineKeys resolves the testable platform keys for the given
// quarantine states: the plan's group must still exist, the key must exist
// and be enabled, its upstream enabled, and it must support the plan's model.
// Quarantined keys are deliberately outside the group while quarantined, so
// membership is intentionally not checked here; only the group's existence
// stops retesting once an operator deletes it.
func (s *Service) intelQuarantineKeys(plan *domain.IntelTestPlan, states []domain.IntelQuarantineState) []domain.PlatformKey {
	if len(states) == 0 {
		return nil
	}
	if !s.intelPlanGroupExists(plan) {
		return nil
	}
	ids := make([]uint, 0, len(states))
	for _, st := range states {
		ids = append(ids, st.PlatformKeyID)
	}
	var keys []domain.PlatformKey
	if err := s.DB.Preload("Upstream").Where("id IN ? AND status = ?", ids, domain.StatusEnabled).Find(&keys).Error; err != nil {
		return nil
	}
	out := keys[:0]
	for i := range keys {
		k := &keys[i]
		if k.Upstream == nil || k.Upstream.Status != domain.StatusEnabled {
			continue
		}
		if !k.SupportsModel(plan.Model) {
			continue
		}
		out = append(out, *k)
	}
	return out
}

// appendIntelQuarantineKeys merges quarantined keys into the member key list,
// deduplicating by key id.
func appendIntelQuarantineKeys(keys, extra []domain.PlatformKey) []domain.PlatformKey {
	if len(extra) == 0 {
		return keys
	}
	seen := make(map[uint]struct{}, len(keys)+len(extra))
	for _, k := range keys {
		seen[k.ID] = struct{}{}
	}
	for _, k := range extra {
		if _, ok := seen[k.ID]; ok {
			continue
		}
		seen[k.ID] = struct{}{}
		keys = append(keys, k)
	}
	return keys
}

// applyIntelOutcome feeds one judged result into the quarantine state machine.
// Transport errors never reset a pass streak (they are not evidence of a dumb
// model) but they do push the next retest further out.
func (s *Service) applyIntelOutcome(plan *domain.IntelTestPlan, key *domain.PlatformKey, res *domain.IntelTestResult) {
	if !plan.QuarantineEnabled || plan.QuestionKind != domain.IntelQuestionCandy {
		return
	}
	now := time.Now()
	var state domain.IntelQuarantineState
	err := s.DB.Where("plan_id = ? AND platform_key_id = ?", plan.ID, key.ID).First(&state).Error
	hasState := err == nil
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("intel quarantine load plan=%d key=%d: %v", plan.ID, key.ID, err)
		return
	}
	quarantined := hasState && state.Status == domain.IntelQuarantineQuarantined

	switch res.Verdict {
	case domain.IntelVerdictCorrect:
		if quarantined {
			state.PassStreak++
			state.BackoffSec = intelQuarantineBaseSec
			state.LastTestedAt = &now
			next := now.Add(time.Duration(state.BackoffSec) * time.Second)
			state.NextTestAt = &next
			if state.PassStreak >= intelQuarantineRestoreAt {
				s.restoreIntelKey(plan, &state)
				return
			}
			if err := s.DB.Save(&state).Error; err != nil {
				log.Printf("intel quarantine save plan=%d key=%d: %v", plan.ID, key.ID, err)
			}
			return
		}
		s.evaluateIntelQuarantine(plan, key, &state, hasState, now)
	case domain.IntelVerdictIncorrect, domain.IntelVerdictInvalid:
		if quarantined {
			state.PassStreak = 0
			state.BackoffSec = nextIntelBackoff(state.BackoffSec)
			state.LastTestedAt = &now
			next := now.Add(time.Duration(state.BackoffSec) * time.Second)
			state.NextTestAt = &next
			if err := s.DB.Save(&state).Error; err != nil {
				log.Printf("intel quarantine save plan=%d key=%d: %v", plan.ID, key.ID, err)
			}
			return
		}
		s.evaluateIntelQuarantine(plan, key, &state, hasState, now)
	default: // error / anything else: scheduling signal only
		if quarantined {
			state.BackoffSec = nextIntelBackoff(state.BackoffSec)
			state.LastTestedAt = &now
			next := now.Add(time.Duration(state.BackoffSec) * time.Second)
			state.NextTestAt = &next
			if err := s.DB.Save(&state).Error; err != nil {
				log.Printf("intel quarantine save plan=%d key=%d: %v", plan.ID, key.ID, err)
			}
		}
	}
}

// evaluateIntelQuarantine checks the plan's accuracy rule and, when it fails,
// removes the key from the group and records the quarantine state.
func (s *Service) evaluateIntelQuarantine(plan *domain.IntelTestPlan, key *domain.PlatformKey, state *domain.IntelQuarantineState, hasState bool, now time.Time) {
	q := s.DB.Where("plan_id = ? AND platform_key_id = ? AND verdict <> ?", plan.ID, key.ID, domain.IntelVerdictError)
	// Results produced while the key was quarantined are not membership
	// evidence; after a restore the observation window restarts fresh, so a
	// single wrong answer cannot immediately re-quarantine a recovered key.
	if hasState && state.Status == domain.IntelQuarantineRestored && state.RestoredAt != nil {
		q = q.Where("created_at > ?", *state.RestoredAt)
	}
	minSamples, threshold := plan.QuarantineRule()
	var rows []domain.IntelTestResult
	if err := q.Order("id DESC").Limit(minSamples).Find(&rows).Error; err != nil {
		log.Printf("intel quarantine window plan=%d key=%d: %v", plan.ID, key.ID, err)
		return
	}
	if len(rows) < minSamples {
		return
	}
	correct := 0
	for _, r := range rows {
		if r.Verdict == domain.IntelVerdictCorrect {
			correct++
		}
	}
	accuracy := float64(correct) / float64(len(rows)) * 100
	// A perfect score is never quarantined, so a threshold of 100 cannot
	// remove keys that answer correctly every time.
	if accuracy > threshold || accuracy >= 100 {
		return
	}
	// Only keys currently in the group can be quarantined; if the operator
	// already removed the key there is nothing to protect.
	var member int64
	if err := s.DB.Model(&domain.RouteGroupKey{}).
		Where("route_group_id = ? AND platform_key_id = ?", plan.RouteGroupID, key.ID).
		Count(&member).Error; err != nil {
		log.Printf("intel quarantine member plan=%d key=%d: %v", plan.ID, key.ID, err)
		return
	}
	if member == 0 {
		return
	}

	reason := fmt.Sprintf("最近 %d 次有效测试正确率 %.0f%%（%d/%d），未达到阈值 %.0f%%",
		len(rows), accuracy, correct, len(rows), threshold)
	backoff := intelQuarantineBaseSec
	next := now.Add(time.Duration(backoff) * time.Second)
	upstreamName := key.Upstream.Name
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("route_group_id = ? AND platform_key_id = ?", plan.RouteGroupID, key.ID).
			Delete(&domain.RouteGroupKey{}).Error; err != nil {
			return err
		}
		if hasState {
			state.Status = domain.IntelQuarantineQuarantined
			state.QuarantineCount++
			state.PassStreak = 0
			state.BackoffSec = backoff
			state.Reason = reason
			state.QuarantinedAt = now
			state.RestoredAt = nil
			state.NextTestAt = &next
			state.LastTestedAt = &now
			return tx.Save(state).Error
		}
		fresh := domain.IntelQuarantineState{
			PlanID: plan.ID, PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
			KeyName: key.Name, UpstreamName: upstreamName,
			Status: domain.IntelQuarantineQuarantined, BackoffSec: backoff,
			NextTestAt: &next, PassStreak: 0, QuarantineCount: 1,
			Reason: reason, QuarantinedAt: now, LastTestedAt: &now,
		}
		if err := tx.Create(&fresh).Error; err != nil {
			return err
		}
		*state = fresh
		return nil
	})
	if err != nil {
		log.Printf("intel quarantine apply plan=%d key=%d: %v", plan.ID, key.ID, err)
		return
	}
	log.Printf("intel key %d (%s) quarantined by plan %d: %s", key.ID, key.Name, plan.ID, reason)
}

// restoreIntelKey re-adds the quarantined key to the plan's group and closes
// the quarantine round. Idempotent when the operator already re-added it.
// When the plan's group no longer exists the key is not re-added anywhere;
// the state is dropped so the scoreboard keeps no phantom quarantine.
func (s *Service) restoreIntelKey(plan *domain.IntelTestPlan, state *domain.IntelQuarantineState) {
	now := time.Now()
	if !s.intelPlanGroupExists(plan) {
		if err := s.DB.Delete(&domain.IntelQuarantineState{}, state.ID).Error; err != nil {
			log.Printf("intel quarantine restore drop plan=%d key=%d: %v", plan.ID, state.PlatformKeyID, err)
			return
		}
		log.Printf("intel key %d (%s) quarantine state dropped: group %d no longer exists (plan %d)",
			state.PlatformKeyID, state.KeyName, plan.RouteGroupID, plan.ID)
		return
	}
	err := s.DB.Transaction(func(tx *gorm.DB) error {
		row := domain.RouteGroupKey{RouteGroupID: plan.RouteGroupID, PlatformKeyID: state.PlatformKeyID, CreatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"status":       domain.IntelQuarantineRestored,
			"restored_at":  now,
			"pass_streak":  0,
			"next_test_at": nil,
		}
		return tx.Model(&domain.IntelQuarantineState{}).Where("id = ?", state.ID).Updates(updates).Error
	})
	if err != nil {
		log.Printf("intel quarantine restore plan=%d key=%d: %v", plan.ID, state.PlatformKeyID, err)
		return
	}
	log.Printf("intel key %d (%s) restored to group %d by plan %d", state.PlatformKeyID, state.KeyName, plan.RouteGroupID, plan.ID)
}

// runIntelQuarantineDue reports plan ids that have a quarantined key due for
// retest, restricted to enabled candy plans with quarantine switched on and
// whose route group still exists (a deleted group has nothing to protect).
func (s *Service) runIntelQuarantineDue() []uint {
	var planIDs []uint
	err := s.DB.Model(&domain.IntelQuarantineState{}).
		Select("DISTINCT intel_quarantine_states.plan_id").
		Joins("JOIN intel_test_plans ON intel_test_plans.id = intel_quarantine_states.plan_id").
		Joins("JOIN route_groups ON route_groups.id = intel_test_plans.route_group_id").
		Where("intel_quarantine_states.status = ?", domain.IntelQuarantineQuarantined).
		Where("intel_quarantine_states.next_test_at IS NULL OR intel_quarantine_states.next_test_at <= ?", time.Now()).
		Where("intel_test_plans.enabled = ? AND intel_test_plans.quarantine_enabled = ? AND intel_test_plans.question_kind = ?",
			true, true, domain.IntelQuestionCandy).
		Pluck("plan_id", &planIDs).Error
	if err != nil {
		log.Printf("intel quarantine scan: %v", err)
		return nil
	}
	return planIDs
}

// RestoreIntelPlanQuarantined re-adds every quarantined key of a plan to its
// group. Used when the plan is deleted so keys are never stranded outside.
func (s *Service) RestoreIntelPlanQuarantined(planID uint) {
	var plan domain.IntelTestPlan
	if err := s.DB.First(&plan, planID).Error; err != nil {
		return
	}
	states, err := s.intelQuarantinedStates(planID, false)
	if err != nil || len(states) == 0 {
		return
	}
	var groupExists int64
	if err := s.DB.Model(&domain.RouteGroup{}).Where("id = ?", plan.RouteGroupID).Count(&groupExists).Error; err != nil || groupExists == 0 {
		return
	}
	ids := make([]uint, 0, len(states))
	for _, st := range states {
		ids = append(ids, st.PlatformKeyID)
	}
	var liveIDs []uint
	if err := s.DB.Model(&domain.PlatformKey{}).Where("id IN ?", ids).Pluck("id", &liveIDs).Error; err != nil {
		return
	}
	now := time.Now()
	for _, keyID := range liveIDs {
		row := domain.RouteGroupKey{RouteGroupID: plan.RouteGroupID, PlatformKeyID: keyID, CreatedAt: now}
		if err := s.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			log.Printf("intel quarantine restore-on-delete plan=%d key=%d: %v", planID, keyID, err)
		}
	}
}

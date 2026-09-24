package handler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/routinghealth"
)

const recoveryPollInterval = 250 * time.Millisecond
const recoveryWaitLimit = 5 * time.Second

type recoveryWaitState struct {
	deadline                     time.Time
	timedOut                     bool
	budgetReady, explorationSlot bool
}

type recoveryGateState struct {
	Generation              uint64
	Open                    bool
	Until, LeaseUntil       time.Time
	CheckUntil, NextCheckAt time.Time
	CheckOK                 bool
}

func (s recoveryGateState) equal(other recoveryGateState) bool {
	return s.Generation == other.Generation && s.Open == other.Open && s.CheckOK == other.CheckOK &&
		s.Until.Equal(other.Until) && s.LeaseUntil.Equal(other.LeaseUntil) && s.CheckUntil.Equal(other.CheckUntil) && s.NextCheckAt.Equal(other.NextCheckAt)
}

type recoveryPollEntry struct {
	done    chan struct{}
	expires time.Time
	state   recoveryGateState
	err     error
}

type recoveryPoller struct {
	mu        sync.Mutex
	entries   map[string]*recoveryPollEntry
	cleanedAt time.Time
}

// Cache only small gate state, never candidate lists or credentials. Concurrent
// waiters on the same scope share a read and at most one sample per 250ms.
func (p *recoveryPoller) get(ctx context.Context, db *gorm.DB, scope string) (recoveryGateState, error) {
	p.mu.Lock()
	if p.entries == nil {
		p.entries = make(map[string]*recoveryPollEntry)
	}
	now := time.Now()
	if now.Sub(p.cleanedAt) >= time.Second {
		for key, e := range p.entries {
			if !e.expires.IsZero() && now.After(e.expires) {
				delete(p.entries, key)
			}
		}
		p.cleanedAt = now
	}
	if e := p.entries[scope]; e != nil && (e.expires.IsZero() || now.Before(e.expires)) {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return recoveryGateState{}, ctx.Err()
		case <-e.done:
			return e.state, e.err
		}
	}
	e := &recoveryPollEntry{done: make(chan struct{})}
	p.entries[scope] = e
	p.mu.Unlock()
	// One caller cancelling must not cancel the shared read for other waiters.
	go func() {
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		var row domain.RoutingCircuit
		err := db.WithContext(queryCtx).Select("generation", "open", "until", "lease_until", "check_until", "next_check_at", "check_ok").Where("scope = ?", scope).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = nil
		}
		state := recoveryGateState{Generation: row.Generation, Open: row.Open, Until: row.Until, LeaseUntil: row.LeaseUntil, CheckOK: row.CheckOK}
		if row.CheckUntil != nil {
			state.CheckUntil = *row.CheckUntil
		}
		if row.NextCheckAt != nil {
			state.NextCheckAt = *row.NextCheckAt
		}
		p.mu.Lock()
		e.state, e.err, e.expires = state, err, time.Now().Add(recoveryPollInterval)
		if err != nil {
			delete(p.entries, scope)
		}
		close(e.done)
		p.mu.Unlock()

	}()
	select {
	case <-ctx.Done():
		return recoveryGateState{}, ctx.Err()
	case <-e.done:
		return e.state, e.err
	}

}

func recoveryWaitScopes(req picker.Request, d picker.Decision) []string {
	seen := map[string]bool{}
	var scopes []string
	for _, c := range d.Candidates {
		if c.SkipReason != "recovery_inflight" && c.SkipReason != "recovery_check_inflight" {
			continue
		}
		dim := routinghealth.Dimension{KeyID: c.KeyID, Protocol: req.Protocol, Model: req.Model, Path: req.Path, Stream: req.Stream}
		for _, scope := range append([]string{routinghealth.KeyScope(c.KeyID), dim.Scope()}, c.RecoveryScopes...) {
			if !seen[scope] {
				seen[scope] = true
				scopes = append(scopes, scope)
			}
		}
	}
	return scopes
}

func (h *Gateway) pickWithRecoveryWait(c *gin.Context, lg *liveLog, req *picker.Request, previous string, started time.Time, wait *recoveryWaitState) (*domain.PlatformKey, *domain.Upstream, picker.Decision, error) {
	dp, ok := h.Picker.(picker.DecisionPicker)
	if !ok {
		key, up, err := h.Picker.Pick(c.Request.Context(), *req)
		return key, up, picker.Decision{}, err
	}
	if previous != "" {
		req.Session = dp.ResolvePrevious(c.Request.Context(), *req, previous)
	}
	if wait.budgetReady {
		req.BudgetReady, req.ExplorationSlot = true, wait.explorationSlot
	}
	for {
		key, up, decision, err := dp.PickDecision(c.Request.Context(), *req)
		req.BudgetReady, req.ExplorationSlot = decision.BudgetReady, decision.ExplorationSlot
		wait.budgetReady, wait.explorationSlot = decision.BudgetReady, decision.ExplorationSlot
		if !errors.Is(err, picker.ErrNoUpstream) {
			return key, up, decision, err
		}
		scopes := recoveryWaitScopes(*req, decision)
		wait.timedOut = false
		if len(scopes) == 0 {
			return key, up, decision, err
		}
		if wait.deadline.IsZero() {
			duration := h.recoveryWait
			if duration <= 0 {
				duration = recoveryWaitLimit
			}
			wait.deadline = time.Now().Add(duration)
			if overall := started.Add(proxyOverallTimeout); overall.Before(wait.deadline) {
				wait.deadline = overall
			}
			lg.traceEvent(h, nil, nil, "waiting", "recovery_in_progress")
		}
		if !time.Now().Before(wait.deadline) {
			wait.timedOut = true
			lg.traceEvent(h, nil, nil, "wait_finished", "recovery_wait_timeout")
			return key, up, decision, err
		}
		ctx, cancel := context.WithDeadline(c.Request.Context(), wait.deadline)
		changed, waitErr := h.waitRecoveryChange(ctx, scopes)
		cancel()
		if c.Request.Context().Err() != nil {
			lg.traceEvent(h, nil, nil, "wait_finished", "client_cancelled")
			return nil, nil, decision, c.Request.Context().Err()
		}
		if waitErr != nil && !errors.Is(waitErr, context.DeadlineExceeded) {
			return nil, nil, decision, waitErr
		}
		if !changed {
			// Use a fresh decision at the deadline rather than logging the snapshot
			// from before the wait; recovery may have finished between poll ticks.
			key, up, decision, err = dp.PickDecision(c.Request.Context(), *req)
			wait.timedOut = errors.Is(err, picker.ErrNoUpstream) && len(recoveryWaitScopes(*req, decision)) > 0
			reason := "recovery_state_changed"
			if wait.timedOut {
				reason = "recovery_wait_timeout"
			}
			lg.traceEvent(h, nil, nil, "wait_finished", reason)
			return key, up, decision, err
		}
		lg.traceEvent(h, nil, nil, "wait_finished", "recovery_state_changed")
	}
}

func (h *Gateway) waitRecoveryChange(ctx context.Context, scopes []string) (bool, error) {
	var before []recoveryGateState
	ticker := time.NewTicker(recoveryPollInterval)
	defer ticker.Stop()
	for {
		states := make([]recoveryGateState, 0, len(scopes))
		active := false
		now := time.Now()
		for _, scope := range scopes {
			state, err := h.recoveryPoll.get(ctx, h.DB, scope)
			if err != nil {
				return false, err
			}
			states = append(states, state)
			active = active || (state.Open && (state.LeaseUntil.After(now) || state.CheckUntil.After(now)))
		}
		if !active {
			return true, nil
		}
		if before != nil {
			for i := range states {
				if !states[i].equal(before[i]) {
					return true, nil
				}
			}
		}
		before = states
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

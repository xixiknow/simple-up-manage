package picker

import (
	"context"
	"math"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/routinghealth"
	"time"
)

type CircuitController interface{ RoutingHealth() routinghealth.Store }

func (p *BandPicker) RoutingHealth() routinghealth.Store {
	return routinghealth.Store{DB: p.db, Settings: p.Settings()}
}

func dimension(req Request, key uint) routinghealth.Dimension {
	return routinghealth.Dimension{KeyID: key, Protocol: req.Protocol, Model: req.Model, Path: req.Path, Stream: req.Stream}
}

func (p *BandPicker) prepareBudget(ctx context.Context, req Request, mutate bool) (Request, error) {
	req.MutateRecovery = mutate
	if req.BudgetReady {
		return req, nil
	}
	req.BudgetReady = true
	if len(req.Exclude)+len(req.ExcludeProviders)+len(req.ExcludeKeyModels) > 0 {
		return req, nil
	}
	interval := 0
	if ratio := p.Settings().ExplorationRatio; ratio > 0 {
		interval = int(math.Ceil(1 / ratio))
	}
	slot, err := (routinghealth.Store{DB: p.db}).Slot(ctx, routeScope(req), interval, mutate)
	req.ExplorationSlot = slot
	return req, err
}

func (p *BandPicker) applyRoutingHealth(ctx context.Context, req Request, cands []Candidate) error {
	dims := make([]routinghealth.Dimension, 0, len(cands))
	ids := make([]uint, 0, len(cands))
	for _, c := range cands {
		dims = append(dims, dimension(req, c.KeyID))
		ids = append(ids, c.KeyID)
	}
	states, err := (routinghealth.Store{DB: p.db}).Snapshot(ctx, dims)
	if err != nil {
		return err
	}
	// Key-wide recovery must also wait for leases on other dimensions, just as
	// Admit does. Otherwise repeated selection would keep choosing a blocked key.
	var gatedKeys []uint
	for _, id := range ids {
		if _, ok := states[routinghealth.KeyScope(id)]; ok {
			gatedKeys = append(gatedKeys, id)
		}
	}
	busyByKey := map[uint][]domain.RoutingCircuit{}
	if len(gatedKeys) > 0 {
		var busy []domain.RoutingCircuit
		now := time.Now()
		if err := p.db.WithContext(ctx).Where("platform_key_id IN ? AND (lease_until > ? OR check_until > ?)", gatedKeys, now, now).Order("scope").Find(&busy).Error; err != nil {
			return err
		}
		for _, r := range busy {
			busyByKey[r.PlatformKeyID] = append(busyByKey[r.PlatformKeyID], r)
		}
	}
	var probes []domain.ProbeLog
	if req.Diagnostic && len(ids) > 0 {
		q := p.db.WithContext(ctx).Model(&domain.ProbeLog{}).Select("*, ROW_NUMBER() OVER (PARTITION BY platform_key_id ORDER BY created_at DESC, id DESC) AS sample_rank").Where("platform_key_id IN ? AND kind IN ?", ids, []string{domain.ProbeLight, domain.ProbeDeep})
		if err := p.db.WithContext(ctx).Table("(?) AS recent", q).Where("sample_rank = 1").Find(&probes).Error; err != nil {
			return err
		}
	}
	byKey := map[uint]domain.ProbeLog{}
	for _, r := range probes {
		byKey[r.PlatformKeyID] = r
	}
	now := time.Now()
	normal := 0
	bestRequest := -1
	bestAny := -1
	for i := range cands {
		c := &cands[i]
		c.CircuitState = "closed"
		if req.Diagnostic {
			c.ProbeStatus = "unknown"
		}
		if r, ok := byKey[c.KeyID]; ok {
			c.ProbeAt = r.CreatedAt.UnixMilli()
			c.ProbeModel = r.Model
			c.ProbePath = r.Path
			c.ProbeStream = r.Stream
			c.ProbeStatus = "down"
			if r.Success {
				c.ProbeStatus = "healthy"
				if r.LatencyMs >= 6000 {
					c.ProbeStatus = "degraded"
				}
			}
		}
		var gate domain.RoutingCircuit
		for _, scope := range []string{dimension(req, c.KeyID).Scope(), routinghealth.KeyScope(c.KeyID)} {
			if r, ok := states[scope]; ok {
				if gate.Scope == "" || recoveryGatePriority(r, now) > recoveryGatePriority(gate, now) || (recoveryGatePriority(r, now) == recoveryGatePriority(gate, now) && r.Until.After(gate.Until)) {
					gate = r
				}
			}
		}
		for _, r := range busyByKey[c.KeyID] {
			c.RecoveryScopes = append(c.RecoveryScopes, r.Scope)
			if recoveryGatePriority(r, now) > recoveryGatePriority(gate, now) {
				gate = r
			}
		}
		if gate.Scope != "" {
			c.RecoveryCheckAt, c.RecoveryNextCheckAt = gate.CheckAt, gate.NextCheckAt
			c.RecoveryCheckError, c.RecoveryLastAt = gate.CheckError, gate.RecoveryAt
			c.RecoveryStatus = "cooldown"
			c.CircuitScope = "request"
			if gate.Scope == routinghealth.KeyScope(c.KeyID) {
				c.CircuitScope = "key"
			}
			c.CircuitReason = gate.Reason
			c.CircuitUntil = gate.Until.UnixMilli()
			c.CircuitState = "open"
			reason := "business_cooldown"
			if !gate.Until.After(now) {
				c.CircuitState = "half_open"
				reason = "recovery_pending"
				c.RecoveryStatus = "waiting_check"
				if gate.CheckAt != nil && !gate.CheckOK {
					c.RecoveryStatus = "check_failed"
				}
				if !c.Key.AllowsProbe() || gate.Path == "" || !routinghealth.SupportsCheck(gate.Path) {
					c.RecoveryStatus = "waiting_request"
				}
			}
			if gate.CheckUntil != nil && gate.CheckUntil.After(now) {
				reason = "recovery_check_inflight"
				c.RecoveryStatus = "checking"
			}
			if !gate.CheckOK && gate.CheckAt != nil && gate.NextCheckAt != nil && gate.NextCheckAt.After(now) {
				reason = "recovery_check_backoff"
				c.RecoveryStatus = "check_failed"
			}
			if gate.LeaseUntil.After(now) {
				reason = "recovery_inflight"
				c.CircuitUntil = gate.LeaseUntil.UnixMilli()
				c.RecoveryStatus = "validating"
			}
			if c.Eligible {
				c.Eligible = false
				c.SkipReason = reason
				// waiting_check/check_failed/backoff gates belong to the
				// synthetic checker, but they qualify as a last resort when
				// nothing else in the group is healthy.
				if reason == "recovery_pending" || reason == "recovery_check_backoff" {
					if reason == "recovery_pending" && c.RecoveryStatus == "waiting_request" {
						if bestRequest < 0 || recoveryBefore(*c, cands[bestRequest]) {
							bestRequest = i
						}
					} else if bestAny < 0 || recoveryBefore(*c, cands[bestAny]) {
						bestAny = i
					}
				}
			}
		} else if c.Eligible {
			normal++
		}
	}
	// Only a currently eligible binding protects a session from validation.
	bound := false
	var boundKey uint
	if req.Session != "" && p.Settings().RankingMode == "stable_latency" {
		if err := p.withStableState(ctx, routeScope(req), false, func(s *stableState) { boundKey = s.Bindings[req.Session].Key }); err != nil {
			return err
		}
	} else if req.Session != "" {
		boundKey = p.stickyKey(ctx, req, p.Settings())
	}
	for _, c := range cands {
		if c.KeyID == boundKey && c.Eligible {
			bound = true
		}
	}
	// Prefer the real-request path (disabled probes, historical dimensions);
	// when no normal candidate exists, a check-owned half-open gate may be
	// validated by the request itself — it then doubles as the evidence.
	recovery := bestRequest
	if normal == 0 && bestAny >= 0 && (recovery < 0 || recoveryBefore(cands[bestAny], cands[recovery])) {
		recovery = bestAny
	}
	allowed := recovery >= 0 && normal == 0
	if recovery >= 0 && normal > 0 && !bound {
		allowed, err = (routinghealth.Store{DB: p.db}).RecoverySlot(ctx, routeScope(req), req.MutateRecovery)
		if err != nil {
			return err
		}
	}
	for i := range cands {
		if cands[i].RecoveryStatus == "waiting_request" && normal > 0 {
			if bound {
				cands[i].RecoveryStatus = "waiting_session"
			} else if !allowed {
				cands[i].RecoveryStatus = "waiting_budget"
			}
		}
	}
	if allowed {
		cands[recovery].Eligible = true
		cands[recovery].Recovery = true
		cands[recovery].SkipReason = ""
	}
	return nil
}

func recoveryBefore(a, b Candidate) bool {
	if a.RecoveryLastAt == nil && b.RecoveryLastAt != nil {
		return true
	}
	if a.RecoveryLastAt != nil && b.RecoveryLastAt == nil {
		return false
	}
	if a.RecoveryLastAt != nil && !a.RecoveryLastAt.Equal(*b.RecoveryLastAt) {
		return a.RecoveryLastAt.Before(*b.RecoveryLastAt)
	}
	if a.CircuitUntil != b.CircuitUntil {
		return a.CircuitUntil < b.CircuitUntil
	}
	return a.KeyID < b.KeyID
}

func recoveryGatePriority(r domain.RoutingCircuit, now time.Time) int {
	if r.LeaseUntil.After(now) {
		return 5
	}
	if r.CheckUntil != nil && r.CheckUntil.After(now) {
		return 4
	}
	if r.Until.After(now) {
		return 3
	}
	if !r.CheckOK && r.NextCheckAt != nil && r.NextCheckAt.After(now) {
		return 3
	}
	if !r.CheckOK && r.Path != "" {
		return 2
	}
	return 1
}

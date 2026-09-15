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
	recovery := -1
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
				if gate.Scope == "" || r.Until.After(gate.Until) || r.LeaseUntil.After(now) {
					gate = r
				}
			}
		}
		if gate.Scope != "" {
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
			}
			if gate.LeaseUntil.After(now) {
				reason = "recovery_inflight"
				c.CircuitUntil = gate.LeaseUntil.UnixMilli()
			}
			if c.Eligible {
				c.Eligible = false
				c.SkipReason = reason
				if reason == "recovery_pending" && (recovery < 0 || c.CircuitUntil < cands[recovery].CircuitUntil) {
					recovery = i
				}
			}
		} else if c.Eligible {
			normal++
		}
	}
	// Recovery uses the same slot as exploration. Active bound sessions keep
	// their key while healthy alternatives exist.
	bound := false
	if req.Session != "" && p.Settings().RankingMode == "stable_latency" {
		if err := p.withStableState(ctx, routeScope(req), false, func(s *stableState) { _, bound = s.Bindings[req.Session] }); err != nil {
			return err
		}
	} else if req.Session != "" {
		bound = p.stickyKey(ctx, req, p.Settings()) > 0
	}
	if recovery >= 0 && (normal == 0 || (req.ExplorationSlot && !bound)) {
		cands[recovery].Eligible = true
		cands[recovery].Recovery = true
		cands[recovery].SkipReason = ""
	}
	return nil
}

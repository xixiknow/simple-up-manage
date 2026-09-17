package picker

import (
	"context"
	"simple-up-manage/internal/domain"
	"testing"
	"time"
)

func TestProbeFailureKeepsStableBinding(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	samples(t, p, req, keys[0].ID, true, 1000, 8, time.Now())
	samples(t, p, req, keys[1].ID, true, 5000, 8, time.Now())
	key, _, d, err := p.PickDecision(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	p.CommitSuccess(ctx, req, d, key.ID, "")
	if err := p.db.Create(&domain.ProbeLog{PlatformKeyID: key.ID, Kind: domain.ProbeDeep, Protocol: "openai", Model: req.Model, Path: "/v1/chat/completions", Success: false, StatusCode: 400}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		next, _, decision, err := p.PickDecision(ctx, req)
		if err != nil || next.ID != key.ID || decision.Reason != "reuse" {
			t.Fatalf("probe disrupted binding: %+v %v", decision, err)
		}
	}
	preview, err := p.Explain(ctx, req)
	if err != nil || preview[0].ProbeStatus != "down" {
		t.Fatalf("missing diagnostic probe state: %+v %v", preview, err)
	}
}

func TestRecoveryHasIndependentTimeBudgetAndDoesNotRebind(t *testing.T) {
	for _, mode := range []string{"adaptive", "stable_latency"} {
		t.Run(mode, func(t *testing.T) {
			p, keys, req := stableFixture(t)
			p.cfg.RankingMode = mode
			p.cfg.ExplorationRatio = 0
			ctx := context.Background()
			gate := domain.RoutingCircuit{Scope: dimension(req, keys[1].ID).Scope(), PlatformKeyID: keys[1].ID, Open: true, Until: time.Now().Add(-time.Second), Reason: "test_failure"}
			if err := p.db.Create(&gate).Error; err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 40; i++ {
				preview, err := p.Explain(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				key, _, d, err := p.PickDecision(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if selectedKey(preview) != key.ID {
					t.Fatal("preview disagrees with next selection")
				}
				if i == 1 || i == 21 {
					if key.ID != keys[1].ID || d.Exploration || d.Reason != "recovery_validation" {
						t.Fatalf("missing recovery at %d: %+v", i, d)
					}
				} else if key.ID != keys[0].ID || d.Exploration {
					t.Fatalf("extra exploration at %d: %+v", i, d)
				}
				p.CommitSuccess(ctx, req, d, key.ID, "")
				if i == 20 {
					if err := p.db.Model(&domain.RoutingBudget{}).Where("scope = ?", "recovery:"+routeScope(req)).Update("recovery_after", time.Now().Add(-time.Second)).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "stable_latency" {
				_ = p.withStableState(ctx, routeScope(req), false, func(s *stableState) {
					if s.Bindings[""].Key != keys[0].ID {
						t.Fatal("recovery replaced preferred key")
					}
				})
			}
		})
	}
}

func TestRecoveryProtectsOnlyEligibleBinding(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(map[bool]string{true: "healthy", false: "unavailable"}[available], func(t *testing.T) {
			p, keys, req := stableFixture(t)
			p.cfg.ExplorationRatio = 0
			req.Session = "active-session"
			ctx := context.Background()
			p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "")
			if !available {
				req.Exclude = []uint{keys[0].ID}
			}
			gate := domain.RoutingCircuit{Scope: dimension(req, keys[1].ID).Scope(), PlatformKeyID: keys[1].ID, Open: true, Until: time.Now().Add(-time.Minute)}
			if err := p.db.Create(&gate).Error; err != nil {
				t.Fatal(err)
			}
			key, _, d, err := p.PickDecision(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if available && (key.ID != keys[0].ID || d.Reason != "reuse") {
				t.Fatalf("binding disrupted: %+v", d)
			}
			if !available && (key.ID != keys[1].ID || d.Reason != "recovery_validation") {
				t.Fatalf("stale binding blocked recovery: %+v", d)
			}
		})
	}
}

func TestRecoveryChecksBeforeRealTraffic(t *testing.T) {
	p, keys, req := stableFixture(t)
	p.cfg.ExplorationRatio = 0
	ctx := context.Background()
	dim := dimension(req, keys[1].ID)
	gate := domain.RoutingCircuit{Scope: dim.Scope(), PlatformKeyID: keys[1].ID, Open: true, Until: time.Now().Add(-time.Minute), Path: "/v1/responses", Protocol: "openai", Model: req.Model}
	req.Path = "/v1/responses"
	gate.Scope = dimension(req, keys[1].ID).Scope()
	if err := p.db.Create(&gate).Error; err != nil {
		t.Fatal(err)
	}
	key, _, _, err := p.PickDecision(ctx, req)
	if err != nil || key.ID != keys[0].ID {
		t.Fatalf("unchecked recovery selected: %v", err)
	}
	if err := p.db.Model(&gate).Update("check_ok", true).Error; err != nil {
		t.Fatal(err)
	}
	key, _, d, err := p.PickDecision(ctx, req)
	if err != nil || key.ID != keys[1].ID || d.Reason != "recovery_validation" {
		t.Fatalf("ready recovery not selected: %+v %v", d, err)
	}
}

func TestExpiredGateCanRecoverWithoutNormalCandidates(t *testing.T) {
	p, keys, req := stableFixture(t)
	req.AllowKeys = map[uint]struct{}{keys[0].ID: {}}
	if err := p.db.Create(&domain.RoutingCircuit{Scope: dimension(req, keys[0].ID).Scope(), PlatformKeyID: keys[0].ID, Open: true, Until: time.Now().Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	key, _, d, err := p.PickDecision(context.Background(), req)
	if err != nil || key.ID != keys[0].ID || d.Reason != "recovery_validation" {
		t.Fatalf("no-candidate recovery: %+v %v", d, err)
	}
}

func TestLegacyBackfillExcludesNeutralRequests(t *testing.T) {
	p, _, keys := testBandPicker(t)
	for _, action := range []string{"request_rejected", "client_cancelled", "exclude_busy_resource"} {
		if err := p.db.Create(&domain.RequestLog{PlatformKeyID: &keys[0].ID, Model: "m", Success: false, FailureAction: action, CreatedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := p.db.Create(&domain.RequestLog{PlatformKeyID: &keys[0].ID, Model: "m", Success: true, TTFTMs: 1000, CreatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	cands, err := p.Explain(context.Background(), Request{Protocol: "openai", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Samples != 1 || cands[0].SuccessRate != 1 {
		t.Fatalf("neutral backfill corrupted metrics: %+v", cands[0])
	}
}

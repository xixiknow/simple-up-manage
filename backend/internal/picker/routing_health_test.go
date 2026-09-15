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

func TestRecoverySharesExplorationBudgetAndDoesNotRebind(t *testing.T) {
	for _, mode := range []string{"adaptive", "stable_latency"} {
		t.Run(mode, func(t *testing.T) {
			p, keys, req := stableFixture(t)
			p.cfg.RankingMode = mode
			p.cfg.ExplorationRatio = .05
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
				if i%20 == 0 {
					if key.ID != keys[1].ID || !d.Exploration || d.Reason != "recovery_validation" {
						t.Fatalf("missing recovery at %d: %+v", i, d)
					}
				} else if key.ID != keys[0].ID || d.Exploration {
					t.Fatalf("extra exploration at %d: %+v", i, d)
				}
				p.CommitSuccess(ctx, req, d, key.ID, "")
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

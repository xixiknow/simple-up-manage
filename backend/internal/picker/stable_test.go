package picker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"simple-up-manage/internal/domain"
)

func stableFixture(t *testing.T) (*BandPicker, []domain.PlatformKey, Request) {
	t.Helper()
	p, _, keys := testBandPicker(t)
	if err := p.db.AutoMigrate(&domain.RequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	p.cfg.RankingMode = "stable_latency"
	p.cfg.ExplorationRatio = 0
	return p, keys, Request{ConsumerID: 1, Protocol: "openai", Model: "m", Path: "/v1/responses", Stream: true}
}

func samples(t *testing.T, p *BandPicker, req Request, key uint, success bool, latency, n int, at time.Time) {
	t.Helper()
	result := "upstream_failure"
	if success {
		result = "success"
	}
	for i := 0; i < n; i++ {
		a := domain.RequestAttempt{ID: uuid.NewString(), PlatformKeyID: key, Protocol: req.Protocol, Model: req.Model, Path: req.Path, Stream: req.Stream, StatsVersion: 2, Result: result, TTFTMs: latency, CompletedAt: at, StartedAt: at.Add(-time.Second)}
		if err := p.db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestStableReusesDespiteLoadAndSmallLatencyChange(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	samples(t, p, req, keys[0].ID, true, 4000, 6, time.Now())
	samples(t, p, req, keys[1].ID, true, 7000, 6, time.Now())
	key, _, d, err := p.PickDecision(ctx, req)
	if err != nil || key.ID != keys[0].ID {
		t.Fatalf("initial: %+v %v", d, err)
	}
	p.CommitSuccess(ctx, req, d, key.ID, "")
	if err := p.db.Model(&domain.RequestAttempt{}).Where("platform_key_id = ?", keys[1].ID).Update("ttft_ms", 3500).Error; err != nil {
		t.Fatal(err)
	}
	p.runtime.keyInflight[keys[0].ID] = 10
	for i := 0; i < 30; i++ {
		key, _, d, err = p.PickDecision(ctx, req)
		if err != nil || key.ID != keys[0].ID || d.Reason != "reuse" {
			t.Fatalf("unstable: %+v %v", d, err)
		}
	}
}

func TestStableConfirmationRequiresTimeAndNewSamples(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	samples(t, p, req, keys[0].ID, true, 10000, 6, time.Now())
	samples(t, p, req, keys[1].ID, true, 5000, 6, time.Now().Add(-2*time.Minute))
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "")
	key, _, d, err := p.PickDecision(ctx, req)
	if err != nil || key.ID != keys[0].ID {
		t.Fatalf("switched early: %+v %v", d, err)
	}
	_ = p.withStableState(ctx, routeScope(req), true, func(s *stableState) {
		b := s.Bindings[""]
		b.Since = time.Now().Add(-61 * time.Second).UnixMilli()
		s.Bindings[""] = b
	})
	key, _, _, _ = p.PickDecision(ctx, req)
	if key.ID != keys[0].ID {
		t.Fatal("switched without new samples")
	}
	samples(t, p, req, keys[1].ID, true, 5000, 3, time.Now())
	key, _, d, err = p.PickDecision(ctx, req)
	if err != nil || key.ID != keys[1].ID || d.Reason != "latency_improved" {
		t.Fatalf("did not improve: %+v %v", d, err)
	}
}

func TestStableStatisticsIsolationAndExpiry(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	other := req
	other.Model = "other"
	samples(t, p, other, keys[0].ID, true, 1, 8, time.Now())
	samples(t, p, req, keys[0].ID, true, 1, 8, time.Now().Add(-16*time.Minute))
	samples(t, p, req, keys[1].ID, true, 4000, 6, time.Now())
	samples(t, p, req, keys[1].ID, false, 1, 1, time.Now())
	cands, err := p.Explain(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if cands[0].Samples != 0 || cands[0].TTFTp50 != 0 || cands[1].TTFTp50 != 4000 || cands[1].LatencySamples != 6 {
		t.Fatalf("contaminated: %+v", cands)
	}
	// A fresh picker recovers from attempts, not historical request totals.
	fresh := NewBand(p.db, nil)
	fresh.cfg = p.cfg
	cands, err = fresh.Explain(ctx, req)
	if err != nil || cands[1].Samples != 7 {
		t.Fatalf("recovery: %+v %v", cands, err)
	}
}

func TestStableExplorationAndPreview(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	p.cfg.ExplorationRatio = .05
	samples(t, p, req, keys[0].ID, true, 4000, 6, time.Now())
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "")
	for i := 0; i < 50; i++ {
		if _, err := p.Explain(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 20; i++ {
		_, _, d, err := p.PickDecision(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if d.Exploration != (i == 20) {
			t.Fatalf("exploration at %d: %+v", i, d)
		}
		p.CommitSuccess(ctx, req, d, d.SelectedKeyID, "")
	}
	_, _, d, _ := p.PickDecision(ctx, req)
	if d.SelectedKeyID != keys[0].ID {
		t.Fatal("exploration replaced preferred key")
	}
	req.Session = "session"
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "")
	for i := 0; i < 40; i++ {
		_, _, d, _ := p.PickDecision(ctx, req)
		if d.Exploration {
			t.Fatal("explored existing session")
		}
	}
}

func TestStableCapacityRouteAndLateCompletion(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	samples(t, p, req, keys[0].ID, true, 4000, 6, time.Now())
	samples(t, p, req, keys[1].ID, true, 8000, 6, time.Now())
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "")
	if err := p.db.Model(&domain.PlatformKey{}).Where("id = ?", keys[0].ID).Update("max_concurrency", 1).Error; err != nil {
		t.Fatal(err)
	}
	p.runtime.keyInflight[keys[0].ID] = 1
	_, _, d, err := p.PickDecision(ctx, req)
	if err != nil || d.SelectedKeyID != keys[1].ID || d.Reason != "key_concurrency_exceeded" {
		t.Fatalf("capacity: %+v %v", d, err)
	}
	p.CommitSuccess(ctx, req, d, keys[1].ID, "")
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "")
	_ = p.withStableState(ctx, routeScope(req), false, func(s *stableState) {
		if s.Bindings[""].Key != keys[1].ID {
			t.Fatal("late success overwrote binding")
		}
	})
	other := req
	other.ConsumerID = 2
	if routeScope(req) == routeScope(other) {
		t.Fatal("consumer not isolated")
	}
	other = req
	other.AllowKeys = map[uint]struct{}{keys[1].ID: {}}
	if routeScope(req) == routeScope(other) {
		t.Fatal("route not isolated")
	}
}

func TestResponsesSessionAndPreviousResponse(t *testing.T) {
	p, keys, req := stableFixture(t)
	ctx := context.Background()
	a := HashSession([]byte(`{"instructions":"system","input":[{"role":"user","content":"hello"}]}`))
	b := HashSession([]byte(`{"input":[{"content":"hello","role":"user"},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}],"instructions":"system"}`))
	if a == "" || a != b {
		t.Fatal("multi-turn session changed")
	}
	s, source, _ := RequestSession(http.Header{"Session_id": []string{"one"}}, nil)
	if s == "" || source != "Session_id" {
		t.Fatal("session header ignored")
	}
	req.Session = s
	p.CommitSuccess(ctx, req, Decision{}, keys[0].ID, "resp_1")
	if p.ResolvePrevious(ctx, req, "resp_1") != s {
		t.Fatal("previous response lost session")
	}
	req.ConsumerID = 2
	if p.ResolvePrevious(ctx, req, "resp_1") != "" {
		t.Fatal("response crossed consumer")
	}
}

func TestStableEmptyPreview(t *testing.T) {
	p, _, req := stableFixture(t)
	req.AllowKeys = map[uint]struct{}{}
	cands, err := p.Explain(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Eligible || c.Selected {
			t.Fatal("empty group selected a key")
		}
	}
}

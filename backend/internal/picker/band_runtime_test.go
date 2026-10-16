package picker

import (
	"context"
	"testing"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testBandPicker(t *testing.T) (*BandPicker, domain.Upstream, []domain.PlatformKey) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.Upstream{}, &domain.PlatformKey{}, &domain.RequestLog{}, &domain.KeyModelCooldown{}, &domain.SchedulerSettings{}, &domain.RoutingCircuit{}, &domain.RoutingObservation{}, &domain.RoutingBudget{}, &domain.ProbeLog{}); err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "provider", BaseURL: "https://example.test", Kind: domain.KindOpenAICompat, Protocols: domain.ProtocolOpenAI, Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	keys := []domain.PlatformKey{
		{UpstreamID: up.ID, Name: "key-a", EncryptedKey: "a", Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy, RateMultiplier: 1},
		{UpstreamID: up.ID, Name: "key-b", EncryptedKey: "b", Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy, RateMultiplier: 1},
	}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	return NewBand(db, nil), up, keys
}

func selectedKey(candidates []Candidate) uint {
	for _, candidate := range candidates {
		if candidate.Selected {
			return candidate.KeyID
		}
	}
	return 0
}

func TestBandRankingModes(t *testing.T) {
	p, _, keys := testBandPicker(t)
	ctx := context.Background()
	cfg := p.Settings()
	cfg.RankingMode = "fixed_order"
	p.cfg = cfg
	candidates, err := p.Explain(ctx, Request{Protocol: domain.ProtocolOpenAI, Model: "gpt-test"})
	if err != nil || selectedKey(candidates) != keys[0].ID {
		t.Fatalf("fixed order selected %d, err=%v", selectedKey(candidates), err)
	}

	cfg.RankingMode = "cache_affinity"
	p.cfg = cfg
	req := Request{Protocol: domain.ProtocolOpenAI, Model: "gpt-test", Session: "session-1"}
	first, err := p.Explain(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Explain(ctx, req)
	if err != nil || selectedKey(first) == 0 || selectedKey(first) != selectedKey(second) {
		t.Fatalf("affinity was not stable: %d then %d, err=%v", selectedKey(first), selectedKey(second), err)
	}

	cfg.RankingMode = "load_balance"
	p.cfg = cfg
	key, up, err := p.Pick(ctx, Request{Protocol: domain.ProtocolOpenAI, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := p.TryAcquire(key, up); !ok {
		t.Fatal("first acquire failed")
	}
	defer p.Release(key, up)
	balanced, err := p.Explain(ctx, Request{Protocol: domain.ProtocolOpenAI, Model: "gpt-test"})
	if err != nil || selectedKey(balanced) == key.ID {
		t.Fatalf("load balance did not avoid busy key %d: selected=%d err=%v", key.ID, selectedKey(balanced), err)
	}
}

func TestBandExplicitRankingModesIgnoreStickySession(t *testing.T) {
	p, up, keys := testBandPicker(t)
	ctx := context.Background()
	req := Request{Protocol: domain.ProtocolOpenAI, Model: "gpt-test", Session: "session-1"}
	p.SetSticky(ctx, req.Protocol, req.Session, keys[1].ID)

	cfg := p.Settings()
	cfg.RankingMode = "fixed_order"
	p.cfg = cfg
	fixed, err := p.Explain(ctx, req)
	if err != nil || selectedKey(fixed) != keys[0].ID {
		t.Fatalf("fixed order with session selected %d, err=%v", selectedKey(fixed), err)
	}

	cfg.RankingMode = "load_balance"
	p.cfg = cfg
	if ok, scope := p.TryAcquire(&keys[1], &up); !ok {
		t.Fatalf("acquire sticky key: scope=%q", scope)
	}
	defer p.Release(&keys[1], &up)
	balanced, err := p.Explain(ctx, req)
	if err != nil || selectedKey(balanced) != keys[0].ID {
		t.Fatalf("load balance with session selected %d, err=%v", selectedKey(balanced), err)
	}
}

func TestBandRuntimeLimitsAndRelease(t *testing.T) {
	p, up, keys := testBandPicker(t)
	key := keys[0]
	key.MaxConcurrency = 1
	if ok, scope := p.TryAcquire(&key, &up); !ok || scope != "" {
		t.Fatalf("first key acquire: ok=%v scope=%q", ok, scope)
	}
	if ok, scope := p.TryAcquire(&key, &up); ok || scope != "key" {
		t.Fatalf("key concurrency should reject: ok=%v scope=%q", ok, scope)
	}
	p.Release(&key, &up)
	if ok, scope := p.TryAcquire(&key, &up); !ok || scope != "" {
		t.Fatalf("release did not restore capacity: ok=%v scope=%q", ok, scope)
	}
	p.Release(&key, &up)

	rpmPicker, rpmProvider, rpmKeys := testBandPicker(t)
	rpmKey := rpmKeys[0]
	rpmKey.RPMLimit = 1
	if ok, _ := rpmPicker.TryAcquire(&rpmKey, &rpmProvider); !ok {
		t.Fatal("first RPM acquire failed")
	}
	rpmPicker.Release(&rpmKey, &rpmProvider)
	if ok, scope := rpmPicker.TryAcquire(&rpmKey, &rpmProvider); ok || scope != "key" {
		t.Fatalf("RPM should reject: ok=%v scope=%q", ok, scope)
	}

	p2, provider, providerKeys := testBandPicker(t)
	provider.Concurrency = 1
	if ok, _ := p2.TryAcquire(&providerKeys[0], &provider); !ok {
		t.Fatal("first provider acquire failed")
	}
	if ok, scope := p2.TryAcquire(&providerKeys[1], &provider); ok || scope != "provider" {
		t.Fatalf("provider concurrency should reject: ok=%v scope=%q", ok, scope)
	}
	p2.Release(&providerKeys[0], &provider)
}

func TestBandKeyModelCooldown(t *testing.T) {
	p, _, keys := testBandPicker(t)
	ctx := context.Background()
	p.CooldownKeyModel(ctx, keys[0].ID, "model-a", "rate limited", 429)
	for _, tc := range []struct {
		model  string
		skip   bool
		reason string
	}{
		{model: "model-a", skip: true, reason: "key_model_cooldown"},
		{model: "model-b", skip: false},
	} {
		candidates, err := p.Explain(ctx, Request{Protocol: domain.ProtocolOpenAI, Model: tc.model})
		if err != nil {
			t.Fatal(err)
		}
		var first Candidate
		for _, candidate := range candidates {
			if candidate.KeyID == keys[0].ID {
				first = candidate
			}
		}
		if tc.skip != !first.Eligible || tc.reason != first.SkipReason {
			t.Fatalf("model %q candidate=%+v", tc.model, first)
		}
	}
	if err := p.db.Model(&domain.KeyModelCooldown{}).Where("platform_key_id = ?", keys[0].ID).Update("cooldown_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	candidates, err := p.Explain(ctx, Request{Protocol: domain.ProtocolOpenAI, Model: "model-a"})
	if err != nil || !candidates[0].Eligible {
		t.Fatalf("expired cooldown did not recover: %+v err=%v", candidates[0], err)
	}
}

// Legacy band ranking starves out-of-band keys: without samples they never
// re-enter the band. On a granted exploration slot a sessionless request must
// instead try the coldest eligible candidate outside the band.
func TestBandExplorationSlotFeedsColdestOutOfBandKey(t *testing.T) {
	p, _, keys := testBandPicker(t)
	ctx := context.Background()
	cfg := p.Settings()
	cfg.RankingMode = "adaptive"
	cfg.ExplorationRatio = 0.05
	p.cfg = cfg

	// key-a: healthy history via backfill from request logs.
	for i := 0; i < 8; i++ {
		if err := p.db.Create(&domain.RequestLog{
			PlatformKeyID: &keys[0].ID, Protocol: domain.ProtocolOpenAI, Model: "gpt-test",
			Success: true, TTFTMs: 300, DurationMs: 800, InFlight: false, CreatedAt: time.Now(),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// key-b: nothing — a fresh or window-wiped key outside the band.
	req := Request{Protocol: domain.ProtocolOpenAI, Model: "gpt-test", ExplorationSlot: true, BudgetReady: true}
	cands, err := p.Explain(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := selectedKey(cands); got != keys[1].ID {
		t.Fatalf("exploration slot selected %d, want cold key %d", got, keys[1].ID)
	}
	for _, c := range cands {
		if c.Selected && c.DecisionReason != "exploration" {
			t.Fatalf("selected reason=%q, want exploration", c.DecisionReason)
		}
	}

	// Without a slot the band winner stays selected.
	req.ExplorationSlot = false
	cands, err = p.Explain(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := selectedKey(cands); got != keys[0].ID {
		t.Fatalf("no-slot selected %d, want band winner %d", got, keys[0].ID)
	}

	// Session requests never explore (they would break stickiness).
	req.ExplorationSlot = true
	req.Session = "s"
	cands, err = p.Explain(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := selectedKey(cands); got != keys[0].ID {
		t.Fatalf("session request explored to %d, want band winner %d", got, keys[0].ID)
	}
}

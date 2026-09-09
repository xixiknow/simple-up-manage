package ops

import (
	"context"
	"testing"

	"simple-up-manage/internal/domain"
)

func TestRouteGroupMatches(t *testing.T) {
	g := &domain.RouteGroup{Status: domain.StatusEnabled}
	if !RouteGroupMatches(g, domain.ProtocolOpenAI, "gpt-4o") {
		t.Fatal("empty protocol/models should match anything")
	}
	g.Protocol = domain.ProtocolAnthropic
	if RouteGroupMatches(g, domain.ProtocolOpenAI, "gpt-4o") {
		t.Fatal("protocol mismatch should not match")
	}
	g.Protocol = ""
	g.Models = domain.JSONStrings{"grok-*"}
	if RouteGroupMatches(g, domain.ProtocolOpenAI, "gpt-4o") {
		t.Fatal("model pattern should reject gpt-4o")
	}
	if !RouteGroupMatches(g, domain.ProtocolOpenAI, "grok-3") {
		t.Fatal("model pattern should accept grok-3")
	}
	g.Status = domain.StatusDisabled
	if RouteGroupMatches(g, domain.ProtocolOpenAI, "grok-3") {
		t.Fatal("disabled group should not match")
	}
}

func TestResolveAllowKeys(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	up := domain.Upstream{Name: "A", BaseURL: "https://a", Kind: domain.KindSub2API, Protocols: "openai,anthropic", Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	mk := func(name string) uint {
		k := domain.PlatformKey{UpstreamID: up.ID, Name: name, EncryptedKey: "x", Status: domain.StatusEnabled}
		if err := db.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
		return k.ID
	}
	k1, k2, k3 := mk("k1"), mk("k2"), mk("k3")

	openaiA := domain.RouteGroup{Name: "openai-a", Protocol: domain.ProtocolOpenAI, Status: domain.StatusEnabled}
	grok := domain.RouteGroup{Name: "grok", Protocol: domain.ProtocolOpenAI, Models: domain.JSONStrings{"grok-*"}, Status: domain.StatusEnabled}
	anth := domain.RouteGroup{Name: "anthropic-a", Protocol: domain.ProtocolAnthropic, Status: domain.StatusEnabled}
	for _, g := range []*domain.RouteGroup{&openaiA, &grok, &anth} {
		if err := db.Create(g).Error; err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Create(&domain.RouteGroupKey{RouteGroupID: openaiA.ID, PlatformKeyID: k1}).Error
	_ = db.Create(&domain.RouteGroupKey{RouteGroupID: grok.ID, PlatformKeyID: k2}).Error
	_ = db.Create(&domain.RouteGroupKey{RouteGroupID: anth.ID, PlatformKeyID: k3}).Error

	consumer := domain.ConsumerKey{Name: "c", Key: "sk-c", Status: domain.StatusEnabled}
	if err := db.Create(&consumer).Error; err != nil {
		t.Fatal(err)
	}
	unbound := domain.ConsumerKey{Name: "u", Key: "sk-u", Status: domain.StatusEnabled}
	if err := db.Create(&unbound).Error; err != nil {
		t.Fatal(err)
	}
	_ = db.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: consumer.ID, RouteGroupID: openaiA.ID}).Error
	_ = db.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: consumer.ID, RouteGroupID: grok.ID}).Error

	// Unbound consumer: nil allow, bound=false.
	allow, drift, bound, err := ResolveAllowKeys(ctx, db, unbound.ID, domain.ProtocolOpenAI, "gpt-4o")
	if err != nil || bound || allow != nil || drift != nil {
		t.Fatalf("unbound: allow=%v drift=%v bound=%v err=%v", allow, drift, bound, err)
	}

	// gpt-4o on openai: only openai-a matches (grok pattern rejects).
	allow, drift, bound, err = ResolveAllowKeys(ctx, db, consumer.ID, domain.ProtocolOpenAI, "gpt-4o")
	if err != nil || !bound {
		t.Fatalf("bound err=%v bound=%v", err, bound)
	}
	if _, ok := allow[k1]; !ok || len(allow) != 1 || len(drift) != 0 {
		t.Fatalf("gpt-4o allow=%v drift=%v", allow, drift)
	}

	// grok-3: both openai-a (no pattern) and grok match.
	allow, _, _, _ = ResolveAllowKeys(ctx, db, consumer.ID, domain.ProtocolOpenAI, "grok-3")
	if len(allow) != 2 {
		t.Fatalf("grok-3 allow=%v", allow)
	}

	// anthropic protocol: consumer is not bound to any anthropic group -> empty set, bound=true.
	allow, _, bound, _ = ResolveAllowKeys(ctx, db, consumer.ID, domain.ProtocolAnthropic, "claude-3")
	if !bound || len(allow) != 0 {
		t.Fatalf("anthropic allow=%v bound=%v", allow, bound)
	}

	// Rate drift: openai-a gets a 0.2~0.8 reference range. k1 is at rate 1 -> drift.
	lo, hi := 0.2, 0.8
	if err := db.Model(&openaiA).Updates(map[string]any{"rate_min": lo, "rate_max": hi}).Error; err != nil {
		t.Fatal(err)
	}
	allow, drift, bound, err = ResolveAllowKeys(ctx, db, consumer.ID, domain.ProtocolOpenAI, "gpt-4o")
	if err != nil || !bound || len(allow) != 0 {
		t.Fatalf("drift: allow=%v bound=%v err=%v", allow, bound, err)
	}
	if _, ok := drift[k1]; !ok || len(drift) != 1 {
		t.Fatalf("drift: expected k1 drifted, got %v", drift)
	}
	// Bring k1 back inside the range -> allowed again.
	if err := db.Model(&domain.PlatformKey{}).Where("id = ?", k1).Update("rate_multiplier", 0.5).Error; err != nil {
		t.Fatal(err)
	}
	allow, drift, _, _ = ResolveAllowKeys(ctx, db, consumer.ID, domain.ProtocolOpenAI, "gpt-4o")
	if _, ok := allow[k1]; !ok || len(drift) != 0 {
		t.Fatalf("after fix: allow=%v drift=%v", allow, drift)
	}
	// grok-3: k1 drifts out of openai-a but grok (no range) still holds k2; k1 out of range only.
	_ = db.Model(&domain.PlatformKey{}).Where("id = ?", k1).Update("rate_multiplier", 1).Error
	allow, drift, _, _ = ResolveAllowKeys(ctx, db, consumer.ID, domain.ProtocolOpenAI, "grok-3")
	if _, ok := allow[k2]; !ok || len(allow) != 1 || len(drift) != 1 {
		t.Fatalf("grok-3 with drift: allow=%v drift=%v", allow, drift)
	}
	_ = db.Model(&openaiA).Updates(map[string]any{"rate_min": nil, "rate_max": nil}).Error

	byKey, err := RouteGroupsForKeys(ctx, db, []uint{k1, k2, k3})
	if err != nil || len(byKey[k1]) != 1 || byKey[k1][0].Name != "openai-a" {
		t.Fatalf("RouteGroupsForKeys=%v err=%v", byKey, err)
	}
	byConsumer, err := RouteGroupsForConsumers(ctx, db, []uint{consumer.ID, unbound.ID})
	if err != nil || len(byConsumer[consumer.ID]) != 2 || len(byConsumer[unbound.ID]) != 0 {
		t.Fatalf("RouteGroupsForConsumers=%v err=%v", byConsumer, err)
	}
}

func TestListVisibleModels(t *testing.T) {
	up := &domain.Upstream{Status: domain.StatusEnabled, Protocols: "openai"}
	enabled := func(id uint, last []string, rate float64) domain.PlatformKey {
		return domain.PlatformKey{
			ID:             id,
			Status:         domain.StatusEnabled,
			RateMultiplier: rate,
			LastModels:     domain.JSONStrings(last),
			Upstream:       up,
		}
	}
	keys := []domain.PlatformKey{
		enabled(1, []string{"gpt-4o", "gpt-4o-mini", "claude-3"}, 1),
		enabled(2, []string{"grok-3", "gpt-4o"}, 0.5),
	}

	// Unbound: union of fetched lists.
	got := ListVisibleModels(false, nil, keys, nil)
	if !hasAll(got, "gpt-4o", "gpt-4o-mini", "claude-3", "grok-3") || len(got) != 4 {
		t.Fatalf("unbound=%v", got)
	}

	exact := domain.RouteGroup{ID: 10, Status: domain.StatusEnabled, Models: domain.JSONStrings{"gpt-4o", "secret-model"}}
	glob := domain.RouteGroup{ID: 11, Status: domain.StatusEnabled, Models: domain.JSONStrings{"grok-*"}}
	open := domain.RouteGroup{ID: 12, Status: domain.StatusEnabled}
	memberOf := map[uint][]uint{1: {10}, 2: {11}}

	// Exact group entries are returned even if they are missing from LastModels.
	got = ListVisibleModels(true, []domain.RouteGroup{exact, glob}, keys, memberOf)
	if !hasAll(got, "gpt-4o", "secret-model", "grok-3") {
		t.Fatalf("bound exact+glob=%v", got)
	}
	if hasAll(got, "gpt-4o-mini") {
		t.Fatalf("glob should not leak gpt-4o-mini: %v", got)
	}

	// Empty group models → the key's full fetched list.
	got = ListVisibleModels(true, []domain.RouteGroup{open}, keys, map[uint][]uint{1: {12}})
	if !hasAll(got, "gpt-4o", "gpt-4o-mini", "claude-3") || len(got) != 3 {
		t.Fatalf("empty group=%v", got)
	}

	// Rate drift excludes the key, so its group's models are omitted.
	lo, hi := 0.1, 0.2
	exact.RateMin, exact.RateMax = &lo, &hi
	got = ListVisibleModels(true, []domain.RouteGroup{exact}, keys, map[uint][]uint{1: {10}})
	if len(got) != 0 {
		t.Fatalf("drifted key should contribute nothing, got %v", got)
	}
}

func hasAll(got []string, want ...string) bool {
	set := map[string]struct{}{}
	for _, s := range got {
		set[s] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

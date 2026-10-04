package dashboard

import (
	"context"
	"testing"
	"time"

	"simple-up-manage/internal/config"
	"simple-up-manage/internal/domain"
)

func fptr(v float64) *float64 { return &v }

func TestComputeCostLegacyCoefficients(t *testing.T) {
	// models.dev card without absolute prices: input $3, output $15 per MTok,
	// cache read ×0.1, cache write ×1.25 (Anthropic protocol keeps input
	// separate from cache tokens).
	price := ModelPrice{Input: 3, Output: 15, OK: true, Matched: "claude-test"}
	res := ComputeCost(price, CostOptions{Protocol: domain.ProtocolAnthropic}, UsageTokens{
		Input: 1000, Output: 500, CacheRead: 2000, CacheWrite: 400, UsageKnown: true,
	})
	if res.TotalUSD == nil {
		t.Fatal("total is nil")
	}
	want := 1000*3e-6 + 500*15e-6 + 2000*3e-7 + 400*3.75e-6
	if *res.TotalUSD != round8(want) {
		t.Fatalf("total = %v want %v", *res.TotalUSD, want)
	}
	d := res.Detail
	if d.CacheWriteMode != "coefficient" || d.CacheWriteCost != 400*3.75e-6 {
		t.Fatalf("detail write = %+v", d)
	}
	if d.Matched != "claude-test" {
		t.Fatalf("matched = %q", d.Matched)
	}
}

func TestComputeCostOpenAIUncachedSubtraction(t *testing.T) {
	price := ModelPrice{Input: 3, Output: 15, OK: true}
	res := ComputeCost(price, CostOptions{Protocol: domain.ProtocolOpenAI}, UsageTokens{
		Input: 10000, Output: 100, CacheRead: 4000, CacheWrite: 1000, UsageKnown: true,
	})
	want := 5000*3e-6 + 100*15e-6 + 4000*3e-7 + 1000*3.75e-6
	if *res.TotalUSD != round8(want) {
		t.Fatalf("total = %v want %v", *res.TotalUSD, want)
	}
	// The receipt records the uncached input so the item rows stay self-consistent.
	if res.Detail.InputUncachedTokens != 5000 || res.Detail.InputTokens != 10000 {
		t.Fatalf("receipt tokens = uncached %d raw %d", res.Detail.InputUncachedTokens, res.Detail.InputTokens)
	}
}

func TestComputeCostCacheBreakdown(t *testing.T) {
	price := ModelPrice{Input: 3, Output: 15, OK: true, CacheRead: fptr(1e-7), CacheWrite5m: fptr(4e-6), CacheWrite1h: fptr(8e-6)}
	ut := UsageTokens{Input: 1000, CacheRead: 1000, CacheWrite: 800, CacheWrite5m: 500, CacheWrite1h: 300, UsageKnown: true}
	res := ComputeCost(price, CostOptions{Protocol: domain.ProtocolAnthropic}, ut)
	want := 1000*3e-6 + 1000*1e-7 + 500*4e-6 + 300*8e-6
	if *res.TotalUSD != round8(want) {
		t.Fatalf("breakdown total = %v want %v", *res.TotalUSD, want)
	}
	if res.Detail.CacheWriteMode != "breakdown" {
		t.Fatalf("mode = %q", res.Detail.CacheWriteMode)
	}

	// No ephemeral details: everything at the 5m price.
	noDetails := ut
	noDetails.CacheWrite5m, noDetails.CacheWrite1h = 0, 0
	res2 := ComputeCost(price, CostOptions{Protocol: domain.ProtocolAnthropic}, noDetails)
	if *res2.TotalUSD != round8(1000*3e-6+1000*1e-7+800*4e-6) {
		t.Fatalf("no-details total = %v", *res2.TotalUSD)
	}

	// 1h price at or below 5m: card is treated as 5m-only.
	dup := ModelPrice{Input: 3, Output: 15, OK: true, CacheWrite5m: fptr(4e-6), CacheWrite1h: fptr(2e-6)}
	res3 := ComputeCost(dup, CostOptions{Protocol: domain.ProtocolAnthropic}, UsageTokens{CacheWrite: 800, CacheWrite5m: 400, CacheWrite1h: 400, UsageKnown: true})
	if *res3.TotalUSD != round8(800*4e-6) {
		t.Fatalf("1h<=5m total = %v", *res3.TotalUSD)
	}
}

func TestComputeCostLongContextLadder(t *testing.T) {
	price := ModelPrice{Input: 1, Output: 2, OK: true, LongCtxThreshold: 1000, LongCtxInputMult: 2, LongCtxOutputMult: 1.5}
	// Anthropic: total context = input + read + write.
	res := ComputeCost(price, CostOptions{Protocol: domain.ProtocolAnthropic}, UsageTokens{
		Input: 2000, Output: 100, CacheRead: 500, UsageKnown: true,
	})
	want := 2000*(1e-6*2) + 100*(2e-6*1.5) + 500*(1e-7*2)
	if *res.TotalUSD != round8(want) {
		t.Fatalf("anthropic ladder total = %v want %v", *res.TotalUSD, want)
	}
	if !res.Detail.LongCtx.Applied || res.Detail.LongCtx.TotalTokens != 2500 {
		t.Fatalf("long ctx detail = %+v", res.Detail.LongCtx)
	}
	// OpenAI: prompt_tokens already include cached tokens.
	res2 := ComputeCost(price, CostOptions{Protocol: domain.ProtocolOpenAI}, UsageTokens{
		Input: 2500, Output: 100, CacheRead: 500, UsageKnown: true,
	})
	if *res2.TotalUSD != round8(want) {
		t.Fatalf("openai ladder total = %v want %v", *res2.TotalUSD, want)
	}
	// Below the threshold: no surcharge.
	res3 := ComputeCost(price, CostOptions{Protocol: domain.ProtocolAnthropic}, UsageTokens{
		Input: 500, Output: 100, CacheRead: 100, UsageKnown: true,
	})
	want3 := 500*1e-6 + 100*2e-6 + 100*1e-7
	if *res3.TotalUSD != round8(want3) {
		t.Fatalf("below-threshold total = %v want %v", *res3.TotalUSD, want3)
	}
	if res3.Detail.LongCtx.Applied {
		t.Fatal("ladder applied below threshold")
	}
	// A zero multiplier never zeroes a component (falls back to ×1).
	zero := price
	zero.LongCtxInputMult = 0
	res4 := ComputeCost(zero, CostOptions{Protocol: domain.ProtocolAnthropic}, UsageTokens{
		Input: 2000, Output: 100, UsageKnown: true,
	})
	if *res4.TotalUSD != round8(2000*1e-6+100*3e-6) {
		t.Fatalf("zero-mult total = %v", *res4.TotalUSD)
	}
}

func TestComputeCostServiceTiers(t *testing.T) {
	base := UsageTokens{Input: 1000, Output: 100, UsageKnown: true}
	standard := round8(1000*3e-6 + 100*15e-6)

	// Priority tier with dedicated prices.
	withPriority := ModelPrice{Input: 3, Output: 15, OK: true, InputPriority: fptr(6), OutputPriority: fptr(30), CacheReadPriority: fptr(6e-7)}
	priorityUsage := base
	priorityUsage.ServiceTier = "priority"
	res := ComputeCost(withPriority, CostOptions{Protocol: domain.ProtocolAnthropic}, priorityUsage)
	if *res.TotalUSD != round8(1000*6e-6+100*30e-6) {
		t.Fatalf("priority prices total = %v", *res.TotalUSD)
	}
	if res.Detail.TierMultiplier != 0 {
		t.Fatalf("tier multiplier should stay unset, got %v", res.Detail.TierMultiplier)
	}

	// Priority tier without dedicated prices: ×2.
	plain := ModelPrice{Input: 3, Output: 15, OK: true}
	res3 := ComputeCost(plain, CostOptions{Protocol: domain.ProtocolAnthropic}, func() UsageTokens { u := base; u.ServiceTier = "priority"; return u }())
	if *res3.TotalUSD != round8(standard*2) || res3.Detail.TierMultiplier != 2 {
		t.Fatalf("priority fallback total = %v mult = %v", *res3.TotalUSD, res3.Detail.TierMultiplier)
	}

	// Flex halves.
	res4 := ComputeCost(plain, CostOptions{Protocol: domain.ProtocolAnthropic}, func() UsageTokens { u := base; u.ServiceTier = "flex"; return u }())
	if *res4.TotalUSD != round8(standard*0.5) || res4.Detail.TierMultiplier != 0.5 {
		t.Fatalf("flex total = %v mult = %v", *res4.TotalUSD, res4.Detail.TierMultiplier)
	}
}

func TestComputeCostDeepSeekPeakValley(t *testing.T) {
	card := ModelPrice{Input: 0.22, Output: 0.66, OK: true, CacheRead: fptr(7e-9)}
	ut := UsageTokens{Input: 1_000_000, Output: 100_000, CacheRead: 500_000, UsageKnown: true}
	// OpenAI protocol: uncached input = prompt - cached = 500k tokens.
	offPeak := round8(5e5*2.2e-7 + 1e5*6.6e-7 + 5e5*7e-9)

	// Wednesday 2026-10-07, 02:00 UTC → peak (input/output/cache-read ×2).
	peakAt := time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)
	peak := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "deepseek-v4-flash", At: peakAt}, ut)
	if *peak.TotalUSD != round8(offPeak*2) || peak.Detail.TimeMultiplier != 2 {
		t.Fatalf("peak total = %v mult = %v", *peak.TotalUSD, peak.Detail.TimeMultiplier)
	}

	// Same weekday, 12:00 UTC → off-peak.
	off := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "deepseek-v4-flash", At: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}, ut)
	if *off.TotalUSD != offPeak || off.Detail.TimeMultiplier != 0 {
		t.Fatalf("off-peak total = %v mult = %v", *off.TotalUSD, off.Detail.TimeMultiplier)
	}

	// Beijing Saturday (2026-10-10) all off-peak, even inside the UTC window.
	weekendAt := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	weekend := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "deepseek-v4-flash", At: weekendAt}, ut)
	if *weekend.TotalUSD != offPeak {
		t.Fatalf("weekend total = %v", *weekend.TotalUSD)
	}

	// Non-DeepSeek models are never surcharged by the builtin rule.
	other := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "gpt-5.1", At: peakAt}, ut)
	if *other.TotalUSD != offPeak {
		t.Fatalf("non-deepseek total = %v", *other.TotalUSD)
	}
}

func TestComputeCostEffortRule(t *testing.T) {
	ConfigureBillingRules(config.Billing{
		EffortRules: []config.EffortRule{{ModelMatch: "gpt-5", Multipliers: map[string]float64{"max": 3}}},
	})
	t.Cleanup(func() { ConfigureBillingRules(config.Billing{}) })

	card := ModelPrice{Input: 3, Output: 15, OK: true}
	ut := UsageTokens{Input: 1000, Output: 100, UsageKnown: true}
	base := round8(1000*3e-6 + 100*15e-6)

	res := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "gpt-5.1", Effort: "max"}, ut)
	if *res.TotalUSD != round8(base*3) || res.Detail.EffortMultiplier != 3 || res.Detail.Effort != "max" {
		t.Fatalf("effort total = %v detail = %+v", *res.TotalUSD, res.Detail)
	}
	other := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "gpt-5.1", Effort: "high"}, ut)
	if *other.TotalUSD != base {
		t.Fatalf("unknown effort total = %v", *other.TotalUSD)
	}
	// Effort rules and the builtin DeepSeek peak rule compose multiplicatively:
	// the model name matches both "deepseek" (peak ×2) and "gpt-5" (effort ×3).
	peak := ComputeCost(card, CostOptions{Protocol: domain.ProtocolOpenAI, Model: "deepseek-gpt-5-proxy", Effort: "max", At: time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC)}, ut)
	if *peak.TotalUSD != round8(base*2*3) {
		t.Fatalf("deepseek+effort total = %v", *peak.TotalUSD)
	}
}

func TestComputeCostUnknownPriceOrUsage(t *testing.T) {
	res := ComputeCost(ModelPrice{}, CostOptions{}, UsageTokens{UsageKnown: true})
	if res.TotalUSD != nil {
		t.Fatal("missing card should yield nil")
	}
	res = ComputeCost(ModelPrice{Input: 3, Output: 15, OK: true}, CostOptions{}, UsageTokens{Input: 1})
	if res.TotalUSD != nil {
		t.Fatal("unknown usage should yield nil")
	}
	if BaseCostUSD(ModelPrice{Input: 3, Output: 15, OK: true}, domain.ProtocolAnthropic, 1000, 0, 0, 100, true) == nil {
		t.Fatal("legacy wrapper should compute")
	}
}

func TestPublishAndLookupVersionPrices(t *testing.T) {
	db := dashboardTestDB(t)
	if err := db.AutoMigrate(&CatalogVersion{}, &CatalogVersionPrice{}, &domain.CatalogModel{}, &domain.LiteLLMPrice{}, &domain.LiteLLMMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.CatalogModel{Vendor: domain.VendorOpenAI, ModelID: "gpt-4o", Protocol: domain.ProtocolOpenAI, InputCost: 3, OutputCost: 15}).Error; err != nil {
		t.Fatal(err)
	}
	write1h := 8e-6
	if err := db.Create(&domain.LiteLLMPrice{
		ModelKey: "claude-sonnet-4-5", Vendor: domain.VendorAnthropic, Mode: "chat",
		InputPricePerToken: 3e-6, OutputPricePerToken: 1.5e-5,
		CacheReadPricePerToken: fptr(3e-7), CacheWrite5mPricePerToken: fptr(3.75e-6), CacheWrite1hPricePerToken: &write1h,
		LongCtxThreshold: 200000, LongCtxInputMult: 2, LongCtxOutputMult: 1.5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// Same model key as the models.dev row: LiteLLM card must win.
	if err := db.Create(&domain.LiteLLMPrice{
		ModelKey: "gpt-4o", Vendor: domain.VendorOpenAI,
		InputPricePerToken: 2.5e-6, OutputPricePerToken: 1e-5,
	}).Error; err != nil {
		t.Fatal(err)
	}
	ver, err := PublishCatalogVersion(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}

	// Date-suffixed request name resolves via alias to the base card.
	p := LookupVersionPrice(db, ver, "claude-sonnet-4-5-20250929")
	if !p.OK || p.Matched != "claude-sonnet-4-5" {
		t.Fatalf("sonnet lookup = %+v", p)
	}
	if p.CacheWrite1h == nil || *p.CacheWrite1h != write1h {
		t.Fatalf("sonnet 1h price = %v", p.CacheWrite1h)
	}
	if p.LongCtxThreshold != 200000 || p.LongCtxInputMult != 2 {
		t.Fatalf("sonnet ladder = %+v", p)
	}

	// Digit-dot spelling variant.
	if err := db.Create(&domain.LiteLLMPrice{ModelKey: "gpt-5.5", Vendor: domain.VendorOpenAI, InputPricePerToken: 5e-6, OutputPricePerToken: 3e-5}).Error; err != nil {
		t.Fatal(err)
	}
	ver, err = PublishCatalogVersion(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	p2 := LookupVersionPrice(db, ver, "gpt-5-5-20260101")
	if !p2.OK || p2.Matched != "gpt-5.5" {
		t.Fatalf("dot-variant lookup = %+v", p2)
	}

	// LiteLLM row overrides the models.dev entry for the same model id.
	p3 := LookupVersionPrice(db, ver, "gpt-4o")
	if !p3.OK || p3.Input != 2.5e-6*1e6 {
		t.Fatalf("gpt-4o lookup input = %v", p3.Input)
	}
	// Provider prefix is stripped before matching.
	p4 := LookupVersionPrice(db, ver, "openai/gpt-4o")
	if !p4.OK || p4.Matched != "gpt-4o" {
		t.Fatalf("prefixed lookup = %+v", p4)
	}

	// Unknown model keeps the legacy coefficient card and stays unpriceable.
	p5 := LookupVersionPrice(db, ver, "totally-unknown")
	if p5.OK || p5.CacheReadCoeff != LegacyCacheReadCoeff || p5.CacheWriteCoeff != LegacyCacheWriteCoeff {
		t.Fatalf("unknown lookup = %+v", p5)
	}
}

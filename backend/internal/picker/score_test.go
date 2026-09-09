package picker

import (
	"testing"

	"simple-up-manage/internal/domain"
)

func TestQualityPrefersStableOverCheap(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	cheapUnstable := qualityScore(0.60, 0.10, 4000, 20, cfg)
	pricierStable := qualityScore(0.98, 0.40, 800, 20, cfg)
	if pricierStable <= cheapUnstable {
		t.Fatalf("stable should outscore unstable: %.3f vs %.3f", pricierStable, cheapUnstable)
	}
	bandFloor := pricierStable * (1 - cfg.Epsilon)
	if cheapUnstable >= bandFloor {
		t.Fatalf("unstable should fall outside near-best band: %.3f >= %.3f", cheapUnstable, bandFloor)
	}
}

func TestEffectiveCostUsesCache(t *testing.T) {
	if effectiveCost(0.5, 0.4) >= effectiveCost(0.5, 0) {
		t.Fatal("higher cache should lower effective cost")
	}
}

func TestLowSampleUsesPrior(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	q := qualityScore(0.0, 0, 0, 1, cfg)
	want := qualityScore(cfg.PriorSuccess, 0, 0, cfg.MinSamples, cfg)
	if q != want {
		t.Fatalf("prior not applied: got %.3f want %.3f", q, want)
	}
}

func TestQualityOmitsMissingCache(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	full := Quality(ScoreInputs{Success: 1, Samples: 20, Cache: 0, HasCache: true, LatencyP50: 0, HasLatency: true}, cfg)
	omit := Quality(ScoreInputs{Success: 1, Samples: 20, HasLatency: true, LatencyP50: 0}, cfg)
	if omit <= full {
		t.Fatalf("omitting cache=0 should raise score: omit=%.3f full=%.3f", omit, full)
	}
	// success 0.45 + ttft 0.25, both 1.0 → 1.0
	if omit < 0.999 {
		t.Fatalf("omit cache expected ~1, got %.3f", omit)
	}
}

func TestQualityOmitsMissingLatency(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	q := Quality(ScoreInputs{Success: 1, Samples: 20, Cache: 0.4, HasCache: true}, cfg)
	// 0.45*1 + 0.30*0.4 = 0.57 / 0.75 = 0.76
	if q < 0.75 || q > 0.77 {
		t.Fatalf("omit latency: %.3f", q)
	}
}

func TestQualityNoTerms(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	cfg.WeightSuccess = 0
	cfg.WeightCache = 0
	cfg.WeightTTFT = 0
	if Quality(ScoreInputs{Success: 1, Samples: 20}, cfg) != 0 {
		t.Fatal("expected 0 with no weights")
	}
}

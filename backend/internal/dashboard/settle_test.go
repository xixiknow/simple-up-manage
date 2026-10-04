package dashboard

import (
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func attemptFact(uuid, source string, httpSent bool, consumption, estimated *float64) AttemptFact {
	return AttemptFact{
		UUID: uuid, RequestUUID: "req-" + uuid, ProviderID: 7, ProviderName: "p",
		CompletedAt: time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC),
		Result:      "success", HTTPSent: httpSent, Source: source,
		ConsumptionUSD: consumption, EstimatedCostUSD: estimated,
	}
}

func TestApplyAttemptAggProbeCountsConsumptionOnly(t *testing.T) {
	db := dashboardTestDB(t)
	cons := 0.5
	est := 0.4
	business := attemptFact("b1", domain.SourceBusiness, true, &cons, &est)
	probe := attemptFact("p1", domain.SourceAdminTest, true, &cons, &est)
	probe.Result = "success"

	if err := applyAttemptAgg(db, business, true); err != nil {
		t.Fatal(err)
	}
	if err := applyAttemptAgg(db, probe, false); err != nil {
		t.Fatal(err)
	}

	var row MinuteAgg
	bucket := business.CompletedAt.UTC().Truncate(time.Minute)
	if err := db.Where("bucket = ? AND family = ? AND dim = ? AND dim_id = ?", bucket, FamilyAttempt, DimGlobal, 0).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	// Consumption aggregates both sources; quality metrics stay business-only.
	if row.ConsumptionUSD != 1.0 {
		t.Fatalf("consumption = %v want 1.0", row.ConsumptionUSD)
	}
	if row.EstimatedConsumptionUSD != 0.8 {
		t.Fatalf("estimated consumption = %v want 0.8", row.EstimatedConsumptionUSD)
	}
	if row.KnownCostUSD != 0.4 {
		t.Fatalf("known cost = %v want 0.4 (business only)", row.KnownCostUSD)
	}
	if row.ProviderSuccess != 1 {
		t.Fatalf("provider success = %v want 1 (business only)", row.ProviderSuccess)
	}
}

func TestApplyAttemptAggUnknownConsumptionAllSources(t *testing.T) {
	db := dashboardTestDB(t)
	probe := attemptFact("p2", domain.SourceAdminTest, true, nil, nil)
	if err := applyAttemptAgg(db, probe, false); err != nil {
		t.Fatal(err)
	}
	var row MinuteAgg
	bucket := probe.CompletedAt.UTC().Truncate(time.Minute)
	if err := db.Where("bucket = ? AND family = ? AND dim = ? AND dim_id = ?", bucket, FamilyAttempt, DimGlobal, 0).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.UnknownConsumption != 1 {
		t.Fatalf("unknown consumption = %d want 1", row.UnknownConsumption)
	}
	if row.ProviderFailure != 0 {
		t.Fatalf("provider failure = %d, probe must not affect quality", row.ProviderFailure)
	}
}

func TestUpdateRequestFactPrefersPrecomputedBase(t *testing.T) {
	group := uint(3)
	fact := RequestFact{
		UUID: "r1", RouteGroupID: &group, SaleMultiplier: fptr(1.5),
		Source: domain.SourceBusiness, Protocol: domain.ProtocolAnthropic,
	}
	base := 0.25
	end := RequestEnd{
		UUID: "r1", Model: "m", Protocol: domain.ProtocolAnthropic, Success: true,
		CompletedAt: time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC), HTTPAttempts: 1,
		InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 400, CacheWriteTokens: 200,
		InputPrice: fptr(3), OutputPrice: fptr(15), UsageKnown: true,
		BaseCostUSD: &base,
	}
	updateRequestFact(&fact, end, nil)
	if fact.BaseCostUSD == nil || *fact.BaseCostUSD != 0.25 {
		t.Fatalf("base cost = %v want 0.25 (precomputed, no recompute)", fact.BaseCostUSD)
	}
	if fact.RevenueUSD == nil || *fact.RevenueUSD != 0.375 {
		t.Fatalf("revenue = %v want 0.375", fact.RevenueUSD)
	}
	if !fact.Covered {
		t.Fatal("fact should be covered")
	}

	// Without a precomputed value the legacy coefficient recompute applies.
	end.BaseCostUSD = nil
	legacy := RequestFact{UUID: "r2", RouteGroupID: &group, SaleMultiplier: fptr(1), Source: domain.SourceBusiness, Protocol: domain.ProtocolAnthropic}
	updateRequestFact(&legacy, end, nil)
	if legacy.BaseCostUSD == nil || *legacy.BaseCostUSD != round8(1000*3e-6+500*15e-6+400*3e-7+200*3.75e-6) {
		t.Fatalf("legacy base cost = %v", legacy.BaseCostUSD)
	}
	if legacy.InputTokens != 1000 || legacy.CacheWriteTokens != 200 {
		t.Fatal("token wiring broken")
	}
}

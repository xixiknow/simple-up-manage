package dashboard

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func dashboardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&RequestFact{}, &AttemptFact{}, &EventLedger{}, &MinuteAgg{}, &DayAgg{}, &Meta{}, &Gap{}, &Settings{}, &domain.Upstream{}, &domain.RouteGroup{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func put(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatal(err)
	}
}

func settleEvent(s *settler, kind, key string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.process(queuedEvent{Key: key, Kind: kind, Payload: raw, At: time.Now()})
}

func closeAmount(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("amount = %.8f, want %.8f", got, want)
	}
}

func TestSettlementWaitsForHTTPAttemptsAndIsIdempotent(t *testing.T) {
	db := dashboardTestDB(t)
	s := &settler{db: db}
	at := time.Now().UTC()
	group, provider := uint(1), uint(2)
	sale, price, zero, cost := 0.3, 1.0, 0.0, 0.15
	start := RequestStart{UUID: "r", RouteGroupID: &group, SaleMultiplier: &sale, Source: domain.SourceBusiness, StartedAt: at}
	local := AttemptFact{UUID: "local", RequestUUID: "r", Source: domain.SourceBusiness, Result: "capacity_rejected", EstimatedCostUSD: &zero, ConsumptionUSD: &zero, CompletedAt: at}
	end := RequestEnd{UUID: "r", Success: true, CompletedAt: at, HTTPAttempts: 1, UsageKnown: true, InputTokens: 1000000, InputPrice: &price, OutputPrice: &price, FinalProviderID: &provider}
	for _, e := range []struct {
		kind, key string
		payload   any
	}{{kindReqStart, "start", start}, {kindAttEnd, "local", local}} {
		if err := settleEvent(s, e.kind, e.key, e.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := settleEvent(s, kindReqEnd, "end", end); err == nil {
		t.Fatal("settled without final HTTP attempt")
	}
	a := AttemptFact{UUID: "sent", RequestUUID: "r", ProviderID: provider, Source: domain.SourceBusiness, Result: "success", HTTPSent: true, EstimatedCostUSD: &cost, ConsumptionUSD: &cost, CompletedAt: at}
	if err := settleEvent(s, kindAttEnd, "sent", a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := settleEvent(s, kindReqEnd, "end", end); err != nil {
			t.Fatal(err)
		}
	}
	var f RequestFact
	if err := db.First(&f, "uuid = ?", "r").Error; err != nil {
		t.Fatal(err)
	}
	if !f.Covered || f.CompletedAt == nil {
		t.Fatalf("request not settled: %+v", f)
	}
	var row MinuteAgg
	if err := db.Where("family = ? AND dim = ?", FamilyRequest, DimGlobal).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.RequestsCompleted != 1 {
		t.Fatalf("duplicate completion: %d", row.RequestsCompleted)
	}
	closeAmount(t, row.CoveredCostUSD, cost)
	closeAmount(t, row.CoveredRevenueUSD, sale)
}

func TestReportedExpenseDoesNotCompleteMissingEstimate(t *testing.T) {
	group := uint(1)
	sale, price, reported := 0.3, 1.0, 0.02
	f := RequestFact{RouteGroupID: &group, SaleMultiplier: &sale}
	updateRequestFact(&f, RequestEnd{Success: true, UsageKnown: true, InputTokens: 1000000, InputPrice: &price, OutputPrice: &price, CompletedAt: time.Now()}, []AttemptFact{{HTTPSent: true, ReportedCostUSD: &reported, ConsumptionUSD: &reported}})
	if f.Covered {
		t.Fatal("reported expense incorrectly completes estimated profit")
	}
	if f.RevenueUSD == nil {
		t.Fatal("known partial revenue should remain available")
	}
	if len(parseReasons(f.Reasons)) == 0 {
		t.Fatal("missing estimate has no explanation")
	}
}

func TestProviderProfitConservesRetryCosts(t *testing.T) {
	db := dashboardTestDB(t)
	at := time.Now().UTC()
	group, final := uint(1), uint(2)
	revenue, aCost, bCost := 0.30, 0.02, 0.15
	f := RequestFact{StartedAt: at, CompletedAt: &at, RouteGroupID: &group, FinalProviderID: &final, Success: true, Covered: true, RevenueUSD: &revenue, HTTPAttempts: 2}
	attempts := []AttemptFact{{ProviderID: 1, HTTPSent: true, EstimatedCostUSD: &aCost}, {ProviderID: 2, HTTPSent: true, EstimatedCostUSD: &bCost}}
	if err := db.Transaction(func(tx *gorm.DB) error { return applyRequestAgg(tx, f, attempts) }); err != nil {
		t.Fatal(err)
	}
	var rows []MinuteAgg
	if err := db.Where("family = ? AND dim = ?", FamilyRequest, DimProvider).Order("dim_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("providers = %d", len(rows))
	}
	closeAmount(t, rows[0].CoveredRevenueUSD-rows[0].CoveredCostUSD, -0.02)
	closeAmount(t, rows[1].CoveredRevenueUSD-rows[1].CoveredCostUSD, 0.15)
	closeAmount(t, rows[0].CoveredCostUSD+rows[1].CoveredCostUSD, 0.17)
}

func TestProtocolCacheCost(t *testing.T) {
	price := ModelPrice{Input: 1, Output: 1, CacheReadCoeff: 0.1, CacheWriteCoeff: 1.25, OK: true}
	for _, tc := range []struct {
		protocol           string
		input, read, write int64
		want               float64
	}{
		{domain.ProtocolOpenAI, 1500, 500, 0, .00105},
		{domain.ProtocolOpenAI, 1600, 500, 100, .001175},
		{domain.ProtocolAnthropic, 1000, 500, 100, .001175},
		{domain.ProtocolOpenAI, 0, 0, 0, 0},
	} {
		got := BaseCostUSD(price, tc.protocol, tc.input, tc.read, tc.write, 0, true)
		if got == nil {
			t.Fatal("known usage lost")
		}
		closeAmount(t, *got, tc.want)
	}
	if BaseCostUSD(price, domain.ProtocolOpenAI, 0, 0, 0, 0, false) != nil {
		t.Fatal("missing usage treated as zero")
	}
}

func TestHistoryUsesDaysAndMinuteEdgesWithoutDoubleCounting(t *testing.T) {
	db := dashboardTestDB(t)
	s := &Service{db: db}
	today := shanghaiDay(time.Now())
	first := today.AddDate(0, 0, -8)
	// First day has 100 requests before the selected edge and two after it.
	for _, row := range []MinuteAgg{
		{Bucket: first.Add(time.Hour), RequestsCompleted: 100},
		{Bucket: first.Add(15 * time.Hour), RequestsCompleted: 2},
		{Bucket: first.AddDate(0, 0, 1).Add(time.Hour), RequestsCompleted: 5},
		{Bucket: today.Add(time.Hour), RequestsCompleted: 3},
	} {
		row.Family, row.Dim = FamilyRequest, DimGlobal
		row.Bucket = row.Bucket.UTC()
		put(t, db, &row)
	}
	for _, row := range []DayAgg{
		{Bucket: first, RequestsCompleted: 102},
		{Bucket: first.AddDate(0, 0, 1), RequestsCompleted: 5},
		{Bucket: today, RequestsCompleted: 3},
	} {
		row.Family, row.Dim = FamilyRequest, DimGlobal
		put(t, db, &row)
	}
	got, err := s.sumRange(context.Background(), Range{From: first.Add(14 * time.Hour), To: today.Add(2 * time.Hour)}, FamilyRequest, DimGlobal, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestsCompleted != 10 {
		t.Fatalf("completed=%d, want 10", got.RequestsCompleted)
	}
	old := today.AddDate(0, 0, -40)
	put(t, db, &DayAgg{Bucket: old, Family: FamilyRequest, Dim: DimGlobal, RequestsCompleted: 7})
	put(t, db, &DayAgg{Bucket: old, Family: FamilyRequest, Dim: DimProvider, DimID: 9, RequestsCompleted: 7, CoveredCount: 7, CoveredRevenueUSD: 3, CoveredCostUSD: 1})
	put(t, db, &Meta{ID: 1, AvailableFrom: old})
	r := Range{From: old, To: old.AddDate(0, 0, 1)}
	trends, err := s.Trends(context.Background(), r, "day")
	if err != nil {
		t.Fatal(err)
	}
	if len(trends.Points) != 1 || trends.Points[0].RequestsCompleted != 7 {
		t.Fatalf("old daily history: %+v", trends.Points)
	}
	ranks, err := s.Rankings(context.Background(), r, DimProvider, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranks.Items) != 1 || ranks.Items[0].RequestsCompleted != 7 {
		t.Fatalf("old rankings: %+v", ranks.Items)
	}
}

func TestRPMExpiresWithoutNewTraffic(t *testing.T) {
	var clock atomic.Int64
	start := time.Now().UTC()
	clock.Store(start.UnixNano())
	m := NewMetrics(func() time.Time { return time.Unix(0, clock.Load()) })
	defer m.Stop()
	sub := m.Subscribe()
	<-sub.Ch
	biz, up := m.BeginBusiness(), m.BeginUpstream()
	m.MarkUpstreamHTTP()
	up.End()
	biz.End()
	biz.End()
	m.flush(false)
	if snap := <-sub.Ch; snap.BusinessRPM != 1 || snap.UpstreamRPM != 1 || snap.BusinessInflight != 0 {
		t.Fatalf("start snapshot: %+v", snap)
	}
	clock.Store(start.Add(60 * time.Second).UnixNano())
	m.flush(false)
	select {
	case snap := <-sub.Ch:
		if snap.BusinessRPM != 0 || snap.UpstreamRPM != 0 {
			t.Fatalf("expired snapshot: %+v", snap)
		}
	default:
		t.Fatal("expiration did not push a snapshot")
	}
}

func TestShutdownDrainsDependencyRetriesAfterClosingStreams(t *testing.T) {
	db := dashboardTestDB(t)
	s := New(db, nil)
	sub := s.Metrics.Subscribe()
	s.Metrics.CloseSubscriptions()
	select {
	case <-sub.Done:
	default:
		t.Fatal("SSE still open")
	}
	late := s.Metrics.Subscribe()
	select {
	case <-late.Done:
	default:
		t.Fatal("new SSE accepted during shutdown")
	}
	s.Metrics.Unsubscribe(sub)
	at := time.Now()
	s.EnqueueStart(RequestStart{UUID: "drain", Source: domain.SourceBusiness, StartedAt: at})
	s.EnqueueEnd(RequestEnd{UUID: "drain", CompletedAt: at, HTTPAttempts: 1})
	s.EnqueueAttempt(AttemptFact{UUID: "a", RequestUUID: "drain", Source: domain.SourceBusiness, HTTPSent: true, CompletedAt: at})
	s.Stop()
	if n := s.settle.QueueDepth(); n != 0 {
		t.Fatalf("events left on shutdown: %d", n)
	}
	var f RequestFact
	if err := db.First(&f, "uuid = ?", "drain").Error; err != nil {
		t.Fatal(err)
	}
	if f.CompletedAt == nil {
		t.Fatal("in-flight completion lost")
	}
}

func TestUrgentPredictsFromObservedWindow(t *testing.T) {
	for _, mode := range []string{"steady", "fresh", "gap", "unknown", "stale", "noburn", "depleted", "idle", "idledepleted", "nobalanceat", "diurnal"} {
		t.Run(mode, func(t *testing.T) {
			db := dashboardTestDB(t)
			// Anchor now to Shanghai noon so clock-hour projections are deterministic.
			nl := time.Now().In(shanghaiLoc())
			now := time.Date(nl.Year(), nl.Month(), nl.Day(), 12, 0, 0, 0, shanghaiLoc()).UTC()
			available := now.Add(-48 * time.Hour)
			balanceAt := now
			if mode == "fresh" {
				available = now.Add(-time.Hour)
			}
			if mode == "stale" {
				balanceAt = now.Add(-10 * time.Minute)
			}
			put(t, db, &Meta{ID: 1, AvailableFrom: available})
			balance := 12.0
			switch mode {
			case "depleted":
				balance = 0
			case "idledepleted":
				balance = -2
			}
			up := &domain.Upstream{ID: 1, Name: "p", Status: domain.StatusEnabled, LastBalance: &balance, LastBalanceAt: &balanceAt}
			if mode == "nobalanceat" {
				up.LastBalanceAt = nil
			}
			put(t, db, up)
			var buckets []MinuteAgg
			switch mode {
			case "fresh":
				// Single hour of traffic: too young for a daily shape, so the
				// flat-rate fallback must still produce 0.5h.
				buckets = []MinuteAgg{{Bucket: now.Add(-time.Hour), Family: FamilyAttempt, Dim: DimProvider, DimID: 1, ConsumptionUSD: 24, ProviderSuccess: 1}}
			case "noburn", "depleted":
				buckets = []MinuteAgg{
					{Bucket: now.Add(-24 * time.Hour), Family: FamilyAttempt, Dim: DimProvider, DimID: 1, ProviderFailure: 3},
					{Bucket: now.Add(-time.Minute), Family: FamilyAttempt, Dim: DimProvider, DimID: 1, ProviderFailure: 3, UnknownConsumption: 5},
				}
			case "idle", "idledepleted":
				buckets = nil
			case "diurnal":
				// $12 every day in the Shanghai-noon hour only. The last 24h
				// window sees exactly one noon bucket, so dailyTotal = $12 with
				// the whole shape concentrated in the hour that starts now.
				for d := BurnProfileWindowDays; d >= 1; d-- {
					buckets = append(buckets, MinuteAgg{Bucket: now.Add(-time.Duration(d) * 24 * time.Hour), Family: FamilyAttempt, Dim: DimProvider, DimID: 1, ConsumptionUSD: 12, ProviderSuccess: 1})
				}
			default:
				// steady, gap, unknown, stale: $1 every hour across the whole
				// profile window. A flat shape must degenerate to the old
				// flat-rate answer of 12h for a $12 balance.
				for i := BurnProfileWindowDays * 24; i >= 1; i-- {
					buckets = append(buckets, MinuteAgg{Bucket: now.Add(-time.Duration(i) * time.Hour), Family: FamilyAttempt, Dim: DimProvider, DimID: 1, ConsumptionUSD: 1, ProviderSuccess: 1})
				}
			}
			if mode == "unknown" {
				buckets[len(buckets)-1].UnknownConsumption = 7
			}
			for i := range buckets {
				put(t, db, &buckets[i])
			}
			if mode == "gap" {
				put(t, db, &Gap{StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-30 * time.Minute), Reason: "test"})
			}
			s := &Service{db: db, settle: &settler{db: db}}
			items, err := s.urgent(context.Background(), Range{From: now.Add(-24 * time.Hour), To: now}, DefaultSettings(), 2*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "noburn" || mode == "idle" || mode == "idledepleted" {
				if len(items) != 0 {
					t.Fatalf("expected idle provider to be excluded: %+v", items)
				}
				return
			}
			if len(items) != 1 {
				t.Fatalf("items=%+v", items)
			}
			it := items[0]
			switch mode {
			case "steady", "gap":
				if it.HoursLeft == nil {
					t.Fatalf("no prediction: %+v", it)
				}
				closeAmount(t, *it.HoursLeft, 12)
			case "fresh":
				if it.HoursLeft == nil {
					t.Fatalf("no prediction: %+v", it)
				}
				closeAmount(t, *it.HoursLeft, 0.5)
			case "unknown":
				if it.HoursLeft == nil || !strings.Contains(it.Reason, "7 次尝试消耗未知") {
					t.Fatalf("unknown-consumption note missing: %+v", it)
				}
				closeAmount(t, *it.HoursLeft, 12)
			case "stale":
				if it.HoursLeft == nil || !strings.Contains(it.Reason, "余额刷新于") {
					t.Fatalf("stale note missing: %+v", it)
				}
			case "depleted":
				if it.HoursLeft == nil || *it.HoursLeft != 0 || !strings.Contains(it.Reason, "余额已耗尽") {
					t.Fatalf("depleted handling: %+v", it)
				}
			case "nobalanceat":
				if !it.Insufficient || it.HoursLeft != nil {
					t.Fatalf("expected insufficient: %+v", it)
				}
			case "diurnal":
				if it.HoursLeft == nil || !strings.Contains(it.Reason, "分时段消耗分布") {
					t.Fatalf("diurnal projection not used: %+v", it)
				}
				// The balance is spent entirely within the noon hour that starts
				// now, so one hour remains.
				closeAmount(t, *it.HoursLeft, 1)
			}
		})
	}
}

func TestOverviewBalanceOrderByUsageThenBalance(t *testing.T) {
	db := dashboardTestDB(t)
	now := time.Now().UTC().Truncate(time.Minute)
	put(t, db, &Meta{ID: 1, AvailableFrom: now.Add(-48 * time.Hour)})
	small, big, idle := 40.0, 100.0, 60.0
	put(t, db, &domain.Upstream{ID: 1, Name: "used-low", Status: domain.StatusEnabled, LastBalance: &small, LastBalanceAt: &now})
	put(t, db, &domain.Upstream{ID: 2, Name: "used-high", Status: domain.StatusEnabled, LastBalance: &big, LastBalanceAt: &now})
	put(t, db, &domain.Upstream{ID: 3, Name: "idle", Status: domain.StatusEnabled, LastBalance: &idle, LastBalanceAt: &now})
	put(t, db, &domain.Upstream{ID: 4, Name: "unlimited", Status: domain.StatusEnabled, LastBalanceAt: &now})
	for _, id := range []uint{1, 2, 4} {
		put(t, db, &MinuteAgg{Bucket: now.Add(-time.Hour), Family: FamilyAttempt, Dim: DimProvider, DimID: id, ConsumptionUSD: 3, ProviderSuccess: 1})
	}
	s := &Service{db: db}
	out, err := s.Overview(context.Background(), Range{From: now.Add(-24 * time.Hour), To: now}, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	want := []uint{4, 2, 1, 3}
	if len(out.Balance.Providers) != len(want) {
		t.Fatalf("providers=%+v", out.Balance.Providers)
	}
	for i, p := range out.Balance.Providers {
		if p.ID != want[i] {
			t.Fatalf("order = %+v", out.Balance.Providers)
		}
	}
	if out.Balance.Providers[2].ConsumptionUSD == nil || *out.Balance.Providers[2].ConsumptionUSD != 3 {
		t.Fatalf("consumption missing: %+v", out.Balance.Providers[2])
	}
	if out.Balance.Providers[3].ConsumptionUSD != nil {
		t.Fatalf("idle consumption should stay nil: %+v", out.Balance.Providers[3])
	}
}

func TestRecommendationsDeduplicateRequestRevenueAndDemand(t *testing.T) {
	db := dashboardTestDB(t)
	at := time.Now().Add(-time.Minute).UTC()
	group, provider := uint(1), uint(1)
	base, revenue, cost := 1.0, .3, .05
	for _, model := range []string{"retry", "single"} {
		put(t, db, &RequestFact{UUID: model, RouteGroupID: &group, Model: model, Protocol: domain.ProtocolOpenAI, Path: "/v1/responses", Source: domain.SourceBusiness, CompletedAt: &at, Success: true, Covered: true, BaseCostUSD: &base, RevenueUSD: &revenue, FinalProviderID: &provider})
		count := 1
		if model == "retry" {
			count = 3
		}
		for i := 0; i < count; i++ {
			result := "upstream_failure"
			if i == count-1 {
				result = "success"
			}
			put(t, db, &AttemptFact{UUID: model + string(rune('a'+i)), RequestUUID: model, ProviderID: provider, Protocol: domain.ProtocolOpenAI, Path: "/v1/responses", Source: domain.SourceBusiness, HTTPSent: true, Result: result, CompletedAt: at, TTFTStatus: "measured", TTFTMs: 100, EstimatedCostUSD: &cost})
		}
	}
	cfg := DefaultSettings()
	cfg.MinQualitySamples, cfg.MinTTFTSamples, cfg.MinSuccessRate = 1, 1, .3
	s := &Service{db: db}
	_, _, boards, _, err := s.invest(context.Background(), Range{From: at.Add(-time.Hour), To: at.Add(time.Hour)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 2 {
		t.Fatalf("boards = %+v", boards)
	}
	for _, board := range boards {
		closeAmount(t, board.Weight, .5)
		want := 5.0
		if board.Key == "g1/openai/retry//v1/responses/false" {
			want = 1
		}
		if len(board.Items) != 1 || board.Items[0].ProfitPerCost == nil {
			t.Fatalf("board = %+v", board)
		}
		closeAmount(t, *board.Items[0].ProfitPerCost, want)
	}
}

func TestRecommendationsKeepRetryCostsAcrossWindowBoundary(t *testing.T) {
	db := dashboardTestDB(t)
	at := time.Now().Add(-time.Minute).UTC()
	group, provider := uint(1), uint(1)
	base, revenue, failedCost, finalCost := 1.0, .30, .02, .15
	put(t, db, &RequestFact{UUID: "boundary", RouteGroupID: &group, Model: "m", Protocol: domain.ProtocolOpenAI, Path: "/v1/responses", Source: domain.SourceBusiness, CompletedAt: &at, Success: true, Covered: true, BaseCostUSD: &base, RevenueUSD: &revenue, FinalProviderID: &provider})
	for _, a := range []AttemptFact{
		{UUID: "old", Result: "upstream_failure", CompletedAt: at.Add(-2 * time.Second), EstimatedCostUSD: &failedCost},
		{UUID: "final", Result: "success", CompletedAt: at, EstimatedCostUSD: &finalCost, TTFTMs: 100, TTFTStatus: "measured"},
	} {
		a.RequestUUID, a.ProviderID, a.Source, a.HTTPSent = "boundary", provider, domain.SourceBusiness, true
		a.Protocol, a.Path = domain.ProtocolOpenAI, "/v1/responses"
		put(t, db, &a)
	}
	cfg := DefaultSettings()
	cfg.MinQualitySamples, cfg.MinTTFTSamples = 1, 1
	s := &Service{db: db}
	_, _, boards, _, err := s.invest(context.Background(), Range{From: at.Add(-time.Second), To: at.Add(time.Hour)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 1 || len(boards[0].Items) != 1 {
		t.Fatalf("boards=%+v", boards)
	}
	item := boards[0].Items[0]
	if item.ProfitPerCost == nil {
		t.Fatal("missing profitability")
	}
	closeAmount(t, *item.ProfitPerCost, .13/.17)
	if item.Samples != 1 || item.SuccessRate == nil || *item.SuccessRate != 1 {
		t.Fatalf("quality included old attempt: %+v", item)
	}
}

func TestInvestFallsBackWhenDemandsDisjoint(t *testing.T) {
	db := dashboardTestDB(t)
	now := time.Now().UTC().Truncate(time.Minute)
	at := now.Add(-time.Minute)
	g1, g2, p1, p2 := uint(1), uint(2), uint(1), uint(2)
	base, revenue, cost := 1.0, .3, .05
	put(t, db, &RequestFact{UUID: "a", RouteGroupID: &g1, Model: "ma", Protocol: domain.ProtocolOpenAI, Path: "/v1/chat/completions", Source: domain.SourceBusiness, CompletedAt: &at, Success: true, Covered: true, BaseCostUSD: &base, RevenueUSD: &revenue, FinalProviderID: &p1})
	put(t, db, &RequestFact{UUID: "b", RouteGroupID: &g2, Model: "mb", Protocol: domain.ProtocolOpenAI, Path: "/v1/chat/completions", Source: domain.SourceBusiness, CompletedAt: &at, Success: true, Covered: true, BaseCostUSD: &base, RevenueUSD: &revenue, FinalProviderID: &p2})
	put(t, db, &AttemptFact{UUID: "a1", RequestUUID: "a", ProviderID: p1, Protocol: domain.ProtocolOpenAI, Path: "/v1/chat/completions", Source: domain.SourceBusiness, HTTPSent: true, Result: "success", CompletedAt: at, EstimatedCostUSD: &cost})
	put(t, db, &AttemptFact{UUID: "b1", RequestUUID: "b", ProviderID: p2, Protocol: domain.ProtocolOpenAI, Path: "/v1/chat/completions", Source: domain.SourceBusiness, HTTPSent: true, Result: "success", CompletedAt: at, EstimatedCostUSD: &cost})
	cfg := DefaultSettings()
	cfg.MinQualitySamples, cfg.MinSuccessRate = 1, .5
	s := &Service{db: db}
	invest, _, _, cov, err := s.invest(context.Background(), Range{From: now.Add(-time.Hour), To: now.Add(time.Hour)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(invest) != 2 {
		t.Fatalf("invest = %+v", invest)
	}
	if invest[0].ProviderID != p1 || invest[1].ProviderID != p2 {
		t.Fatalf("order = %+v", invest)
	}
	closeAmount(t, *invest[0].ProfitPerCost, 5)
	if !strings.Contains(invest[0].Reason, "无可比共同需求") {
		t.Fatalf("reason = %q", invest[0].Reason)
	}
	if cov == nil || *cov != 0 {
		t.Fatalf("cov = %v", cov)
	}
}

func TestInvestQualifiesWithoutTTFTSamples(t *testing.T) {
	db := dashboardTestDB(t)
	now := time.Now().UTC().Truncate(time.Minute)
	at := now.Add(-time.Minute)
	group, provider := uint(1), uint(1)
	base, revenue, cost := 1.0, .3, .05
	put(t, db, &RequestFact{UUID: "r", RouteGroupID: &group, Model: "m", Protocol: domain.ProtocolOpenAI, Path: "/v1/chat/completions", Source: domain.SourceBusiness, CompletedAt: &at, Success: true, Covered: true, BaseCostUSD: &base, RevenueUSD: &revenue, FinalProviderID: &provider})
	put(t, db, &AttemptFact{UUID: "r1", RequestUUID: "r", ProviderID: provider, Protocol: domain.ProtocolOpenAI, Path: "/v1/chat/completions", Source: domain.SourceBusiness, HTTPSent: true, Result: "success", CompletedAt: at, EstimatedCostUSD: &cost, TTFTStatus: "no_output"})
	cfg := DefaultSettings()
	cfg.MinQualitySamples, cfg.MinSuccessRate = 1, .5
	s := &Service{db: db}
	invest, watch, _, _, err := s.invest(context.Background(), Range{From: now.Add(-time.Hour), To: now.Add(time.Hour)}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(invest) != 1 || invest[0].ProviderID != provider {
		t.Fatalf("invest=%+v watch=%+v", invest, watch)
	}
	if invest[0].TTFTp95MS != nil {
		t.Fatalf("p95 = %v", *invest[0].TTFTp95MS)
	}
}

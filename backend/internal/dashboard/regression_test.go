package dashboard

import (
	"context"
	"encoding/json"
	"math"
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
	s := New(db)
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

func TestUrgentRequiresActualCoverage(t *testing.T) {
	for _, mode := range []string{"new", "gap", "complete"} {
		t.Run(mode, func(t *testing.T) {
			db := dashboardTestDB(t)
			now := time.Now().UTC()
			available := now.Add(-48 * time.Hour)
			if mode == "new" {
				available = now.Add(-time.Hour)
			}
			put(t, db, &Meta{ID: 1, AvailableFrom: available})
			balance := 12.0
			put(t, db, &domain.Upstream{ID: 1, Name: "p", Status: domain.StatusEnabled, LastBalance: &balance, LastBalanceAt: &now})
			put(t, db, &MinuteAgg{Bucket: now.Add(-time.Minute).Truncate(time.Minute), Family: FamilyAttempt, Dim: DimProvider, DimID: 1, ConsumptionUSD: 24, ProviderSuccess: 1})
			if mode == "gap" {
				put(t, db, &Gap{StartedAt: now.Add(-time.Hour), EndedAt: now.Add(-30 * time.Minute), Reason: "test"})
			}
			s := &Service{db: db, settle: &settler{db: db}}
			items, err := s.urgent(context.Background(), Range{From: now.Add(-24 * time.Hour), To: now}, DefaultSettings(), 2*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 {
				t.Fatalf("items=%+v", items)
			}
			if mode == "complete" {
				if items[0].HoursLeft == nil {
					t.Fatal("complete data rejected")
				}
				closeAmount(t, *items[0].HoursLeft, 12)
			} else if !items[0].Insufficient || items[0].HoursLeft != nil {
				t.Fatalf("incomplete prediction: %+v", items[0])
			}
		})
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

package ops

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/store"
	"simple-up-manage/internal/upstream"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pulse.db")
	db, err := store.Open("sqlite://" + filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	db.Logger = db.Logger.LogMode(logger.Silent)
	if err := store.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

func TestHealthPulsesMixesProbeAndTraffic(t *testing.T) {
	db := testDB(t)
	now := time.Now().Truncate(time.Minute)
	keyID := uint(7)
	_ = db.Create(&domain.ProbeLog{
		PlatformKeyID: keyID,
		Kind:          domain.ProbeLight,
		Success:       true,
		LatencyMs:     200,
		CreatedAt:     now.Add(-2 * time.Minute),
	}).Error
	_ = db.Create(&domain.RequestLog{
		PlatformKeyID: &keyID,
		Success:       false,
		DurationMs:    800,
		CreatedAt:     now.Add(-2 * time.Minute),
	}).Error
	_ = db.Create(&domain.RequestLog{
		PlatformKeyID: &keyID,
		Success:       true,
		DurationMs:    300,
		CreatedAt:     now,
	}).Error

	s := &Service{DB: db}
	pulses := s.HealthPulses(context.Background(), []uint{keyID})
	cells := pulses[keyID]
	if len(cells) != 3 {
		t.Fatalf("cells=%d", len(cells))
	}
	var bad, ok int
	for _, c := range cells {
		switch c.State {
		case "bad":
			bad++
			if c.Ok != 0 || c.Fail != 1 {
				t.Fatalf("bad counts %+v", c)
			}
		case "ok":
			ok++
			if c.Ok < 1 {
				t.Fatalf("ok counts %+v", c)
			}
			if (c.LastLatencyMs != 200 && c.LastLatencyMs != 300) || c.Score != 10 {
				t.Fatalf("ok latency/score %+v", c)
			}
		}
	}
	if bad != 1 || ok != 2 {
		t.Fatalf("bad=%d ok=%d", bad, ok)
	}
}

func TestHealthPulsesKeepsLatest60RecordsPerKey(t *testing.T) {
	db := testDB(t)
	now := time.Now().Truncate(time.Second)
	keyID, sparseID, emptyID := uint(1), uint(2), uint(3)
	// Interleave both sources beyond the old one-hour window.
	for i := 0; i < 140; i++ {
		at := now.Add(time.Duration(i-140) * time.Hour)
		var err error
		if i%2 == 0 {
			err = db.Create(&domain.ProbeLog{PlatformKeyID: keyID, Kind: domain.ProbeDeep, Success: true, LatencyMs: i + 1, CreatedAt: at}).Error
		} else {
			err = db.Create(&domain.RequestLog{PlatformKeyID: &keyID, Success: true, DurationMs: i + 1, CreatedAt: at}).Error
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{domain.ProbeBalance, domain.ProbeBilling, domain.ProbeModels} {
		if err := db.Create(&domain.ProbeLog{PlatformKeyID: keyID, Kind: kind, CreatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&domain.RequestLog{PlatformKeyID: &keyID, InFlight: true, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.ProbeLog{PlatformKeyID: sparseID, Kind: domain.ProbeLight, Success: true, LatencyMs: 7000, CreatedAt: now.Add(-24 * time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	got := s.HealthPulses(context.Background(), []uint{keyID, sparseID, emptyID})
	if len(got[keyID]) != PulseSamples {
		t.Fatalf("samples=%d", len(got[keyID]))
	}
	for i, cell := range got[keyID] {
		if cell.LastLatencyMs != i+81 || cell.Ok != 1 || cell.Fail != 0 {
			t.Fatalf("sample %d: %+v", i, cell)
		}
		if i > 0 && !cell.Start.After(got[keyID][i-1].Start) {
			t.Fatalf("samples are not chronological: %+v", got[keyID])
		}
	}
	if len(got[sparseID]) != 1 || got[sparseID][0].State != "degraded" {
		t.Fatalf("sparse history padded or dropped: %+v", got[sparseID])
	}
	if len(got[emptyID]) != 0 {
		t.Fatalf("empty history padded: %+v", got[emptyID])
	}
}

func TestHealthPulsesOrdersSameTimestampByID(t *testing.T) {
	db := testDB(t)
	at := time.Now()
	for i := 1; i <= 65; i++ {
		if err := db.Create(&domain.ProbeLog{PlatformKeyID: 1, Kind: domain.ProbeLight, Success: true, LatencyMs: i, CreatedAt: at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{DB: db}
	cells := s.HealthPulses(context.Background(), []uint{1})[1]
	if len(cells) != PulseSamples {
		t.Fatalf("samples=%d", len(cells))
	}
	for i, cell := range cells {
		if cell.LastLatencyMs != i+6 {
			t.Fatalf("sample %d has unstable ordering: %+v", i, cell)
		}
	}
}

func TestPulseScore(t *testing.T) {
	if state, s := pulseScore(0, 0, 0); state != "empty" || s != 0 {
		t.Fatalf("empty state=%s score=%d", state, s)
	}
	if state, s := pulseScore(0, 2, 100); state != "bad" || s != 0 {
		t.Fatalf("all fail state=%s score=%d", state, s)
	}
	if state, s := pulseScore(2, 0, 400); state != "ok" || s != 10 {
		t.Fatalf("fast ok state=%s score=%d", state, s)
	}
	if state, s := pulseScore(2, 0, 4000); state != "ok" || s != 10 {
		t.Fatalf("under 6s should stay ok, got %s score=%d", state, s)
	}
	if state, s := pulseScore(1, 0, 6000); state != "degraded" || s != 6 {
		t.Fatalf("slow ok state=%s score=%d", state, s)
	}
	if state, s := pulseScore(1, 1, 200); state != "bad" || s != 0 {
		t.Fatalf("any fail should be bad, got %s score=%d", state, s)
	}
}

func TestProbeHTTPOutcomeV1(t *testing.T) {
	target := ProbeTarget{Model: "deepseek-chat", Vendor: "deepseek"}
	ok := probeHTTPOutcome(&upstream.Result{
		Status: 200,
		Body:   []byte(`{"choices":[{"message":{"content":"hi"}}]}`),
	}, nil, target)
	if !ok.Success {
		t.Fatalf("2xx with text should succeed: %+v", ok)
	}
	empty := probeHTTPOutcome(&upstream.Result{
		Status: 200,
		Body:   []byte(`{"choices":[{"message":{"content":""}}]}`),
	}, nil, target)
	if empty.Success {
		t.Fatal("2xx with empty text should fail")
	}
	fail := probeHTTPOutcome(&upstream.Result{Status: 502, Body: []byte(`upstream down`)}, nil, target)
	if fail.Success {
		t.Fatal("502 should fail")
	}
}

func TestKeyCacheRates(t *testing.T) {
	db := testDB(t)
	keyID := uint(9)
	_ = db.Create(&domain.RequestLog{
		PlatformKeyID:       &keyID,
		InputTokens:         80,
		CacheReadTokens:     20,
		CacheCreationTokens: 0,
		CreatedAt:           time.Now(),
	}).Error
	rates := (&Service{DB: db}).KeyCacheRates(context.Background(), []uint{keyID})
	c := rates[keyID]
	if c.Samples != 1 || c.Rate < 0.19 || c.Rate > 0.21 {
		t.Fatalf("cache %+v", c)
	}
}

func TestLastProbeAtIgnoresBalance(t *testing.T) {
	db := testDB(t)
	keyID := uint(8)
	now := time.Now()
	_ = db.Create(&domain.ProbeLog{
		PlatformKeyID: keyID, Kind: domain.ProbeDeep, Success: true, LatencyMs: 100, CreatedAt: now.Add(-2 * time.Minute),
	}).Error
	_ = db.Create(&domain.ProbeLog{
		PlatformKeyID: keyID, Kind: domain.ProbeBalance, Success: true, LatencyMs: 50, CreatedAt: now,
	}).Error
	s := &Service{DB: db}
	got := s.lastProbeAt(context.Background(), []domain.PlatformKey{{ID: keyID}})
	at, ok := got[keyID]
	if !ok {
		t.Fatal("missing last health probe")
	}
	if now.Sub(at) < time.Minute {
		t.Fatalf("used balance tick %v", at)
	}
}

func TestParseLooseTime(t *testing.T) {
	raw := "2026-09-06 19:27:21.123+08:00"
	got, ok := domain.ParseLooseTime(raw)
	if !ok {
		t.Fatal("parse failed")
	}
	if got.Year() != 2026 || got.Month() != 9 || got.Day() != 6 {
		t.Fatalf("got %v", got)
	}
}

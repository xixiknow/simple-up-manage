package ops

import (
	"context"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func TestKeyWindowStatsIncludesProbesAndOmitsCacheWithoutTokens(t *testing.T) {
	db := testDB(t)
	keyID := uint(3)
	now := time.Now()
	_ = db.Create(&domain.ProbeLog{
		PlatformKeyID: keyID, Kind: domain.ProbeLight, Success: true, LatencyMs: 200, CreatedAt: now,
	}).Error
	s := &Service{DB: db}
	m := s.KeyWindowStatsMap(context.Background(), []uint{keyID}, 15*time.Minute, 50)
	st := m[keyID]
	if st.Samples != 1 || !st.HasLatency || st.HasCache {
		t.Fatalf("probe-only stats %+v", st)
	}
	if st.Success != 1 {
		t.Fatalf("success=%v", st.Success)
	}
}

func TestKeyWindowStatsCacheFromRequests(t *testing.T) {
	db := testDB(t)
	keyID := uint(4)
	now := time.Now()
	id := keyID
	_ = db.Create(&domain.RequestLog{
		PlatformKeyID: &id, Success: true, InputTokens: 60, CacheReadTokens: 40,
		TTFTMs: 300, DurationMs: 800, CreatedAt: now,
	}).Error
	s := &Service{DB: db}
	st := s.KeyWindowStatsMap(context.Background(), []uint{keyID}, 15*time.Minute, 50)[keyID]
	if !st.HasCache || st.Cache < 0.39 || st.Cache > 0.41 {
		t.Fatalf("cache %+v", st)
	}
	if !st.HasLatency || st.LatencyP50 != 300 {
		t.Fatalf("latency %+v", st)
	}
}

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

// OpenAI RequestLog rows carry protocol-native prompt_tokens (8 = 3 uncached
// + 5 cached); the window aggregation must normalize before computing the
// cache rate, giving 5/8 rather than 5/13.
func TestKeyWindowStatsCacheOpenAINormalized(t *testing.T) {
	db := testDB(t)
	keyID := uint(5)
	id := keyID
	_ = db.Create(&domain.RequestLog{
		PlatformKeyID: &id, Protocol: domain.ProtocolOpenAI, Success: true,
		InputTokens: 8, CacheReadTokens: 5,
		TTFTMs: 300, DurationMs: 800, CreatedAt: time.Now(),
	}).Error
	s := &Service{DB: db}
	st := s.KeyWindowStatsMap(context.Background(), []uint{keyID}, 15*time.Minute, 50)[keyID]
	if !st.HasCache || st.Cache < 0.61 || st.Cache > 0.64 {
		t.Fatalf("cache %+v", st)
	}
}

// Arrears probe failures are availability state: they must stay out of the
// console score so a recharged key's score recovers immediately.
func TestKeyWindowStatsExcludesArrearsProbeFailures(t *testing.T) {
	db := testDB(t)
	keyID := uint(6)
	now := time.Now()
	create := func(success bool, status int, msg string) {
		_ = db.Create(&domain.ProbeLog{
			PlatformKeyID: keyID, Kind: domain.ProbeDeep, Success: success,
			StatusCode: status, ErrorMessage: msg, LatencyMs: 500, CreatedAt: now,
		}).Error
	}
	// Billing/credential failures: excluded.
	create(false, 402, "")
	create(false, 403, `{"error":{"type":"billing_error","message":"insufficient balance"}}`)
	create(false, 429, `{"error":{"type":"usage_limit_reached"}}`)
	// Quality failures: still counted.
	create(false, 500, "upstream error")
	create(false, 0, "probe timed out after 30 seconds")
	create(true, 200, "")
	s := &Service{DB: db}
	st := s.KeyWindowStatsMap(context.Background(), []uint{keyID}, 15*time.Minute, 50)[keyID]
	if st.Samples != 3 {
		t.Fatalf("samples=%d, want 3 (arrears rows excluded)", st.Samples)
	}
	if st.Success < 0.33 || st.Success > 0.34 {
		t.Fatalf("success=%v, want 1/3", st.Success)
	}
}

// Request-log rows tagged with availability failure actions stay out too.
func TestKeyWindowStatsExcludesAvailabilityRequestFailures(t *testing.T) {
	db := testDB(t)
	keyID := uint(7)
	id := keyID
	now := time.Now()
	create := func(success bool, action string) {
		_ = db.Create(&domain.RequestLog{
			PlatformKeyID: &id, Success: success, FailureAction: action,
			TTFTMs: 300, DurationMs: 800, CreatedAt: now,
		}).Error
	}
	create(false, domain.ActionKeyQuotaExhausted)
	create(false, domain.ActionQuotaRateLimited)
	create(false, "request_scope_failure")
	create(true, "")
	s := &Service{DB: db}
	st := s.KeyWindowStatsMap(context.Background(), []uint{keyID}, 15*time.Minute, 50)[keyID]
	if st.Samples != 2 {
		t.Fatalf("samples=%d, want 2 (availability rows excluded)", st.Samples)
	}
	if st.Success != 0.5 {
		t.Fatalf("success=%v, want 0.5", st.Success)
	}
}

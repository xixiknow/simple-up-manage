package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
)

func TestProbeFilteredCadence(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 58, 35, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		probeAge    time.Duration
		probeKind   string
		intervalSec int
		requestAge  time.Duration
		lastRequest bool
		manual      bool
		disabled    bool
		wantProbe   bool
		wantSkipped int
	}{
		{name: "previous minute completed 59 seconds ago", probeAge: 59 * time.Second, wantProbe: true},
		{name: "tick jitter", probeAge: time.Minute - time.Millisecond, wantProbe: true},
		{name: "already checked this minute", probeAge: 10 * time.Second, wantSkipped: 1},
		{name: "custom five minute window due", probeAge: 299 * time.Second, intervalSec: 300, wantProbe: true},
		{name: "custom five minute window already checked", probeAge: 30 * time.Second, intervalSec: 300, wantSkipped: 1},
		{name: "custom interval spans multiple ticks", probeAge: 2 * time.Minute, intervalSec: 300, wantSkipped: 1},
		{name: "light probe counts", probeAge: 10 * time.Second, probeKind: domain.ProbeLight, wantSkipped: 1},
		{name: "balance does not suppress probes", probeAge: 10 * time.Second, probeKind: domain.ProbeBalance, wantProbe: true},
		{name: "recent request still suppresses probes", probeAge: 59 * time.Second, requestAge: 10 * time.Second, wantSkipped: 1},
		{name: "recent observed request still suppresses probes", probeAge: 59 * time.Second, requestAge: 10 * time.Second, lastRequest: true, wantSkipped: 1},
		{name: "old request does not suppress probes", probeAge: 59 * time.Second, requestAge: 2 * time.Minute, wantProbe: true},
		{name: "manual probe bypasses schedule", probeAge: 10 * time.Second, intervalSec: 300, requestAge: 5 * time.Second, manual: true, wantProbe: true},
		{name: "disabled key stays excluded", probeAge: 2 * time.Minute, disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/models" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":[{"id":"fixture"}]}`)
			}))
			defer server.Close()
			db := testDB(t)
			db.NowFunc = func() time.Time { return now }
			enc, err := crypto.New(strings.Repeat("01", 32))
			if err != nil {
				t.Fatal(err)
			}
			secret, err := enc.Encrypt("fixture")
			if err != nil {
				t.Fatal(err)
			}
			up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: "openai"}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret, ProbeIntervalSec: tc.intervalSec}
			if tc.disabled {
				key.Status = domain.StatusDisabled
			}
			requestAt := now.Add(-tc.requestAge)
			if tc.lastRequest {
				key.LastRequestAt = &requestAt
			}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			kind := tc.probeKind
			if kind == "" {
				kind = domain.ProbeDeep
			}
			probe := domain.ProbeLog{PlatformKeyID: key.ID, Kind: kind, Success: true, LatencyMs: 1000, CreatedAt: now.Add(-tc.probeAge)}
			if err := db.Create(&probe).Error; err != nil {
				t.Fatal(err)
			}
			if tc.requestAge > 0 && !tc.lastRequest {
				if err := db.Create(&domain.RequestLog{PlatformKeyID: &key.ID, Success: true, CreatedAt: requestAt}).Error; err != nil {
					t.Fatal(err)
				}
			}
			interval := time.Minute
			if tc.manual {
				interval = 0
			}
			s := New(db, enc, nil)
			ok, failed, skipped := s.probeFilteredAt(context.Background(), false, nil, interval, now)
			wantCalls := 0
			if tc.wantProbe {
				wantCalls = 1
			}
			if ok != wantCalls || failed != 0 || skipped != tc.wantSkipped || int(calls.Load()) != wantCalls {
				t.Fatalf("ok=%d failed=%d skipped=%d calls=%d; want ok/calls=%d skipped=%d", ok, failed, skipped, calls.Load(), wantCalls, tc.wantSkipped)
			}
			var logs int64
			if err := db.Model(&domain.ProbeLog{}).Where("platform_key_id = ?", key.ID).Count(&logs).Error; err != nil {
				t.Fatal(err)
			}
			if logs != int64(1+wantCalls) {
				t.Fatalf("probe logs=%d, want %d", logs, 1+wantCalls)
			}
			if tc.wantProbe && !tc.manual {
				ok, failed, skipped = s.probeFilteredAt(context.Background(), false, nil, interval, now.Add(time.Second))
				if ok != 0 || failed != 0 || skipped != 1 || calls.Load() != 1 {
					t.Fatalf("duplicate probe in same window: ok=%d failed=%d skipped=%d calls=%d", ok, failed, skipped, calls.Load())
				}
			}
		})
	}
}

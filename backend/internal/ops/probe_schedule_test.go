package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
		inFlight    bool
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
		{name: "observed request without completed log does not suppress probes", probeAge: 59 * time.Second, requestAge: 10 * time.Second, lastRequest: true, wantProbe: true},
		{name: "previous minute request does not leave current minute empty", probeAge: 59 * time.Second, requestAge: 40 * time.Second, wantProbe: true},
		{name: "previous custom window request does not suppress probes", probeAge: 299 * time.Second, intervalSec: 300, requestAge: 250 * time.Second, wantProbe: true},
		{name: "in flight request does not suppress probes", probeAge: 59 * time.Second, requestAge: 10 * time.Second, inFlight: true, wantProbe: true},
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
				if err := db.Create(&domain.RequestLog{PlatformKeyID: &key.ID, Success: true, InFlight: tc.inFlight, CreatedAt: requestAt}).Error; err != nil {
					t.Fatal(err)
				}
			}
			interval := time.Minute
			if tc.manual {
				interval = 0
			}
			s := New(db, enc, nil)
			r := s.probeFilteredAt(context.Background(), false, nil, interval, now)
			ok, failed, skipped := r.OK, r.Failed, r.Skipped
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
				r2 := s.probeFilteredAt(context.Background(), false, nil, interval, now.Add(time.Second))
				ok, failed, skipped = r2.OK, r2.Failed, r2.Skipped
				if ok != 0 || failed != 0 || skipped != 1 || calls.Load() != 1 {
					t.Fatalf("duplicate probe in same window: ok=%d failed=%d skipped=%d calls=%d", ok, failed, skipped, calls.Load())
				}
			}
		})
	}
}

func TestProbeFilteredRunsWithBoundedConcurrency(t *testing.T) {
	entered := make(chan struct{}, probeConcurrency+1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"fixture"}]}`)
	}))
	defer server.Close()
	defer unblock()
	db := testDB(t)
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
	for i := 0; i < probeConcurrency+1; i++ {
		key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret}
		if err := db.Create(&key).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := New(db, enc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan [3]int, 1)
	go func() {
		ok, fail, skipped := s.ProbeFiltered(ctx, false, nil, time.Minute)
		done <- [3]int{ok, fail, skipped}
	}()
	defer func() {
		unblock()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("probe workers did not stop")
		}
	}()
	for i := 0; i < probeConcurrency; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d probes started while earlier probes were blocked", i)
		}
	}
	select {
	case <-entered:
		t.Fatal("probe concurrency limit exceeded")
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	select {
	case result := <-done:
		done <- result
		if result != [3]int{probeConcurrency + 1, 0, 0} {
			t.Fatalf("unexpected probe counts: %v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe batch did not finish")
	}
}

func TestProbeLogUsesStartMinute(t *testing.T) {
	started := time.Date(2026, 9, 14, 12, 0, 58, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(started.UnixNano())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clock.Store(started.Add(5 * time.Second).UnixNano())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"fixture"}]}`)
	}))
	defer server.Close()
	db := testDB(t)
	db.NowFunc = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
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
	key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	s := New(db, enc, nil)
	if _, err := s.ProbeKey(context.Background(), key.ID, false); err != nil {
		t.Fatal(err)
	}
	var probe domain.ProbeLog
	if err := db.First(&probe).Error; err != nil {
		t.Fatal(err)
	}
	if !probe.CreatedAt.Equal(started) {
		t.Fatalf("probe moved to completion minute: got %s want %s", probe.CreatedAt, started)
	}
	r := s.probeFilteredAt(context.Background(), false, nil, time.Minute, started.Add(time.Minute))
	ok, fail, skipped := r.OK, r.Failed, r.Skipped
	if ok != 1 || fail != 0 || skipped != 0 {
		t.Fatalf("cross-minute completion suppressed next probe: %d/%d/%d", ok, fail, skipped)
	}
}

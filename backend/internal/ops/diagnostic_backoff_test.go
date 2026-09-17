package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLightProbeHonorsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1800")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
	}))
	defer server.Close()
	s := &Service{Client: upstream.NewClient()}
	out := s.lightProbe(context.Background(), &domain.PlatformKey{Upstream: &domain.Upstream{BaseURL: server.URL, Protocols: "openai"}}, "test")
	if out.Success || out.RetryAfter != "1800" || out.StatusCode != 429 {
		t.Fatalf("outcome: %+v", out)
	}
}

func TestDiagnosticRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, message, retry string
		status, failures     int
		delay                time.Duration
	}{
		{"disabled", `{"code":"API_KEY_DISABLED"}`, "", 401, 1, time.Hour},
		{"deleted", `{"code":"GROUP_DELETED"}`, "", 403, 1, time.Hour},
		{"quota", `{"code":"INSUFFICIENT_BALANCE"}`, "", 403, 1, 15 * time.Minute},
		{"capability", `{"error":{"code":"model_not_found"}}`, "", 404, 1, 30 * time.Minute},
		{"temporary", "timeout", "", 0, 1, time.Minute},
		{"exponential", "timeout", "", 0, 4, 8 * time.Minute},
		{"capped", "timeout", "", 0, 6, 15 * time.Minute},
		{"retry after", "busy", "1800", 429, 1, 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			s := &Service{DB: db}
			at := time.Now()
			for i := tc.failures; i > 0; i-- {
				r := domain.ProbeLog{PlatformKeyID: 1, Kind: domain.ProbeDeep, StatusCode: tc.status, ErrorMessage: tc.message, RetryAfter: tc.retry, CreatedAt: at.Add(-time.Duration(i-1) * time.Minute)}
				if err := db.Create(&r).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, delta := range []time.Duration{tc.delay - time.Second, tc.delay + time.Second} {
				ready, err := s.diagnosticReady(context.Background(), 1, domain.ProbeDeep, at.Add(delta))
				if err != nil || ready != (delta > tc.delay) {
					t.Fatalf("delta=%v ready=%v err=%v", delta, ready, err)
				}
			}
			if err := db.Create(&domain.ProbeLog{PlatformKeyID: 1, Kind: domain.ProbeDeep, Success: true, CreatedAt: at.Add(time.Second)}).Error; err != nil {
				t.Fatal(err)
			}
			if ready, err := s.diagnosticReady(context.Background(), 1, domain.ProbeDeep, at.Add(2*time.Second)); err != nil || !ready {
				t.Fatal("success did not clear diagnostic backoff", err)
			}
		})
	}
}

func TestScheduledDiagnosticsBackOffButManualChecksRun(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m"}],"effective_rate_multiplier":1,"remaining":10}`)
	}))
	defer server.Close()
	db := testDB(t)
	enc, _ := crypto.New(strings.Repeat("01", 32))
	secret, _ := enc.Encrypt("test")
	u := domain.Upstream{Name: "test", BaseURL: server.URL, Kind: domain.KindSub2API, Protocols: "openai", Status: domain.StatusEnabled}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	k := domain.PlatformKey{UpstreamID: u.ID, EncryptedKey: secret, Status: domain.StatusEnabled}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{domain.ProbeLight, domain.ProbeBilling, domain.ProbeBalance} {
		if err := db.Create(&domain.ProbeLog{PlatformKeyID: k.ID, Kind: kind, StatusCode: 403, ErrorMessage: `{"code":"GROUP_DELETED"}`, CreatedAt: time.Now().Add(-2 * time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := New(db, enc, nil)
	ctx := context.Background()
	if _, fail, skip := s.ProbeFiltered(ctx, false, nil, time.Minute); fail != 0 || skip != 1 {
		t.Fatalf("probe counts: %d %d", fail, skip)
	}
	if ok, fail := s.RefreshAllBalances(ctx); ok != 1 || fail != 0 {
		t.Fatalf("balance counts %d %d", ok, fail)
	}
	if ok, fail := s.RefreshAllBilling(ctx); ok != 1 || fail != 0 {
		t.Fatalf("billing counts %d %d", ok, fail)
	}
	if calls.Load() != 2 {
		t.Fatal("balance and billing must retain their normal schedule")
	}
	if _, err := s.ProbeKey(ctx, k.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshBalance(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	_ = s.RefreshBilling(ctx, k.ID)
	if calls.Load() != 5 {
		t.Fatalf("manual checks did not run: %d", calls.Load())
	}
	var gates int64
	db.Model(&domain.RoutingCircuit{}).Count(&gates)
	if gates != 0 {
		t.Fatal("diagnostics changed business circuits")
	}
}

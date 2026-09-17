package ops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/routinghealth"
)

func TestBalanceRecoveryPreservesOtherAndNewerFailures(t *testing.T) {
	db := testDB(t)
	s := &Service{DB: db}
	cut := time.Now().Add(-time.Minute)
	for _, id := range []uint{1, 2} {
		if err := db.Create(&domain.Upstream{ID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	keys := []domain.PlatformKey{{UpstreamID: 1}, {UpstreamID: 1}, {UpstreamID: 2}}
	for i := range keys {
		if err := db.Create(&keys[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	dim := routinghealth.Dimension{KeyID: keys[0].ID, Protocol: "openai", Model: "m", Path: "/v1/responses"}
	until := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		scope, reason string
		key           uint
		newer         bool
	}{
		{routinghealth.KeyScope(keys[0].ID), "key_quota_exhausted", keys[0].ID, false},
		{dim.Scope(), "key_quota_exhausted", keys[0].ID, false},
		{"other-model", "rate_limit", keys[0].ID, false},
		{"auth", "credential_disabled", keys[0].ID, false},
		{"capability", "model_not_found", keys[0].ID, false},
		{routinghealth.KeyScope(keys[1].ID), "key_quota_exhausted", keys[1].ID, true},
		{routinghealth.KeyScope(keys[2].ID), "key_quota_exhausted", keys[2].ID, false},
	} {
		opened := cut.Add(-time.Minute)
		if tc.newer {
			opened = cut.Add(time.Second)
		}
		gate := domain.RoutingCircuit{Scope: tc.scope, PlatformKeyID: tc.key, Open: true, OpenedAt: opened, Until: until, Reason: tc.reason, Failures: 3, BackoffSec: 900, Lease: "old-request", LeaseUntil: until, CheckLease: "old-check", CheckUntil: &until, NextCheckAt: &until}
		if err := db.Create(&gate).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.releaseBalanceCooldowns(context.Background(), 1, cut); err != nil {
		t.Fatal(err)
	}
	store := routinghealth.Store{DB: db}
	if err := store.FinishCheck(context.Background(), dim.Scope(), "old-check", false, false, "old failure", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(context.Background(), dim, "old-request", routinghealth.Outcome{RequestID: "stale", StartedAt: cut.Add(-time.Minute), AuthFailure: true, Reason: "key_quota_exhausted"}); err != nil {
		t.Fatal(err)
	}
	var gates []domain.RoutingCircuit
	if err := db.Find(&gates).Error; err != nil {
		t.Fatal(err)
	}
	for _, gate := range gates {
		cleared := gate.Scope == dim.Scope() || gate.Scope == routinghealth.KeyScope(keys[0].ID)
		if cleared {
			if gate.Open || gate.Reason != "" || gate.Failures != 0 || gate.BackoffSec != 0 || gate.Lease != "" || gate.CheckLease != "" || gate.NextCheckAt != nil || !gate.Until.IsZero() || !gate.OpenedAt.After(cut) {
				t.Fatalf("quota gate not reset: %+v", gate)
			}
		} else if !gate.Open || gate.Lease != "old-request" || !gate.Until.Equal(until) {
			t.Fatalf("unrelated/newer gate changed: %+v", gate)
		}
	}
}

func TestBalanceTaskReleasesOnlyConfirmedRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		status             int
		unlimited, release bool
	}{
		{"positive", `{"remaining":10}`, 200, false, true},
		{"zero", `{"remaining":0}`, 200, false, false},
		{"negative", `{"remaining":-1}`, 200, false, false},
		{"unknown", `{}`, 200, false, false},
		{"error", `{"error":"unavailable"}`, 503, false, false},
		{"unlimited", `{"data":{"unlimited_quota":true}}`, 200, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.unlimited && r.URL.Path == "/v1/dashboard/billing/subscription" {
					_, _ = io.WriteString(w, `{"hard_limit_usd":100000000}`)
					return
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			db := testDB(t)
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("test")
			zero := 0.0
			up := domain.Upstream{Name: "balance", BaseURL: server.URL, Kind: domain.KindSub2API, Status: domain.StatusEnabled, LastBalance: &zero}
			if tc.unlimited {
				up.Kind = domain.KindNewAPI
			}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				key := domain.PlatformKey{UpstreamID: up.ID, EncryptedKey: secret, Status: domain.StatusEnabled, HealthStatus: domain.HealthLowBalance}
				if err := db.Create(&key).Error; err != nil {
					t.Fatal(err)
				}
				gate := domain.RoutingCircuit{Scope: routinghealth.KeyScope(key.ID), PlatformKeyID: key.ID, Open: true, OpenedAt: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Reason: "key_quota_exhausted"}
				if err := db.Create(&gate).Error; err != nil {
					t.Fatal(err)
				}
			}
			s := New(db, enc, nil)
			ok, fail := s.RefreshAllBalances(context.Background())
			if (tc.status == 200 && (ok != 1 || fail != 0)) || (tc.status != 200 && (ok != 0 || fail != 1)) {
				t.Fatalf("counts %d %d", ok, fail)
			}
			var count int64
			if err := db.Model(&domain.RoutingCircuit{}).Where("open = ?", true).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if (tc.release && count != 0) || (!tc.release && count != 2) {
				t.Fatalf("open=%d release=%v", count, tc.release)
			}
			if tc.release {
				var unhealthy int64
				db.Model(&domain.PlatformKey{}).Where("health_status <> ?", domain.HealthHealthy).Count(&unhealthy)
				if unhealthy != 0 {
					t.Fatal("balance health not restored")
				}
			}
		})
	}
}

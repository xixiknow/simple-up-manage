package routinghealth

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"simple-up-manage/internal/domain"
)

func fixture(t *testing.T) (Store, Dimension) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "routing.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&domain.RoutingCircuit{}, &domain.RoutingObservation{}, &domain.RoutingBudget{}); err != nil {
		t.Fatal(err)
	}
	return Store{DB: db}, Dimension{KeyID: 1, Protocol: "openai", Model: "m", Path: "/v1/responses", Stream: true}
}

func row(t *testing.T, s Store, scope string) domain.RoutingCircuit {
	t.Helper()
	var r domain.RoutingCircuit
	if err := s.DB.Where("scope = ?", scope).First(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func record(t *testing.T, s Store, d Dimension, id string, success bool) {
	t.Helper()
	if err := s.Observe(context.Background(), d, "", Outcome{RequestID: id, StartedAt: time.Now(), Success: success, Reason: "request_scope_failure"}); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentRequestsAndDimensions(t *testing.T) {
	s, d := fixture(t)
	for i := 0; i < 6; i++ {
		record(t, s, d, "one-request", false)
	}
	if r := row(t, s, d.Scope()); r.Open || r.Failures != 1 {
		t.Fatalf("retries inflated failures: %+v", r)
	}
	record(t, s, d, "two", false)
	record(t, s, d, "three", false)
	if r := row(t, s, d.Scope()); !r.Open || r.BackoffSec != 30 {
		t.Fatalf("missing circuit: %+v", r)
	}
	for _, other := range []Dimension{{1, "openai", "other", d.Path, true}, {1, "openai", "m", "/v1/chat/completions", true}, {1, "openai", "m", d.Path, false}, {2, "openai", "m", d.Path, true}, {1, "anthropic", "m", d.Path, true}} {
		if _, err := s.Admit(context.Background(), other, false); err != nil {
			t.Fatalf("unrelated dimension blocked: %+v %v", other, err)
		}
	}
	if _, err := s.Admit(context.Background(), d, true); err != ErrUnavailable {
		t.Fatalf("open admitted: %v", err)
	}
}

func TestSuccessNeutralAndRollingWindow(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	record(t, s, d, "1", false)
	record(t, s, d, "2", false)
	if err := s.Observe(ctx, d, "", Outcome{RequestID: "cancel", StartedAt: time.Now(), Neutral: true}); err != nil {
		t.Fatal(err)
	}
	if r := row(t, s, d.Scope()); r.Failures != 2 || r.Open {
		t.Fatal(r)
	}
	record(t, s, d, "success", true)
	record(t, s, d, "3", false)
	r := row(t, s, d.Scope())
	if r.Failures != 1 || r.Open {
		t.Fatal(r)
	}
	r.FailureTimes = []int64{time.Now().Add(-61 * time.Second).UnixMilli(), time.Now().Add(-10 * time.Second).UnixMilli()}
	if err := s.DB.Save(&r).Error; err != nil {
		t.Fatal(err)
	}
	record(t, s, d, "4", false)
	if r := row(t, s, d.Scope()); r.Failures != 2 || r.Open {
		t.Fatal(r)
	}
	record(t, s, d, "5", false)
	if !row(t, s, d.Scope()).Open {
		t.Fatal("rolling window failed")
	}
}

func TestRecoveryLeaseBackoffAndStaleSuccess(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	started := time.Now().Add(-time.Second)
	for i := 0; i < 3; i++ {
		record(t, s, d, fmt.Sprint(i), false)
	}
	if err := s.Observe(ctx, d, "old", Outcome{RequestID: "old", StartedAt: started, Success: true}); err != nil {
		t.Fatal(err)
	}
	if !row(t, s, d.Scope()).Open {
		t.Fatal("late success cleared newer circuit")
	}
	for i, want := range []int{60, 120, 300, 300, 300} {
		if err := s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", d.Scope()).Update("until", time.Now().Add(-time.Second)).Error; err != nil {
			t.Fatal(err)
		}
		token, err := s.Admit(ctx, d, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Admit(ctx, d, true); err != ErrUnavailable {
			t.Fatalf("duplicate half-open: %v", err)
		}
		if err := s.Observe(ctx, d, token, Outcome{RequestID: fmt.Sprint("retry", i), StartedAt: time.Now(), Reason: "request_scope_failure"}); err != nil {
			t.Fatal(err)
		}
		if r := row(t, s, d.Scope()); r.BackoffSec != want || r.Lease != "" {
			t.Fatal(r)
		}
	}
	_ = s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", d.Scope()).Update("until", time.Now().Add(-time.Second)).Error
	token, err := s.Admit(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(ctx, d, token, Outcome{RequestID: "recovered", StartedAt: time.Now(), Success: true}); err != nil {
		t.Fatal(err)
	}
	if r := row(t, s, d.Scope()); r.Open || r.Failures != 0 || r.BackoffSec != 0 {
		t.Fatal(r)
	}
}

func TestLeaseExpiryCancellationAndAuthGate(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	if err := s.Observe(ctx, d, "", Outcome{RequestID: "auth", StartedAt: time.Now(), AuthFailure: true, Reason: "authentication_failure"}); err != nil {
		t.Fatal(err)
	}
	other := d
	other.Model = "other"
	if _, err := s.Admit(ctx, other, false); err != ErrUnavailable {
		t.Fatal("auth gate not shared")
	}
	_ = s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", KeyScope(d.KeyID)).Update("until", time.Now().Add(-time.Second)).Error
	token, err := s.Admit(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(ctx, other, token, Outcome{RequestID: "cancel", StartedAt: time.Now(), Neutral: true}); err != nil {
		t.Fatal(err)
	}
	if r := row(t, s, KeyScope(d.KeyID)); !r.Open || r.Lease != "" {
		t.Fatal(r)
	}
	old, err := s.Admit(ctx, other, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", KeyScope(d.KeyID)).Update("lease_until", time.Now().Add(-time.Second)).Error
	token, err = s.Admit(ctx, other, true)
	if err != nil || token == old {
		t.Fatalf("lease not renewed: %v", err)
	}
	if err := s.Observe(ctx, other, old, Outcome{RequestID: "expired-owner", StartedAt: time.Now(), Success: true}); err != nil {
		t.Fatal(err)
	}
	if !row(t, s, KeyScope(d.KeyID)).Open {
		t.Fatal("expired owner cleared gate")
	}
}

func TestRetryAfterAndSharedBudget(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	if err := s.Observe(ctx, d, "", Outcome{RequestID: "limited", StartedAt: time.Now(), Limited: true, RetryAfter: 90 * time.Second, Reason: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	if r := row(t, s, d.Scope()); !r.Open || time.Until(r.Until) < 89*time.Second {
		t.Fatal(r)
	}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := Store{DB: s.DB}
			slot, err := other.Slot(ctx, "pool", 20, true)
			if err != nil {
				t.Error(err)
			}
			if slot {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 5 {
		t.Fatalf("budget=%d", granted.Load())
	}
	for i := 0; i < 40; i++ {
		if _, err := s.Slot(ctx, "pool", 20, false); err != nil {
			t.Fatal(err)
		}
	}
	var b domain.RoutingBudget
	s.DB.First(&b, "scope = ?", "pool")
	if b.Counter != 0 {
		t.Fatal("preview consumed budget")
	}
}

func TestConcurrentRecoveryAcrossStores(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	if err := s.DB.Create(&domain.RoutingCircuit{Scope: d.Scope(), PlatformKeyID: d.KeyID, Open: true, Until: time.Now().Add(-time.Second)}).Error; err != nil {
		t.Fatal(err)
	}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (Store{DB: s.DB}).Admit(ctx, d, true)
			if err == nil {
				granted.Add(1)
			} else if err != ErrUnavailable {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 1 {
		t.Fatalf("half-open admissions=%d", granted.Load())
	}
}

func TestFirstTokenTimeoutWeightOpensSparseGate(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	s.Settings.FTTFailureWeight = 2
	observe := func(store Store, dim Dimension, id string) {
		t.Helper()
		if err := store.Observe(ctx, dim, "", Outcome{RequestID: id, StartedAt: time.Now(), Reason: domain.FailureFirstTokenTimeout}); err != nil {
			t.Fatal(err)
		}
	}
	// Two slow-fail requests weigh as four failures and open the gate.
	observe(s, d, "a")
	observe(s, d, "b")
	if r := row(t, s, d.Scope()); !r.Open || r.Failures != 4 {
		t.Fatalf("FTT weight ignored: %+v", r)
	}
	// With weighting off, the same traffic keeps the gate closed.
	s2, d2 := fixture(t)
	observe(s2, d2, "a")
	observe(s2, d2, "b")
	if r := row(t, s2, d2.Scope()); r.Open || r.Failures != 2 {
		t.Fatalf("unweighted gate opened: %+v", r)
	}
}

func TestTrafficScaledThresholdToleratesBursts(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	s.Settings.CircuitRateFactor = 0.15
	observe := func(id string, success bool) {
		t.Helper()
		if err := s.Observe(ctx, d, "", Outcome{RequestID: id, StartedAt: time.Now(), Success: success, Reason: "request_scope_failure"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 30; i++ {
		observe(fmt.Sprintf("ok-%d", i), true)
	}
	// 34 attempts scale the threshold to ceil(34*0.15)=6, so a four-failure
	// blip (13% of traffic) no longer trips the fixed threshold of 3.
	for i := 0; i < 4; i++ {
		observe(fmt.Sprintf("bad-%d", i), false)
	}
	if r := row(t, s, d.Scope()); r.Open {
		t.Fatalf("burst tripped the scaled threshold: %+v", r)
	}
	observe("bad-4", false)
	observe("bad-5", false)
	if r := row(t, s, d.Scope()); !r.Open || r.Failures != 6 {
		t.Fatalf("scaled threshold never opened: %+v", r)
	}
}

func TestSustainedFailureRuleCatchesColdDimension(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	// Eight attempts with five failures (62% failure rate) open the gate even
	// though no burst window reached the failure threshold.
	for i := 0; i < 3; i++ {
		if err := s.Observe(ctx, d, "", Outcome{RequestID: fmt.Sprintf("ok-%d", i), StartedAt: time.Now(), Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := s.Observe(ctx, d, "", Outcome{RequestID: fmt.Sprintf("bad-%d", i), StartedAt: time.Now(), Reason: "request_scope_failure"}); err != nil {
			t.Fatal(err)
		}
	}
	if r := row(t, s, d.Scope()); !r.Open {
		t.Fatalf("sustained failure not detected: %+v", r)
	}
}

func TestFinishCheckBackoffEscalation(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	expiredGate(t, s, d)
	for _, want := range []time.Duration{time.Second, 30 * time.Second, 60 * time.Second, 90 * time.Second, 90 * time.Second} {
		if err := s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", d.Scope()).Update("next_check_at", time.Now().Add(-time.Second)).Error; err != nil {
			t.Fatal(err)
		}
		token, err := s.ClaimCheck(ctx, d.Scope(), d)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishCheck(ctx, d.Scope(), token, false, false, "probe timeout", 0); err != nil {
			t.Fatal(err)
		}
		r := row(t, s, d.Scope())
		if r.CheckOK {
			t.Fatal("failed check recorded as ok")
		}
		delay := time.Until(*r.NextCheckAt)
		if delay < want-time.Second || delay > want+time.Second {
			t.Fatalf("check backoff %v, want %v", delay, want)
		}
	}
}

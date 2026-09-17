package routinghealth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func expiredGate(t *testing.T, s Store, d Dimension) {
	t.Helper()
	r := domain.RoutingCircuit{Scope: d.Scope(), PlatformKeyID: d.KeyID, Open: true, Until: time.Now().Add(-time.Minute), Protocol: d.Protocol, Model: d.Model, Path: d.Path, Stream: d.Stream}
	if err := s.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticCheckDoesNotCloseGate(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	expiredGate(t, s, d)
	token, err := s.ClaimCheck(ctx, d.Scope(), d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(ctx, d, true); err != ErrUnavailable {
		t.Fatalf("overlapping validation: %v", err)
	}
	if err := s.FinishCheck(ctx, d.Scope(), token, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	r := row(t, s, d.Scope())
	if !r.Open || !r.CheckOK || r.CheckLease != "" {
		t.Fatalf("check changed business health: %+v", r)
	}
	lease, err := s.Admit(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(ctx, d, lease, Outcome{RequestID: "real", StartedAt: time.Now(), Success: true}); err != nil {
		t.Fatal(err)
	}
	if row(t, s, d.Scope()).Open {
		t.Fatal("real success did not close gate")
	}
}

func TestRecoveryBudgetConcurrentAndPreview(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		ok, err := s.RecoverySlot(ctx, "pool", false)
		if err != nil || !ok {
			t.Fatalf("preview: %v %v", ok, err)
		}
	}
	var granted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.RecoverySlot(ctx, "pool", true)
			if err != nil {
				t.Error(err)
			}
			if ok {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 1 {
		t.Fatalf("granted %d", granted.Load())
	}
}

func TestAdmitRespectsFailedCheckBackoff(t *testing.T) {
	for _, keyWide := range []bool{false, true} {
		name := "model"
		if keyWide {
			name = "key"
		}
		t.Run(name, func(t *testing.T) {
			s, d := fixture(t)
			ctx := context.Background()
			expiredGate(t, s, d)
			scope := d.Scope()
			if keyWide {
				scope = KeyScope(d.KeyID)
				if err := s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", d.Scope()).Update("scope", scope).Error; err != nil {
					t.Fatal(err)
				}
			}
			token, err := s.ClaimCheck(ctx, scope, d)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.FinishCheck(ctx, scope, token, false, false, "rate limited", time.Minute); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Admit(ctx, d, true); err != ErrUnavailable {
				t.Fatalf("validation bypassed failed check backoff: %v", err)
			}
			if r := row(t, s, scope); r.Lease != "" || r.RecoveryAt != nil {
				t.Fatalf("rejected admission changed validation state: %+v", r)
			}
			if err := s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", scope).Update("next_check_at", time.Now().Add(-time.Second)).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := s.Admit(ctx, d, true); err != nil {
				t.Fatalf("validation blocked after backoff: %v", err)
			}
		})
	}
}

func TestCheckGlobalLimitAndRetryAfter(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	tokens := []string{}
	for i := uint(1); i <= 3; i++ {
		d.KeyID = i
		expiredGate(t, s, d)
		token, err := s.ClaimCheck(ctx, d.Scope(), d)
		if i <= 2 && err != nil {
			t.Fatal(err)
		}
		if i == 3 && err != ErrUnavailable {
			t.Fatalf("global cap: %v", err)
		}
		tokens = append(tokens, token)
	}
	d.KeyID = 1
	if err := s.FinishCheck(ctx, d.Scope(), tokens[0], false, false, "rate limited", 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	r := row(t, s, d.Scope())
	if !r.Open || r.CheckOK || r.NextCheckAt == nil || time.Until(*r.NextCheckAt) < 9*time.Minute {
		t.Fatalf("retry after: %+v", r)
	}
	if _, err := s.ClaimCheck(ctx, d.Scope(), d); err != ErrUnavailable {
		t.Fatalf("early retry: %v", err)
	}
	if err := s.FinishCheck(ctx, d.Scope(), tokens[0], true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	if row(t, s, d.Scope()).CheckOK {
		t.Fatal("stale completion accepted")
	}
}

func TestHistoricalDimensionResolution(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	if err := s.DB.AutoMigrate(&domain.RequestAttempt{}); err != nil {
		t.Fatal(err)
	}
	for i, model := range []string{"other", d.Model} {
		a := domain.RequestAttempt{ID: model, PlatformKeyID: d.KeyID, Protocol: d.Protocol, Model: model, Path: d.Path, Stream: d.Stream, CompletedAt: time.Now().Add(time.Duration(i) * time.Second)}
		if err := s.DB.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := domain.RoutingCircuit{Scope: d.Scope(), PlatformKeyID: d.KeyID}
	got, known, err := s.ResolveDimension(ctx, r)
	if err != nil || !known || got != d {
		t.Fatalf("dimension: %+v %v %v", got, known, err)
	}
	r.Scope = "unknown"
	_, known, err = s.ResolveDimension(ctx, r)
	if err != nil || known {
		t.Fatal("guessed an unknown dimension")
	}
}

func TestKeyRecoveryUsesFailedDimension(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	if _, err := s.Admit(ctx, d, false); err != nil {
		t.Fatal(err)
	}
	failed := d
	failed.Model = "failed-model"
	failed.Stream = !d.Stream
	if err := s.Observe(ctx, failed, "", Outcome{StartedAt: time.Now(), AuthFailure: true, Reason: "unauthorized"}); err != nil {
		t.Fatal(err)
	}
	got, known, err := s.ResolveDimension(ctx, row(t, s, KeyScope(d.KeyID)))
	if err != nil || !known || got != failed {
		t.Fatalf("key recovery dimension: %+v %v %v", got, known, err)
	}
}

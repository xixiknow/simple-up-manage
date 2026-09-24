package routinghealth

import (
	"context"
	"simple-up-manage/internal/domain"
	"testing"
	"time"
)

func TestOutputReleasePreservesFinalFailureAndNewerCircuits(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	expiredGate(t, s, d)
	a, err := s.AdmitRequest(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ReleaseOnOutput(ctx, d, a); err != nil || !changed {
		t.Fatalf("release: %v %v", changed, err)
	}
	if changed, err := s.ReleaseOnOutput(ctx, d, a); err != nil || changed {
		t.Fatalf("duplicate release: %v %v", changed, err)
	}
	b, err := s.AdmitRequest(ctx, d, false)
	if err != nil {
		t.Fatal("first stream blocked second request", err)
	}
	// A later successful request must not make the earlier stream's failure stale.
	if err := s.Observe(ctx, d, b.Token, Outcome{Admission: b, RequestID: "second", Success: true}); err != nil {
		t.Fatal(err)
	}
	failure := Outcome{Admission: a, RequestID: "first", Reason: "response_failure"}
	for i := 0; i < 2; i++ {
		if err := s.Observe(ctx, d, a.Token, failure); err != nil {
			t.Fatal(err)
		}
	}
	if r := row(t, s, d.Scope()); r.Open || r.Failures != 1 {
		t.Fatalf("lost/double final failure: %+v", r)
	}
	var count int64
	s.DB.Model(&domain.RoutingObservation{}).Count(&count)
	if count != 2 {
		t.Fatalf("progress recorded sample: %d", count)
	}
	// A newly opened circuit invalidates all older admissions, including progress.
	current, err := s.AdmitRequest(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(ctx, d, current.Token, Outcome{Admission: current, RequestID: "limited", Limited: true, Reason: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	generation := row(t, s, d.Scope()).Generation
	if err := s.Observe(ctx, d, b.Token, Outcome{Admission: b, RequestID: "late", Success: true}); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ReleaseOnOutput(ctx, d, a); err != nil || changed {
		t.Fatalf("stale release: %v %v", changed, err)
	}
	if r := row(t, s, d.Scope()); !r.Open || r.Generation != generation {
		t.Fatalf("new circuit overwritten: %+v", r)
	}
}

func TestRecoveryProgressKeyAndRequestGates(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	expiredGate(t, s, d)
	if err := s.DB.Create(&domain.RoutingCircuit{Scope: KeyScope(d.KeyID), PlatformKeyID: d.KeyID, Open: true, Until: time.Now().Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	a, err := s.AdmitRequest(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ReleaseOnOutput(ctx, d, a); err != nil || !changed {
		t.Fatalf("release %v %v", changed, err)
	}
	for _, scope := range []string{KeyScope(d.KeyID), d.Scope()} {
		if r := row(t, s, scope); r.Open || r.Generation != 1 {
			t.Fatalf("gate not released: %+v", r)
		}
	}
	if err := s.Observe(ctx, d, a.Token, Outcome{Admission: a, RequestID: "cancel", Neutral: true}); err != nil {
		t.Fatal(err)
	}
	if r := row(t, s, d.Scope()); r.Open || r.Failures != 0 {
		t.Fatal(r)
	}
}

func TestExpiredProgressOwnerAndStaleCheck(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	expiredGate(t, s, d)
	a, err := s.AdmitRequest(ctx, d, true)
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", d.Scope()).Update("lease_until", time.Now().Add(-time.Second))
	if changed, err := s.ReleaseOnOutput(ctx, d, a); err != nil || changed {
		t.Fatalf("expired progress: %v %v", changed, err)
	}
	token, err := s.ClaimCheck(ctx, d.Scope(), d)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh failure invalidates the check lease atomically.
	r := row(t, s, d.Scope())
	cfg := domain.DefaultSchedulerSettings()
	open(&r, time.Now(), "new_failure", 0, cfg)
	if err := s.DB.Save(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCheck(ctx, d.Scope(), token, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	if r := row(t, s, d.Scope()); !r.Open || r.Reason != "new_failure" {
		t.Fatal(r)
	}
}

func TestSyntheticCheckDoesNotClearOtherScopesOrAcceptExpiredLease(t *testing.T) {
	s, d := fixture(t)
	ctx := context.Background()
	expiredGate(t, s, d)
	other := d
	other.Model = "other"
	expiredGate(t, s, other)
	token, err := s.ClaimCheck(ctx, d.Scope(), d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCheck(ctx, d.Scope(), token, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	if row(t, s, d.Scope()).Open || !row(t, s, other.Scope()).Open {
		t.Fatal("check closed wrong dimensions")
	}
	token, err = s.ClaimCheck(ctx, other.Scope(), other)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", other.Scope()).Update("check_until", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.FinishCheck(ctx, other.Scope(), token, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	if !row(t, s, other.Scope()).Open {
		t.Fatal("expired check closed gate")
	}
}

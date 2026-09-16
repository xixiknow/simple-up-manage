package picker

import "testing"

func TestAttemptReservationRefundsOnlyUnsentRPM(t *testing.T) {
	p, up, keys := testBandPicker(t)
	key := keys[0]
	key.RPMLimit, key.MaxConcurrency, up.Concurrency = 2, 0, 0
	first, scope := p.TryAcquireAttempt(&key, &up)
	if first == nil {
		t.Fatalf("first: %s", scope)
	}
	second, scope := p.TryAcquireAttempt(&key, &up)
	if second == nil {
		t.Fatalf("second: %s", scope)
	}
	if third, _ := p.TryAcquireAttempt(&key, &up); third != nil {
		t.Fatal("RPM reservation not enforced")
	}
	first(false)
	first(false) // completion is idempotent, even with another active request.
	if rpm, active, _ := p.resourceSnapshot(key.ID, up.ID); rpm != 1 || active != 1 {
		t.Fatalf("refund: rpm=%d active=%d", rpm, active)
	}
	third, scope := p.TryAcquireAttempt(&key, &up)
	if third == nil {
		t.Fatalf("refunded slot not available: %s", scope)
	}
	second(true)
	third(false)
	if rpm, active, providers := p.resourceSnapshot(key.ID, up.ID); rpm != 1 || active != 0 || providers != 0 {
		t.Fatalf("sent RPM lost: rpm=%d active=%d providers=%d", rpm, active, providers)
	}
}

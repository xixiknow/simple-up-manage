package ops

import (
	"math"
	"simple-up-manage/internal/domain"
	"testing"
)

func TestRateRangeHalfOpen(t *testing.T) {
	lo, hi := 0.05, 0.1
	g := &domain.RouteGroup{RateMin: &lo, RateMax: &hi}
	for _, tc := range []struct {
		rate float64
		want bool
	}{
		{math.Nextafter(lo, 0), false}, {lo, true}, {0.075, true},
		{math.Nextafter(hi, 0), true}, {hi, false}, {math.Nextafter(hi, 1), false},
	} {
		if got := RateInRange(g, tc.rate); got != tc.want {
			t.Fatalf("rate %.17g: got %v want %v", tc.rate, got, tc.want)
		}
	}
	g.RateMin = nil
	if !RateInRange(g, 0) || RateInRange(g, hi) {
		t.Fatal("upper-only range")
	}
	g.RateMin, g.RateMax = &lo, nil
	if !RateInRange(g, lo) || !RateInRange(g, hi) {
		t.Fatal("lower-only range")
	}
	g.RateMax = &lo
	if RateInRange(g, lo) {
		t.Fatal("equal bounds must be empty")
	}
	adjacent := &domain.RouteGroup{RateMin: &hi}
	g.RateMin, g.RateMax = &lo, &hi
	if RateInRange(g, hi) || !RateInRange(adjacent, hi) {
		t.Fatal("adjacent ranges overlap or leave gap")
	}
}

package metrics

import (
	"context"
	"testing"
	"time"
)

func TestSnapshotComputesRates(t *testing.T) {
	s := NewStore(nil, 15*time.Minute, 50)
	ctx := context.Background()
	now := time.Now()
	s.Observe(ctx, Observation{KeyID: 1, Model: "m", Success: true, InputTokens: 60, CacheReadTokens: 40, TTFTMs: 200, At: now})
	s.Observe(ctx, Observation{KeyID: 1, Model: "m", Success: false, InputTokens: 100, TTFTMs: 800, At: now})
	w := s.Snapshot(ctx, 1, "m")
	if w.Samples != 2 {
		t.Fatalf("samples=%d", w.Samples)
	}
	if w.SuccessRate != 0.5 {
		t.Fatalf("success=%v", w.SuccessRate)
	}
	if w.CacheRate < 0.19 || w.CacheRate > 0.21 {
		t.Fatalf("cache=%v", w.CacheRate)
	}
}

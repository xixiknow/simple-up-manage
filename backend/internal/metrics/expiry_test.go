package metrics

import (
	"context"
	"testing"
	"time"
)

func TestExpiredSamplesNeverRevive(t *testing.T) {
	s := NewStore(nil, time.Minute, 50)
	s.Observe(context.Background(), Observation{KeyID: 1, Model: "m", Success: true, TTFTMs: 1, At: time.Now().Add(-2 * time.Minute)})
	if w := s.Snapshot(context.Background(), 1, "m"); w.Samples != 0 || w.HasLatency {
		t.Fatalf("expired samples revived: %+v", w)
	}
}

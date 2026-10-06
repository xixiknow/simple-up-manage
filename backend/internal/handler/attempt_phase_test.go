package handler

import "testing"

// Late transport trace callbacks (they run on the pool's read/write
// goroutines) must not roll the attempt phase backwards once the flow has
// moved on — regression masked first-token-timeout diagnostics on loaded
// runners.
func TestAttemptPhaseSetNeverRegresses(t *testing.T) {
	p := &attemptPhase{phase: "local"}
	for _, step := range []string{"connecting", "awaiting_headers", "awaiting_first_output", "compacting"} {
		p.set(step)
	}
	for _, late := range []string{"dns", "sending_request", "awaiting_headers", "streaming"} {
		p.set(late)
		if got := p.get(); got != "compacting" {
			t.Fatalf("late %q regressed phase to %q", late, got)
		}
	}
	p.set("streaming")
	if got := p.get(); got != "compacting" {
		t.Fatalf("streaming after compacting regressed phase to %q", got)
	}
}

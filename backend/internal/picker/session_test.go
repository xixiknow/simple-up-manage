package picker

import "testing"

func TestSessionFromHeaderWins(t *testing.T) {
	got := SessionFromRequest(" sess-1 ", []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if got != "sess-1" {
		t.Fatalf("got %q", got)
	}
}

func TestHashSessionStable(t *testing.T) {
	body := []byte(`{"system":"s","messages":[{"role":"user","content":"hello"}]}`)
	a := HashSession(body)
	b := HashSession(body)
	if a == "" || a != b {
		t.Fatalf("unstable hash %q %q", a, b)
	}
}

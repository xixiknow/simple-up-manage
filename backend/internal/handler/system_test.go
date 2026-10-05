package handler

import "testing"

func TestUpdateTargetRe(t *testing.T) {
	valid := []string{"latest", "previous", "abc123", "abc123def4567890abcdef1234567890abcdef12", "1.0.0", "v1.2.3", "v10.20.30"}
	for _, target := range valid {
		if !updateTargetRe.MatchString(target) {
			t.Errorf("%q should be accepted", target)
		}
	}
	invalid := []string{"", "v1", "1.2", "1.2.3.4", "latest ", "../escape", "1.2.3-x", "zzzz"}
	for _, target := range invalid {
		if updateTargetRe.MatchString(target) {
			t.Errorf("%q should be rejected", target)
		}
	}
}

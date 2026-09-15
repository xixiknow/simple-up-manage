package upstream

import (
	"strings"
	"testing"
)

func TestProbeProtocolValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path, ct, body string
		stream, ok           bool
	}{
		{"greeting", "/v1/responses", "text/plain", "Hi!", false, false},
		{"greeting fake json", "/v1/responses", "application/json", "Hi!", false, false},
		{"error json", "/v1/responses", "application/json", `{"error":{"code":"bad"}}`, false, false},
		{"tool json", "/v1/chat/completions", "application/json", `{"choices":[{"message":{"tool_calls":[]},"finish_reason":"tool_calls"}]}`, false, true},
		{"empty completed", "/v1/responses", "application/json", `{"id":"r","object":"response","status":"completed","output":[]}`, false, true},
		{"wrong content type", "/v1/responses", "application/json", `{"id":"r"}`, true, false},
		{"wrong terminal", "/v1/responses", "text/event-stream", "data: [DONE]\n\n", true, false},
		{"early eof", "/v1/responses", "text/event-stream", "data: {\"type\":\"response.created\"}\n\n", true, false},
		{"stream failure", "/v1/responses", "text/event-stream", "data: {\"type\":\"response.failed\"}\n\n", true, false},
		{"chat refusal", "/v1/chat/completions", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"refusal\":\"No\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProbeResponse(tc.path, tc.ct, tc.stream, strings.NewReader(tc.body))
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

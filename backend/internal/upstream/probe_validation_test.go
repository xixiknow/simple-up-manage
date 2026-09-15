package upstream

import (
	"strings"
	"testing"
	"testing/iotest"
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

func TestProbeResponsesMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"before output", testResponsesRateLimits + testResponsesDelta + testResponsesCompleted, true},
		{"consecutive metadata", testResponsesRateLimits + testResponsesMetadata + testResponsesDelta + testResponsesMetadata + testResponsesCompleted, true},
		{"full metadata sequence", testResponsesRateLimits + testResponsesMetadata + testResponsesDelta + testResponsesTiming + testResponsesCompleted, true},
		{"timing without completion", testResponsesDelta + testResponsesTiming, false},
		{"timing only", testResponsesTiming, false},
		{"timing with error", "data: {\"type\":\"responsesapi.websocket_timing\",\"error\":{\"message\":\"failed\"}}\n\n" + testResponsesCompleted, false},
		{"response metadata then eof", testResponsesMetadata, false},
		{"response metadata then failure", testResponsesMetadata + "data: {\"type\":\"response.failed\"}\n\n", false},
		{"response metadata event name", "event: codex.response.metadata\ndata: {\"headers\":{}}\n\n" + testResponsesCompleted, true},
		{"between output", testResponsesDelta + testResponsesRateLimits + testResponsesDelta + testResponsesCompleted, true},
		{"repeated metadata", testResponsesRateLimits + testResponsesRateLimits + testResponsesCompleted, true},
		{"event name only", "event: codex.rate_limits\ndata: {\"rate_limits\":{\"allowed\":true}}\n\n" + testResponsesCompleted, true},
		{"metadata then eof", testResponsesRateLimits, false},
		{"metadata then failure", testResponsesRateLimits + "data: {\"type\":\"response.failed\"}\n\n", false},
		{"metadata then error", testResponsesRateLimits + "data: {\"type\":\"error\",\"message\":\"failed\"}\n\n", false},
		{"metadata with error", "data: {\"type\":\"codex.rate_limits\",\"error\":{\"message\":\"failed\"}}\n\n" + testResponsesCompleted, false},
		{"metadata then wrong terminal", testResponsesRateLimits + "data: [DONE]\n\n", false},
		{"metadata then malformed completion", testResponsesRateLimits + "data: {\"type\":\"response.completed\"}\n\n", false},
		{"unknown extension", "data: {\"type\":\"codex.unknown\"}\n\n" + testResponsesCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProbeResponse("/v1/responses", "text/event-stream", true, iotest.OneByteReader(strings.NewReader(tc.body)))
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

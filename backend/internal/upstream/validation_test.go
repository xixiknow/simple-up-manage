package upstream

import (
	"strings"
	"testing"
)

const testResponsesRateLimits = "data: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"allowed\":true,\"limit_reached\":false}}\n\n"
const testResponsesMetadata = "data: {\"type\":\"codex.response.metadata\",\"headers\":{\"x-models-etag\":\"test\",\"x-codex-turn-state\":\"opaque\"}}\n\n"
const testResponsesTiming = "data: {\"type\":\"responsesapi.websocket_timing\",\"timing_metrics\":{\"response_id\":\"r\",\"first_sampled_message_ttft_ms\":1,\"total_turn_time_s\":2}}\n\n"
const testResponsesDelta = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"
const testResponsesCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"

func TestResponsesMetadataValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path, event, data string
		ok                      bool
	}{
		{"data type", "/v1/responses", "", `{"type":"codex.rate_limits","rate_limits":{"allowed":true}}`, true},
		{"event name", "/v1/responses", "codex.rate_limits", `{"rate_limits":{"allowed":true}}`, true},
		{"type wins", "/v1/responses", "response.completed", `{"type":"codex.rate_limits"}`, true},
		{"limited metadata", "/v1/responses", "", `{"type":"codex.rate_limits","rate_limits":{"allowed":false,"limit_reached":true}}`, true},
		{"not a response", "/v1/responses", "", `{"type":"codex.rate_limits","response":{"id":"other","object":"response","status":"completed","output":[]}}`, true},
		{"null error", "/v1/responses", "", `{"type":"codex.rate_limits","error":null}`, true},
		{"explicit error", "/v1/responses", "", `{"type":"codex.rate_limits","error":{"message":"failed"}}`, false},
		{"malformed", "/v1/responses", "codex.rate_limits", `{`, false},
		{"non object", "/v1/responses", "codex.rate_limits", `[]`, false},
		{"unknown type wins", "/v1/responses", "codex.rate_limits", `{"type":"codex.unknown"}`, false},
		{"missing type", "/v1/responses", "", `{}`, false},
		{"chat remains strict", "/v1/chat/completions", "", `{"type":"codex.rate_limits"}`, false},
		{"messages remains strict", "/v1/messages", "", `{"type":"codex.rate_limits"}`, false},
		{"response metadata", "/v1/responses", "", `{"type":"codex.response.metadata","headers":{"x-models-etag":"test"}}`, true},
		{"response metadata event name", "/v1/responses", "codex.response.metadata", `{"headers":{}}`, true},
		{"response metadata with error", "/v1/responses", "", `{"type":"codex.response.metadata","error":{"message":"failed"}}`, false},
		{"response metadata not completion", "/v1/responses", "response.completed", `{"type":"codex.response.metadata","response":{"id":"other"}}`, true},
		{"response metadata chat", "/v1/chat/completions", "", `{"type":"codex.response.metadata","headers":{}}`, false},
		{"timing metadata", "/v1/responses", "", `{"type":"responsesapi.websocket_timing","timing_metrics":{"first_sampled_message_ttft_ms":1}}`, true},
		{"timing event name", "/v1/responses", "responsesapi.websocket_timing", `{"timing_metrics":{}}`, true},
		{"timing with error", "/v1/responses", "", `{"type":"responsesapi.websocket_timing","error":{"message":"failed"}}`, false},
		{"timing chat", "/v1/chat/completions", "", `{"type":"responsesapi.websocket_timing","timing_metrics":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := StreamValidator{Path: tc.path, Strict: true, ResponseID: "original"}
			v.Event(tc.event, tc.data)
			if (v.Err == nil) != tc.ok {
				t.Fatalf("error=%v", v.Err)
			}
			if v.SawValid || v.ChatFinished || v.ResponseID != "original" {
				t.Fatalf("metadata changed response state: %+v", v)
			}
		})
	}
}

func TestUnexpectedResponsesEventDiagnostic(t *testing.T) {
	v := StreamValidator{Path: "/v1/responses", Strict: true}
	v.Event("unknown\n\""+strings.Repeat("x", 1024), `{}`)
	if v.Err == nil {
		t.Fatal("unknown event accepted")
	}
	msg := v.Err.Error()
	if !strings.HasPrefix(msg, `unexpected upstream Responses event: "unknown\n\"`) || strings.Contains(msg, "\n") || len(msg) > 200 {
		t.Fatalf("unbounded or unescaped diagnostic: %q", msg)
	}
}

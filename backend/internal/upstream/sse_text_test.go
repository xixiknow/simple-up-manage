package upstream

import "testing"

func TestSSELineHasText(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`event: ping`, false},
		{`data: [DONE]`, false},
		{`data: {"choices":[{"delta":{"role":"assistant"}}]}`, false},
		{`data: {"choices":[{"delta":{"content":""}}]}`, false},
		{`data: {"choices":[{"delta":{"content":"Hi"}}]}`, true},
		{`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`, true},
		{`data: {"type":"message_start","message":{"role":"assistant"}}`, false},
		{`data: {"type":"response.output_text.delta","delta":"A"}`, true},
		{`data: {"usage":{"output_tokens":3}}`, false},
	}
	for _, tc := range cases {
		if got := SSELineHasText(tc.line); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.line, got, tc.want)
		}
	}
}

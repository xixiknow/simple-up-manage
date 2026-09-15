package upstream

import "testing"

func TestResponsesCompletedContent(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"type":"response.function_call_arguments.done","arguments":"{}"}`, true},
		{`{"type":"response.function_call_arguments.done","arguments":""}`, false},
		{`{"type":"response.custom_tool_call_input.delta","delta":"command"}`, true},
		{`{"type":"response.custom_tool_call_input.done","input":"command"}`, true},
		{`{"type":"response.output_text.done","text":"hello"}`, true},
		{`{"type":"response.reasoning_summary_text.done","text":"reason"}`, true},
		{`{"type":"response.refusal.done","refusal":"no"}`, true},
		{`{"type":"response.content_part.done","part":{"type":"output_text","text":"hello"}}`, true},
		{`{"type":"response.output_item.done","item":{"type":"function_call","arguments":"{}"}}`, true},
		{`{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}`, true},
		{`{"type":"response.output_item.done","item":{"type":"message","role":"user","content":[{"type":"output_text","text":"echo"}]}}`, false},
		{`{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"opaque","summary":[]}}`, false},
		{`{"type":"response.created","response":{"output":[{"type":"function_call","arguments":"echo"}]}}`, false},
		{`{"type":"response.created","delta":{"content":"echo"}}`, false},
		{`{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","arguments":"{}"}]}}`, true},
		{`{"type":"response.completed","response":{"status":"completed","output":[]}}`, false},
		{`{"type":"response.completed","response":{"status":"failed","output":[{"type":"function_call","arguments":"{}"}]}}`, false},
		{`{"type":"response.function_call_arguments.done","arguments":"{}","error":{"message":"bad"}}`, false},
	} {
		if got := JSONHasGeneratedText([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: got %v", tc.body, got)
		}
	}
	if !SSEEventHasText("response.function_call_arguments.done", []byte(`{"arguments":"{}"}`)) {
		t.Fatal("event name was ignored")
	}
}

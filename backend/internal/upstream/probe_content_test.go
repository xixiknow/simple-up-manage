package upstream

import "testing"

func TestExtractReplyText(t *testing.T) {
	cases := []struct {
		name string
		path string
		raw  string
		want string
	}{
		{"openai chat", "/v1/chat/completions", `{"choices":[{"message":{"role":"assistant","content":"你好，我是助手。"}}]}`, "你好，我是助手。"},
		{"openai chat parts", "/v1/chat/completions", `{"choices":[{"message":{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}]}`, "ab"},
		{"openai legacy text", "/v1/chat/completions", `{"choices":[{"text":"legacy"}]}`, "legacy"},
		{"anthropic", "/v1/messages", `{"content":[{"type":"text","text":"第一段"},{"type":"tool_use","id":"x"},{"type":"text","text":"第二段"}]}`, "第一段第二段"},
		{"responses output_text", "/v1/responses", `{"output_text":"done"}`, "done"},
		{"responses output items", "/v1/responses", `{"output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`, "answer"},
		{"unknown path fallback", "/v1/other", `{"choices":[{"message":{"content":"fallback"}}]}`, "fallback"},
		{"empty content", "/v1/chat/completions", `{"choices":[{"message":{"content":""}}]}`, ""},
		{"not json", "/v1/chat/completions", `<html>err</html>`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractReplyText(tc.path, []byte(tc.raw)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestExtractStreamReplyText(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start"}` + "\n\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"今天"}}` + "\n\n" +
		"event: ping\n" +
		`data: {"type":"ping"}` + "\n\n" +
		`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hidden"}}` + "\n\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"天气不错"}}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	if got := ExtractStreamReplyText([]byte(sse)); got != "今天天气不错" {
		t.Fatalf("anthropic stream: got %q", got)
	}

	chat := `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"Hel"}}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"lo"}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	if got := ExtractStreamReplyText([]byte(chat)); got != "Hello" {
		t.Fatalf("openai stream: got %q", got)
	}

	responses := "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"part"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n\n"
	if got := ExtractStreamReplyText([]byte(responses)); got != "part" {
		t.Fatalf("responses stream: got %q", got)
	}
}

func TestExtractErrorMessage(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"openai", `{"error":{"message":"Incorrect API key","type":"auth"}}`, "Incorrect API key"},
		{"anthropic", `{"type":"error","error":{"type":"rate_limit","message":"慢了"}}`, "慢了"},
		{"gateway message", `{"message":"quota exceeded"}`, "quota exceeded"},
		{"error string", `{"error":"bad key"}`, "bad key"},
		{"not json", `upstream timeout`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractErrorMessage([]byte(tc.raw)); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

package externalprobe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const rpPrompt = "Compute exactly, using standard math precedence (multiplication before add and subtract): 4 * 8 * 4. Reply with ONLY RP_ANSWER=N where N is the integer result (it may be negative). No other text."
const examplePrompt = "Calculate and respond with ONLY the number, nothing else.\n\nQ: 3 + 5 = ?\nA: 8\n\nQ: 12 - 7 = ?\nA: 5\n\nQ: 40 + 28 = ?\nA:"
const toolPrompt = "Call the probe_ping function with ok=true to acknowledge readiness. You must use the tool."

func payload(path, prompt string, blocks bool) map[string]any {
	content := any(prompt)
	blockType := "text"
	if path == "/v1/responses" {
		blockType = "input_text"
	}
	if blocks {
		content = []any{map[string]any{"type": blockType, "text": prompt}}
	}
	messages := []any{map[string]any{"role": "user", "content": content}}
	r := map[string]any{"model": "fixture", "tools": []any{}}
	switch path {
	case "/v1/responses":
		r["input"], r["instructions"], r["max_output_tokens"] = messages, "Reply exactly: ok.", 16
	case "/v1/chat/completions":
		r["messages"] = append([]any{map[string]any{"role": "system", "content": "Reply exactly: ok."}}, messages...)
		r["max_completion_tokens"] = 16
	case "/v1/messages":
		r["messages"], r["system"], r["max_tokens"] = messages, []any{map[string]any{"type": "text", "text": "Reply exactly: ok."}}, 16
	}
	return r
}

func TestClassifyTemplatesAndProtocols(t *testing.T) {
	h := http.Header{"User-Agent": {"Mozilla/5.0 gpt-health-manager/20260719"}}
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, tc := range []struct{ text, want string }{
			{rpPrompt, RPArithmetic},
			{strings.Replace(rpPrompt, "4 * 8 * 4", "-12 + 3 - 5", 1), RPArithmetic},
			{examplePrompt, ExampleArithmetic},
			{strings.Replace(examplePrompt, "40 + 28", "20 - 6", 1), ExampleArithmetic},
			{"Reply exactly: ok", HealthManager},
		} {
			for _, blocks := range []bool{false, true} {
				raw, _ := json.Marshal(payload(path, tc.text, blocks))
				if got := Classify(path, h, raw); got != tc.want {
					t.Errorf("path=%s blocks=%v want=%s got=%s", path, blocks, tc.want, got)
				}
			}
		}
	}
	r := payload("/v1/responses", rpPrompt, false)
	r["input"] = rpPrompt
	raw, _ := json.Marshal(r)
	if Classify("/v1/responses", nil, raw) != RPArithmetic {
		t.Fatal("Responses string input not recognized")
	}
}

func TestClassifyDoesNotMatchOrdinaryRequests(t *testing.T) {
	h := http.Header{"User-Agent": {"Mozilla/5.0 gpt-health-manager/20260719"}}
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, kind := range []string{"tools", "history", "quote"} {
			r := payload(path, rpPrompt, true)
			field := "messages"
			if path == "/v1/responses" {
				field = "input"
			}
			switch kind {
			case "tools":
				r["tools"] = []any{map[string]any{"name": "probe_ping", "type": "function"}}
			case "history":
				r[field] = append(r[field].([]any), map[string]any{"role": "assistant", "content": "OK"})
			case "quote":
				r = payload(path, "Please explain: "+rpPrompt, false)
			}
			raw, _ := json.Marshal(r)
			if got := Classify(path, h, raw); got != "" {
				t.Errorf("%s/%s matched %s", path, kind, got)
			}
		}
	}
	for _, prompt := range []string{"hi", "who are you", "Reply exactly: ok.", toolPrompt, "Explain this template: " + rpPrompt, rpPrompt + " Please explain.", "Compute RP_ANSWER=N"} {
		raw, _ := json.Marshal(payload("/v1/responses", prompt, true))
		if got := Classify("/v1/responses", h, raw); got != "" {
			t.Errorf("ordinary input matched %s", got)
		}
	}
	mutations := map[string]func(map[string]any){
		"tool":            func(r map[string]any) { r["tools"] = []any{map[string]any{"type": "function", "name": "probe_ping"}} },
		"legacy function": func(r map[string]any) { r["functions"] = []any{map[string]any{"name": "weather"}} },
		"previous":        func(r map[string]any) { r["previous_response_id"] = "resp-fixture" },
		"conversation":    func(r map[string]any) { r["conversation"] = map[string]any{"id": "fixture"} },
		"history": func(r map[string]any) {
			r["input"] = append(r["input"].([]any), map[string]any{"role": "assistant", "content": "reply"})
		},
		"assistant":   func(r map[string]any) { r["input"].([]any)[0].(map[string]any)["role"] = "assistant" },
		"tool result": func(r map[string]any) { r["input"].([]any)[0].(map[string]any)["type"] = "function_call_output" },
		"tool call": func(r map[string]any) {
			r["input"].([]any)[0].(map[string]any)["tool_calls"] = []any{map[string]any{"id": "call_fixture"}}
		},
		"image": func(r map[string]any) {
			m := r["input"].([]any)[0].(map[string]any)
			m["content"] = append(m["content"].([]any), map[string]any{"type": "input_image", "image_url": "fixture"})
		},
		"wrong block": func(r map[string]any) {
			r["input"].([]any)[0].(map[string]any)["content"] = []any{map[string]any{"type": "text", "text": rpPrompt}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r := payload("/v1/responses", rpPrompt, true)
			mutate(r)
			raw, _ := json.Marshal(r)
			if got := Classify("/v1/responses", h, raw); got != "" {
				t.Fatalf("matched %s", got)
			}
		})
	}
	r := payload("/v1/responses", "Reply exactly: ok", true)
	raw, _ := json.Marshal(r)
	if Classify("/v1/responses", nil, raw) != "" {
		t.Fatal("health UA is required")
	}
	r["instructions"] = "You are helpful."
	raw, _ = json.Marshal(r)
	if Classify("/v1/responses", h, raw) != "" {
		t.Fatal("health system instruction is required")
	}
	r["instructions"], r["max_output_tokens"] = "Reply exactly: ok.", 128
	raw, _ = json.Marshal(r)
	if Classify("/v1/responses", h, raw) != "" {
		t.Fatal("health output limit is required")
	}
	for _, body := range []string{"{", "null", "[]", `{"input":123}`, `{"input":[]}`} {
		if Classify("/v1/responses", h, []byte(body)) != "" {
			t.Fatal("invalid request matched")
		}
	}
	raw, _ = json.Marshal(payload("/v1/responses", rpPrompt, true))
	if Classify("/v1/images/generations", h, raw) != "" {
		t.Fatal("image endpoint matched")
	}
}

// Optional read-only replay: production records and credentials never enter test
// output or repository fixtures. Truncated bodies are not guessed or repaired.
func TestProductionSamples(t *testing.T) {
	dir := os.Getenv("EXTERNAL_PROBE_SAMPLE_DIR")
	if dir == "" {
		t.Skip("EXTERNAL_PROBE_SAMPLE_DIR not set")
	}
	classified, err := os.ReadFile(filepath.Join(dir, "classified.json"))
	if err != nil {
		t.Fatal(err)
	}
	var labels []struct {
		ID       uint   `json:"id"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal(classified, &labels); err != nil {
		t.Fatal(err)
	}
	expected := map[uint]string{}
	categoryRules := map[string]string{"RP算术探针": RPArithmetic, "示例加减法探针": ExampleArithmetic, "health-manager探针": HealthManager}
	tools := map[uint]bool{}
	for _, r := range labels {
		if rule := categoryRules[r.Category]; rule != "" {
			expected[r.ID] = rule
		}
		if r.Category == "probe_ping工具探针" {
			tools[r.ID] = true
		}
	}
	f, err := os.Open(filepath.Join(dir, "requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 8<<20)
	counts := map[string]int{}
	total, toolN := 0, 0
	for scanner.Scan() {
		var r struct {
			ID      uint   `json:"id"`
			Path    string `json:"path"`
			Headers string `json:"request_headers"`
			Body    string `json:"body"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal("invalid sample JSON")
		}
		var headers map[string]string
		// Some archived requests have no usable header snapshot. They cannot
		// satisfy a header-dependent rule, but can still be checked structurally.
		_ = json.Unmarshal([]byte(r.Headers), &headers)
		h := http.Header{}
		for k, v := range headers {
			h.Set(k, v)
		}
		got := Classify(r.Path, h, []byte(r.Body))
		if got != expected[r.ID] {
			t.Errorf("log %d: got %q want %q", r.ID, got, expected[r.ID])
		}
		if got != "" {
			counts[got]++
		}
		if tools[r.ID] {
			toolN++
		}
		total++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{RPArithmetic: 979, ExampleArithmetic: 178, HealthManager: 15}
	if !reflect.DeepEqual(counts, want) || toolN != 5 || total != 15089 {
		t.Fatalf("counts=%v tool_samples=%d total=%d", counts, toolN, total)
	}
	t.Log(fmt.Sprintf("Read-only replay: %d requests; counts=%v; %d tool probes unclassified", total, counts, toolN))
}

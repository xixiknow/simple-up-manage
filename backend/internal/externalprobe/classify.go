// Package externalprobe recognizes a small set of complete diagnostic requests.
// Classification controls routing eligibility, not billing or request ownership.
package externalprobe

import (
	"bytes"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

const (
	RPArithmetic      = "rp_arithmetic"
	ExampleArithmetic = "example_arithmetic"
	HealthManager     = "health_manager"
)

var (
	rpTemplate      = regexp.MustCompile(`\ACompute exactly, using standard math precedence \(multiplication before add and subtract\): -?[0-9]+(?: [*+\-] -?[0-9]+)+\. Reply with ONLY RP_ANSWER=N where N is the integer result \(it may be negative\)\. No other text\.\z`)
	exampleTemplate = regexp.MustCompile(`\ACalculate and respond with ONLY the number, nothing else\.\n\nQ: 3 \+ 5 = \?\nA: 8\n\nQ: 12 - 7 = \?\nA: 5\n\nQ: -?[0-9]+ [+\-] -?[0-9]+ = \?\nA:\z`)
	healthAgent     = regexp.MustCompile(`(?:^|\s)gpt-health-manager/[0-9]{8}(?:\s|$)`)
)

func ValidRule(rule string) bool {
	return rule == RPArithmetic || rule == ExampleArithmetic || rule == HealthManager
}

type message struct {
	Role       string          `json:"role"`
	Type       string          `json:"type"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  json.RawMessage `json:"tool_calls"`
	ToolCallID json.RawMessage `json:"tool_call_id"`
}

type request struct {
	Input               json.RawMessage `json:"input"`
	Messages            []message       `json:"messages"`
	Instructions        json.RawMessage `json:"instructions"`
	System              json.RawMessage `json:"system"`
	Tools               json.RawMessage `json:"tools"`
	Functions           json.RawMessage `json:"functions"`
	Previous            json.RawMessage `json:"previous_response_id"`
	Conversation        json.RawMessage `json:"conversation"`
	MaxOutputTokens     int             `json:"max_output_tokens"`
	MaxCompletionTokens int             `json:"max_completion_tokens"`
	MaxTokens           int             `json:"max_tokens"`
}

// Classify only accepts an entire single-user text request. In particular, tool
// probes, quoted templates in conversations, and ordinary short greetings remain
// business traffic without a probe tag.
func Classify(path string, headers http.Header, body []byte) string {
	if path != "/v1/responses" && path != "/v1/chat/completions" && path != "/v1/messages" {
		return ""
	}
	var r request
	if json.Unmarshal(body, &r) != nil || !emptyArray(r.Tools) || !emptyArray(r.Functions) ||
		!absent(r.Previous) || !absent(r.Conversation) {
		return ""
	}
	var input, system string
	var ok bool
	var messages []message
	limit := r.MaxOutputTokens
	switch path {
	case "/v1/responses":
		if len(r.Messages) != 0 || !absent(r.System) {
			return ""
		}
		if !absent(r.Instructions) {
			if system, ok = textContent(r.Instructions, "input_text"); !ok {
				return ""
			}
		}
		// Responses also accepts a plain input string as one user message.
		if json.Unmarshal(r.Input, &input) == nil && !absent(r.Input) {
			break
		}
		if json.Unmarshal(r.Input, &messages) != nil {
			return ""
		}
	case "/v1/chat/completions":
		if !absent(r.Input) || !absent(r.Instructions) || !absent(r.System) {
			return ""
		}
		messages = r.Messages
		limit = r.MaxCompletionTokens
		if limit == 0 {
			limit = r.MaxTokens
		}
		if len(messages) == 2 && (messages[0].Role == "system" || messages[0].Role == "developer") {
			if system, ok = messageText(messages[0], "text"); !ok {
				return ""
			}
			messages = messages[1:]
		}
	case "/v1/messages":
		if !absent(r.Input) || !absent(r.Instructions) {
			return ""
		}
		messages = r.Messages
		limit = r.MaxTokens
		if !absent(r.System) {
			if system, ok = textContent(r.System, "text"); !ok {
				return ""
			}
		}
	}
	if messages != nil {
		if len(messages) != 1 || messages[0].Role != "user" {
			return ""
		}
		blockType := "text"
		if path == "/v1/responses" {
			blockType = "input_text"
		}
		if input, ok = messageText(messages[0], blockType); !ok {
			return ""
		}
	}
	if rpTemplate.MatchString(input) {
		return RPArithmetic
	}
	if exampleTemplate.MatchString(input) {
		return ExampleArithmetic
	}
	if input == "Reply exactly: ok" && system == "Reply exactly: ok." && limit == 16 && healthAgent.MatchString(headers.Get("User-Agent")) {
		return HealthManager
	}
	return ""
}

func messageText(m message, blockType string) (string, bool) {
	if (m.Type != "" && m.Type != "message") || !emptyArray(m.ToolCalls) || !absent(m.ToolCallID) {
		return "", false
	}
	return textContent(m.Content, blockType)
}

func textContent(raw json.RawMessage, blockType string) (string, bool) {
	if absent(raw) {
		return "", false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var blocks []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return "", false
	}
	var joined strings.Builder
	for _, b := range blocks {
		if b.Type != blockType || b.Text == nil {
			return "", false
		}
		joined.WriteString(*b.Text)
	}
	return joined.String(), true
}

func absent(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == `""`
}

func emptyArray(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) == 0
}

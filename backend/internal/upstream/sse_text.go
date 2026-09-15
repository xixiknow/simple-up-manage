package upstream

import (
	"encoding/json"
	"strings"
)

// SSELineHasText reports whether an SSE line carries generated text (first token).
// Role-only deltas, pings, and usage-only chunks do not count.
func SSELineHasText(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return false
	}
	return JSONHasGeneratedText([]byte(payload))
}

func JSONHasGeneratedText(raw []byte) bool {
	return SSEEventHasText("", raw)
}

// SSEEventHasText also handles streams that provide the type only in event:.
func SSEEventHasText(eventName string, raw []byte) bool {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return false
	}
	typ, _ := top["type"].(string)
	if typ == "" {
		typ = eventName
	}
	if top["error"] != nil {
		return false
	}
	if strings.HasPrefix(typ, "response.") {
		return responsesHasOutput(typ, top)
	}
	if d, ok := asMap(top["delta"]); ok && deltaHasText(d) {
		return true
	}
	if choices, ok := top["choices"].([]any); ok {
		for _, ch := range choices {
			m, ok := asMap(ch)
			if !ok {
				continue
			}
			if d, ok := asMap(m["delta"]); ok && deltaHasText(d) {
				return true
			}
			if nonEmptyStr(m["text"]) != "" {
				return true
			}
		}
	}
	if arr, ok := top["content"].([]any); ok {
		for _, item := range arr {
			m, ok := asMap(item)
			if !ok {
				continue
			}
			if nonEmptyStr(m["text"]) != "" {
				return true
			}
		}
	}
	return false
}

func responsesHasOutput(typ string, top map[string]any) bool {
	switch typ {
	case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta", "response.refusal.delta", "response.custom_tool_call_input.delta":
		return nonEmptyStr(top["delta"]) != ""
	case "response.output_text.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
		return nonEmptyStr(top["text"]) != ""
	case "response.function_call_arguments.done":
		return nonEmptyStr(top["arguments"]) != ""
	case "response.custom_tool_call_input.done":
		return nonEmptyStr(top["input"]) != ""
	case "response.refusal.done":
		return nonEmptyStr(top["refusal"]) != ""
	case "response.content_part.added", "response.content_part.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part, _ := asMap(top["part"])
		return responsePartHasOutput(part)
	case "response.output_item.added", "response.output_item.done":
		item, _ := asMap(top["item"])
		return responseItemHasOutput(item)
	case "response.completed":
		response, _ := asMap(top["response"])
		if response["status"] != "completed" {
			return false
		}
		items, _ := response["output"].([]any)
		for _, value := range items {
			item, _ := asMap(value)
			if responseItemHasOutput(item) {
				return true
			}
		}
	}
	return false
}

func responsePartHasOutput(part map[string]any) bool {
	switch part["type"] {
	case "output_text", "reasoning_text", "summary_text":
		return nonEmptyStr(part["text"]) != ""
	case "refusal":
		return nonEmptyStr(part["refusal"]) != ""
	}
	return false
}

func responseItemHasOutput(item map[string]any) bool {
	switch item["type"] {
	case "function_call":
		return nonEmptyStr(item["arguments"]) != ""
	case "custom_tool_call":
		return nonEmptyStr(item["input"]) != ""
	case "message":
		if item["role"] != "assistant" {
			return false
		}
	case "reasoning":
	default:
		return false
	}
	for _, field := range []string{"content", "summary"} {
		parts, _ := item[field].([]any)
		for _, value := range parts {
			part, _ := asMap(value)
			if responsePartHasOutput(part) {
				return true
			}
		}
	}
	return false
}

func deltaHasText(d map[string]any) bool {
	for _, k := range []string{"content", "text", "reasoning_content", "thinking", "reasoning", "partial_json", "refusal"} {
		if nonEmptyStr(d[k]) != "" {
			return true
		}
	}
	if tools, ok := d["tool_calls"].([]any); ok {
		for _, tool := range tools {
			if m, ok := asMap(tool); ok {
				if f, ok := asMap(m["function"]); ok && nonEmptyStr(f["arguments"]) != "" {
					return true
				}
			}
		}
	}
	if f, ok := asMap(d["function_call"]); ok && nonEmptyStr(f["arguments"]) != "" {
		return true
	}
	if parts, ok := d["content"].([]any); ok {
		for _, p := range parts {
			if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
				return true
			}
			if m, ok := asMap(p); ok && nonEmptyStr(m["text"]) != "" {
				return true
			}
		}
	}
	return false
}

func nonEmptyStr(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

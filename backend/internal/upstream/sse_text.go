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
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return false
	}
	if nonEmptyStr(top["delta"]) != "" {
		return true
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

func deltaHasText(d map[string]any) bool {
	for _, k := range []string{"content", "text", "reasoning_content", "thinking", "reasoning"} {
		if nonEmptyStr(d[k]) != "" {
			return true
		}
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

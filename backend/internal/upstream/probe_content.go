package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

// MaxProbeBodyBytes mirrors the validator's response size ceiling so buffered
// reads keep validation byte-identical to streaming reads.
const MaxProbeBodyBytes = 16 << 20

// maxProbeReplyBytes caps extracted reply/error text shown in the console.
const maxProbeReplyBytes = 8 << 10

// ExtractReplyText pulls the assistant-visible answer out of a buffered
// non-stream probe response. Path only picks the preferred format; unknown
// vendors fall through chat, responses and messages extraction in turn. It
// returns "" when nothing readable is found.
func ExtractReplyText(path string, raw []byte) string {
	var top map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &top); err != nil {
		return ""
	}
	var parts []string
	switch path {
	case "/v1/messages":
		parts = anthropicTextParts(top)
	case "/v1/responses":
		parts = responsesTextParts(top)
	default:
		parts = openaiChatTextParts(top)
		if len(parts) == 0 {
			parts = responsesTextParts(top)
		}
		if len(parts) == 0 {
			parts = anthropicTextParts(top)
		}
	}
	return clampReply(strings.Join(parts, ""))
}

// ExtractStreamReplyText reassembles assistant text from a buffered SSE probe
// stream: OpenAI chat deltas, Anthropic text deltas and responses output_text.
// Reasoning-only deltas are ignored.
func ExtractStreamReplyText(raw []byte) string {
	scan := bufio.NewScanner(NewSSEReader(bytes.NewReader(raw)))
	scan.Buffer(make([]byte, 4096), MaxSSEEventBytes+1)
	name := ""
	var data []string
	var b strings.Builder
	for scan.Scan() {
		line := scan.Text()
		if line != "" {
			if strings.HasPrefix(line, "event:") {
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			continue
		}
		payload := strings.Join(data, "\n")
		if strings.TrimSpace(payload) != "" && payload != "[DONE]" {
			appendStreamText(&b, name, payload)
		}
		name, data = "", nil
	}
	return clampReply(b.String())
}

// ExtractErrorMessage pulls a human-readable message out of an upstream error
// body. It understands OpenAI/Anthropic style {"error":{"message":...}} and
// gateway style {"message":...} payloads; "" when nothing readable is found.
func ExtractErrorMessage(raw []byte) string {
	var top map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &top); err != nil {
		return ""
	}
	if errVal, ok := top["error"]; ok && errVal != nil {
		switch e := errVal.(type) {
		case string:
			if msg := nonEmptyStr(e); msg != "" {
				return clampReply(msg)
			}
		case map[string]any:
			if msg := nonEmptyStr(e["message"]); msg != "" {
				return clampReply(msg)
			}
		}
	}
	return clampReply(nonEmptyStr(top["message"]))
}

func appendStreamText(b *strings.Builder, eventName, payload string) {
	var top map[string]any
	if err := json.Unmarshal([]byte(payload), &top); err != nil {
		return
	}
	typ, _ := top["type"].(string)
	if typ == "" {
		typ = eventName
	}
	if strings.HasPrefix(typ, "response.") {
		// Only the user-visible answer text; reasoning deltas stay out.
		if typ == "response.output_text.delta" {
			b.WriteString(nonEmptyStr(top["delta"]))
		}
		return
	}
	if typ == "content_block_delta" {
		if d, ok := asMap(top["delta"]); ok {
			if dt, _ := d["type"].(string); dt == "" || dt == "text_delta" || dt == "input_text" {
				b.WriteString(nonEmptyStr(d["text"]))
			}
		}
		return
	}
	if typ == "content_block_start" {
		if block, ok := asMap(top["content_block"]); ok && block["type"] == "text" {
			b.WriteString(nonEmptyStr(block["text"]))
		}
		return
	}
	// OpenAI chat chunk ("", "chat.completion.chunk" or vendor variants).
	choices, _ := top["choices"].([]any)
	for _, ch := range choices {
		m, ok := asMap(ch)
		if !ok {
			continue
		}
		if d, ok := asMap(m["delta"]); ok {
			b.WriteString(contentFieldText(d["content"]))
		}
		b.WriteString(nonEmptyStr(m["text"]))
	}
}

func openaiChatTextParts(top map[string]any) []string {
	choices, _ := top["choices"].([]any)
	for _, ch := range choices {
		m, ok := asMap(ch)
		if !ok {
			continue
		}
		if msg, ok := asMap(m["message"]); ok {
			if text := contentFieldText(msg["content"]); text != "" {
				return []string{text}
			}
		}
		if d, ok := asMap(m["delta"]); ok {
			if text := contentFieldText(d["content"]); text != "" {
				return []string{text}
			}
		}
		if text := nonEmptyStr(m["text"]); text != "" {
			return []string{text}
		}
	}
	return nil
}

func anthropicTextParts(top map[string]any) []string {
	blocks, _ := top["content"].([]any)
	var out []string
	for _, block := range blocks {
		switch b := block.(type) {
		case string:
			if b != "" {
				out = append(out, b)
			}
		case map[string]any:
			if t := b["type"]; t != nil && t != "text" {
				continue
			}
			if text := nonEmptyStr(b["text"]); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func responsesTextParts(top map[string]any) []string {
	if text := nonEmptyStr(top["output_text"]); text != "" {
		return []string{text}
	}
	items, _ := top["output"].([]any)
	var out []string
	for _, value := range items {
		item, ok := asMap(value)
		if !ok {
			continue
		}
		if t := item["type"]; t != nil && t != "message" {
			continue
		}
		parts, _ := item["content"].([]any)
		for _, pv := range parts {
			part, ok := asMap(pv)
			if !ok {
				continue
			}
			switch part["type"] {
			case nil, "", "output_text", "text":
				if text := nonEmptyStr(part["text"]); text != "" {
					out = append(out, text)
				}
			}
		}
	}
	return out
}

// contentFieldText accepts the plain string and the content-parts array shapes.
func contentFieldText(v any) string {
	switch c := v.(type) {
	case string:
		return strings.TrimSpace(c)
	case []any:
		var b strings.Builder
		for _, p := range c {
			switch item := p.(type) {
			case string:
				b.WriteString(item)
			case map[string]any:
				b.WriteString(nonEmptyStr(item["text"]))
			}
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func clampReply(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxProbeReplyBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxProbeReplyBytes], "")
}

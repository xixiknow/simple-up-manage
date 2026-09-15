package picker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

func SessionFromRequest(header string, body []byte) string {
	if s := strings.TrimSpace(header); s != "" {
		return s
	}
	return HashSession(body)
}

func HashSession(body []byte) string {
	var peek struct {
		System       any             `json:"system"`
		Instructions any             `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		Messages     []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return ""
	}
	system := []any{peek.System, peek.Instructions}
	var first any
	for _, m := range peek.Messages {
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, m.Content)
		}
		if m.Role == "user" {
			first = m.Content
			break
		}
	}
	if first == nil && len(peek.Input) > 0 {
		var text string
		if json.Unmarshal(peek.Input, &text) == nil {
			if text != "" {
				first = text
			}
		} else {
			var items []map[string]any
			if json.Unmarshal(peek.Input, &items) == nil {
				for _, item := range items {
					if item["role"] == "user" {
						first = item["content"]
						break
					}
				}
			}
		}
	}
	if first == nil {
		return ""
	}
	b, err := json.Marshal([]any{system, first})
	if err != nil {
		return ""
	}
	return sessionDigest(string(b))
}

func sessionDigest(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

func RequestSession(headers http.Header, body []byte) (session, source, previous string) {
	for _, name := range []string{"X-Session-Id", "Session_id"} {
		if s := strings.TrimSpace(headers.Get(name)); s != "" {
			return sessionDigest(s), name, ""
		}
	}
	var peek struct {
		Previous string `json:"previous_response_id"`
	}
	_ = json.Unmarshal(body, &peek)
	if peek.Previous != "" {
		return "", "previous_response_id", peek.Previous
	}
	if s := HashSession(body); s != "" {
		return s, "derived", ""
	}
	return "", "none", ""
}

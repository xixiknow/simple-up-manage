package domain

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"
)

// RequestStream reads request intent before log bodies are clipped or omitted.
func RequestStream(contentType string, body []byte) (stream, known bool) {
	mt, params, err := mime.ParseMediaType(contentType)
	if err == nil && strings.HasPrefix(mt, "multipart/") {
		if params["boundary"] == "" {
			return false, false
		}
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := r.NextPart()
			if err == io.EOF {
				return stream, true
			}
			if err != nil {
				return false, false
			}
			if part.FormName() == "stream" && part.FileName() == "" {
				value, err := io.ReadAll(io.LimitReader(part, 16))
				if err != nil {
					return false, false
				}
				stream, err = strconv.ParseBool(strings.TrimSpace(string(value)))
				if err != nil {
					return false, false
				}
			}
			_ = part.Close()
		}
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(body, &value) != nil || value == nil {
		return false, false
	}
	raw, exists := value["stream"]
	if !exists || string(raw) == "null" {
		return false, true
	}
	if json.Unmarshal(raw, &stream) != nil {
		return false, false
	}
	return stream, true
}

// RequestCompaction reports whether an OpenAI Responses request carries the
// official compaction_trigger input item ("Compacts the current context.
// Must be the final input item."). The trigger marks request intent, so the
// check runs before any upstream response exists and tolerates the item at
// any position of the input array.
func RequestCompaction(path string, body []byte) bool {
	if path != "/v1/responses" {
		return false
	}
	var value struct {
		Input []json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	for _, raw := range value.Input {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Type == "compaction_trigger" {
			return true
		}
	}
	return false
}

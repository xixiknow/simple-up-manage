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

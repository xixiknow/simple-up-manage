package upstream

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"strings"
)

// ValidateProbeResponse shares JSON and event validation with gateway traffic.
func ValidateProbeResponse(path, contentType string, stream bool, body io.Reader) error {
	ct, _, _ := mime.ParseMediaType(contentType)
	if !stream {
		if ct != "application/json" {
			return errors.New("invalid upstream response content type")
		}
		raw, err := io.ReadAll(io.LimitReader(body, (16<<20)+1))
		if err != nil {
			return err
		}
		if len(raw) > 16<<20 {
			return errors.New("upstream response exceeded limit")
		}
		_, err = ValidateJSONResponse(path, raw)
		return err
	}
	if !IsSSEContentType(contentType) {
		return errors.New("invalid upstream stream content type")
	}
	scan := bufio.NewScanner(NewSSEReader(body))
	scan.Buffer(make([]byte, 4096), MaxSSEEventBytes+1)
	v := StreamValidator{Path: path, Strict: true}
	name := ""
	var data []string
	size := 0
	for scan.Scan() {
		line := scan.Text()
		if line != "" {
			size += len(line)
			if size > MaxSSEEventBytes {
				return ErrSSEEventTooLarge
			}
			if strings.HasPrefix(line, "event:") {
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
			continue
		}
		raw := strings.Join(data, "\n")
		if strings.TrimSpace(raw) == "" {
			name, data, size = "", nil, 0
			continue
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(raw), &event) == nil && event.Type != "" {
			name = event.Type
		}
		v.Event(name, raw)
		if v.Err != nil {
			return v.Err
		}
		switch name {
		case "error", "response.failed", "response.incomplete":
			return errors.New("upstream stream failed")
		}
		if name == "response.completed" || name == "message_stop" || raw == "[DONE]" {
			return nil
		}
		name = ""
		data = nil
		size = 0
	}
	if err := scan.Err(); err != nil {
		return err
	}
	return errors.New("upstream stream missing terminal")
}

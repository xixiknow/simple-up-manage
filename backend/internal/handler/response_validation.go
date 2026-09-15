package handler

import (
	"encoding/json"
	"errors"
	"mime"
	"strings"
)

func isTextAPI(path string) bool {
	return path == "/v1/responses" || path == "/v1/chat/completions" || path == "/v1/messages"
}
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

func validateJSONResponse(path string, raw []byte) (string, error) {
	var v map[string]json.RawMessage
	invalid := errors.New("invalid upstream JSON response")
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return "", invalid
	}
	if e, ok := v["error"]; ok && string(e) != "null" {
		return "", invalid
	}
	var id, kind, status string
	_ = json.Unmarshal(v["id"], &id)
	_ = json.Unmarshal(v["object"], &kind)
	_ = json.Unmarshal(v["status"], &status)
	switch path {
	case "/v1/responses":
		var output []json.RawMessage
		if kind != "response" || id == "" || status != "completed" || json.Unmarshal(v["output"], &output) != nil || output == nil {
			return "", invalid
		}
	case "/v1/chat/completions":
		var choices []struct {
			Message map[string]json.RawMessage `json:"message"`
			Finish  *string                    `json:"finish_reason"`
		}
		if json.Unmarshal(v["choices"], &choices) != nil || len(choices) == 0 {
			return "", invalid
		}
		for _, c := range choices {
			if c.Message == nil || c.Finish == nil {
				return "", invalid
			}
		}
	case "/v1/messages":
		var typ string
		_ = json.Unmarshal(v["type"], &typ)
		var content []json.RawMessage
		if typ != "message" || json.Unmarshal(v["content"], &content) != nil || content == nil || string(v["stop_reason"]) == "null" || len(v["stop_reason"]) == 0 {
			return "", invalid
		}
	}
	return id, nil
}

func (s *streamCollector) validateEvent(name, data string) {
	if !s.strict {
		return
	}
	var v map[string]json.RawMessage
	if data == "[DONE]" {
		if s.protocolPath != "/v1/chat/completions" || !s.sawValid || !s.chatFinished {
			s.terminalErr = errors.New("invalid upstream stream terminal")
		}
		return
	}
	if strings.TrimSpace(data) == "" {
		return
	}
	if json.Unmarshal([]byte(data), &v) != nil || v == nil {
		s.terminalErr = errors.New("invalid upstream SSE payload")
		return
	}
	if e, ok := v["error"]; ok && string(e) != "null" {
		s.terminalErr = errors.New("upstream SSE error")
		return
	}
	var typ string
	_ = json.Unmarshal(v["type"], &typ)
	if typ != "" {
		name = typ
	}
	switch s.protocolPath {
	case "/v1/responses":
		if !strings.HasPrefix(name, "response.") && name != "error" {
			s.terminalErr = errors.New("unexpected upstream Responses event")
			return
		}
		var r struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(v["response"], &r)
		if r.ID != "" {
			s.responseID = r.ID
		}
		if name == "response.completed" {
			_, err := validateJSONResponse(s.protocolPath, v["response"])
			if err != nil {
				s.terminalErr = err
			} else {
				s.sawValid = true
			}
		}
		if name == "message_stop" {
			s.terminalErr = errors.New("unexpected upstream terminal")
		}
	case "/v1/chat/completions":
		if _, hasChoices := v["choices"]; !hasChoices {
			if _, hasUsage := v["usage"]; !hasUsage && name != "error" {
				s.terminalErr = errors.New("unexpected upstream chat event")
				return
			}
		}
		var choices []struct {
			Delta  map[string]json.RawMessage `json:"delta"`
			Finish *string                    `json:"finish_reason"`
		}
		if json.Unmarshal(v["choices"], &choices) == nil {
			for _, c := range choices {
				if c.Delta != nil {
					s.sawValid = true
				}
				if c.Finish != nil {
					s.chatFinished = true
				}
			}
		}
		if name == "message_stop" || name == "response.completed" {
			s.terminalErr = errors.New("unexpected upstream terminal")
		}
	case "/v1/messages":
		if name != "ping" && name != "error" && !strings.HasPrefix(name, "message_") && !strings.HasPrefix(name, "content_block_") {
			s.terminalErr = errors.New("unexpected upstream Messages event")
			return
		}
		if name == "message_start" {
			var msg struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(v["message"], &msg)
			s.sawValid = msg.Type == "message"
		}
		if name == "message_stop" && !s.sawValid {
			s.terminalErr = errors.New("invalid upstream message stream")
		}
		if name == "response.completed" {
			s.terminalErr = errors.New("unexpected upstream terminal")
		}
	}
}

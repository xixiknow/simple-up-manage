package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func IsResponsesMetadataEvent(name string) bool {
	switch name {
	case "codex.rate_limits", "codex.response.metadata", "responsesapi.websocket_timing":
		return true
	}
	return false
}

type StreamValidator struct {
	Path         string
	Strict       bool
	SawValid     bool
	ChatFinished bool
	ResponseID   string
	Err          error
}

func ValidateJSONResponse(path string, raw []byte) (string, error) {
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

func (s *StreamValidator) Event(name, data string) {
	if !s.Strict {
		return
	}
	var v map[string]json.RawMessage
	if data == "[DONE]" {
		if s.Path != "/v1/chat/completions" || !s.SawValid || !s.ChatFinished {
			s.Err = errors.New("invalid upstream stream terminal")
		}
		return
	}
	if strings.TrimSpace(data) == "" {
		return
	}
	if json.Unmarshal([]byte(data), &v) != nil || v == nil {
		s.Err = errors.New("invalid upstream SSE payload")
		return
	}
	if e, ok := v["error"]; ok && string(e) != "null" {
		s.Err = errors.New("upstream SSE error")
		return
	}
	var typ string
	_ = json.Unmarshal(v["type"], &typ)
	if typ != "" {
		name = typ
	}
	switch s.Path {
	case "/v1/responses":
		if IsResponsesMetadataEvent(name) {
			return
		}
		if !strings.HasPrefix(name, "response.") && name != "error" {
			s.Err = fmt.Errorf("unexpected upstream Responses event: %.128q", name)
			return
		}
		var r struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(v["response"], &r)
		if r.ID != "" {
			s.ResponseID = r.ID
		}
		if name == "response.completed" {
			_, err := ValidateJSONResponse(s.Path, v["response"])
			if err != nil {
				s.Err = err
			} else {
				s.SawValid = true
			}
		}
		if name == "message_stop" {
			s.Err = errors.New("unexpected upstream terminal")
		}
	case "/v1/chat/completions":
		if _, hasChoices := v["choices"]; !hasChoices {
			if _, hasUsage := v["usage"]; !hasUsage && name != "error" {
				s.Err = errors.New("unexpected upstream chat event")
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
					s.SawValid = true
				}
				if c.Finish != nil {
					s.ChatFinished = true
				}
			}
		}
		if name == "message_stop" || name == "response.completed" {
			s.Err = errors.New("unexpected upstream terminal")
		}
	case "/v1/messages":
		if name != "ping" && name != "error" && !strings.HasPrefix(name, "message_") && !strings.HasPrefix(name, "content_block_") {
			s.Err = errors.New("unexpected upstream Messages event")
			return
		}
		if name == "message_start" {
			var msg struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(v["message"], &msg)
			s.SawValid = msg.Type == "message"
		}
		if name == "message_stop" && !s.SawValid {
			s.Err = errors.New("invalid upstream message stream")
		}
		if name == "response.completed" {
			s.Err = errors.New("unexpected upstream terminal")
		}
	}
}

package handler

import (
	"strings"
	"time"
)

type eventDiagnostics struct {
	Counts    map[string]int `json:"counts"`
	FirstMs   int64          `json:"first_ms"`
	LastMs    int64          `json:"last_ms"`
	Terminal  string         `json:"terminal,omitempty"`
	Oversized int            `json:"oversized"`
}

func (s *streamCollector) noteEvent(name, data string) {
	if name == "" && strings.TrimSpace(data) == "" {
		return
	}
	if len(name) > 128 {
		name = name[:128]
	}
	if name == "" {
		name = "data"
	}
	if s.events.Counts == nil {
		s.events.Counts = make(map[string]int)
		s.events.FirstMs = s.eventElapsed()
	}
	if _, ok := s.events.Counts[name]; !ok && len(s.events.Counts) >= 64 {
		name = "other"
	}
	s.events.Counts[name]++
	s.events.LastMs = s.eventElapsed()
	switch name {
	case "response.completed", "response.failed", "response.incomplete", "error", "message_stop", "done":
		s.events.Terminal = name
	}
}

func (s *streamCollector) eventElapsed() int64 {
	start := s.attemptStart
	if start.IsZero() {
		start = s.start
	}
	return time.Since(start).Milliseconds()
}

func (s *streamCollector) ttftStatus(success bool) string {
	if s.ttftMs > 0 {
		return "measured"
	}
	if s.events.Oversized > 0 {
		return "event_limit"
	}
	if success {
		return "no_output"
	}
	return "interrupted"
}

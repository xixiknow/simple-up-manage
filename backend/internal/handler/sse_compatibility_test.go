package handler

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gin-gonic/gin"
	"simple-up-manage/internal/upstream"
)

func TestGatewayAndProbeSSECompatibility(t *testing.T) {
	added := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"content\":[]}}\n\n"
	delta := "event: response.output_text.delta\n" + testResponseDelta
	completed := "event: response.completed\n" + testResponseCompleted
	for _, tc := range []struct {
		name, path, contentType, body, wantBody string
		ok, first                               bool
	}{
		{"basispoints metadata", "/v1/responses", "text/event-stream", testBasispointsMetadata + testResponseDelta + testResponseCompleted, "", true, true},
		{"basispoints is not output", "/v1/responses", "text/event-stream", testBasispointsMetadata + testResponseCompleted, "", true, false},
		{"basispoints only", "/v1/responses", "text/event-stream", testBasispointsMetadata, "", false, false},
		{"basispoints error", "/v1/responses", "text/event-stream", "data: {\"type\":\"basispoints.response.metadata\",\"error\":{\"message\":\"bad\"}}\n\n", "", false, false},
		{"empty response terminal", "/v1/responses", "text/event-stream", "event: response.completed\n\n", "", false, false},
		{"blank response terminal", "/v1/responses", "text/event-stream", "event: response.completed\ndata: \n\n", "", false, false},
		{"empty message terminal", "/v1/messages", "text/event-stream", "event: message_stop\n\n", "", false, false},
		{"cross protocol empty terminal", "/v1/chat/completions", "text/event-stream", "event: response.completed\n\n", "", false, false},
		{"empty terminal then valid completion", "/v1/responses", "text/event-stream", "event: response.completed\n\n" + testResponseCompleted, "", true, false},
		{"blank terminal then output", "/v1/responses", "text/event-stream", "event: response.completed\ndata: \n\n" + testResponseDelta + testResponseCompleted, "", true, true},
		{"empty error then valid completion", "/v1/responses", "text/event-stream", "event: error\n\n" + testResponseCompleted, "", true, false},
		{"empty message terminal then valid message", "/v1/messages", "text/event-stream", "event: message_stop\n\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "", true, false},
		{"empty terminal after output still requires completion", "/v1/responses", "text/event-stream", testResponseDelta + "event: response.completed\n\n", "", false, true},
		{"empty heartbeat", "/v1/responses", "text/event-stream", "event: ping\n\n" + testResponseCompleted, "", true, false},
		{"comments", "/v1/responses", "text/event-stream", ": keepalive\n\n" + testResponseCompleted, "", true, false},
		{"stream alias", "/v1/responses", "text/stream; charset=utf-8", testResponseDelta + testResponseCompleted, "", true, true},
		{"missing event separators", "/v1/responses", "text/event-stream", strings.TrimSuffix(added, "\n") + strings.TrimSuffix(delta, "\n") + completed, added + delta + completed, true, true},
		{"CRLF missing separator", "/v1/responses", "text/event-stream", strings.ReplaceAll(strings.TrimSuffix(added, "\n"), "\n", "\r\n") + completed, strings.ReplaceAll(strings.TrimSuffix(added, "\n"), "\n", "\r\n") + "\n" + completed, true, false},
		{"multiline JSON", "/v1/responses", "text/event-stream", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\ndata: \"delta\":\"ok\"}\n\n" + completed, "", true, true},
		{"incomplete JSON before next event", "/v1/responses", "text/event-stream", "event: response.created\ndata: {\"type\":\n" + completed, "", false, false},
		{"adjacent data objects remain invalid", "/v1/responses", "text/event-stream", strings.TrimSuffix(testResponseDelta, "\n") + testResponseCompleted, "", false, false},
		{"unterminated last event", "/v1/responses", "text/event-stream", strings.TrimSuffix(testResponseCompleted, "\n"), "", false, false},
	} {
		for _, fragmented := range []bool{false, true} {
			name := tc.name
			if fragmented {
				name += "/fragmented"
			}
			t.Run(name, func(t *testing.T) {
				reader := func() io.Reader {
					var r io.Reader = strings.NewReader(tc.body)
					if fragmented {
						r = iotest.OneByteReader(r)
					}
					return r
				}
				if !isSSEContentType(tc.contentType) {
					t.Fatal("gateway rejected content type")
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				col := &streamCollector{start: time.Now(), protocolPath: tc.path, strict: true}
				err := copySSE(c.Writer, reader(), col, true, nil)
				probeErr := upstream.ValidateProbeResponse(tc.path, tc.contentType, true, reader())
				if (err == nil) != tc.ok || (probeErr == nil) != tc.ok || (col.ttftMs > 0) != tc.first {
					t.Fatalf("gateway=%v probe=%v ttft=%d", err, probeErr, col.ttftMs)
				}
				want := tc.wantBody
				if want == "" {
					want = tc.body
				}
				if tc.ok && rec.Body.String() != want {
					t.Fatalf("forwarded body differs: %q", rec.Body.String())
				}
				if !tc.ok && !tc.first && c.Writer.Written() {
					t.Fatal("invalid stream committed before failover")
				}
			})
		}
	}
}

func TestEmptySSEFramesDoNotSetTerminalOrOutput(t *testing.T) {
	for _, name := range []string{"response.completed", "response.failed", "message_stop", "error"} {
		col := &streamCollector{start: time.Now(), protocolPath: "/v1/responses", strict: true}
		col.feed([]byte("event: " + name + "\n\n"))
		if col.terminal || col.terminalErr != nil || col.ttftMs != 0 || col.events.Terminal != "" {
			t.Fatalf("empty %s changed stream state: terminal=%v err=%v ttft=%d", name, col.terminal, col.terminalErr, col.ttftMs)
		}
	}
}

func TestLargeSSECompletion(t *testing.T) {
	completed := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + strings.Repeat("x", (1<<20)+100) + "\"}]}]}}\n\n"
	for _, prefix := range []string{"", testResponseDelta} {
		body := prefix + completed
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		col := &streamCollector{start: time.Now(), protocolPath: "/v1/responses", strict: true}
		if err := copySSE(c.Writer, strings.NewReader(body), col, true, nil); err != nil {
			t.Fatal(err)
		}
		if !col.terminal || col.events.Oversized != 0 || col.ttftMs == 0 || rec.Body.String() != body {
			t.Fatal("large completion lost terminal, first output or body")
		}
		if err := upstream.ValidateProbeResponse("/v1/responses", "text/event-stream", true, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSSEEventLimitIsExplicit(t *testing.T) {
	body := "data: " + strings.Repeat("x", upstream.MaxSSEEventBytes) + "\n\n" + testResponseCompleted
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	col := &streamCollector{start: time.Now(), protocolPath: "/v1/responses", strict: true}
	err := copySSE(c.Writer, strings.NewReader(body), col, true, nil)
	probeErr := upstream.ValidateProbeResponse("/v1/responses", "text/event-stream", true, strings.NewReader(body))
	if !errors.Is(err, upstream.ErrSSEEventTooLarge) || !errors.Is(probeErr, upstream.ErrSSEEventTooLarge) || c.Writer.Written() || col.events.Oversized != 1 {
		t.Fatalf("gateway=%v probe=%v oversized=%d written=%v", err, probeErr, col.events.Oversized, c.Writer.Written())
	}
}

func TestAnthropicInitialBlockReleasesFirstOutput(t *testing.T) {
	col := &streamCollector{start: time.Now(), protocolPath: "/v1/messages", strict: true}
	outputs := 0
	col.onFirstOutput = func() { outputs++ }
	col.feed([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\"}}\n\n"))
	col.feed([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Hello\"}}\n\n"))
	if outputs != 1 || col.ttftMs == 0 || col.firstEvent != "content_block_start" || col.terminalErr != nil {
		t.Fatalf("outputs=%d ttft=%d event=%s error=%v", outputs, col.ttftMs, col.firstEvent, col.terminalErr)
	}
}

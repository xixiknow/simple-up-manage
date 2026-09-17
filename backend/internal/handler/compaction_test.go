package handler

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gin-gonic/gin"
)

const testResponseCompaction = "event: response.compaction.compacting\ndata: {\"type\":\"response.compaction.compacting\",\"item_id\":\"compact_1\",\"output_index\":0}\n\n"

func TestCompactionGraceRequiresValidatedProgress(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		grace            bool
	}{
		{"compacting", "/v1/responses", testResponseCompaction, true},
		{"event name", "/v1/responses", "event: response.compaction.compacting\ndata: {\"item_id\":\"compact_1\"}\n\n", true},
		{"item added", "/v1/responses", "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"compact_1\",\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\n", true},
		{"repeated progress", "/v1/responses", strings.Repeat(testResponseCompaction, 3), true},
		{"ordinary progress", "/v1/responses", "data: {\"type\":\"response.in_progress\"}\n\n", false},
		{"reasoning item", "/v1/responses", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"opaque\"}}\n\n", false},
		{"metadata", "/v1/responses", testResponseMetadata, false},
		{"compaction error", "/v1/responses", "data: {\"type\":\"response.compaction.compacting\",\"error\":{\"message\":\"failed\"}}\n\n", false},
		{"malformed data", "/v1/responses", "event: response.compaction.compacting\ndata: broken\n\n", false},
		{"unknown event", "/v1/responses", "data: {\"type\":\"response.compaction.unknown\"}\n\n", false},
		{"different endpoint", "/v1/chat/completions", testResponseCompaction, false},
	} {
		for _, fragmented := range []bool{false, true} {
			name := tc.name
			if fragmented {
				name += "/fragmented"
			}
			t.Run(name, func(t *testing.T) {
				var src io.Reader = strings.NewReader(tc.body)
				if fragmented {
					src = iotest.OneByteReader(src)
				}
				calls := 0
				col := &streamCollector{start: time.Now(), strict: true, protocolPath: tc.path, onCompaction: func() { calls++ }}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				err := copySSE(c.Writer, src, col, true, nil)
				wantCalls := 0
				if tc.grace {
					wantCalls = 1
				}
				if calls != wantCalls || col.compaction != tc.grace || col.ttftMs != 0 || c.Writer.Written() || err == nil {
					t.Fatalf("grace=%v calls=%d ttft=%d written=%v err=%v", col.compaction, calls, col.ttftMs, c.Writer.Written(), err)
				}
			})
		}
	}
}

func TestCompactionAfterOutputDoesNotChangeFirstToken(t *testing.T) {
	calls := 0
	col := &streamCollector{start: time.Now(), strict: true, protocolPath: "/v1/responses", onCompaction: func() { calls++ }}
	col.feed([]byte(testResponseDelta))
	firstAt := col.firstAt
	col.feed([]byte(testResponseCompaction + testResponseCompleted))
	if calls != 0 || col.compaction || col.firstEvent != "response.output_text.delta" || !col.firstAt.Equal(firstAt) || col.terminalErr != nil {
		t.Fatalf("compaction changed measured first output: %+v calls=%d", col, calls)
	}
}

package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPeekStream(t *testing.T) {
	if peekStream([]byte(`{"model":"gpt-4o","stream":true}`)) != true {
		t.Fatal("stream true")
	}
	if peekStream([]byte(`{"model":"gpt-4o","stream":false}`)) {
		t.Fatal("stream false")
	}
	if peekStream([]byte(`{"model":"gpt-4o"}`)) {
		t.Fatal("omitted stream is sync")
	}
	if peekStream([]byte(`not json`)) {
		t.Fatal("invalid json is sync")
	}
}

func TestPeekModelFromJSON(t *testing.T) {
	got := peekModelFrom("application/json", []byte(`{"model":" gpt-image-1 ","prompt":"a cat"}`))
	if got != "gpt-image-1" {
		t.Fatalf("want gpt-image-1, got %q", got)
	}
	if got := peekModelFrom("", []byte(`{"model":"dall-e-3"}`)); got != "dall-e-3" {
		t.Fatalf("empty content type should fall back to JSON, got %q", got)
	}
	if got := peekModelFrom("application/json", []byte(`not json`)); got != "" {
		t.Fatalf("want empty for invalid json, got %q", got)
	}
}

func TestPeekModelFromMultipart(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("image", "in.png")
	_, _ = fw.Write(bytes.Repeat([]byte{0xff}, 4096))
	_ = w.WriteField("prompt", "make it blue")
	_ = w.WriteField("model", "gpt-image-1")
	_ = w.Close()

	got := peekModelFrom(w.FormDataContentType(), buf.Bytes())
	if got != "gpt-image-1" {
		t.Fatalf("want gpt-image-1, got %q", got)
	}

	// No model field at all.
	var buf2 bytes.Buffer
	w2 := multipart.NewWriter(&buf2)
	_ = w2.WriteField("prompt", "x")
	_ = w2.Close()
	if got := peekModelFrom(w2.FormDataContentType(), buf2.Bytes()); got != "" {
		t.Fatalf("want empty, got %q", got)
	}

	// Missing boundary must not panic.
	if got := peekModelFrom("multipart/form-data", buf.Bytes()); got != "" {
		t.Fatalf("want empty without boundary, got %q", got)
	}
}

func TestExtractGatewayError(t *testing.T) {
	got := extractGatewayError([]byte(`{"error":{"message":"no enabled upstream for protocol","type":"api_error"}}`), 503)
	if got != "no enabled upstream for protocol" {
		t.Fatalf("got %q", got)
	}
	if got := extractGatewayError(nil, 502); got != "Bad Gateway" {
		t.Fatalf("empty body: %q", got)
	}
}

func TestShouldFailoverStatus(t *testing.T) {
	for _, code := range []int{401, 403, 404, 429, 402, 500, 502, 529} {
		if !shouldFailoverStatus(code) {
			t.Fatalf("%d should failover", code)
		}
	}
	for _, code := range []int{200, 400} {
		if shouldFailoverStatus(code) {
			t.Fatalf("%d must not failover", code)
		}
	}
}

func TestClassifyHTTPFailure(t *testing.T) {
	quota := []byte(`{"error":{"code":"insufficient_quota"}}`)
	tests := []struct {
		name, scope, action string
		code                int
		body                []byte
		failover, low       bool
	}{
		{name: "bad request", code: 400},
		{name: "unauthorized", code: 401, failover: true, scope: failureScopeKey, action: "cooldown_key"},
		{name: "forbidden", code: 403, failover: true, scope: failureScopeKey, action: "cooldown_key"},
		{name: "quota forbidden", code: 403, body: quota, failover: true, low: true, scope: failureScopeProvider, action: "mark_low_balance"},
		{name: "not found", code: 404, failover: true, scope: failureScopeProvider, action: "cooldown_provider"},
		{name: "rate limit", code: 429, failover: true, scope: failureScopeKeyModel, action: "cooldown_key_model"},
		{name: "server error", code: 500, failover: true, scope: failureScopeProvider, action: "cooldown_provider"},
		{name: "overloaded", code: 529, failover: true, scope: failureScopeProvider, action: "cooldown_provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyHTTPFailure(tt.code, tt.body)
			if got.failOver != tt.failover || got.lowBalance != tt.low || got.scope != tt.scope || got.action != tt.action {
				t.Fatalf("classification = %+v", got)
			}
		})
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatalf("zero delay: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, 50*time.Millisecond); err == nil {
		t.Fatal("cancelled context should abort sleep")
	}
}

func TestIsQuotaExhaustedBody(t *testing.T) {
	newAPI := []byte(`{"error":{"message":"用户额度不足 (request id: x)","type":"new_api_error","code":"insufficient_user_quota"}}`)
	if !isQuotaExhaustedBody(newAPI) {
		t.Fatal("new-api insufficient_user_quota should match")
	}
	if !isQuotaExhaustedBody([]byte(`{"error":{"code":"pre_consume_token_quota_failed","type":"new_api_error"}}`)) {
		t.Fatal("pre_consume_token_quota_failed should match")
	}
	if isQuotaExhaustedBody([]byte(`{"error":{"message":"forbidden","type":"new_api_error","code":"access_denied"}}`)) {
		t.Fatal("access_denied must not match")
	}
	if isQuotaExhaustedBody([]byte(`not json`)) {
		t.Fatal("garbage must not match")
	}
}

func TestUsageCaptureBounded(t *testing.T) {
	c := &usageCapture{limit: 8}
	n, _ := c.Write([]byte("0123456789"))
	if n != 10 {
		t.Fatalf("Write must report full length, got %d", n)
	}
	if c.Bytes() != nil {
		t.Fatalf("over-limit capture must be discarded")
	}
	c2 := &usageCapture{limit: 8}
	_, _ = c2.Write([]byte("abc"))
	_, _ = c2.Write([]byte("de"))
	if string(c2.Bytes()) != "abcde" {
		t.Fatalf("want abcde, got %q", c2.Bytes())
	}
}

// TestLargePassthrough verifies that a non-streaming body larger than the usage
// capture limit is forwarded in full (the previous implementation truncated at
// 16MB and produced broken JSON).
func TestLargePassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := []byte(`{"data":[{"b64_json":"` + strings.Repeat("A", 40) + `"}],"usage":{"input_tokens":3,"output_tokens":5}}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	capture := &usageCapture{limit: 16}
	n, err := io.Copy(c.Writer, io.TeeReader(bytes.NewReader(payload), capture))
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != len(payload) || rec.Body.Len() != len(payload) {
		t.Fatalf("body truncated: copied=%d written=%d want=%d", n, rec.Body.Len(), len(payload))
	}
	if capture.Bytes() != nil {
		t.Fatalf("capture beyond limit should be dropped")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
}

func TestConcLimiter(t *testing.T) {
	l := newConcLimiter()
	if !l.Acquire(1, 0) {
		t.Fatal("unlimited should allow")
	}
	if !l.Acquire(1, 1) {
		t.Fatal("first slot should allow")
	}
	if l.Acquire(1, 1) {
		t.Fatal("at cap should deny")
	}
	l.Release(1, 1)
	if !l.Acquire(1, 1) {
		t.Fatal("after release should allow")
	}
	if l.Acquire(2, 2) && l.Acquire(2, 2) && l.Acquire(2, 2) {
		t.Fatal("third slot at limit 2 should deny")
	}
}

func TestCopySSEHoldsUntilFirstText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	col := &streamCollector{start: time.Now()}
	released := false
	src := bytes.NewReader([]byte(
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n",
	))
	if err := copySSE(c.Writer, src, col, true, func() {
		released = true
		c.Status(200)
	}); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("should release headers on first generated text")
	}
	if col.ttftMs == 0 {
		t.Fatal("ttft should be set")
	}
	if !strings.Contains(rec.Body.String(), "Hi") {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestCopySSEReleasesOnEOFWithoutText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	col := &streamCollector{start: time.Now()}
	released := false
	src := bytes.NewReader([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n"))
	if err := copySSE(c.Writer, src, col, true, func() { released = true }); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("complete stream with no text should still flush")
	}
	if col.ttftMs != 0 {
		t.Fatalf("role-only eof should not set ttft, got %d", col.ttftMs)
	}
}

func TestProxyTimeoutMessage(t *testing.T) {
	if got := proxyTimeoutMessage(context.DeadlineExceeded, true); got != "first token timeout" {
		t.Fatalf("got %q", got)
	}
	if got := proxyTimeoutMessage(context.DeadlineExceeded, false); got != "upstream timeout" {
		t.Fatalf("got %q", got)
	}
	if got := proxyTimeoutMessage(io.EOF, false); got != io.EOF.Error() {
		t.Fatalf("got %q", got)
	}
}

func TestFirstTokenWatchStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &firstTokenWatch{}
	w.start(cancel, 30*time.Millisecond, time.Now().Add(time.Second))
	if !w.running() {
		t.Fatal("should be running")
	}
	w.stop()
	time.Sleep(50 * time.Millisecond)
	if w.timedOut() {
		t.Fatal("stopped watch should not fire")
	}
	select {
	case <-ctx.Done():
		t.Fatal("stopped watch should not cancel")
	default:
	}
}

func TestStreamCollectorTTFTOnFirstText(t *testing.T) {
	col := &streamCollector{start: time.Now().Add(-800 * time.Millisecond)}
	col.feed([]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n"))
	if col.ttftMs != 0 {
		t.Fatalf("role-only should not set ttft, got %d", col.ttftMs)
	}
	col.feed([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n"))
	if col.ttftMs < 700 {
		t.Fatalf("ttft %d too small, want ~800ms after first text", col.ttftMs)
	}
}

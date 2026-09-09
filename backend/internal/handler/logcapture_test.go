package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHeadersJSONRedactsSecrets(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("x-api-key", "secret-key")
	h.Set("Content-Type", "application/json")
	h.Set("X-Request-Id", "abc")
	got := headersJSON(h, "POST", "/v1/chat/completions")
	if strings.Contains(got, "sk-secret") || strings.Contains(got, "secret-key") {
		t.Fatalf("secrets leaked: %s", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("want redacted marker, got %s", got)
	}
	if !strings.Contains(got, "application/json") || !strings.Contains(got, "abc") {
		t.Fatalf("non-secret headers missing: %s", got)
	}
	if !strings.Contains(got, `"POST"`) || !strings.Contains(got, "/v1/chat/completions") {
		t.Fatalf("method/path missing: %s", got)
	}
}

func TestRequestClientIPFromRemoteAddr(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("POST", "/v1/chat/completions", nil)
	c.Request.RemoteAddr = "203.0.113.10:54321"
	if got := requestClientIP(c); got != "203.0.113.10" {
		t.Fatalf("got %q", got)
	}
}

func TestCaptureBodyTruncates(t *testing.T) {
	big := strings.Repeat("a", maxLogBodyBytes+32)
	got, trunc := captureBody("application/json", []byte(big), len(big))
	if !trunc {
		t.Fatal("want truncated")
	}
	if len(got) > maxLogBodyBytes {
		t.Fatalf("stored %d bytes, max %d", len(got), maxLogBodyBytes)
	}
}

func TestCaptureBodyOmitsMultipart(t *testing.T) {
	got, trunc := captureBody("multipart/form-data; boundary=x", []byte("not-the-raw-image"), 4096)
	if !trunc {
		t.Fatal("omitted binary should be marked truncated")
	}
	if strings.Contains(got, "not-the-raw-image") {
		t.Fatalf("raw multipart leaked: %s", got)
	}
	if !strings.Contains(got, "4096") {
		t.Fatalf("want byte count, got %s", got)
	}
}

func TestCaptureInbound(t *testing.T) {
	req, _ := http.NewRequest("POST", "/v1/messages?foo=1", nil)
	req.Header.Set("Authorization", "Bearer ck-secret")
	req.Header.Set("Content-Type", "application/json")
	cap := captureInbound(req, []byte(`{"model":"claude-3","messages":[]}`))
	if strings.Contains(cap.ReqHeaders, "ck-secret") {
		t.Fatalf("consumer key leaked: %s", cap.ReqHeaders)
	}
	if !strings.Contains(cap.ReqBody, "claude-3") {
		t.Fatalf("body missing: %s", cap.ReqBody)
	}
	if cap.ReqTrunc {
		t.Fatal("small json should not truncate")
	}
}

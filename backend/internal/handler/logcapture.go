package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"simple-up-manage/internal/domain"
)

// maxLogBodyBytes caps stored request/response bodies so a single log row
// cannot balloon the database (images / SSE / large JSON).
const maxLogBodyBytes = 64 << 10

var redactHeaderKeys = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"x-api-key":           {},
	"api-key":             {},
	"cookie":              {},
	"set-cookie":          {},
	"x-goog-api-key":      {},
}

type ioCapture struct {
	StartedAt   time.Time
	ReqStream   bool
	StreamKnown bool
	ReqHeaders  string
	ReqBody     string
	ReqTrunc    bool
	RespHeaders string
	RespBody    string
	RespTrunc   bool
}

func captureInbound(r *http.Request, body []byte) ioCapture {
	if r == nil {
		return ioCapture{}
	}
	bodyStr, trunc := captureBody(r.Header.Get("Content-Type"), body, len(body))
	stream, known := domain.RequestStream(r.Header.Get("Content-Type"), body)
	return ioCapture{
		ReqStream:   stream,
		StreamKnown: known,
		ReqHeaders:  headersJSON(r.Header, r.Method, requestURI(r)),
		ReqBody:     bodyStr,
		ReqTrunc:    trunc,
	}
}

func (cap ioCapture) withResponse(h http.Header, contentType string, prefix []byte, total int) ioCapture {
	body, trunc := captureBody(contentType, prefix, total)
	cap.RespHeaders = headersJSON(h, "", "")
	cap.RespBody = body
	cap.RespTrunc = trunc
	return cap
}

func requestURI(r *http.Request) string {
	if r.URL == nil {
		return r.RequestURI
	}
	if q := r.URL.RawQuery; q != "" {
		return r.URL.Path + "?" + q
	}
	return r.URL.Path
}

func headersJSON(h http.Header, method, path string) string {
	m := map[string]any{}
	if method != "" {
		m[":method"] = method
	}
	if path != "" {
		m[":path"] = path
	}
	for k, vs := range h {
		if _, hide := redactHeaderKeys[strings.ToLower(k)]; hide {
			m[k] = "[redacted]"
			continue
		}
		if len(vs) == 1 {
			m[k] = vs[0]
		} else {
			m[k] = vs
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func captureBody(contentType string, body []byte, total int) (string, bool) {
	if total < len(body) {
		total = len(body)
	}
	mt := mediaType(contentType)
	if omitRawBody(mt) {
		if mt == "" {
			mt = "binary"
		}
		return fmt.Sprintf("[%s %d bytes omitted]", mt, total), true
	}
	text, truncated := clipBody(body, maxLogBodyBytes)
	return text, truncated || total > len(body)
}

func mediaType(contentType string) string {
	ct := strings.TrimSpace(strings.ToLower(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

func omitRawBody(mt string) bool {
	if mt == "" {
		return false
	}
	if strings.HasPrefix(mt, "multipart/") ||
		strings.HasPrefix(mt, "image/") ||
		strings.HasPrefix(mt, "audio/") ||
		strings.HasPrefix(mt, "video/") ||
		mt == "application/octet-stream" ||
		mt == "application/pdf" {
		return true
	}
	return false
}

func clipBody(b []byte, max int) (string, bool) {
	if len(b) <= max {
		return string(b), false
	}
	b = b[:max]
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b), true
}

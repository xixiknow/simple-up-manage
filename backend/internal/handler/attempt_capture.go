package handler

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"sync"

	"simple-up-manage/internal/logarchive"
)

type attemptPhase struct {
	mu    sync.Mutex
	phase string
}

func (p *attemptPhase) set(s string) { p.mu.Lock(); p.phase = s; p.mu.Unlock() }
func (p *attemptPhase) get() string  { p.mu.Lock(); defer p.mu.Unlock(); return p.phase }
func (p *attemptPhase) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:           func(string) { p.set("connecting") },
		DNSStart:          func(httptrace.DNSStartInfo) { p.set("dns") },
		ConnectStart:      func(string, string) { p.set("connecting") },
		TLSHandshakeStart: func() { p.set("tls") },
		TLSHandshakeDone:  func(tls.ConnectionState, error) {},
		GotConn:           func(httptrace.GotConnInfo) { p.set("sending_request") },
		WroteRequest: func(i httptrace.WroteRequestInfo) {
			if i.Err == nil {
				p.set("awaiting_headers")
			}
		},
	}
}

type archivedResponse struct {
	io.ReadCloser
	archive     *logarchive.Capture
	received    int64
	eof         bool
	binary      bool
	contentType string
}

func (r *archivedResponse) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if r.received == 0 && n > 0 && r.contentType == "" {
		r.binary = omitRawBody(mediaType(http.DetectContentType(p[:n])))
	}
	r.received += int64(n)
	if n > 0 && !r.binary {
		_, _ = r.archive.Write(p[:n])
	}
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}
func (r *archivedResponse) finish(success bool) {
	if r.binary {
		_, _ = r.archive.Write([]byte(fmt.Sprintf("[%s %d bytes omitted]", r.contentType, r.received)))
		r.archive.SetReceivedBytes(r.received)
		r.archive.Finish(false, "binary_omitted")
		return
	}
	reason := ""
	if !success && !r.eof {
		reason = "stream_interrupted"
	}
	r.archive.Finish(success || r.eof, reason)
}

func (h *Gateway) archiveRequest(logID uint, r *http.Request, body []byte) {
	if h.Ops == nil || h.Ops.Archives == nil {
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(body)
	}
	cap := h.Ops.Archives.Begin(logID, "", "request", ct)
	mt, params, err := mime.ParseMediaType(ct)
	if err == nil && mt == "multipart/form-data" {
		var parts []map[string]any
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, e := mr.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				parts = append(parts, map[string]any{"error": "invalid multipart"})
				break
			}
			item := map[string]any{"name": part.FormName(), "filename": part.FileName(), "content_type": part.Header.Get("Content-Type")}
			if part.FileName() != "" || omitRawBody(mediaType(part.Header.Get("Content-Type"))) {
				n, _ := io.Copy(io.Discard, part)
				item["bytes"] = n
				item["omitted"] = true
			} else {
				b, _ := io.ReadAll(part)
				item["text"] = string(b)
				item["bytes"] = len(b)
			}
			_ = part.Close()
			parts = append(parts, item)
		}
		summary, _ := json.Marshal(map[string]any{"parts": parts})
		_, _ = cap.Write(summary)
		cap.SetReceivedBytes(int64(len(body)))
		cap.Finish(false, "multipart_files_omitted")
		return
	}
	if omitRawBody(mediaType(ct)) {
		_, _ = cap.Write([]byte(fmt.Sprintf("[%s %d bytes omitted]", mediaType(ct), len(body))))
		cap.SetReceivedBytes(int64(len(body)))
		cap.Finish(false, "binary_omitted")
		return
	}
	_, _ = cap.Write(body)
	cap.Finish(true, "")
}

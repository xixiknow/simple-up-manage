package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/logarchive"
	"simple-up-manage/internal/middleware"
	"simple-up-manage/internal/ops"
)

func TestGatewayDoneOnlyArchivesAndDiagnostics(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"instructions\":\"" + strings.Repeat("x", 100000) + "\",\"output\":[]}}\n\n"
	done := "event: response.function_call_arguments.done\ndata: {\"arguments\":\"{}\"}\n\n"
	response := created + done + testResponseCompleted
	for _, mode := range []string{"done", "headers_timeout", "output_timeout", "connect_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if mode == "headers_timeout" || mode == "cancelled" {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "output_timeout" {
					_, _ = io.WriteString(w, testResponseMetadata)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			if mode == "connect_failure" {
				server.Close()
			}
			db := logTestDB(t)
			if err := db.AutoMigrate(&domain.LogBody{}); err != nil {
				t.Fatal(err)
			}
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("fixture")
			up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Protocols: "openai"}
			db.Create(&up)
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret}
			db.Create(&key)
			ck := domain.ConsumerKey{Name: "fixture", Key: "sk-fixture", Status: domain.StatusEnabled}
			db.Create(&ck)
			archive, err := logarchive.New(db, t.TempDir(), 64<<20, 10<<30)
			if err != nil {
				t.Fatal(err)
			}
			svc := ops.New(db, enc, nil)
			svc.Archives = archive
			h := NewGateway(db, enc, svc, fixturePicker{key: &key, up: &up})
			h.firstTokenWait = 50 * time.Millisecond
			request := `{"model":"m","stream":true,"instructions":"` + strings.Repeat("q", 100000) + `"}`
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(request))
			r.Header.Set("Content-Type", "application/json")
			if mode == "cancelled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = r
			snap := captureInbound(r, []byte(request))
			snap.StartedAt = time.Now()
			lg := h.beginLog(&ck, "openai", "m", r.URL.Path, "fixture", "", snap)
			h.archiveRequest(lg.id, r, []byte(request))
			out := h.forwardOnce(c, &ck, &key, &up, "openai", "m", "", "fixture", []byte(request), snap, lg, snap.StartedAt)
			archive.Wait()
			var a domain.RequestAttempt
			if err := db.First(&a, "request_log_id = ?", lg.id).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "done" {
				if !out.validSuccess || w.Body.String() != response || a.TTFTMs <= 0 || a.TTFTEvent != "response.function_call_arguments.done" || a.TTFTStatus != "measured" {
					t.Fatalf("out=%+v attempt=%+v", out, a)
				}
				if a.HeadersMs <= 0 || a.ReceivedBytes != int64(len(response)) || !strings.Contains(a.EventSummary, "response.completed") {
					t.Fatalf("diagnostics=%+v", a)
				}
			} else {
				if out.validSuccess || a.TTFTMs != 0 || a.ErrorMessage == "" {
					t.Fatalf("out=%+v attempt=%+v", out, a)
				}
				switch mode {
				case "headers_timeout":
					if a.StatusCode != 0 || a.FailurePhase != "awaiting_headers" || a.FailureAction != "transport_failure" || a.ErrorMessage != "first token timeout" {
						t.Fatalf("attempt=%+v", a)
					}
				case "output_timeout":
					if a.StatusCode != 200 || a.FailurePhase != "awaiting_first_output" || a.ReceivedBytes == 0 {
						t.Fatalf("attempt=%+v", a)
					}
				case "connect_failure":
					if a.FailurePhase != "connecting" || a.FailureAction != "transport_failure" {
						t.Fatalf("attempt=%+v", a)
					}
				case "cancelled":
					if a.Result != "client_cancelled" || a.FailureAction != "client_cancelled" {
						t.Fatalf("attempt=%+v", a)
					}
				}
			}
			var bodies []domain.LogBody
			db.Where("request_log_id = ?", lg.id).Find(&bodies)
			for _, meta := range bodies {
				reader, err := archive.Open(meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(reader)
				_ = reader.Close()
				if err != nil {
					t.Fatal(err)
				}
				if meta.Direction == "request" && (string(raw) != request || meta.Status != "complete") {
					t.Fatal("request archive incomplete")
				}
				if meta.Direction == "response" && mode == "done" && (string(raw) != response || meta.Status != "complete" || meta.AttemptID != a.ID) {
					t.Fatal("response archive incomplete")
				}
				if meta.Direction == "response" && mode == "output_timeout" && meta.Status != "partial" {
					t.Fatal("interrupted response marked complete")
				}
			}
		})
	}
}

func TestBodyAPIAndMultipartMetadata(t *testing.T) {
	db := logTestDB(t)
	db.AutoMigrate(&domain.LogBody{})
	s, err := logarchive.New(db, t.TempDir(), 64<<20, 10<<30)
	if err != nil {
		t.Fatal(err)
	}
	opsSvc := &ops.Service{DB: db, Archives: s}
	h := &Gateway{Ops: opsSvc}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("prompt", "text field")
	part, _ := mw.CreateFormFile("image", "input.png")
	_, _ = part.Write([]byte("secret-binary"))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/v1/images/edits", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	h.archiveRequest(1, req, body.Bytes())
	s.Wait()
	var meta domain.LogBody
	db.First(&meta, "request_log_id = ?", 1)
	if meta.Status != "omitted" || meta.ReceivedBytes != int64(body.Len()) {
		t.Fatalf("meta=%+v", meta)
	}
	admin := &Admin{DB: db, Ops: opsSvc}
	engine := gin.New()
	engine.Use(middleware.AdminAuth("test-admin"))
	engine.GET("/logs/:id/bodies/:body_id", admin.GetLogBody)
	get := func(path, token string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		engine.ServeHTTP(w, r)
		return w
	}
	path := "/logs/1/bodies/" + meta.ID
	if w := get(path, ""); w.Code != 401 {
		t.Fatalf("unauthorized=%d", w.Code)
	}
	if w := get("/logs/2/bodies/"+meta.ID, "test-admin"); w.Code != 404 {
		t.Fatalf("cross log=%d", w.Code)
	}
	if w := get(path+"?offset=-1", "test-admin"); w.Code != 400 {
		t.Fatalf("offset=%d", w.Code)
	}
	w := get(path+"?download=1", "test-admin")
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-binary") || !strings.Contains(w.Body.String(), "input.png") || !strings.Contains(w.Body.String(), "text field") {
		t.Fatalf("metadata download=%d %s", w.Code, w.Body.String())
	}
	capture := s.Begin(2, "", "response", "text/plain")
	text := strings.Repeat("\u4e2d", 100000)
	_, _ = capture.Write([]byte(text))
	capture.Finish(true, "")
	s.Wait()
	meta = domain.LogBody{}
	db.First(&meta, "request_log_id = ?", 2)
	// Read by returned byte offsets to exercise UTF-8 boundaries.
	var collected strings.Builder
	offset := 0
	for {
		w = get(fmt.Sprintf("/logs/2/bodies/%s?offset=%d", meta.ID, offset), "test-admin")
		var result struct {
			Data struct {
				Text string `json:"text"`
				Next int    `json:"next_offset"`
				EOF  bool   `json:"eof"`
			}
		}
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatalf("page=%d %s", w.Code, w.Body.String())
		}
		collected.WriteString(result.Data.Text)
		if result.Data.EOF {
			break
		}
		if result.Data.Next <= offset {
			t.Fatal("offset did not advance")
		}
		offset = result.Data.Next
	}
	if collected.String() != text {
		t.Fatal("UTF-8 pagination corrupted content")
	}
}

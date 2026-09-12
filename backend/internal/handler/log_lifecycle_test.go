package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/upstream"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type fixturePicker struct {
	picker.Picker
	key *domain.PlatformKey
	up  *domain.Upstream
}

func (p fixturePicker) Settings() domain.SchedulerSettings { return domain.DefaultSchedulerSettings() }
func (p fixturePicker) Pick(context.Context, picker.Request) (*domain.PlatformKey, *domain.Upstream, error) {
	return p.key, p.up, nil
}
func (p fixturePicker) Observe(context.Context, uint, string, bool, int64, int64, int64, int) {}
func (p fixturePicker) SetSticky(context.Context, string, string, uint)                       {}
func (p fixturePicker) Cooldown(context.Context, uint)                                        {}
func (p fixturePicker) MarkLowBalance(context.Context, uint)                                  {}

func TestGatewayHTTPCompletion(t *testing.T) {
	for _, mode := range []string{"sync", "stream", "request-stream-json-response", "stream-error", "log-create-failure"} {
		t.Run(mode, func(t *testing.T) {
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "stream" || mode == "stream-error" {
					w.Header().Set("Content-Type", "text/event-stream")
					if mode == "stream-error" {
						_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\"}\n\n")
					} else {
						_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":8}}\n\ndata: [DONE]\n\n")
					}
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":3,"completion_tokens":8}}`)
			}))
			defer upstreamServer.Close()
			db := logTestDB(t)
			enc, err := crypto.New(strings.Repeat("01", 32))
			if err != nil {
				t.Fatal(err)
			}
			secret, _ := enc.Encrypt("fixture")
			up := domain.Upstream{Name: "fixture", BaseURL: upstreamServer.URL, Kind: domain.KindOpenAICompat, Protocols: "openai"}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			consumer := domain.ConsumerKey{Name: "fixture", Key: "sk-fixture", Status: domain.StatusEnabled}
			if err := db.Create(&consumer).Error; err != nil {
				t.Fatal(err)
			}
			h := NewGateway(db, enc, nil, fixturePicker{key: &key, up: &up})
			if mode == "log-create-failure" {
				failed := false
				if err := db.Callback().Create().Before("gorm:create").Register("test:log_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "request_logs" && !failed {
						failed = true
						tx.AddError(errors.New("injected log creation failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			r := gin.New()
			r.POST("/v1/chat/completions", h.ChatCompletions)
			server := httptest.NewServer(r)
			defer server.Close()
			stream := mode != "sync"
			body := `{"stream":false}`
			if stream {
				body = `{"stream":true}`
			}
			req, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer sk-fixture")
			req.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 2 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			var row domain.RequestLog
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if err := db.First(&row).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatal(err)
				}
				if row.ID > 0 && !row.InFlight {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if row.ID == 0 || row.InFlight || row.Stream != stream || !row.StreamKnown || row.Success != (mode != "stream-error") || row.CompletedAt == nil {
				t.Fatalf("state: inflight=%v stream=%v known=%v success=%v", row.InFlight, row.Stream, row.StreamKnown, row.Success)
			}
			if mode != "stream-error" && row.OutputTokens != 8 {
				t.Fatalf("usage output=%d", row.OutputTokens)
			}
		})
	}
}

func TestTerminalLogRejectsLateUpdates(t *testing.T) {
	db := logTestDB(t)
	h := &Gateway{DB: db}
	row := domain.RequestLog{InFlight: true}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	final := map[string]any{"in_flight": false, "success": true, "duration_ms": 123, "output_tokens": 42, "log_revision": 3}
	if err := h.persistLog(ctx, row.ID, 3, final); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []uint64{1, 2, 4} {
		late := map[string]any{"in_flight": true, "success": false, "duration_ms": 999, "output_tokens": 1, "log_revision": revision}
		if err := h.persistLog(ctx, row.ID, revision, late); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.InFlight || !row.Success || row.DurationMs != 123 || row.OutputTokens != 42 || row.LogRevision != 3 {
		t.Fatalf("terminal overwritten: %+v", row)
	}
}

func TestLogKeepsOriginalStreamAndStart(t *testing.T) {
	db := logTestDB(t)
	h := &Gateway{DB: db}
	body := `{"prompt":"` + strings.Repeat("x", 70000) + `","stream":true}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	snap := captureInbound(req, []byte(body))
	snap.StartedAt = time.Now().Add(-time.Second)
	lg := h.beginLog(nil, "anthropic", "model", req.URL.Path, "test", "", snap)
	if lg == nil {
		t.Fatal("missing log")
	}
	var row domain.RequestLog
	if err := db.First(&row, lg.id).Error; err != nil {
		t.Fatal(err)
	}
	if !row.Stream || !row.StreamKnown || !row.RequestBodyTrunc || !row.CreatedAt.Equal(snap.StartedAt) {
		t.Fatalf("initial stream=%v known=%v truncated=%v start=%s", row.Stream, row.StreamKnown, row.RequestBodyTrunc, row.CreatedAt)
	}
	h.finishLog(lg, nil, nil, nil, "anthropic", "model", req.URL.Path, "test", "", 400, false, upstream.TokenUsage{}, 0, 1000, "bad request", snap)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := db.First(&row, lg.id).Error; err != nil {
			t.Fatal(err)
		}
		if !row.InFlight {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if row.InFlight || !row.Stream || !row.StreamKnown || row.CompletedAt == nil {
		t.Fatalf("final in_flight=%v stream=%v known=%v completed_at=%v", row.InFlight, row.Stream, row.StreamKnown, row.CompletedAt)
	}
}

func TestSuccessfulFailoverLogClearsPreviousFailure(t *testing.T) {
	db := logTestDB(t)
	h := &Gateway{DB: db}
	snap := ioCapture{StartedAt: time.Now()}
	lg := h.beginLog(nil, domain.ProtocolOpenAI, "model", "/v1/chat/completions", "failover-success", "", snap)
	if lg == nil {
		t.Fatal("missing log")
	}
	lg.markFailure(h, failureScopeProvider, "cooldown_provider")
	h.finishLog(lg, nil, nil, nil, domain.ProtocolOpenAI, "model", "/v1/chat/completions", "failover-success", "", http.StatusOK, true, upstream.TokenUsage{}, 0, 10, "", snap)

	var row domain.RequestLog
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := db.First(&row, lg.id).Error; err != nil {
			t.Fatal(err)
		}
		if !row.InFlight {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if row.InFlight || !row.Success || row.FailureScope != "" || row.FailureAction != "" {
		t.Fatalf("final log: inflight=%v success=%v scope=%q action=%q", row.InFlight, row.Success, row.FailureScope, row.FailureAction)
	}
}

func TestSSETerminalWithoutEOF(t *testing.T) {
	for _, terminal := range []string{
		"data: [DONE]\r\n\r\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\n" + "data: \"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":8}}}\n\n",
	} {
		t.Run(terminal, func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			col := &streamCollector{start: time.Now()}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			done := make(chan error, 1)
			go func() { done <- copySSE(c.Writer, r, col, true, nil) }()
			for _, piece := range []string{terminal[:5], terminal[5:]} {
				if _, err := io.WriteString(w, piece); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("waited for EOF after terminal")
			}
			if rec.Body.String() != terminal {
				t.Fatalf("terminal not preserved: %q", rec.Body.String())
			}
			if strings.Contains(terminal, "response.completed") && col.usage.OutputTokens != 8 {
				t.Fatalf("usage=%+v", col.usage)
			}
		})
	}
}

func TestSSEBoundedEventAndFailure(t *testing.T) {
	col := &streamCollector{start: time.Now()}
	for i := 0; i < 2048; i++ {
		col.feed([]byte("data: " + strings.Repeat("x", 1024) + "\n"))
	}
	if len(col.eventData) > 0 || !col.eventOversized {
		t.Fatal("event buffer is unbounded")
	}
	col.feed([]byte("\nevent: error\ndata: {\"type\":\"error\"}\n\n"))
	if !col.terminal || col.terminalErr == nil {
		t.Fatal("error event was not terminal")
	}
}

func TestSyncTTFTBeforeCompletion(t *testing.T) {
	col := &streamCollector{start: time.Now().Add(-20 * time.Millisecond)}
	capture := &usageCapture{limit: 1024, onBytes: col.noteBytes}
	_, _ = capture.Write([]byte("first"))
	first := col.ttftMs
	col.start = col.start.Add(-time.Second)
	_, _ = capture.Write([]byte("last"))
	if first == 0 || col.ttftMs != first || int(time.Since(col.start).Milliseconds())-col.ttftMs < 900 {
		t.Fatal("TTFT moved to completion")
	}
}

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/gin-gonic/gin"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
)

const testRateLimitsFrame = "data: {\"type\":\"codex.rate_limits\",\"rate_limits\":{\"allowed\":true,\"limit_reached\":false}}\n\n"
const testResponseDelta = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"
const testResponseCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"

func TestResponsesRateLimitsStream(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		ok, hasFirst bool
	}{
		{"before output", testRateLimitsFrame + testResponseDelta + testResponseCompleted, true, true},
		{"between output", testResponseDelta + testRateLimitsFrame + testResponseDelta + testResponseCompleted, true, true},
		{"repeated metadata", testRateLimitsFrame + testRateLimitsFrame + testResponseCompleted, true, false},
		{"event name only", "event: codex.rate_limits\ndata: {\"rate_limits\":{\"allowed\":true}}\n\n" + testResponseCompleted, true, false},
		{"text in metadata", "event: codex.rate_limits\ndata: {\"delta\":{\"content\":\"metadata\"}}\n\n" + testResponseCompleted, true, false},
		{"metadata then eof", testRateLimitsFrame, false, false},
		{"metadata then failure", testRateLimitsFrame + "data: {\"type\":\"response.failed\"}\n\n", false, false},
		{"metadata then error", testRateLimitsFrame + "data: {\"type\":\"error\",\"message\":\"failed\"}\n\n", false, false},
		{"metadata with error", "data: {\"type\":\"codex.rate_limits\",\"error\":{\"message\":\"failed\"}}\n\n" + testResponseCompleted, false, false},
		{"metadata then wrong terminal", testRateLimitsFrame + "data: [DONE]\n\n", false, false},
		{"metadata then malformed completion", testRateLimitsFrame + "data: {\"type\":\"response.completed\"}\n\n", false, false},
		{"unknown extension", "data: {\"type\":\"codex.unknown\"}\n\n" + testResponseCompleted, false, false},
		{"failure after output", testResponseDelta + testRateLimitsFrame + "data: {\"type\":\"response.failed\"}\n\n", false, true},
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
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				col := &streamCollector{start: time.Now(), protocolPath: "/v1/responses", strict: true}
				err := copySSE(c.Writer, src, col, true, nil)
				if (err == nil) != tc.ok || (col.ttftMs > 0) != tc.hasFirst {
					t.Fatalf("error=%v ttft=%d", err, col.ttftMs)
				}
				if tc.ok && rec.Body.String() != tc.body {
					t.Fatal("accepted stream was not forwarded unchanged")
				}
				if !tc.ok && !tc.hasFirst && c.Writer.Written() {
					t.Fatal("invalid stream committed before failover")
				}
			})
		}
	}
}

func TestResponsesRateLimitsDoNotReleaseOrStopFirstTokenWatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	watch := &firstTokenWatch{}
	watch.start(cancel, 50*time.Millisecond, time.Now().Add(time.Second))
	defer watch.stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.WriteString(writer, testRateLimitsFrame+testRateLimitsFrame)
		<-ctx.Done()
		_ = writer.CloseWithError(ctx.Err())
	}()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	col := &streamCollector{start: time.Now(), protocolPath: "/v1/responses", strict: true}
	err := copySSE(c.Writer, reader, col, true, watch.stop)
	if err == nil || !watch.timedOut() || col.ttftMs != 0 || c.Writer.Written() {
		t.Errorf("error=%v timedOut=%v ttft=%d written=%v", err, watch.timedOut(), col.ttftMs, c.Writer.Written())
	}
	cancel()
	_ = reader.Close()
	<-done
}

func TestStrictStreamValidation(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		ok, hasFirst     bool
	}{
		{"text greeting", "/v1/responses", "Hi! What can I help you with?", false, false},
		{"early eof", "/v1/responses", "data: {\"type\":\"response.created\"}\n\n", false, false},
		{"error before output", "/v1/responses", "data: {\"type\":\"response.failed\"}\n\n", false, false},
		{"empty completed", "/v1/responses", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n", true, false},
		{"tool only", "/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", true, true},
		{"refusal", "/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"refusal\":\"No\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", true, true},
		{"wrong terminal", "/v1/responses", "data: [DONE]\n\n", false, false},
		{"malformed completion", "/v1/responses", "data: {\"type\":\"response.completed\"}\n\n", false, false},
		{"anthropic empty", "/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			col := &streamCollector{start: time.Now(), strict: true, protocolPath: tc.path}
			err := copySSE(c.Writer, strings.NewReader(tc.body), col, true, nil)
			if (err == nil) != tc.ok || (col.ttftMs > 0) != tc.hasFirst {
				t.Fatalf("err=%v ttft=%d", err, col.ttftMs)
			}
			if !tc.ok && !tc.hasFirst && c.Writer.Written() {
				t.Fatal("invalid stream committed before failover")
			}
		})
	}
}

func TestJSONProtocolValidation(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		ok         bool
	}{
		{"/v1/responses", `{"id":"r","object":"response","status":"completed","output":[]}`, true},
		{"/v1/responses", `{"id":"r","object":"response","status":"failed","output":[]}`, false},
		{"/v1/responses", `{"error":{"message":"bad"}}`, false},
		{"/v1/chat/completions", `{"choices":[{"message":{"role":"assistant","tool_calls":[]},"finish_reason":"tool_calls"}]}`, true},
		{"/v1/messages", `{"type":"message","content":[],"stop_reason":"end_turn"}`, true},
		{"/v1/messages", `{}`, false},
	} {
		_, err := validateJSONResponse(tc.path, []byte(tc.body))
		if (err == nil) != tc.ok {
			t.Fatalf("%s: %v", tc.body, err)
		}
	}
}

func TestAttemptTimingAndNeutralResults(t *testing.T) {
	db := logTestDB(t)
	h := &Gateway{DB: db}
	key := &domain.PlatformKey{ID: 1}
	now := time.Now()
	col := &streamCollector{start: now.Add(-32 * time.Second), firstAt: now, ttftMs: 32000}
	a := domain.RequestAttempt{ID: "once", Protocol: "openai", Model: "m", StartedAt: now.Add(-2 * time.Second)}
	h.completeAttempt(context.Background(), key, &a, col, forwardOutcome{validSuccess: true})
	h.completeAttempt(context.Background(), key, &a, col, forwardOutcome{validSuccess: true})
	var rows []domain.RequestAttempt
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TTFTMs != 2000 {
		t.Fatalf("attempt timing/dedup: %+v", rows)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := domain.RequestAttempt{ID: "cancel", StartedAt: now}
	h.completeAttempt(ctx, key, &b, nil, forwardOutcome{})
	if b.Result != "client_cancelled" {
		t.Fatal(b.Result)
	}
	b = domain.RequestAttempt{ID: "busy"}
	h.completeAttempt(context.Background(), key, &b, nil, forwardOutcome{capacityBusy: true})
	if b.Result != "capacity_rejected" {
		t.Fatal(b.Result)
	}
}

func TestGatewayResponseValidationRouting(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		name := "greeting fails over once"
		if metadata {
			name = "rate limits complete without failover"
		}
		t.Run(name, func(t *testing.T) {
			var badCalls, goodCalls atomic.Int32
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				badCalls.Add(1)
				if metadata {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, testRateLimitsFrame+testResponseDelta+testRateLimitsFrame+testResponseCompleted)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "Hi! What can I help you with?")
			}))
			defer bad.Close()
			good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				goodCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer good.Close()
			db := logTestDB(t)
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("test")
			for _, url := range []string{bad.URL, good.URL} {
				up := domain.Upstream{Name: url, BaseURL: url, Kind: domain.KindOpenAICompat, Protocols: "openai", Status: domain.StatusEnabled}
				if err := db.Create(&up).Error; err != nil {
					t.Fatal(err)
				}
				key := domain.PlatformKey{UpstreamID: up.ID, Name: url, EncryptedKey: secret, Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy, RateMultiplier: 1}
				if err := db.Create(&key).Error; err != nil {
					t.Fatal(err)
				}
			}
			consumer := domain.ConsumerKey{Name: "test", Key: "test", Status: domain.StatusEnabled}
			if err := db.Create(&consumer).Error; err != nil {
				t.Fatal(err)
			}
			p := picker.NewBand(db, nil)
			cfg := p.Settings()
			cfg.RankingMode = "stable_latency"
			cfg.ExplorationRatio = 0
			cfg.RetryMax = 5
			if err := p.UpdateSettings(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			h := NewGateway(db, enc, nil, p)
			r := gin.New()
			r.POST("/v1/responses", h.Responses)
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"hello"}`))
			req.Header.Set("Authorization", "Bearer test")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 200 || strings.Contains(w.Body.String(), "Hi!") || badCalls.Load() != 1 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, badCalls.Load(), w.Body.String())
			}
			var attempts []domain.RequestAttempt
			db.Order("started_at").Find(&attempts)
			if metadata {
				if len(attempts) != 1 || attempts[0].Result != "success" || goodCalls.Load() != 0 || !strings.Contains(w.Body.String(), "codex.rate_limits") {
					t.Fatalf("metadata triggered retry/failover: attempts=%+v fallback_calls=%d", attempts, goodCalls.Load())
				}
				var gates []domain.RoutingCircuit
				if err := db.Find(&gates).Error; err != nil {
					t.Fatal(err)
				}
				for _, gate := range gates {
					if gate.Open || gate.Failures != 0 {
						t.Fatalf("metadata affected routing health: %+v", gate)
					}
				}
			} else if len(attempts) != 2 || attempts[0].Result != "upstream_failure" || attempts[0].TTFTMs != 0 || attempts[1].Result != "success" || goodCalls.Load() != 1 {
				t.Fatalf("attempts %+v", attempts)
			}
			var log domain.RequestLog
			db.First(&log)
			var trace []selectionTraceEvent
			_ = json.Unmarshal([]byte(log.SelectionTrace), &trace)
			if len(trace) == 0 || trace[len(trace)-1].Decision == nil || !metadata && trace[len(trace)-1].Reason != "failover" {
				t.Fatalf("decision overwritten: %+v", trace)
			}
		})
	}
}

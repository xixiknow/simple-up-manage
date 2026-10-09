package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"

	"github.com/gin-gonic/gin"
)

// bestEffortFixture builds a gateway with a real BandPicker and a bound route
// group holding n keys served by the same upstream, so failover walks keys in
// fixed order.
func bestEffortFixture(t *testing.T, cfg func(*domain.SchedulerSettings)) (*Gateway, *picker.BandPicker) {
	t.Helper()
	db := logTestDB(t)
	enc, err := crypto.New(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	var keys []domain.PlatformKey
	up := domain.Upstream{Name: "fixture", BaseURL: "http://unused", Kind: domain.KindOpenAICompat, Protocols: "openai", Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		secret, err := enc.Encrypt(fmt.Sprintf("upstream-fixture-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		k := domain.PlatformKey{UpstreamID: up.ID, Name: fmt.Sprintf("key-%d", i), EncryptedKey: secret, Status: domain.StatusEnabled, RateMultiplier: 1}
		if err := db.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	consumer := domain.ConsumerKey{Name: "test", Key: "test", Status: domain.StatusEnabled}
	if err := db.Create(&consumer).Error; err != nil {
		t.Fatal(err)
	}
	group := domain.RouteGroup{Name: "g", Models: domain.JSONStrings{"m"}, Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: consumer.ID, RouteGroupID: group.ID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := db.Create(&domain.RouteGroupKey{RouteGroupID: group.ID, PlatformKeyID: k.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	p := picker.NewBand(db, nil)
	settings := p.Settings()
	settings.RankingMode = "fixed_order"
	settings.StickyOpenAI = true
	settings.ExplorationRatio = 0
	settings.RetryMax = 0
	settings.FilterByModels = nil
	if cfg != nil {
		cfg(&settings)
	}
	if err := p.UpdateSettings(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	h := NewGateway(db, enc, nil, p)
	return h, p
}

// With FailoverExhaustPool enabled the gateway keeps switching keys past the
// FailoverMax cap until the pool is exhausted; the client sees the last
// upstream failure as a 502.
func TestGatewayExhaustPoolAttemptsEveryKey(t *testing.T) {
	var calls atomic.Int32
	var bearer []string
	var mu atomic.Pointer[[]string]
	mu.Store(&bearer)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b := mu.Load()
		*b = append(*b, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream overloaded"}}`))
	}))
	defer server.Close()

	h, _ := bestEffortFixture(t, func(s *domain.SchedulerSettings) {
		s.FailoverMax = 2
		s.FailoverExhaustPool = true
	})
	if err := h.DB.Model(&domain.Upstream{}).Where("1=1").Update("base_url", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	h.firstTokenWait = time.Second

	engine := gin.New()
	engine.POST("/v1/chat/completions", h.ChatCompletions)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("attempts=%d, want 3 (every key despite FailoverMax=2)", got)
	}
	seen := map[string]bool{}
	for _, b := range *mu.Load() {
		if seen[b] {
			t.Fatalf("key retried across failover: %v", *mu.Load())
		}
		seen[b] = true
	}
	if !strings.Contains(w.Body.String(), "upstream overloaded") {
		t.Fatalf("last upstream error not preserved: %s", w.Body.String())
	}
}

// The default (exhaust pool off) keeps the FailoverMax cap: FailoverMax=2
// stops after two keys even with three available.
func TestGatewayDefaultStopsAtFailoverMax(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer server.Close()

	h, _ := bestEffortFixture(t, func(s *domain.SchedulerSettings) {
		s.FailoverMax = 2
	})
	if err := h.DB.Model(&domain.Upstream{}).Where("1=1").Update("base_url", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	h.firstTokenWait = time.Second

	engine := gin.New()
	engine.POST("/v1/chat/completions", h.ChatCompletions)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts=%d, want 2 (FailoverMax cap)", got)
	}
}

// A mid-pool key succeeds: the request returns 200 without trying the rest.
func TestGatewayExhaustPoolSucceedsMidPool(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	h, _ := bestEffortFixture(t, func(s *domain.SchedulerSettings) {
		s.FailoverExhaustPool = true
	})
	if err := h.DB.Model(&domain.Upstream{}).Where("1=1").Update("base_url", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	h.firstTokenWait = time.Second

	engine := gin.New()
	engine.POST("/v1/chat/completions", h.ChatCompletions)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts=%d, want 2", got)
	}
}

// responsesFixture wires a streaming /v1/responses gateway against an upstream
// that replays scripted SSE writes per attempt. hold enables the stream
// hold-until-token setting.
func responsesFixture(t *testing.T, hold bool, script func(attempt int, w http.ResponseWriter)) (*Gateway, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		script(int(calls.Add(1)), w)
	}))
	h, p := bestEffortFixture(t, func(s *domain.SchedulerSettings) {
		s.FailoverMax = 2
		s.FailoverFirstTokenWaitSec = 1
		s.StreamHoldUntilToken = hold
	})
	if err := h.DB.Model(&domain.Upstream{}).Where("1=1").Update("base_url", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	_ = p
	h.firstTokenWait = 2 * time.Second
	return h, server, &calls
}

func serveResponses(h *Gateway, body string) *httptest.ResponseRecorder {
	engine := gin.New()
	engine.POST("/v1/responses", h.Responses)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func sseWrite(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	sseEvents(w, events...)
}

// sseEvents writes events onto an already-committed response.
func sseEvents(w http.ResponseWriter, events ...string) {
	for _, e := range events {
		_, _ = w.Write([]byte(e + "\n\n"))
	}
	w.(http.Flusher).Flush()
}

const responsesCreatedEvent = `event: response.created
data: {"type":"response.created","response":{"id":"r1"}}`

const responsesFailedEvent = `event: response.failed
data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"Our servers are currently overloaded. Please try again later."}}}`

const responsesCompletedEvent = `event: response.completed
data: {"type":"response.completed","response":{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}`

// Hold-until-token: a response.failed that arrives before any text token is
// retried on the second key instead of being forwarded as a 200 stream.
func TestResponsesHoldUntilTokenFailoverOnStreamError(t *testing.T) {
	h, _, calls := responsesFixture(t, true, func(attempt int, w http.ResponseWriter) {
		switch attempt {
		case 1:
			sseWrite(w, responsesCreatedEvent, responsesFailedEvent)
		default:
			sseWrite(w, responsesCreatedEvent, responsesCompletedEvent)
		}
	})
	_ = h

	w := serveResponses(h, `{"model":"m","stream":true}`)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts=%d, want 2 (failed attempt retried)", got)
	}
	body := w.Body.String()
	if strings.Contains(body, "response.failed") || strings.Contains(body, "overloaded") {
		t.Fatalf("upstream failure leaked to client: %s", body)
	}
	if !strings.Contains(body, "response.completed") {
		t.Fatalf("successful stream not delivered: %s", body)
	}
}

// Without the hold, the pre-token response.failed is forwarded as-is (current
// behavior): one attempt, 200, failure event visible to the client.
func TestResponsesDefaultForwardsPreTokenStreamError(t *testing.T) {
	h, _, calls := responsesFixture(t, false, func(attempt int, w http.ResponseWriter) {
		sseWrite(w, responsesCreatedEvent, responsesFailedEvent)
	})
	_ = h

	w := serveResponses(h, `{"model":"m","stream":true}`)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts=%d, want 1 (failure forwarded)", got)
	}
	if !strings.Contains(w.Body.String(), "response.failed") {
		t.Fatalf("failure event not forwarded: %s", w.Body.String())
	}
}

// Commit-on-timeout: a stream that produced bytes (response.created) but no
// text token before the first-token budget expires is a live, merely slow
// upstream — the watch commits the held response instead of cancelling, so a
// slow reasoning model is not killed per key. This mirrors Aether's
// pre-commit budget (commit_on_timeout=true once bytes flowed).
func TestResponsesHoldUntilTokenCommitsOnBudgetWithBytes(t *testing.T) {
	h, _, calls := responsesFixture(t, true, func(attempt int, w http.ResponseWriter) {
		sseWrite(w, responsesCreatedEvent)
		time.Sleep(1500 * time.Millisecond)
		sseEvents(w, responsesCompletedEvent)
	})
	_ = h

	w := serveResponses(h, `{"model":"m","stream":true}`)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts=%d, want 1 (salvaged, no failover)", got)
	}
	if !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("stream not delivered: %s", w.Body.String())
	}
}

// A fully silent upstream (zero body bytes) still fails over at the budget:
// commit-on-timeout only rescues attempts with observable progress.
func TestResponsesHoldUntilTokenSilentStillFailsOver(t *testing.T) {
	h, _, calls := responsesFixture(t, true, func(attempt int, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(1500 * time.Millisecond)
	})
	_ = h

	start := time.Now()
	w := serveResponses(h, `{"model":"m","stream":true}`)
	elapsed := time.Since(start)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts=%d, want 2", got)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("elapsed=%v, budget not applied per attempt", elapsed)
	}
}

// An in-budget response.failed still failovers even though bytes flowed: the
// watch never fires, and the failed attempt must not be committed.
func TestResponsesHoldUntilTokenErrorBeforeBudgetStillFailsOver(t *testing.T) {
	h, _, calls := responsesFixture(t, true, func(attempt int, w http.ResponseWriter) {
		switch attempt {
		case 1:
			sseWrite(w, responsesCreatedEvent, responsesFailedEvent)
		default:
			sseWrite(w, responsesCreatedEvent, responsesCompletedEvent)
		}
	})
	_ = h

	w := serveResponses(h, `{"model":"m","stream":true}`)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts=%d, want 2", got)
	}
	if strings.Contains(w.Body.String(), "overloaded") {
		t.Fatalf("failed attempt committed to client: %s", w.Body.String())
	}
}

// Keepalive bytes without text keep the hold pending; once the budget expires
// with bytes flowing the attempt commits and the later text still arrives.
func TestResponsesHoldUntilTokenKeepaliveThenTextCommits(t *testing.T) {
	h, _, calls := responsesFixture(t, true, func(attempt int, w http.ResponseWriter) {
		sseWrite(w, responsesCreatedEvent, "event: ping\ndata: {\"type\":\"ping\"}")
		time.Sleep(1500 * time.Millisecond)
		sseEvents(w, responsesCompletedEvent)
	})
	_ = h

	w := serveResponses(h, `{"model":"m","stream":true}`)

	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts=%d, want 1", got)
	}
	if !strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("stream not delivered: %s", w.Body.String())
	}
}

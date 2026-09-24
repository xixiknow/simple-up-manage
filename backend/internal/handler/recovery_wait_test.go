package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/routinghealth"
)

func recoveryGateway(t *testing.T, upstreamHandler http.HandlerFunc, probes bool) (*Gateway, *gin.Engine, routinghealth.Dimension) {
	t.Helper()
	server := httptest.NewServer(upstreamHandler)
	t.Cleanup(server.Close)
	db := logTestDB(t)
	enc, _ := crypto.New(strings.Repeat("01", 32))
	secret, _ := enc.Encrypt("test")
	up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Protocols: "openai", Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	key := domain.PlatformKey{Name: "fixture", UpstreamID: up.ID, EncryptedKey: secret, Status: domain.StatusEnabled, ProbeEnabled: &probes}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	ck := domain.ConsumerKey{Name: "fixture", Key: "test", Status: domain.StatusEnabled}
	if err := db.Create(&ck).Error; err != nil {
		t.Fatal(err)
	}
	p := picker.NewBand(db, nil)
	h := NewGateway(db, enc, nil, p)
	engine := gin.New()
	engine.POST("/v1/responses", h.Responses)
	dim := routinghealth.Dimension{KeyID: key.ID, Protocol: "openai", Model: "m", Path: "/v1/responses", Stream: true}
	gate := domain.RoutingCircuit{Scope: dim.Scope(), PlatformKeyID: key.ID, Protocol: dim.Protocol, Model: dim.Model, Path: dim.Path, Stream: true, Open: true, Until: time.Now().Add(-time.Minute)}
	if err := db.Create(&gate).Error; err != nil {
		t.Fatal(err)
	}
	return h, engine, dim
}

func recoveryRequest(ctx context.Context) *http.Request {
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func startRecoveryRequest(ctx context.Context, engine *gin.Engine) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); engine.ServeHTTP(w, recoveryRequest(ctx)); done <- w }()
	return done
}

func receiveRecovery(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
		return nil
	}
}

func waitForRecoveryLog(t *testing.T, db *gorm.DB) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Model(&domain.RequestLog{}).Where("selection_trace LIKE ?", "%recovery_in_progress%").Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("request never entered recovery wait")
}

func TestRecoveryFirstOutputAdmitsWaitingRequestBeforeStreamEnds(t *testing.T) {
	for _, end := range []string{"success", "failure", "cancel"} {
		t.Run(end, func(t *testing.T) {
			metaSent := make(chan struct{})
			output := make(chan struct{})
			finish := make(chan struct{})
			var calls atomic.Int32
			h, engine, dim := recoveryGateway(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if n == 1 {
					io.WriteString(w, "data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"r\"}}\n\n")
					w.(http.Flusher).Flush()
					close(metaSent)
					select {
					case <-output:
					case <-r.Context().Done():
						return
					}
					io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n")
					w.(http.Flusher).Flush()
					select {
					case <-finish:
					case <-r.Context().Done():
						return
					}
					if end == "failure" {
						io.WriteString(w, "data: {\"type\":\"response.failed\",\"error\":{\"message\":\"failed mid-stream\"}}\n\n")
						return
					}
				}
				io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
			}, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := startRecoveryRequest(ctx, engine)
			select {
			case <-metaSent:
			case <-time.After(3 * time.Second):
				t.Fatal("first never reached upstream")
			}
			second := startRecoveryRequest(ctx, engine)
			waitForRecoveryLog(t, h.DB)
			if calls.Load() != 1 {
				t.Fatal("metadata released recovery")
			}
			close(output)
			if w := receiveRecovery(t, second); w.Code != 200 {
				t.Fatalf("second=%d %s", w.Code, w.Body.String())
			}
			select {
			case <-first:
				t.Fatal("first stream already finished")
			default:
			}
			if end == "cancel" {
				cancel()
			} else {
				close(finish)
			}
			receiveRecovery(t, first)
			var gate domain.RoutingCircuit
			if err := h.DB.First(&gate, "scope = ?", dim.Scope()).Error; err != nil {
				t.Fatal(err)
			}
			want := 0
			if end == "failure" {
				want = 1
			}
			if gate.Open || gate.Failures != want {
				t.Fatalf("final gate=%+v", gate)
			}
			var attempts []domain.RequestAttempt
			h.DB.Order("started_at").Find(&attempts)
			if len(attempts) != 2 {
				t.Fatalf("attempts=%d", len(attempts))
			}
			result := map[string]string{"success": "success", "failure": "upstream_failure", "cancel": "client_cancelled"}[end]
			if attempts[0].Result != result {
				t.Fatalf("first result=%s", attempts[0].Result)
			}
			var budget domain.RoutingBudget
			if err := h.DB.Where("scope NOT LIKE ?", "recovery:%").First(&budget).Error; err != nil {
				t.Fatal(err)
			}
			if budget.Counter != 2 {
				t.Fatalf("wait consumed exploration slots: %d", budget.Counter)
			}
		})
	}
}

func TestRecoveryWaitForDedicatedCheck(t *testing.T) {
	var calls atomic.Int32
	h, engine, dim := recoveryGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}, true)
	health := routinghealth.Store{DB: h.DB}
	token, err := health.ClaimCheck(context.Background(), dim.Scope(), dim)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startRecoveryRequest(ctx, engine)
	waitForRecoveryLog(t, h.DB)
	if calls.Load() != 0 {
		t.Fatal("sent before check completed")
	}
	if err := health.FinishCheck(ctx, dim.Scope(), token, true, false, "", 0); err != nil {
		t.Fatal(err)
	}
	if w := receiveRecovery(t, done); w.Code != 200 {
		t.Fatalf("response=%d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestRecoveryWaitTimeoutCancellationAndHardRejection(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "disabled", "cooldown"} {
		t.Run(mode, func(t *testing.T) {
			h, engine, dim := recoveryGateway(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") }, true)
			health := routinghealth.Store{DB: h.DB}
			if mode != "cooldown" {
				if _, err := health.ClaimCheck(context.Background(), dim.Scope(), dim); err != nil {
					t.Fatal(err)
				}
			} else {
				h.DB.Model(&domain.RoutingCircuit{}).Where("scope = ?", dim.Scope()).Update("until", time.Now().Add(time.Minute))
			}
			if mode == "disabled" {
				h.DB.Model(&domain.PlatformKey{}).Where("id = ?", dim.KeyID).Update("status", domain.StatusDisabled)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := time.Now()
			done := startRecoveryRequest(ctx, engine)
			if mode == "cancel" {
				waitForRecoveryLog(t, h.DB)
				cancel()
			}
			// timeout deliberately uses the production five-second bound.
			var w *httptest.ResponseRecorder
			select {
			case w = <-done:
			case <-time.After(7 * time.Second):
				t.Fatal("unbounded wait")
			}
			if mode == "cancel" {
				if w.Code != 499 {
					t.Fatalf("cancel status %d", w.Code)
				}
				return
			}
			if w.Code != 503 {
				t.Fatalf("status=%d %s", w.Code, w.Body.String())
			}
			elapsed := time.Since(started)
			if mode == "timeout" {
				if elapsed < 5*time.Second || elapsed > 6*time.Second {
					t.Fatalf("wait duration=%v", elapsed)
				}
				if !strings.Contains(w.Body.String(), "recovery is still in progress") {
					t.Fatal(w.Body.String())
				}
			} else if elapsed > time.Second {
				t.Fatalf("hard rejection waited: %v", elapsed)
			}
			var n int64
			h.DB.Model(&domain.RequestAttempt{}).Count(&n)
			if n != 0 {
				t.Fatalf("local waits generated %d attempts", n)
			}
		})
	}
}

func TestRecoveryOutputSignals(t *testing.T) {
	for _, tc := range []struct {
		name, path, event string
		want              int
	}{
		{"metadata", "/v1/responses", `{"type":"response.created","response":{"id":"r"}}`, 0},
		{"compaction", "/v1/responses", `{"type":"response.compaction.compacting"}`, 0},
		{"keepalive", "/v1/responses", `{"type":"keepalive"}`, 0},
		{"reasoning", "/v1/responses", `{"type":"response.reasoning_text.delta","delta":"thinking"}`, 1},
		{"tool", "/v1/responses", `{"type":"response.function_call_arguments.delta","delta":"{}"}`, 1},
		{"chat", "/v1/chat/completions", `{"choices":[{"delta":{"content":"hi"}}]}`, 1},
		{"anthropic", "/v1/messages", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`, 1},
		{"error", "/v1/responses", `{"type":"response.output_text.delta","delta":"hi","error":{"message":"bad"}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			col := &streamCollector{strict: true, protocolPath: tc.path, start: time.Now(), onFirstOutput: func() { n++ }}
			col.feed([]byte(fmt.Sprintf("data: %s\n\ndata: %s\n\n", tc.event, tc.event)))
			if n != tc.want {
				t.Fatalf("callback count=%d", n)
			}
		})
	}
}

func TestRecoveryDoesNotDelayHealthyAlternative(t *testing.T) {
	var calls atomic.Int32
	h, engine, dim := recoveryGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}, true)
	health := routinghealth.Store{DB: h.DB}
	if _, err := health.ClaimCheck(context.Background(), dim.Scope(), dim); err != nil {
		t.Fatal(err)
	}
	var original domain.PlatformKey
	if err := h.DB.First(&original, dim.KeyID).Error; err != nil {
		t.Fatal(err)
	}
	original.ID = 0
	original.Name = "alternate"
	if err := h.DB.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, recoveryRequest(context.Background()))
	if w.Code != 200 || time.Since(start) > time.Second {
		t.Fatalf("healthy alternative delayed: %d %v", w.Code, time.Since(start))
	}
	var log domain.RequestLog
	h.DB.First(&log)
	if strings.Contains(log.SelectionTrace, "recovery_in_progress") || log.PlatformKeyID == nil || *log.PlatformKeyID != original.ID {
		t.Fatalf("route %+v", log)
	}
}

func TestRecoveryPollCoalescesWithoutSharingCancellation(t *testing.T) {
	db := logTestDB(t)
	scope := "poll-fixture"
	if err := db.Create(&domain.RoutingCircuit{Scope: scope, Open: true}).Error; err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var reads atomic.Int32
	if err := db.Callback().Query().Before("gorm:query").Register("test:hold-poll", func(tx *gorm.DB) {
		if tx.Statement.Table == "routing_circuits" {
			if reads.Add(1) == 1 {
				close(entered)
			}
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	var poll recoveryPoller
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := poll.get(ctx, db, scope); first <- err }()
	<-entered
	cancel()
	select {
	case err := <-first:
		if err != context.Canceled {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation blocked")
	}
	const n = 10
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { _, err := poll.get(context.Background(), db, scope); done <- err }()
	}
	close(release)
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("coalesced reads=%d", reads.Load())
	}
}

func TestRecoveryAdmissionRaceReselectsWithoutExcludingKey(t *testing.T) {
	var calls atomic.Int32
	h, engine, dim := recoveryGateway(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}, false)
	health := routinghealth.Store{DB: h.DB}
	ownerReady := make(chan *routinghealth.Admission, 1)
	var once atomic.Bool
	// Steal the recovery lease just after selection, before the gateway admits it.

	p := h.Picker.(*picker.BandPicker)
	h.Picker = &raceDecisionPicker{BandPicker: p, afterPick: func() {
		if once.CompareAndSwap(false, true) {
			owner, err := health.AdmitRequest(context.Background(), dim, true)
			if err != nil {
				t.Error(err)
			}
			ownerReady <- owner
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startRecoveryRequest(ctx, engine)
	waitForRecoveryLog(t, h.DB)
	owner := <-ownerReady
	if owner == nil {
		t.Fatal("race owner missing")
	}
	if _, err := health.ReleaseOnOutput(ctx, dim, owner); err != nil {
		t.Fatal(err)
	}
	if w := receiveRecovery(t, done); w.Code != 200 {
		t.Fatalf("request=%d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("attempts=%d", calls.Load())
	}
}

type raceDecisionPicker struct {
	*picker.BandPicker
	afterPick func()
}

func (p *raceDecisionPicker) PickDecision(ctx context.Context, req picker.Request) (*domain.PlatformKey, *domain.Upstream, picker.Decision, error) {
	key, up, d, err := p.BandPicker.PickDecision(ctx, req)
	if err == nil {
		p.afterPick()
	}
	return key, up, d, err
}

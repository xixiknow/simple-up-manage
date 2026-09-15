package handler

import (
	"context"
	"encoding/json"
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

type traceFixturePicker struct {
	fixturePicker
	settings domain.SchedulerSettings
	keys     []domain.PlatformKey
	ups      []domain.Upstream
	picks    int
}

func (p *traceFixturePicker) Settings() domain.SchedulerSettings { return p.settings }
func (p *traceFixturePicker) Pick(context.Context, picker.Request) (*domain.PlatformKey, *domain.Upstream, error) {
	if p.picks >= len(p.keys) {
		return nil, nil, picker.ErrNoUpstream
	}
	i := p.picks
	p.picks++
	return &p.keys[i], &p.ups[i], nil
}

func TestSelectionTracePersistsAttemptResults(t *testing.T) {
	for _, tc := range []struct {
		name       string
		retries    int
		attempts   int
		statuses   []int
		wantRetry  int
		wantLastID int
		wantResult string
	}{
		{"retry succeeds", 1, 1, []int{500, 200}, 1, 0, "selected"},
		{"disabled retry switches same named keys", 0, 2, []int{500, 200}, 0, 1, "selected"},
		{"exhausted retry fails", 1, 1, []int{500, 500}, 1, 0, "failed"},
		{"non retryable response fails", 1, 1, []int{400}, 0, 0, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(calls.Add(1)) - 1
				if i >= len(tc.statuses) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statuses[i])
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			db := logTestDB(t)
			enc, err := crypto.New(strings.Repeat("01", 32))
			if err != nil {
				t.Fatal(err)
			}
			secret, err := enc.Encrypt("fixture")
			if err != nil {
				t.Fatal(err)
			}
			p := &traceFixturePicker{settings: domain.DefaultSchedulerSettings()}
			p.settings.RetryMax, p.settings.FailoverMax = tc.retries, tc.attempts
			for i := 0; i < tc.attempts; i++ {
				up := domain.Upstream{Name: "provider-" + string(rune('A'+i)), BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: "openai"}
				if err := db.Create(&up).Error; err != nil {
					t.Fatal(err)
				}
				key := domain.PlatformKey{UpstreamID: up.ID, Name: "same-name", EncryptedKey: secret}
				if err := db.Create(&key).Error; err != nil {
					t.Fatal(err)
				}
				p.ups = append(p.ups, up)
				p.keys = append(p.keys, key)
			}
			consumer := domain.ConsumerKey{Name: "fixture", Key: "sk-fixture", Status: domain.StatusEnabled}
			if err := db.Create(&consumer).Error; err != nil {
				t.Fatal(err)
			}
			h := NewGateway(db, enc, nil, p)
			router := gin.New()
			router.POST("/v1/chat/completions", h.ChatCompletions)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"fixture"}`))
			req.Header.Set("Authorization", "Bearer sk-fixture")
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(httptest.NewRecorder(), req)
			var row domain.RequestLog
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				if err := db.First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if !row.InFlight {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if row.InFlight || int(calls.Load()) != len(tc.statuses) {
				t.Fatalf("in_flight=%v calls=%d", row.InFlight, calls.Load())
			}
			var trace []selectionTraceEvent
			if err := json.Unmarshal([]byte(row.SelectionTrace), &trace); err != nil {
				t.Fatal(err)
			}
			if len(trace) == 0 {
				t.Fatal("missing trace")
			}
			last := trace[len(trace)-1]
			if last.KeyID != p.keys[tc.wantLastID].ID || last.UpstreamName != p.ups[tc.wantLastID].Name || last.Result != tc.wantResult {
				t.Fatalf("unexpected final event: %+v", last)
			}
			if tc.wantResult == "selected" && last.Reason != "success" {
				t.Fatalf("success reason lost: %+v", last)
			}
			retries := 0
			for _, event := range trace {
				retries += event.RetryCount
				if event.Result == "retry" && event.Reason != "cooldown_provider" {
					t.Fatalf("retry reason lost: %+v", event)
				}
			}
			if retries != tc.wantRetry {
				t.Fatalf("retries=%d want=%d trace=%+v", retries, tc.wantRetry, trace)
			}
		})
	}
}

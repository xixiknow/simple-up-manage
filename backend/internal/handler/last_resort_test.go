package handler

import (
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

// A first-token timeout on the only usable key must not 502 the client while
// the exclusion tables are empty: the gateway performs one clean re-pick (last
// resort) and retries the transient failure with the shortened first-token wait.
func TestGatewayLastResortAfterFirstTokenTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()
	db := logTestDB(t)
	enc, _ := crypto.New(strings.Repeat("01", 32))
	secret, _ := enc.Encrypt("test")
	up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: "openai", Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret, Status: domain.StatusEnabled}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	ck := domain.ConsumerKey{Name: "test", Key: "test", Status: domain.StatusEnabled}
	if err := db.Create(&ck).Error; err != nil {
		t.Fatal(err)
	}
	p := picker.NewBand(db, nil)
	cfg := p.Settings()
	cfg.ExplorationRatio = 0
	if err := p.UpdateSettings(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	h := NewGateway(db, enc, nil, p)
	h.firstTokenWait = 60 * time.Millisecond
	engine := gin.New()
	engine.POST("/v1/responses", h.Responses)
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	// Attempt 0 FTTs and excludes the only key; the re-pick finds no candidate
	// so the last resort runs one more attempt before the client sees a 502.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts=%d", calls.Load())
	}
}

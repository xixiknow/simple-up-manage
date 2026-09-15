package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/routinghealth"
)

func TestGatewayCountsFailedRequestsNotRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body.Model == "bad" {
			w.WriteHeader(500)
			w.Write([]byte(`{"error":{"message":"unavailable"}}`))
			return
		}
		w.Write([]byte(`{"id":"r","object":"response","status":"completed","output":[]}`))
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
	cfg.RetryMax = 2
	cfg.ExplorationRatio = 0
	if err := p.UpdateSettings(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	h := NewGateway(db, enc, nil, p)
	engine := gin.New()
	engine.POST("/v1/responses", h.Responses)
	dim := routinghealth.Dimension{KeyID: key.ID, Protocol: "openai", Model: "bad", Path: "/v1/responses"}
	request := func(model string) int {
		r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q}`, model)))
		r.Header.Set("Authorization", "Bearer test")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, r)
		return w.Code
	}
	for i := 1; i <= 3; i++ {
		if code := request("bad"); code != 502 {
			t.Fatalf("response=%d", code)
		}
		var r domain.RoutingCircuit
		if err := db.First(&r, "scope = ?", dim.Scope()).Error; err != nil {
			t.Fatal(err)
		}
		if r.Failures != i || r.Open != (i == 3) {
			t.Fatalf("after %d requests: %+v", i, r)
		}
	}
	if calls.Load() != 9 {
		t.Fatalf("attempts=%d", calls.Load())
	}
	if code := request("bad"); code != 503 || calls.Load() != 9 {
		t.Fatalf("open circuit sent traffic: %d %d", code, calls.Load())
	}
	if code := request("good"); code != 200 {
		t.Fatalf("unrelated model blocked: %d", code)
	}
	var after domain.Upstream
	db.First(&after, up.ID)
	if after.CooldownUntil != nil {
		t.Fatal("model failure cooled provider")
	}
	// Wait for the existing asynchronous log writer before closing the fixture.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		db.Model(&domain.RequestLog{}).Where("in_flight = ?", true).Count(&n)
		if n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRetryAfterAndCapabilityClassification(t *testing.T) {
	now := time.Now()
	if got := parseRetryAfter("4", now); got != 4*time.Second {
		t.Fatal(got)
	}
	if got := parseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now); got < 89*time.Second || got > 90*time.Second {
		t.Fatal(got)
	}
	for _, raw := range []string{"-2", "nonsense", "0"} {
		if parseRetryAfter(raw, now) != 0 {
			t.Fatal(raw)
		}
	}
	for _, code := range []string{"model_not_found", "unsupported_endpoint"} {
		o := classifyHTTPFailure(400, []byte(fmt.Sprintf(`{"error":{"code":%q}}`, code)))
		if !o.failOver || o.scope != failureScopeKeyModel || o.neutral {
			t.Fatal(o)
		}
	}
	if o := classifyHTTPFailure(400, []byte(`{"error":{"code":"invalid_request"}}`)); o.failOver || !o.neutral {
		t.Fatal(o)
	}
	if o := classifyHTTPFailure(403, []byte(`{"error":{"code":"invalid_api_key"}}`)); o.scope != failureScopeKey {
		t.Fatal(o)
	}
}

func TestClientErrorBodyIsNotTruncatedOrCountedAsUpstreamFailure(t *testing.T) {
	body := `{"error":{"message":"` + strings.Repeat("x", maxLogBodyBytes*2) + `"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Diagnostic", "preserved")
		w.WriteHeader(400)
		w.Write([]byte(body))
	}))
	defer server.Close()
	db := logTestDB(t)
	enc, _ := crypto.New(strings.Repeat("01", 32))
	secret, _ := enc.Encrypt("test")
	up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Protocols: "openai", Status: domain.StatusEnabled}
	db.Create(&up)
	key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret, Status: domain.StatusEnabled}
	db.Create(&key)
	ck := domain.ConsumerKey{Name: "test", Key: "test", Status: domain.StatusEnabled}
	db.Create(&ck)
	p := picker.NewBand(db, nil)
	h := NewGateway(db, enc, nil, p)
	engine := gin.New()
	engine.POST("/v1/responses", h.Responses)
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m"}`))
	r.Header.Set("Authorization", "Bearer test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	if w.Code != 400 || w.Body.String() != body || w.Header().Get("X-Upstream-Diagnostic") != "preserved" {
		t.Fatalf("error response changed: status=%d bytes=%d", w.Code, w.Body.Len())
	}
	var a domain.RequestAttempt
	if err := db.First(&a).Error; err != nil {
		t.Fatal(err)
	}
	if a.Result != "request_rejected" {
		t.Fatal(a.Result)
	}
	var gate domain.RoutingCircuit
	db.First(&gate, "scope = ?", (routinghealth.Dimension{KeyID: key.ID, Protocol: "openai", Model: "m", Path: "/v1/responses"}).Scope())
	if gate.Open || gate.Failures != 0 {
		t.Fatal(gate)
	}
}

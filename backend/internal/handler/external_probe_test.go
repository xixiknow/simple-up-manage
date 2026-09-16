package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/externalprobe"
	"simple-up-manage/internal/picker"

	"github.com/gin-gonic/gin"
)

const externalRP = "Compute exactly, using standard math precedence (multiplication before add and subtract): 4 * 8 * 4. Reply with ONLY RP_ANSWER=N where N is the integer result (it may be negative). No other text."

func externalPayload(path, prompt string, tool bool) []byte {
	body := map[string]any{"model": "m", "stream": false}
	field := "messages"
	if path == "/v1/responses" {
		field = "input"
	}
	body[field] = []any{map[string]any{"role": "user", "content": prompt}}
	if tool {
		body["tools"] = []any{map[string]any{"type": "function", "name": "probe_ping", "parameters": map[string]any{"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}}}}
		body["tool_choice"] = "required"
	}
	raw, _ := json.Marshal(body)
	return raw
}

func serveExternal(h *Gateway, path string, raw []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	c.Request.Header.Set("Authorization", "Bearer sk-dashboard")
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("X-Session-Id", "external-fixture")
	switch path {
	case "/v1/responses":
		h.Responses(c)
	case "/v1/messages":
		h.Messages(c)
	default:
		h.ChatCompletions(c)
	}
	return w
}

func externalSuccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/v1/responses":
		io.WriteString(w, `{"id":"r-fixture","object":"response","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"RP_ANSWER=128"}]}],"usage":{"input_tokens":1500,"output_tokens":1}}`)
	case "/v1/messages":
		io.WriteString(w, `{"id":"msg-fixture","type":"message","role":"assistant","content":[{"type":"text","text":"RP_ANSWER=128"}],"stop_reason":"end_turn","usage":{"input_tokens":1500,"output_tokens":1}}`)
	default:
		io.WriteString(w, dashboardResponse)
	}
}

func externalFixture(t *testing.T, server *httptest.Server) (*Gateway, *picker.BandPicker, []domain.PlatformKey, *domain.ConsumerKey) {
	t.Helper()
	h, first, consumer := dashboardGatewayFixture(t, server)
	if err := h.DB.Model(&domain.Upstream{}).Where("id = ?", first.UpstreamID).Update("protocols", "openai,anthropic").Error; err != nil {
		t.Fatal(err)
	}
	keys := []domain.PlatformKey{*first}
	for i := 1; i <= 2; i++ {
		secret, err := h.Enc.Encrypt(fmt.Sprintf("key-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		k := domain.PlatformKey{UpstreamID: first.UpstreamID, Name: fmt.Sprintf("key-%d", i), EncryptedKey: secret, Status: domain.StatusEnabled, RateMultiplier: 1}
		if err := h.DB.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	p := picker.NewBand(h.DB, nil)
	cfg := p.Settings()
	cfg.RankingMode = "fixed_order"
	cfg.StickyOpenAI = true
	cfg.RetryMax = 1
	cfg.FailoverMax = 3
	cfg.ExplorationRatio = 0
	if err := p.UpdateSettings(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	h.Picker = p
	t.Cleanup(func() { waitExternalLogs(t, h) })
	return h, p, keys, consumer
}

func waitExternalLogs(t *testing.T, h *Gateway) []domain.RequestLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var rows []domain.RequestLog
		if err := h.DB.Order("id").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		pending := false
		for _, r := range rows {
			pending = pending || r.InFlight
		}
		if !pending {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("request logs did not finish")
	return nil
}

func bindExternalGroup(t *testing.T, h *Gateway, consumerID uint, keys []domain.PlatformKey) {
	t.Helper()
	g := domain.RouteGroup{Name: "probe-group", Models: domain.JSONStrings{"m"}}
	if err := h.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.DB.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: consumerID, RouteGroupID: g.ID}).Error; err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := h.DB.Create(&domain.RouteGroupKey{RouteGroupID: g.ID, PlatformKeyID: k.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestExternalProbeRoutingAndBusinessAccounting(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		for _, bound := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bound=%v", path, bound), func(t *testing.T) {
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Header.Get("Authorization") == "Bearer upstream-fixture" {
						t.Error("sent to protected key")
					}
					externalSuccess(w, r)
				}))
				defer server.Close()
				h, p, keys, consumer := externalFixture(t, server)
				if bound {
					bindExternalGroup(t, h, consumer.ID, keys[:2])
				}
				if err := h.DB.Model(&keys[0]).Update("probe_enabled", false).Error; err != nil {
					t.Fatal(err)
				}
				header := http.Header{"X-Session-Id": {"external-fixture"}}
				session, _, _ := picker.RequestSession(header, nil)
				protocol := "openai"
				if path == "/v1/messages" {
					protocol = "anthropic"
				}
				p.SetSticky(context.Background(), protocol, session, keys[0].ID)
				w := serveExternal(h, path, externalPayload(path, externalRP, false))
				if w.Code != 200 || calls.Load() != 1 {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
				}
				logs := waitExternalLogs(t, h)
				if len(logs) != 1 || logs[0].ExternalProbeRule == nil || *logs[0].ExternalProbeRule != externalprobe.RPArithmetic || logs[0].Source != domain.SourceBusiness {
					t.Fatalf("logs=%+v", logs)
				}
				if bound && (logs[0].PlatformKeyID == nil || *logs[0].PlatformKeyID != keys[1].ID) {
					t.Fatal("escaped bound group")
				}
				h.Dash.Stop()
				var facts []dashboard.RequestFact
				if err := h.DB.Find(&facts).Error; err != nil {
					t.Fatal(err)
				}
				if len(facts) != 1 || facts[0].Source != domain.SourceBusiness || facts[0].HTTPAttempts != 1 {
					t.Fatalf("facts=%+v", facts)
				}
				var attempts []dashboard.AttemptFact
				if err := h.DB.Find(&attempts).Error; err != nil {
					t.Fatal(err)
				}
				if len(attempts) != 1 || attempts[0].ConsumptionUSD == nil || *attempts[0].ConsumptionUSD <= 0 {
					t.Fatal("missing external probe cost")
				}
				snap := h.Dash.Metrics.Snapshot()
				if snap.BusinessRPM != 1 || snap.UpstreamRPM != 1 {
					t.Fatalf("metrics=%+v", snap)
				}
			})
		}
	}
}

func TestExternalProbeAllDisabledAndOrdinaryToolTraffic(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); externalSuccess(w, r) }))
	defer server.Close()
	h, _, keys, consumer := externalFixture(t, server)
	// A third, probe-enabled key exists outside the group and must never rescue it.
	bindExternalGroup(t, h, consumer.ID, keys[:2])
	if err := h.DB.Model(&domain.PlatformKey{}).Where("id IN ?", []uint{keys[0].ID, keys[1].ID}).Update("probe_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	w := serveExternal(h, "/v1/responses", externalPayload("/v1/responses", externalRP, false))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "probe_disabled") || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	for _, tc := range []struct {
		prompt string
		tool   bool
	}{
		{"hi", false}, {"who are you", false},
		{"Call the probe_ping function with ok=true to acknowledge readiness. You must use the tool.", true},
	} {
		w = serveExternal(h, "/v1/responses", externalPayload("/v1/responses", tc.prompt, tc.tool))
		if w.Code != 200 {
			t.Fatalf("ordinary request blocked: %s", w.Body.String())
		}
	}
	logs := waitExternalLogs(t, h)
	if len(logs) != 4 {
		t.Fatalf("logs=%d", len(logs))
	}
	for _, r := range logs[1:] {
		if r.ExternalProbeRule != nil {
			t.Fatal("ordinary request tagged")
		}
	}
	var failures int64
	h.DB.Model(&domain.RequestAttempt{}).Where("result = ?", "upstream_failure").Count(&failures)
	if failures != 0 {
		t.Fatal("local rejection recorded as upstream failure")
	}
	// Unbound all-disabled and completely empty pools remain distinct.
	h.DB.Where("consumer_key_id = ?", consumer.ID).Delete(&domain.ConsumerRouteGroup{})
	h.DB.Model(&keys[2]).Update("probe_enabled", false)
	w = serveExternal(h, "/v1/responses", externalPayload("/v1/responses", externalRP, false))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "probe_disabled") {
		t.Fatalf("unbound: %s", w.Body.String())
	}
	h.DB.Model(&domain.PlatformKey{}).Where("id > 0").Update("status", domain.StatusDisabled)
	w = serveExternal(h, "/v1/responses", externalPayload("/v1/responses", externalRP, false))
	if w.Code != 503 || strings.Contains(w.Body.String(), "probe_disabled") {
		t.Fatalf("empty pool: %s", w.Body.String())
	}
}

type closeProbeOnAcquire struct {
	*picker.BandPicker
	once  sync.Once
	close func()
}

func (p *closeProbeOnAcquire) TryAcquireAttempt(k *domain.PlatformKey, u *domain.Upstream) (func(bool), string) {
	p.once.Do(p.close)
	return p.BandPicker.TryAcquireAttempt(k, u)
}

func TestExternalProbeRechecksSwitchBeforeSendAndRetry(t *testing.T) {
	for _, mode := range []string{"before-send", "same-key-retry", "retry-all-disabled", "failover"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var h *Gateway
			var keys []domain.PlatformKey
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") == "Bearer upstream-fixture" {
					if mode == "before-send" {
						raw, _ := io.ReadAll(r.Body)
						if strings.Contains(string(raw), externalRP) {
							t.Error("protected key was sent")
						}
						externalSuccess(w, r)
						return
					}
					if mode != "failover" {
						if err := h.DB.Model(&keys[0]).Update("probe_enabled", false).Error; err != nil {
							t.Error(err)
						}
					}
					w.WriteHeader(500)
					io.WriteString(w, `{"error":{"message":"retry fixture"}}`)
					return
				}
				externalSuccess(w, r)
			}))
			defer server.Close()
			var p *picker.BandPicker
			var consumer *domain.ConsumerKey
			h, p, keys, consumer = externalFixture(t, server)
			groupKeys := keys[:2]
			if mode == "retry-all-disabled" {
				groupKeys = keys[:1]
			}
			bindExternalGroup(t, h, consumer.ID, groupKeys)
			cfg := p.Settings()
			cfg.FailoverMax = 1
			if mode == "failover" {
				cfg.FailoverMax = 2
				cfg.RetryMax = 0
			}
			if err := p.UpdateSettings(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			if mode == "before-send" {
				if err := h.DB.Model(&keys[0]).Update("rpm_limit", 1).Error; err != nil {
					t.Fatal(err)
				}
				h.Picker = &closeProbeOnAcquire{BandPicker: p, close: func() {
					if err := h.DB.Model(&keys[0]).Update("probe_enabled", false).Error; err != nil {
						t.Error(err)
					}
				}}
			}
			w := serveExternal(h, "/v1/chat/completions", externalPayload("/v1/chat/completions", externalRP, false))
			wantStatus, wantCalls := 200, int32(2)
			if mode == "before-send" {
				wantCalls = 1
			}
			if mode == "retry-all-disabled" {
				wantStatus = 503
				wantCalls = 1
			}
			if w.Code != wantStatus || calls.Load() != wantCalls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
			if mode == "retry-all-disabled" && !strings.Contains(w.Body.String(), "probe_disabled") {
				t.Fatal("missing probe_disabled")
			}
			waitExternalLogs(t, h)
			if mode != "failover" {
				var skipped []domain.RequestAttempt
				h.DB.Where("result = ?", domain.ProbeSkipDisabled).Find(&skipped)
				if len(skipped) != 1 {
					t.Fatalf("skipped=%+v", skipped)
				}
				var circuits []domain.RoutingCircuit
				h.DB.Where("platform_key_id = ?", keys[0].ID).Find(&circuits)
				for _, c := range circuits {
					if c.Failures != 0 || c.Open {
						t.Fatalf("local skip damaged circuit: %+v", c)
					}
				}
				h.Dash.Stop()
				var attempts []dashboard.AttemptFact
				h.DB.Where("result = ?", domain.ProbeSkipDisabled).Find(&attempts)
				if len(attempts) != 1 || attempts[0].HTTPSent || attempts[0].ConsumptionUSD == nil || *attempts[0].ConsumptionUSD != 0 {
					t.Fatalf("skip cost=%+v", attempts)
				}
			}
			if mode == "before-send" {
				// The skipped key has RPM=1. Force the next ordinary request to it:
				// a local skip must not spend the one available business RPM slot.
				if err := h.DB.Model(&keys[1]).Update("status", domain.StatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
				cfg.RetryMax = 0
				if err := p.UpdateSettings(t.Context(), cfg); err != nil {
					t.Fatal(err)
				}
				before := calls.Load()
				ordinary := serveExternal(h, "/v1/chat/completions", externalPayload("/v1/chat/completions", "hi", false))
				if ordinary.Code != 200 || calls.Load() != before+1 {
					t.Fatal("local probe skip consumed business RPM")
				}
			}
		})
	}
}

func TestExternalProbeLogFilterAndDetail(t *testing.T) {
	h := &Admin{DB: logTestDB(t)}
	rules := []string{externalprobe.RPArithmetic, externalprobe.ExampleArithmetic, externalprobe.HealthManager}
	for _, rule := range rules {
		if err := h.DB.Create(&domain.RequestLog{ExternalProbeRule: &rule}).Error; err != nil {
			t.Fatal(err)
		}
	}
	h.DB.Create(&domain.RequestLog{})
	for _, rule := range append([]string{"any"}, rules...) {
		page := getAdminPage[logPage](t, h.ListRequestLogs, "external_probe_rule="+rule)
		want := 1
		if rule == "any" {
			want = 3
		}
		if page.Total != want || len(page.Items) != want {
			t.Fatalf("rule=%s page=%+v", rule, page)
		}
		for _, r := range page.Items {
			if r.ExternalProbeRule == nil || (rule != "any" && *r.ExternalProbeRule != rule) {
				t.Fatalf("row=%+v", r)
			}
		}
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/?external_probe_rule=probe_ping", nil)
	h.ListRequestLogs(c)
	if w.Code != 400 {
		t.Fatalf("invalid rule status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/request-logs/1", nil)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.GetRequestLog(c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"external_probe_rule":"rp_arithmetic"`) {
		t.Fatalf("detail=%s", w.Body.String())
	}
}

func TestExternalProbeMultipleDynamicSkips(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); externalSuccess(w, r) }))
	defer server.Close()
	h, p, keys, consumer := externalFixture(t, server)
	bindExternalGroup(t, h, consumer.ID, keys)
	cfg := p.Settings()
	cfg.FailoverMax = 1
	if err := p.UpdateSettings(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	h.Picker = &closeProbeOnAcquire{BandPicker: p, close: func() {
		if err := h.DB.Model(&domain.PlatformKey{}).Where("id > 0").Update("probe_enabled", false).Error; err != nil {
			t.Error(err)
		}
	}}
	w := serveExternal(h, "/v1/responses", externalPayload("/v1/responses", externalRP, false))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "probe_disabled") || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	waitExternalLogs(t, h)
	var skips int64
	if err := h.DB.Model(&domain.RequestAttempt{}).Where("result = ?", domain.ProbeSkipDisabled).Count(&skips).Error; err != nil {
		t.Fatal(err)
	}
	if skips != 3 {
		t.Fatalf("local skips=%d", skips)
	}
	candidates, err := p.Explain(t.Context(), picker.Request{Protocol: "openai", Model: "m", Path: "/v1/responses"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.CurrentRPM != 0 || candidate.KeyInflight != 0 {
			t.Fatalf("local skip reserved capacity: %+v", candidate)
		}
	}
}

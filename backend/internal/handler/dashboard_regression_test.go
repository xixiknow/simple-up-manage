package handler

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const dashboardResponse = `{"choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1500,"prompt_tokens_details":{"cached_tokens":500},"completion_tokens":0}}`

func dashboardGatewayFixture(t *testing.T, server *httptest.Server) (*Gateway, *domain.PlatformKey, *domain.ConsumerKey) {
	t.Helper()
	db := logTestDB(t)
	enc, err := crypto.New(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := enc.Encrypt("upstream-fixture")
	if err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "dashboard", BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: "openai", Status: domain.StatusEnabled}
	key := domain.PlatformKey{Name: "dashboard", EncryptedKey: secret, RateMultiplier: 1, Status: domain.StatusEnabled}
	consumer := domain.ConsumerKey{Name: "dashboard", Key: "sk-dashboard", Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	key.UpstreamID = up.ID
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&consumer).Error; err != nil {
		t.Fatal(err)
	}
	version := dashboard.CatalogVersion{Source: "test", PublishedAt: time.Now()}
	if err := db.Create(&version).Error; err != nil {
		t.Fatal(err)
	}
	price := dashboard.CatalogVersionPrice{VersionID: version.ID, Vendor: "test", ModelID: "m", InputCost: 1, OutputCost: 1, CacheReadCoeff: .1, CacheWriteCoeff: 1.25}
	if err := db.Create(&price).Error; err != nil {
		t.Fatal(err)
	}
	h := NewGateway(db, enc, nil, fixturePicker{key: &key, up: &up})
	h.Dash = dashboard.New(db)
	t.Cleanup(h.Dash.Stop)
	return h, &key, &consumer
}

func serveDashboardRequest(h *Gateway, body io.Reader, source string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-dashboard")
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(dashboard.WithSource(req.Context(), source))
	h.ChatCompletions(c)
	return rec
}

func TestDashboardGatewayCachedCostAndDiagnosticIsolation(t *testing.T) {
	for _, tc := range []struct {
		source     string
		logFailure bool
	}{
		{domain.SourceBusiness, false}, {domain.SourceAdminTest, false},
		{domain.SourceBusiness, true}, {domain.SourceAdminTest, true},
	} {
		name := tc.source
		if tc.logFailure {
			name += "/log_failure"
		}
		t.Run(name, func(t *testing.T) {
			source := tc.source
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, dashboardResponse)
			}))
			defer server.Close()
			h, _, _ := dashboardGatewayFixture(t, server)
			if tc.logFailure {
				var failed atomic.Bool
				if err := h.DB.Callback().Create().Before("gorm:create").Register("test:dashboard_log_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "request_logs" && failed.CompareAndSwap(false, true) {
						tx.AddError(errors.New("injected log write failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			rec := serveDashboardRequest(h, strings.NewReader(`{"model":"m"}`), source)
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			h.Dash.Stop()
			var facts []dashboard.RequestFact
			if err := h.DB.Find(&facts).Error; err != nil {
				t.Fatal(err)
			}
			if len(facts) != 1 || facts[0].CompletedAt == nil || facts[0].HTTPAttempts != 1 || facts[0].Source != source {
				t.Fatalf("facts=%+v", facts)
			}
			var attempts []dashboard.AttemptFact
			if err := h.DB.Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 || attempts[0].EstimatedCostUSD == nil {
				t.Fatalf("attempts=%+v", attempts)
			}
			a := attempts[0]
			if math.Abs(*a.EstimatedCostUSD-.00105) > 1e-8 {
				t.Fatalf("cache cost=%f", *a.EstimatedCostUSD)
			}
			if a.ReportedCostUSD != nil || a.CostSource != dashboard.CostSourceEstimated {
				t.Fatalf("fabricated reported expense: %+v", a)
			}
			_, depth, _ := h.Dash.DataQualityExtra()
			if depth != 0 {
				t.Fatalf("unmatched diagnostic events=%d", depth)
			}
			if source == domain.SourceAdminTest {
				var n int64
				h.DB.Model(&dashboard.MinuteAgg{}).Count(&n)
				if n != 0 {
					t.Fatalf("diagnostic polluted business aggregates: %d", n)
				}
				snap := h.Dash.Metrics.Snapshot()
				if snap.BusinessRPM != 0 || snap.UpstreamRPM != 0 {
					t.Fatalf("diagnostic polluted live metrics: %+v", snap)
				}
			}
		})
	}
}

func TestDashboardProbeSwitchRecheckedBeforeRetry(t *testing.T) {
	var db *gorm.DB
	var keyID uint
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := db.Model(&domain.PlatformKey{}).Where("id = ?", keyID).Update("probe_enabled", false).Error; err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"retry"}}`)
	}))
	defer server.Close()
	h, key, _ := dashboardGatewayFixture(t, server)
	db, keyID = h.DB, key.ID
	rec := serveDashboardRequest(h, strings.NewReader(`{"model":"m"}`), domain.SourceAdminTest)
	if calls.Load() != 1 {
		t.Fatalf("sent %d probes after disabling", calls.Load())
	}
	if !strings.Contains(rec.Body.String(), domain.ProbeSkipDisabled) {
		t.Fatalf("missing skip reason: %s", rec.Body.String())
	}
	h.Dash.Stop()
	_, depth, _ := h.Dash.DataQualityExtra()
	if depth != 0 {
		t.Fatalf("unsettled events=%d", depth)
	}
}

func TestDashboardDisabledProbeStillServesBusiness(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, dashboardResponse)
	}))
	defer server.Close()
	h, key, _ := dashboardGatewayFixture(t, server)
	if err := h.DB.Model(key).Update("probe_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	skipped := serveDashboardRequest(h, strings.NewReader(`{"model":"m"}`), domain.SourceAdminTest)
	if calls.Load() != 0 || !strings.Contains(skipped.Body.String(), domain.ProbeSkipDisabled) {
		t.Fatalf("probe was not skipped: %s", skipped.Body.String())
	}
	actual := serveDashboardRequest(h, strings.NewReader(`{"model":"m"}`), domain.SourceBusiness)
	if actual.Code != 200 || calls.Load() != 1 {
		t.Fatalf("business status=%d calls=%d", actual.Code, calls.Load())
	}
	h.Dash.Stop()
	_, depth, _ := h.Dash.DataQualityExtra()
	if depth != 0 {
		t.Fatalf("skipped probe left pending events=%d", depth)
	}
}

type dashboardPickCapture struct {
	fixturePicker
	request picker.Request
}

func (p *dashboardPickCapture) Pick(_ context.Context, r picker.Request) (*domain.PlatformKey, *domain.Upstream, error) {
	p.request = r
	return p.key, p.up, nil
}

type bindingChangeReader struct {
	io.Reader
	change func()
}

func (r *bindingChangeReader) Read(p []byte) (int, error) {
	if r.change != nil {
		r.change()
		r.change = nil
	}
	return r.Reader.Read(p)
}

func TestDashboardBindingSnapshotSurvivesBodyUpload(t *testing.T) {
	for _, mode := range []string{"rebind", "unbind", "initially_unbound"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, dashboardResponse)
			}))
			defer server.Close()
			h, key, consumer := dashboardGatewayFixture(t, server)
			capture := &dashboardPickCapture{fixturePicker: h.Picker.(fixturePicker)}
			h.Picker = capture
			a := domain.RouteGroup{Name: "A", Status: domain.StatusEnabled}
			b := domain.RouteGroup{Name: "B", Status: domain.StatusEnabled}
			for _, g := range []*domain.RouteGroup{&a, &b} {
				if err := h.DB.Create(g).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := h.DB.Create(&domain.RouteGroupKey{RouteGroupID: a.ID, PlatformKeyID: key.ID}).Error; err != nil {
				t.Fatal(err)
			}
			if mode != "initially_unbound" {
				if err := h.DB.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: consumer.ID, RouteGroupID: a.ID}).Error; err != nil {
					t.Fatal(err)
				}
			}
			body := &bindingChangeReader{Reader: strings.NewReader(`{"model":"m"}`), change: func() {
				if err := h.DB.Where("consumer_key_id = ?", consumer.ID).Delete(&domain.ConsumerRouteGroup{}).Error; err != nil {
					t.Fatal(err)
				}
				if mode != "unbind" {
					if err := h.DB.Create(&domain.ConsumerRouteGroup{ConsumerKeyID: consumer.ID, RouteGroupID: b.ID}).Error; err != nil {
						t.Fatal(err)
					}
				}
			}}
			rec := serveDashboardRequest(h, body, domain.SourceBusiness)
			if rec.Code != 200 {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if mode == "initially_unbound" {
				if capture.request.AllowKeys != nil {
					t.Fatalf("unbound snapshot changed: %+v", capture.request.AllowKeys)
				}
			} else if _, ok := capture.request.AllowKeys[key.ID]; !ok || len(capture.request.AllowKeys) != 1 {
				t.Fatalf("bound snapshot lost: %+v", capture.request.AllowKeys)
			}
		})
	}
}

package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
)

func TestStructuredFailuresIsolation(t *testing.T) {
	for _, tc := range []struct {
		code                int
		body, scope, action string
		delay               time.Duration
	}{
		{403, `{"code":"GROUP_DELETED"}`, failureScopeKey, "credential_disabled", time.Hour},
		{401, `{"error":{"code":"API_KEY_DISABLED"}}`, failureScopeKey, "credential_disabled", time.Hour},
		{403, `{"code":"INSUFFICIENT_BALANCE"}`, failureScopeKey, "key_quota_exhausted", 15 * time.Minute},
		{403, `{"error":{"type":"billing_error","message":"insufficient balance"}}`, failureScopeKey, "key_quota_exhausted", 15 * time.Minute},
		{403, `{"error":{"type":"permission_error"}}`, failureScopeKeyModel, "request_scope_failure", 0},
		{404, `{"error":{"code":"model_not_found"}}`, failureScopeKeyModel, "capability_unsupported", 15 * time.Minute},
		{503, `{"error":{"code":"model_not_found"}}`, failureScopeKeyModel, "capability_unsupported", 15 * time.Minute},
	} {
		out := classifyHTTPFailure(tc.code, []byte(tc.body))
		if out.scope != tc.scope || out.action != tc.action || out.retryAfter != tc.delay || !out.failOver || out.retrySame {
			t.Fatalf("%s: %+v", tc.body, out)
		}
	}
}

type rejectedRoutePicker struct{ fixturePicker }

func (p rejectedRoutePicker) Pick(context.Context, picker.Request) (*domain.PlatformKey, *domain.Upstream, error) {
	return nil, nil, picker.ErrNoUpstream
}
func (p rejectedRoutePicker) Explain(context.Context, picker.Request) ([]picker.Candidate, error) {
	return []picker.Candidate{{KeyID: 1, SkipReason: "low_balance", KeyPreview: "secret", LastBalance: new(float64)}}, nil
}

func TestRejectedRouteRecordsCandidatesAndRetryAfter(t *testing.T) {
	db := logTestDB(t)
	ck := domain.ConsumerKey{Name: "test", Key: "test", Status: domain.StatusEnabled}
	if err := db.Create(&ck).Error; err != nil {
		t.Fatal(err)
	}
	h := NewGateway(db, nil, nil, rejectedRoutePicker{})
	r := gin.New()
	r.POST("/v1/responses", h.Responses)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 503 || w.Header().Get("Retry-After") != "30" || !strings.Contains(w.Body.String(), "no_available_route") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var row domain.RequestLog
	if err := db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	var trace []selectionTraceEvent
	if err := json.Unmarshal([]byte(row.SelectionTrace), &trace); err != nil {
		t.Fatal(err)
	}
	if row.InFlight || row.FailureAction != "no_available_route" || len(trace) != 1 || trace[0].Decision == nil {
		t.Fatalf("missing rejection evidence: %+v", row)
	}
	c := trace[0].Decision.Candidates[0]
	if c.SkipReason != "low_balance" || c.KeyPreview != "" || c.LastBalance != nil {
		t.Fatalf("incorrect diagnostic: %+v", c)
	}
}

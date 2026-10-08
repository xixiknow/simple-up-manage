package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"

	"github.com/gin-gonic/gin"
)

func TestCompactionRequestIntentSurvivesNoRoute(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"trigger", `{"model":"m","stream":true,"input":[{"type":"message","role":"user"},{"type":"compaction_trigger"}]}`, true},
		{"no trigger", `{"model":"m","stream":true,"input":[{"type":"message","role":"user"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := logTestDB(t)
			ck := domain.ConsumerKey{Name: "test", Key: "test", Status: domain.StatusEnabled}
			if err := db.Create(&ck).Error; err != nil {
				t.Fatal(err)
			}
			h := NewGateway(db, nil, nil, rejectedRoutePicker{})
			r := gin.New()
			r.POST("/v1/responses", h.Responses)
			req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer test")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var row domain.RequestLog
			if err := db.First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.InFlight || row.Compaction != tc.want {
				t.Fatalf("inflight=%v compaction=%v want=%v", row.InFlight, row.Compaction, tc.want)
			}
		})
	}
}

func TestFinishLogCompactionOrSemantics(t *testing.T) {
	for _, tc := range []struct {
		name             string
		body             string
		responseObserved bool
		want             bool
	}{
		{"request side only", `{"input":[{"type":"compaction_trigger"}]}`, false, true},
		{"response side only", `{"input":[{"type":"message"}]}`, true, true},
		{"both sides", `{"input":[{"type":"compaction_trigger"}]}`, true, true},
		{"neither", `{"input":[{"type":"message"}]}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := logTestDB(t)
			h := NewGateway(db, nil, nil, nil)
			ck := &domain.ConsumerKey{ID: 1}
			snap := captureInbound(httptest.NewRequest("POST", "/v1/responses", strings.NewReader(tc.body)), []byte(tc.body))
			h.finishLog(nil, ck, nil, nil, domain.ProtocolOpenAI, "m", "/v1/responses", "req-1", "127.0.0.1", 200, true, upstream.TokenUsage{}, 0, 10, "", snap, nil, tc.responseObserved)
			var row domain.RequestLog
			if err := db.First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.Compaction != tc.want {
				t.Fatalf("compaction=%v want=%v", row.Compaction, tc.want)
			}
		})
	}
}

func TestListRequestLogsCompactionFilter(t *testing.T) {
	db := logTestDB(t)
	h := &Admin{DB: db}
	for _, want := range []bool{true, true, false} {
		if err := db.Create(&domain.RequestLog{Compaction: want}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if page := getAdminPage[logPage](t, h.ListRequestLogs, "compaction=true"); page.Total != 2 {
		t.Fatalf("total=%d want=2", page.Total)
	}
	if page := getAdminPage[logPage](t, h.ListRequestLogs, "compaction=false"); page.Total != 1 {
		t.Fatalf("total=%d want=1", page.Total)
	}
	if page := getAdminPage[logPage](t, h.ListRequestLogs, ""); page.Total != 3 {
		t.Fatalf("total=%d want=3", page.Total)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/?compaction=bogus", nil)
	h.ListRequestLogs(c)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=400", recorder.Code)
	}
}

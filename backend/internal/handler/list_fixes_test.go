package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"simple-up-manage/internal/domain"
	"testing"
	"time"
)

func TestExpiredProviderCooldownDTO(t *testing.T) {
	past, future := time.Now().Add(-5*time.Minute), time.Now().Add(time.Minute)
	u := domain.Upstream{HealthStatus: domain.HealthCooldown, CooldownUntil: &past, LastError: "Not Found"}
	d := toUpstreamDTO(u)
	if d.HealthStatus != domain.HealthHealthy || d.CooldownUntil != nil || d.LastError != "Not Found" {
		t.Fatalf("expired cooldown: %+v", d)
	}
	u.CooldownUntil = &future
	d = toUpstreamDTO(u)
	if d.HealthStatus != domain.HealthCooldown || d.CooldownUntil == nil {
		t.Fatal("active cooldown lost")
	}
}

func TestRequestLogConsumerFilter(t *testing.T) {
	db := logTestDB(t)
	a, b := uint(11), uint(12)
	for _, id := range []*uint{&a, &b, nil} {
		if err := db.Create(&domain.RequestLog{ConsumerKeyID: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := &Admin{DB: db}
	r := gin.New()
	r.GET("/logs", h.ListRequestLogs)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/logs?consumer_key_id=11", nil))
	var got struct {
		Data struct {
			Items []logDTO `json:"items"`
			Total int      `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Data.Total != 1 || len(got.Data.Items) != 1 || *got.Data.Items[0].ConsumerKeyID != a {
		t.Fatalf("filter: %s", w.Body.String())
	}
}

func TestRouteCandidatesUseKeyProtocols(t *testing.T) {
	db := logTestDB(t)
	up := domain.Upstream{Name: "fixture", BaseURL: "https://example.test", Kind: domain.KindOpenAICompat, Protocols: "openai,anthropic"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"anthropic", ""} {
		if err := db.Create(&domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: "fixture", Protocols: p}).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := &Admin{DB: db}
	r := gin.New()
	r.GET("/candidates", h.RouteGroupCandidates)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/candidates", nil))
	var got struct {
		Data struct {
			Items []routeCandidateDTO `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data.Items) != 2 || len(got.Data.Items[0].Protocols) != 1 || got.Data.Items[0].Protocols[0] != "anthropic" || len(got.Data.Items[1].Protocols) != 2 {
		t.Fatalf("protocols: %s", w.Body.String())
	}
}

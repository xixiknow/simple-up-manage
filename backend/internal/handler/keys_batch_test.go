package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"simple-up-manage/internal/domain"

	"github.com/gin-gonic/gin"
)

type keyListPage struct {
	Items []keyDTO `json:"items"`
	Total int      `json:"total"`
}

// getUpstreamKeyPage calls ListUpstreamKeys with the :id route param injected,
// mirroring the real /upstreams/:id/keys route.
func getUpstreamKeyPage(t *testing.T, handler gin.HandlerFunc, upstreamID uint, query string) keyListPage {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(upstreamID)}}
	handler(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data keyListPage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

func postAdminJSON(t *testing.T, handler gin.HandlerFunc, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", path, bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	return recorder.Code, response
}

func TestKeyListSearchStatusHealthSort(t *testing.T) {
	db := logTestDB(t)
	h := &Admin{DB: db}
	up := domain.Upstream{Name: "Pool", BaseURL: "https://pool.example", Kind: domain.KindNewAPI, Protocols: domain.ProtocolOpenAI, Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	keys := []domain.PlatformKey{
		{UpstreamID: up.ID, Name: "alpha-key-01", KeyPreview: "sk-...aaa", Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy, RateMultiplier: 0.5, NameTag: "01"},
		{UpstreamID: up.ID, Name: "beta-key-02", KeyPreview: "sk-...bbb", Status: domain.StatusDisabled, HealthStatus: domain.HealthDisabled, RateMultiplier: 1.5, NameTag: "02"},
		{UpstreamID: up.ID, Name: "gamma-key-03", KeyPreview: "sk-...ccc", Status: domain.StatusEnabled, HealthStatus: domain.HealthDown, RateMultiplier: 0.9, NameTag: "03"},
	}
	for i := range keys {
		if err := db.Create(&keys[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	page := getUpstreamKeyPage(t, h.ListUpstreamKeys, up.ID, "")
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("all=%+v", page)
	}
	search := getUpstreamKeyPage(t, h.ListUpstreamKeys, up.ID, "search=alpha")
	if search.Total != 1 || search.Items[0].Name != "alpha-key-01" {
		t.Fatalf("search=%+v", search)
	}
	searchTag := getUpstreamKeyPage(t, h.ListUpstreamKeys, up.ID, "search=02")
	if searchTag.Total != 1 || searchTag.Items[0].Name != "beta-key-02" {
		t.Fatalf("search-tag=%+v", searchTag)
	}
	byStatus := getUpstreamKeyPage(t, h.ListUpstreamKeys, up.ID, "status=disabled")
	if byStatus.Total != 1 || byStatus.Items[0].ID != keys[1].ID {
		t.Fatalf("status=%+v", byStatus)
	}
	byHealth := getUpstreamKeyPage(t, h.ListUpstreamKeys, up.ID, "health_status=down")
	if byHealth.Total != 1 || byHealth.Items[0].ID != keys[2].ID {
		t.Fatalf("health=%+v", byHealth)
	}
	byRate := getUpstreamKeyPage(t, h.ListUpstreamKeys, up.ID, "sort=rate_multiplier&order=desc")
	if len(byRate.Items) != 3 || byRate.Items[0].ID != keys[1].ID || byRate.Items[2].ID != keys[0].ID {
		t.Fatalf("sort=%+v", byRate)
	}
	if rec := httptest.NewRecorder(); func() bool {
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/?sort=secret", nil)
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(up.ID)}}
		h.ListUpstreamKeys(c)
		return rec.Code >= 400
	}() != true {
		t.Fatal("invalid sort should be rejected")
	}
	if rec := httptest.NewRecorder(); func() bool {
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/?status=weird", nil)
		c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(up.ID)}}
		h.ListUpstreamKeys(c)
		return rec.Code >= 400
	}() != true {
		t.Fatal("invalid status should be rejected")
	}
}

func TestBatchKeyStatusAndDelete(t *testing.T) {
	db := logTestDB(t)
	h := &Admin{DB: db}
	up := domain.Upstream{Name: "Pool", BaseURL: "https://pool.example", Kind: domain.KindNewAPI, Protocols: domain.ProtocolOpenAI, Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	keys := make([]domain.PlatformKey, 0, 4)
	for i := 0; i < 4; i++ {
		k := domain.PlatformKey{UpstreamID: up.ID, Name: string(rune('a'+i)) + "-key", Status: domain.StatusEnabled, HealthStatus: domain.HealthHealthy}
		if err := db.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	group := domain.RouteGroup{Name: "g", Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.RouteGroupKey{RouteGroupID: group.ID, PlatformKeyID: keys[0].ID}).Error; err != nil {
		t.Fatal(err)
	}

	code, body := postAdminJSON(t, h.BatchKeyStatus, "/keys/batch-status", map[string]any{
		"ids":    []uint{keys[0].ID, keys[1].ID, keys[0].ID},
		"status": domain.StatusDisabled,
	})
	if code != 200 || body["data"].(map[string]any)["updated"].(float64) != 2 {
		t.Fatalf("batch-status code=%d body=%v", code, body)
	}
	var count int64
	db.Model(&domain.PlatformKey{}).Where("id IN ? AND status = ?", []uint{keys[0].ID, keys[1].ID}, domain.StatusDisabled).Count(&count)
	if count != 2 {
		t.Fatalf("status not applied: %d", count)
	}
	if bad, _ := postAdminJSON(t, h.BatchKeyStatus, "/keys/batch-status", map[string]any{"ids": []uint{keys[0].ID}, "status": "weird"}); bad < 400 {
		t.Fatal("invalid status should be rejected")
	}
	if empty, _ := postAdminJSON(t, h.BatchKeyDelete, "/keys/batch-delete", map[string]any{"ids": []uint{}}); empty < 400 {
		t.Fatal("empty ids should be rejected")
	}

	code, body = postAdminJSON(t, h.BatchKeyDelete, "/keys/batch-delete", map[string]any{"ids": []uint{keys[0].ID, keys[2].ID}})
	if code != 200 || body["data"].(map[string]any)["deleted"].(float64) != 2 {
		t.Fatalf("batch-delete code=%d body=%v", code, body)
	}
	var links int64
	db.Model(&domain.RouteGroupKey{}).Where("platform_key_id = ?", keys[0].ID).Count(&links)
	if links != 0 {
		t.Fatalf("group links not cleaned: %d", links)
	}
	var remaining int64
	db.Model(&domain.PlatformKey{}).Count(&remaining)
	if remaining != 2 {
		t.Fatalf("remaining keys: %d", remaining)
	}
}

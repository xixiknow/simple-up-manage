package handler

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/store"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func logTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func getAdminPage[T any](t *testing.T, handler gin.HandlerFunc, query string) T {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/?"+query, nil)
	handler(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Data
}

type logPage struct {
	Items      []logDTO `json:"items"`
	Total      int      `json:"total"`
	SnapshotID uint     `json:"snapshot_id"`
	SnapshotAt string   `json:"snapshot_at"`
}

func TestRequestLogPaginationSnapshot(t *testing.T) {
	db := logTestDB(t)
	h := &Admin{DB: db}
	ended := time.Now().Add(-time.Second).UTC()
	zero := 0.0
	for i := 0; i < 53; i++ {
		if err := db.Create(&domain.RequestLog{Success: true, CostUSD: &zero, CompletedAt: &ended}).Error; err != nil {
			t.Fatal(err)
		}
	}
	first := getAdminPage[logPage](t, h.ListRequestLogs, "page_size=20")
	if first.Total != 53 || len(first.Items) != 20 || first.Items[0].ID != 53 {
		t.Fatalf("first=%+v", first)
	}
	if err := db.Create(&domain.RequestLog{Success: true, CostUSD: &zero, CompletedAt: &ended}).Error; err != nil {
		t.Fatal(err)
	}
	second := getAdminPage[logPage](t, h.ListRequestLogs, fmt.Sprintf("page=2&page_size=20&snapshot_id=%d", first.SnapshotID))
	third := getAdminPage[logPage](t, h.ListRequestLogs, fmt.Sprintf("page=3&page_size=20&snapshot_id=%d", first.SnapshotID))
	if second.Total != 53 || second.Items[0].ID != 33 || third.Items[0].ID != 13 || len(third.Items) != 13 {
		t.Fatalf("pages second=%+v third=%+v", second, third)
	}
	large := getAdminPage[logPage](t, h.ListRequestLogs, "page_size=50")
	if len(large.Items) != 50 || large.Total != 54 {
		t.Fatalf("large=%+v", large)
	}
}

func TestSuccessSnapshotExcludesLaterCompletions(t *testing.T) {
	db := logTestDB(t)
	h := &Admin{DB: db}
	ended := time.Now().Add(-time.Second).UTC()
	zero := 0.0
	for i := 1; i <= 4; i++ {
		row := domain.RequestLog{Success: true, CompletedAt: &ended, CostUSD: &zero}
		if i == 3 {
			row.InFlight = true
			row.Success = false
			row.CompletedAt = nil
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	first := getAdminPage[logPage](t, h.ListRequestLogs, "success=true&page_size=2")
	if err := db.Model(&domain.RequestLog{}).Where("id = 3").Updates(map[string]any{"in_flight": false, "success": true, "completed_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	second := getAdminPage[logPage](t, h.ListRequestLogs, fmt.Sprintf("success=true&page=2&page_size=2&snapshot_id=%d&snapshot_at=%s", first.SnapshotID, url.QueryEscape(first.SnapshotAt)))
	if len(first.Items) != 2 || first.Items[1].ID != 2 || second.Total != 3 || len(second.Items) != 1 || second.Items[0].ID != 1 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestProviderSummaryAndAllKeyPages(t *testing.T) {
	db := logTestDB(t)
	h := &Admin{DB: db}
	for i := 1; i <= 150; i++ {
		up := domain.Upstream{Name: fmt.Sprintf("provider-%d", i), BaseURL: "https://example.com", Kind: "openai_compat", Protocols: "openai", Status: "enabled"}
		if err := db.Create(&up).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 151; i++ {
		k := domain.PlatformKey{UpstreamID: 150, Name: fmt.Sprintf("key-%d", i), EncryptedKey: "encrypted", HealthStatus: "down"}
		if err := db.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
	}
	providers := getAdminPage[struct {
		Items []upstreamDTO
		Total int
	}](t, h.ListUpstreams, "include_summary=true&page=2&page_size=100")
	if providers.Total != 150 || len(providers.Items) != 50 {
		t.Fatalf("providers=%+v", providers)
	}
	summary := providers.Items[49].Summary
	if summary.KeyCount != 151 || summary.AbnormalCount != 151 {
		t.Fatalf("summary=%+v", summary)
	}
	keys := getAdminPage[struct {
		Items []keyDTO
		Total int
	}](t, h.ListAllKeys, "upstream_ids=150&page=2&page_size=100")
	if keys.Total != 151 || len(keys.Items) != 51 || keys.Items[0].ID != 101 {
		t.Fatalf("keys=%+v", keys)
	}
	options := getAdminPage[struct {
		Items []map[string]any
		Total int
	}](t, h.ListAllKeys, "view=options&page=2&page_size=100")
	if options.Total != 151 || len(options.Items) != 51 || len(options.Items[0]) != 4 {
		t.Fatalf("options=%+v", options)
	}
	rates := getAdminPage[struct {
		Items []keyRateDTO
		Total int
	}](t, h.ListAllKeys, "view=rates&upstream_ids=150&page=2&page_size=100")
	if rates.Total != 151 || len(rates.Items) != 51 || rates.Items[0].ID != 101 || rates.Items[0].UpstreamID != 150 || rates.Items[0].UpstreamName != "provider-150" || rates.Items[0].RateMultiplier != 1 {
		t.Fatalf("rates=%+v", rates)
	}
	if err := db.Model(&domain.PlatformKey{}).Where("id = ?", 151).Update("rate_multiplier", 0.123456).Error; err != nil {
		t.Fatal(err)
	}
	light := getAdminPage[struct {
		Items []map[string]any
		Total int
	}](t, h.ListAllKeys, "view=rates&page=2&page_size=100")
	if light.Total != 151 || len(light.Items[50]) != 5 || light.Items[50]["rate_multiplier"] != 0.123456 || light.Items[50]["name"] != "key-150" {
		t.Fatalf("light=%+v", light)
	}
	empty := getAdminPage[struct {
		Items []keyRateDTO
		Total int
	}](t, h.ListAllKeys, "view=rates&upstream_ids=1")
	if empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty=%+v", empty)
	}
}

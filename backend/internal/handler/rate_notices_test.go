package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"
	"simple-up-manage/internal/store"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func noticeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "notices.db")
	db, err := store.Open("sqlite://" + filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	db.Logger = db.Logger.LogMode(logger.Silent)
	if err := store.AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

func noticeRouter(h *Admin) *gin.Engine {
	r := gin.New()
	r.GET("/api/v1/admin/rate-notices", h.ListRateNotices)
	r.GET("/api/v1/admin/rate-notices/unread-count", h.RateNoticeUnreadCount)
	r.POST("/api/v1/admin/rate-notices/read-all", h.MarkAllRateNoticesRead)
	r.POST("/api/v1/admin/rate-notices/:id/read", h.MarkRateNoticeRead)
	return r
}

func doJSON(t *testing.T, r http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeOK(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("not ok: %s", rec.Body.String())
	}
	if dest == nil {
		return
	}
	raw, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatal(err)
	}
}

func TestRateNoticesListUnreadReadAll(t *testing.T) {
	db := noticeTestDB(t)
	h := &Admin{DB: db}
	r := noticeRouter(h)

	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	readAt := time.Now().Add(-30 * time.Minute)
	rows := []domain.RateChangeNotice{
		{
			PlatformKeyID: 1, UpstreamID: 10, KeyName: "old-up", UpstreamName: "a",
			OldRate: 1, NewRate: 1.2, Direction: domain.RateChangeUp, Source: domain.RateChangeBilling,
			CreatedAt: older,
		},
		{
			PlatformKeyID: 2, UpstreamID: 10, KeyName: "new-down", UpstreamName: "a",
			OldRate: 0.8, NewRate: 0.5, Direction: domain.RateChangeDown, Source: domain.RateChangeManual,
			CreatedAt: newer,
		},
		{
			PlatformKeyID: 3, UpstreamID: 11, KeyName: "already-read", UpstreamName: "b",
			OldRate: 1, NewRate: 2, Direction: domain.RateChangeUp, Source: domain.RateChangeManual,
			ReadAt: &readAt, CreatedAt: time.Now(),
		},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	var count struct {
		Unread int64 `json:"unread"`
	}
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/rate-notices/unread-count"), &count)
	if count.Unread != 2 {
		t.Fatalf("unread=%d", count.Unread)
	}

	var list httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/rate-notices?page_size=50"), &list)
	items := asNotices(t, list.Items)
	if list.Total != 3 || len(items) != 3 {
		t.Fatalf("list total=%d items=%d", list.Total, len(items))
	}
	if items[0].KeyName != "already-read" || items[1].KeyName != "new-down" || items[2].KeyName != "old-up" {
		t.Fatalf("order %+v", []string{items[0].KeyName, items[1].KeyName, items[2].KeyName})
	}

	var unreadList httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/rate-notices?unread=1"), &unreadList)
	unreadItems := asNotices(t, unreadList.Items)
	if unreadList.Total != 2 || len(unreadItems) != 2 {
		t.Fatalf("unread list total=%d items=%d", unreadList.Total, len(unreadItems))
	}
	for _, it := range unreadItems {
		if it.ReadAt != nil {
			t.Fatalf("unread filter leaked read row %+v", it)
		}
	}

	unreadID := unreadItems[0].ID
	var marked domain.RateChangeNotice
	decodeOK(t, doJSON(t, r, http.MethodPost, "/api/v1/admin/rate-notices/"+strconv.FormatUint(uint64(unreadID), 10)+"/read"), &marked)
	if marked.ReadAt == nil {
		t.Fatal("mark read left read_at nil")
	}
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/rate-notices/unread-count"), &count)
	if count.Unread != 1 {
		t.Fatalf("after one read unread=%d", count.Unread)
	}

	var all struct {
		Unread int64 `json:"unread"`
	}
	decodeOK(t, doJSON(t, r, http.MethodPost, "/api/v1/admin/rate-notices/read-all"), &all)
	if all.Unread != 0 {
		t.Fatalf("read-all unread=%d", all.Unread)
	}
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/rate-notices/unread-count"), &count)
	if count.Unread != 0 {
		t.Fatalf("after read-all unread=%d", count.Unread)
	}

	var still httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/rate-notices"), &still)
	if still.Total != 3 {
		t.Fatalf("read should not delete rows, total=%d", still.Total)
	}

	rec := doJSON(t, r, http.MethodPost, "/api/v1/admin/rate-notices/9999/read")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func asNotices(t *testing.T, items any) []domain.RateChangeNotice {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.RateChangeNotice
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

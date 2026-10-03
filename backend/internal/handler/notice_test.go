package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"simple-up-manage/internal/store"
)

func noticeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := t.TempDir() + "/notices.db"
	db, err := store.Open("sqlite://" + path)
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
	r.GET("/api/v1/admin/notices", h.ListNotices)
	r.GET("/api/v1/admin/notices/unread-count", h.NoticeUnreadCount)
	r.POST("/api/v1/admin/notices/read-all", h.MarkAllNoticesRead)
	r.POST("/api/v1/admin/notices/:id/read", h.MarkNoticeRead)
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

func TestNoticesListFilterRead(t *testing.T) {
	db := noticeTestDB(t)
	h := &Admin{DB: db}
	r := noticeRouter(h)

	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-time.Hour)
	readAt := time.Now().Add(-30 * time.Minute)
	rows := []domain.Notice{
		{Kind: domain.NoticeKindRateChange, Source: domain.RateChangeBilling, Summary: "old rate move",
			Payload: `{"key_name":"old-up","old_rate":1,"new_rate":1.2,"direction":"up"}`, CreatedAt: older},
		{Kind: domain.NoticeKindModelChange, Source: domain.NoticeSourceModelsSync, Summary: "group models",
			Payload: `{"group_name":"vip","added":["m3"],"removed":["m1"]}`, CreatedAt: newer},
		{Kind: domain.NoticeKindRateChange, Source: domain.RateChangeManual, Summary: "read rate move",
			Payload: `{"key_name":"read"}`, ReadAt: &readAt, CreatedAt: time.Now()},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	var count struct {
		Unread int64            `json:"unread"`
		ByKind map[string]int64 `json:"by_kind"`
	}
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/notices/unread-count"), &count)
	if count.Unread != 2 || count.ByKind[domain.NoticeKindRateChange] != 1 || count.ByKind[domain.NoticeKindModelChange] != 1 {
		t.Fatalf("count=%+v", count)
	}
	readCount := func() (int64, map[string]int64) {
		var c struct {
			Unread int64            `json:"unread"`
			ByKind map[string]int64 `json:"by_kind"`
		}
		decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/notices/unread-count"), &c)
		return c.Unread, c.ByKind
	}

	var list httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/notices?page_size=50"), &list)
	if list.Total != 3 {
		t.Fatalf("list total=%d", list.Total)
	}
	items, ok := list.Items.([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("list items=%T", list.Items)
	}
	first, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var newest struct {
		Summary string          `json:"summary"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(first, &newest); err != nil {
		t.Fatal(err)
	}
	if newest.Summary != "read rate move" || len(newest.Payload) == 0 {
		t.Fatalf("newest=%+v", newest)
	}

	// kind filter only returns model_change rows.
	var modelList httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/notices?kind=model_change"), &modelList)
	if modelList.Total != 1 {
		t.Fatalf("model list total=%d", modelList.Total)
	}

	var unreadList httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/notices?unread=1"), &unreadList)
	if unreadList.Total != 2 {
		t.Fatalf("unread list total=%d", unreadList.Total)
	}

	// Mark the model_change row read via the unread list order (newest unread first).
	var unreadItems []struct {
		ID     uint       `json:"id"`
		Kind   string     `json:"kind"`
		ReadAt *time.Time `json:"read_at"`
	}
	raw, err := json.Marshal(unreadList.Items)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &unreadItems); err != nil {
		t.Fatal(err)
	}
	target := unreadItems[0]
	var marked struct {
		ID     uint       `json:"id"`
		ReadAt *time.Time `json:"read_at"`
	}
	decodeOK(t, doJSON(t, r, http.MethodPost, "/api/v1/admin/notices/"+strconv.FormatUint(uint64(target.ID), 10)+"/read"), &marked)
	if marked.ReadAt == nil {
		t.Fatal("mark read left read_at nil")
	}
	unread, byKind := readCount()
	if unread != 1 || byKind[target.Kind] != 0 {
		t.Fatalf("after one read unread=%d byKind=%+v", unread, byKind)
	}

	// read-all scoped to a kind with no unread rows must not touch the rest.
	var scoped struct {
		Unread int64 `json:"unread"`
	}
	decodeOK(t, doJSON(t, r, http.MethodPost, "/api/v1/admin/notices/read-all?kind=model_change"), &scoped)
	unread, _ = readCount()
	if unread != 1 {
		t.Fatalf("scoped read-all unread=%d", unread)
	}
	decodeOK(t, doJSON(t, r, http.MethodPost, "/api/v1/admin/notices/read-all"), &scoped)
	unread, _ = readCount()
	if unread != 0 {
		t.Fatalf("after read-all unread=%d", unread)
	}

	var still httpx.ListData
	decodeOK(t, doJSON(t, r, http.MethodGet, "/api/v1/admin/notices"), &still)
	if still.Total != 3 {
		t.Fatalf("read should not delete rows, total=%d", still.Total)
	}

	rec := doJSON(t, r, http.MethodPost, "/api/v1/admin/notices/9999/read")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id status=%d body=%s", rec.Code, rec.Body.String())
	}
}

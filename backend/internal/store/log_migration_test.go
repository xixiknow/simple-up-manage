package store

import (
	"path/filepath"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func TestMigrateLegacyLogState(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-log.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&domain.RequestLog{}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"stream":true}`, `{}`, `{"stream":`} {
		if err := db.Create(&domain.RequestLog{RequestBody: body, Success: true, CreatedAt: time.Now().Add(-time.Minute), DurationMs: 123}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("UPDATE request_logs SET in_flight = NULL").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatal(err)
		}
	}
	var rows []domain.RequestLog
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if !rows[0].Stream || !rows[0].StreamKnown || rows[1].Stream || !rows[1].StreamKnown || rows[2].StreamKnown {
		t.Fatalf("migrated types: %+v", rows)
	}
	for _, row := range rows {
		if row.InFlight || row.CompletedAt == nil || row.CompletedAt.Sub(row.CreatedAt) != 123*time.Millisecond {
			t.Fatalf("migrated state: %+v", row)
		}
	}
	var count int64
	db.Model(&domain.RequestLog{}).Where("in_flight = ? AND success = ?", false, true).Count(&count)
	if count != 3 {
		t.Fatalf("historical results missing: %d", count)
	}
}

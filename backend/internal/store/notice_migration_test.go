package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLegacyRateNoticesMigrateIntoNotices(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notices-migrate.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	// A database from before the unified notices table: legacy rows exist,
	// the notices table does not.
	if err := db.Exec(`CREATE TABLE rate_change_notices (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform_key_id INTEGER NOT NULL,
		upstream_id INTEGER NOT NULL,
		key_name TEXT,
		upstream_name TEXT,
		old_rate NUMERIC,
		new_rate NUMERIC,
		direction TEXT,
		source TEXT,
		read_at DATETIME,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatal(err)
	}
	readAt := time.Now().Add(-time.Hour)
	createdAt := time.Now().Add(-2 * time.Hour)
	if err := db.Exec(`INSERT INTO rate_change_notices
		(platform_key_id, upstream_id, key_name, upstream_name, old_rate, new_rate, direction, source, read_at, created_at)
		VALUES (1, 10, 'plus-a-1', 'plus', 1, 1.2, 'up', 'billing', NULL, ?),
		       (2, 10, 'plus-b-0.5', 'plus', 0.8, 0.5, 'down', 'manual', ?, ?)`,
		createdAt, readAt, createdAt).Error; err != nil {
		t.Fatal(err)
	}

	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var rows []domain.Notice
	if err := db.Where("kind = ?", domain.NoticeKindRateChange).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("migrated rows=%d", len(rows))
	}
	first, second := rows[0], rows[1]
	if first.Source != domain.RateChangeBilling || first.ReadAt != nil || first.Summary == "" {
		t.Fatalf("first=%+v", first)
	}
	if second.ReadAt == nil || second.Source != domain.RateChangeManual {
		t.Fatalf("second=%+v", second)
	}
	var p struct {
		KeyName     string  `json:"key_name"`
		Upstream    string  `json:"upstream_name"`
		OldRate     float64 `json:"old_rate"`
		NewRate     float64 `json:"new_rate"`
		Direction   string  `json:"direction"`
		PlatformKey uint    `json:"platform_key_id"`
	}
	if err := json.Unmarshal([]byte(second.Payload), &p); err != nil {
		t.Fatal(err)
	}
	if p.KeyName != "plus-b-0.5" || p.Upstream != "plus" || p.OldRate != 0.8 || p.NewRate != 0.5 || p.Direction != domain.RateChangeDown || p.PlatformKey != 2 {
		t.Fatalf("payload=%+v", p)
	}
	if db.Migrator().HasTable("rate_change_notices") {
		t.Fatal("legacy table should be dropped")
	}

	// A restart must not duplicate anything.
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.Model(&domain.Notice{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("restart changed row count to %d", n)
	}
}

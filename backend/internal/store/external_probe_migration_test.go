package store

import (
	"path/filepath"
	"testing"

	"simple-up-manage/internal/domain"
)

func TestExternalProbeMigrationPreservesHistory(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "probe-migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	// Model a pre-feature log table without the new classification column.
	if err := db.Exec(`CREATE TABLE request_logs (id integer PRIMARY KEY AUTOINCREMENT, request_id text, source text, model text)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO request_logs (request_id, source, model) VALUES ('historical', 'business', 'fixture')`).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := AutoMigrate(db); err != nil {
			t.Fatal(err)
		}
		var row domain.RequestLog
		if err := db.First(&row, 1).Error; err != nil {
			t.Fatal(err)
		}
		if row.RequestID != "historical" || row.Source != "business" || row.Model != "fixture" || row.ExternalProbeRule != nil {
			t.Fatalf("history changed: %+v", row)
		}
		if !db.Migrator().HasIndex(&domain.RequestLog{}, "ExternalProbeRule") {
			t.Fatal("missing classification index")
		}
	}
}

package handler

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"simple-up-manage/internal/domain"
)

func TestPostgresFinalLogWithClippedUTF8(t *testing.T) {
	dsn := os.Getenv("TEST_ROUTING_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_ROUTING_DATABASE_URL is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	defer pool.Close()
	schema := "log_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	isolated, err := gorm.Open(postgres.Open(dsn+sep+"search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := isolated.DB()
	defer conn.Close()
	if err := isolated.AutoMigrate(&domain.RequestLog{}); err != nil {
		t.Fatal(err)
	}
	row := domain.RequestLog{InFlight: true}
	if err := isolated.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	full := []byte(strings.Repeat("a", maxLogBodyBytes-1) + "\u4e2d" + "tail")
	body, truncated := captureBody("text/event-stream", full[:maxLogBodyBytes], len(full))
	if !truncated {
		t.Fatal("missing truncation flag")
	}
	h := &Gateway{DB: isolated}
	updates := map[string]any{"in_flight": false, "success": true, "response_body": body, "error_message": truncateErr(strings.Repeat("a", 999) + "\u4e2d"), "log_revision": 1}
	if err := h.persistLog(context.Background(), row.ID, 1, updates); err != nil {
		t.Fatal(err)
	}
	if err := isolated.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.InFlight || !row.Success || row.ResponseBody != body {
		t.Fatal("final result was not persisted")
	}
}

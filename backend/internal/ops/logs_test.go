package ops

import (
	"context"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func TestPurgeRequestLogsKeepsRecent(t *testing.T) {
	db := testDB(t)
	s := &Service{DB: db}
	old := domain.RequestLog{Path: "/old", CreatedAt: time.Now().Add(-25 * time.Hour)}
	fresh := domain.RequestLog{Path: "/fresh", CreatedAt: time.Now().Add(-time.Hour)}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeRequestLogs(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d want 1", n)
	}
	var left []domain.RequestLog
	if err := db.Find(&left).Error; err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Path != "/fresh" {
		t.Fatalf("left=%+v", left)
	}
}

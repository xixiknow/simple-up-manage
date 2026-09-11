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

func TestFinalizeStaleInFlightLogs(t *testing.T) {
	db := testDB(t)
	s := &Service{DB: db}
	stale := domain.RequestLog{Path: "/stale", InFlight: true, CreatedAt: time.Now().Add(-20 * time.Minute)}
	live := domain.RequestLog{Path: "/live", InFlight: true, CreatedAt: time.Now()}
	done := domain.RequestLog{Path: "/done", InFlight: false, Success: true, CreatedAt: time.Now().Add(-time.Hour)}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&live).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&done).Error; err != nil {
		t.Fatal(err)
	}
	n, err := s.FinalizeStaleInFlightLogs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("finalized=%d want 1", n)
	}
	var rows []domain.RequestLog
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].InFlight || rows[0].Success || rows[0].ErrorMessage == "" {
		t.Fatalf("stale %+v", rows[0])
	}
	if !rows[1].InFlight {
		t.Fatalf("live should stay in-flight %+v", rows[1])
	}
	if rows[2].InFlight || !rows[2].Success {
		t.Fatalf("done %+v", rows[2])
	}
}

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

func TestStaleLogReconcilesOnlyLatestSuccessfulAttempt(t *testing.T) {
	db := testDB(t)
	s := &Service{DB: db}
	started := time.Now().Add(-10 * time.Minute)
	for _, latestSuccess := range []bool{true, false} {
		row := domain.RequestLog{InFlight: true, CreatedAt: started, FailureAction: "invalid_response", LogRevision: 3}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		a := domain.RequestAttempt{ID: time.Now().String(), RequestLogID: row.ID, PlatformKeyID: 1, StartedAt: started.Add(time.Second), CompletedAt: started.Add(5 * time.Second), StatusCode: 200, Result: "success", OutputTokens: 42, TTFTMs: 1000, TTFTStatus: "measured", TTFTEvent: "response.output_text.delta"}
		if err := db.Create(&a).Error; err != nil {
			t.Fatal(err)
		}
		if !latestSuccess {
			a.ID += "-later"
			a.CompletedAt = a.CompletedAt.Add(time.Second)
			a.Result = "upstream_failure"
			if err := db.Create(&a).Error; err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.FinalizeStaleInFlightLogs(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := db.First(&row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
		if row.InFlight || row.Success != latestSuccess {
			t.Fatalf("incorrect reconciliation: %+v", row)
		}
		if latestSuccess && (row.DurationMs != 5000 || row.TTFTMs != 2000 || row.OutputTokens != 42 || row.FailureAction != "" || row.ErrorMessage != "") {
			t.Fatalf("incorrect restored metrics: %+v", row)
		}
	}
}

package logarchive

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/store"
)

func testStore(t *testing.T, body, total int64) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&domain.LogBody{}); err != nil {
		t.Fatal(err)
	}
	s, err := New(db, t.TempDir(), body, total)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Wait)
	return s
}

func TestArchiveRoundTripAndRetention(t *testing.T) {
	s := testStore(t, 64<<20, 10<<30)
	body := []byte(strings.Repeat("text\n", 200000))
	c := s.Begin(1, "attempt", "response", "text/event-stream")
	_, _ = c.Write(body)
	c.Finish(true, "")
	s.Wait()
	var meta domain.LogBody
	if err := s.DB.First(&meta, "id = ?", c.meta.ID).Error; err != nil {
		t.Fatal(err)
	}
	if meta.Status != "complete" || meta.SavedBytes != int64(len(body)) || meta.StoredBytes >= meta.SavedBytes {
		t.Fatalf("meta=%+v", meta)
	}
	r, err := s.Open(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("round trip bytes=%d error=%v", len(got), err)
	}
	if err = s.Purge(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if s.used != 0 {
		t.Fatalf("quota leak: %d", s.used)
	}
	if _, err = s.Open(meta.ID); !os.IsNotExist(err) {
		t.Fatalf("archive still present: %v", err)
	}
}

func TestArchiveLimitsAndFailures(t *testing.T) {
	for _, mode := range []string{"size", "queue", "quota", "disk", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			s := testStore(t, 64<<20, 10<<30)
			switch mode {
			case "size":
				s.maxBody = 10
			case "queue":
				s.buffered = bufferLimit
			case "quota":
				s.maxTotal = 1
			case "disk":
				if err := os.Remove(s.dir); err != nil {
					t.Fatal(err)
				}
			}
			c := s.Begin(1, "", "request", "application/json")
			body := []byte(strings.Repeat("x", 100))
			if n, err := c.Write(body); n != len(body) || err != nil {
				t.Fatalf("capture affected proxy: %d %v", n, err)
			}
			if mode == "interrupted" {
				c.Finish(false, "stream_interrupted")
			} else {
				c.Finish(true, "")
			}
			s.Wait()
			var meta domain.LogBody
			if err := s.DB.First(&meta, "id = ?", c.meta.ID).Error; err != nil {
				t.Fatal(err)
			}
			if meta.Status == "complete" || meta.Reason == "" || meta.ReceivedBytes != 100 {
				t.Fatalf("meta=%+v", meta)
			}
			if mode == "size" && (meta.SavedBytes != 10 || meta.Reason != "size_limit") {
				t.Fatalf("size meta=%+v", meta)
			}
			if mode == "quota" || mode == "disk" {
				if meta.Status != "error" || s.used != 0 {
					t.Fatalf("error meta=%+v used=%d", meta, s.used)
				}
			}
		})
	}
}

func TestArchiveRestartAndPathIsolation(t *testing.T) {
	s := testStore(t, 64<<20, 10<<30)
	if _, err := s.Open("../../secret"); err == nil {
		t.Fatal("accepted path traversal")
	}
	meta := domain.LogBody{ID: "unfinished", Status: "saving"}
	s.DB.Create(&meta)
	if err := os.WriteFile(filepath.Join(s.dir, "unfinished.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.DB, s.dir, s.maxBody, s.maxTotal); err != nil {
		t.Fatal(err)
	}
	s.DB.First(&meta, "id = ?", meta.ID)
	if meta.Status != "error" || meta.Reason != "process_interrupted" {
		t.Fatalf("meta=%+v", meta)
	}
}

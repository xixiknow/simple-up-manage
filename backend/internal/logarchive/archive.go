package logarchive

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"simple-up-manage/internal/domain"
)

const bufferLimit = 32 << 20
const chunkSize = 32 << 10

type Store struct {
	DB                *gorm.DB
	dir               string
	maxBody, maxTotal int64
	mu                sync.Mutex
	buffered, used    int64
	wg                sync.WaitGroup
}

func New(db *gorm.DB, dir string, maxBody, maxTotal int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{DB: db, dir: dir, maxBody: maxBody, maxTotal: maxTotal}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		if strings.HasSuffix(e.Name(), ".gz") {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			s.used += info.Size()
		}
	}
	if err := db.Model(&domain.LogBody{}).Where("status = ?", "saving").Updates(map[string]any{"status": "error", "reason": "process_interrupted"}).Error; err != nil {
		return nil, err
	}
	return s, nil
}

type Capture struct {
	s                  *Store
	meta               domain.LogBody
	mu                 sync.Mutex
	received, accepted int64
	reason             string
	complete           bool
	chunks             chan []byte
	closed             bool
}

func (s *Store) Begin(logID uint, attemptID, direction, contentType string) *Capture {
	if s == nil || logID == 0 {
		return nil
	}
	c := &Capture{s: s, meta: domain.LogBody{ID: uuid.NewString(), RequestLogID: logID, AttemptID: attemptID, Direction: direction, ContentType: contentType, Status: "saving", CreatedAt: time.Now().UTC()}, chunks: make(chan []byte, 1024)}
	s.wg.Add(1)
	go c.run()
	return c
}

// Write never waits for disk or queue capacity and never changes proxy errors.
func (c *Capture) Write(p []byte) (int, error) {
	if c == nil {
		return len(p), nil
	}
	n := len(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return n, nil
	}
	c.received += int64(n)
	for len(p) > 0 && c.reason == "" {
		size := min(len(p), chunkSize)
		room := c.s.maxBody - c.accepted
		if room <= 0 {
			c.reason = "size_limit"
			break
		}
		size = min(size, int(room))
		c.s.mu.Lock()
		if c.s.buffered+int64(size) > bufferLimit {
			c.s.mu.Unlock()
			c.reason = "queue_full"
			break
		}
		c.s.buffered += int64(size)
		c.s.mu.Unlock()
		buf := append([]byte(nil), p[:size]...)
		select {
		case c.chunks <- buf:
			c.accepted += int64(size)
			p = p[size:]
		default:
			c.s.mu.Lock()
			c.s.buffered -= int64(size)
			c.s.mu.Unlock()
			c.reason = "queue_full"
		}
	}
	return n, nil
}

func (c *Capture) Finish(complete bool, reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.complete = complete
	if c.reason == "" {
		c.reason = reason
	}
	close(c.chunks)
}

func (c *Capture) SetReceivedBytes(n int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received = n
}

func (c *Capture) fail(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reason == "" {
		c.reason = reason
	}
}

type quotaWriter struct {
	s       *Store
	f       *os.File
	written int64
}

func (w *quotaWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	if w.s.used+int64(len(p)) > w.s.maxTotal {
		w.s.mu.Unlock()
		return 0, errors.New("archive quota exceeded")
	}
	w.s.used += int64(len(p))
	w.s.mu.Unlock()
	n, err := w.f.Write(p)
	w.s.mu.Lock()
	w.s.used -= int64(len(p) - n)
	w.s.mu.Unlock()
	w.written += int64(n)
	return n, err
}

func (c *Capture) run() {
	defer c.s.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := c.s.DB.WithContext(ctx).Create(&c.meta).Error
	cancel()
	if err != nil {
		c.fail("metadata_write_failed")
		slog.Error("create body archive", "body_id", c.meta.ID, "error", err)
	}
	path := filepath.Join(c.s.dir, c.meta.ID+".tmp")
	var f *os.File
	var zw *gzip.Writer
	var qw *quotaWriter
	if err == nil {
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			qw = &quotaWriter{s: c.s, f: f}
			zw, _ = gzip.NewWriterLevel(qw, gzip.BestSpeed)
		} else {
			c.fail("storage_error")
		}
	}
	var saved int64
	for chunk := range c.chunks {
		if err == nil {
			var n int
			n, err = zw.Write(chunk)
			saved += int64(n)
			if err != nil {
				c.fail("storage_or_quota_error")
			}
		}
		c.s.mu.Lock()
		c.s.buffered -= int64(len(chunk))
		c.s.mu.Unlock()
	}
	if zw != nil {
		if closeErr := zw.Close(); err == nil {
			err = closeErr
		}
	}
	if f != nil {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = os.Rename(path, filepath.Join(c.s.dir, c.meta.ID+".gz"))
	}
	if err != nil {
		c.fail("storage_or_quota_error")
		_ = os.Remove(path)
		if qw != nil {
			c.s.mu.Lock()
			c.s.used -= qw.written
			c.s.mu.Unlock()
		}
		saved = 0
	}
	c.mu.Lock()
	c.meta.ReceivedBytes = c.received
	c.meta.SavedBytes = saved
	c.meta.Reason = c.reason
	c.meta.Status = "complete"
	if !c.complete || c.reason != "" {
		c.meta.Status = "partial"
	}
	if c.reason == "binary_omitted" || c.reason == "multipart_files_omitted" {
		c.meta.Status = "omitted"
	}
	if err != nil {
		c.meta.Status = "error"
	}
	if qw != nil && err == nil {
		c.meta.StoredBytes = qw.written
	}
	c.mu.Unlock()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := c.s.DB.WithContext(ctx).Model(&domain.LogBody{}).Where("id = ?", c.meta.ID).Updates(map[string]any{"status": c.meta.Status, "reason": c.meta.Reason, "received_bytes": c.meta.ReceivedBytes, "saved_bytes": c.meta.SavedBytes, "stored_bytes": c.meta.StoredBytes}).Error; e != nil {
		slog.Error("finalize body archive", "body_id", c.meta.ID, "error", e)
	}
}

type readCloser struct {
	io.Reader
	file *os.File
	gzip *gzip.Reader
}

func (r *readCloser) Close() error { _ = r.gzip.Close(); return r.file.Close() }
func (s *Store) Open(id string) (io.ReadCloser, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(s.dir, id+".gz"))
	if err != nil {
		return nil, err
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &readCloser{Reader: z, file: f, gzip: z}, nil
}

func (s *Store) Purge(ctx context.Context, cut time.Time) error {
	if s == nil {
		return nil
	}
	var rows []domain.LogBody
	for {
		if err := s.DB.WithContext(ctx).Where("created_at < ?", cut).Limit(200).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if err := s.remove(row.ID + ".gz"); err != nil {
				return err
			}
			if err := s.DB.WithContext(ctx).Delete(&domain.LogBody{}, "id = ?", row.ID).Error; err != nil {
				return err
			}
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cut) && (strings.HasSuffix(entry.Name(), ".gz") || strings.HasSuffix(entry.Name(), ".tmp")) {
			if err := s.remove(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) remove(name string) error {
	path := filepath.Join(s.dir, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		s.mu.Lock()
		s.used -= info.Size()
		s.mu.Unlock()
	}
	return nil
}

// Wait is used during tests and shutdown after request admission has stopped.
func (s *Store) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}

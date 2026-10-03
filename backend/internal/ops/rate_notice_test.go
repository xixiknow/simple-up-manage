package ops

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

func seedRateKey(t *testing.T, rate float64, synced *time.Time) (*Service, *domain.PlatformKey) {
	t.Helper()
	db := testDB(t)
	up := domain.Upstream{
		Name: "plus", BaseURL: "https://x", Kind: domain.KindSub2API,
		Protocols: "openai", Status: domain.StatusEnabled,
	}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	k := domain.PlatformKey{
		UpstreamID:     up.ID,
		Name:           "plus-tag-" + domain.FormatRate(rate),
		EncryptedKey:   "x",
		Status:         domain.StatusEnabled,
		RateMultiplier: rate,
		RateSyncedAt:   synced,
	}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	k.Upstream = &up
	return &Service{DB: db}, &k
}

func rateNoticeCount(t *testing.T, s *Service) int {
	t.Helper()
	var n int64
	if err := s.DB.Model(&domain.Notice{}).Where("kind = ?", domain.NoticeKindRateChange).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

type ratePayload struct {
	PlatformKeyID uint    `json:"platform_key_id"`
	UpstreamID    uint    `json:"upstream_id"`
	KeyName       string  `json:"key_name"`
	UpstreamName  string  `json:"upstream_name"`
	OldRate       float64 `json:"old_rate"`
	NewRate       float64 `json:"new_rate"`
	Direction     string  `json:"direction"`
}

func lastRateNotice(t *testing.T, s *Service) (domain.Notice, ratePayload) {
	t.Helper()
	var n domain.Notice
	if err := s.DB.Where("kind = ?", domain.NoticeKindRateChange).Order("id DESC").First(&n).Error; err != nil {
		t.Fatal(err)
	}
	var p ratePayload
	if err := json.Unmarshal([]byte(n.Payload), &p); err != nil {
		t.Fatal(err)
	}
	return n, p
}

func TestRecordRateChangeWritesOnMove(t *testing.T) {
	s, key := seedRateKey(t, 0.5, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 0.5, 0.8, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 1 {
		t.Fatalf("count=%d", rateNoticeCount(t, s))
	}
	n, p := lastRateNotice(t, s)
	if p.OldRate != 0.5 || p.NewRate != 0.8 || p.Direction != domain.RateChangeUp || n.Source != domain.RateChangeBilling {
		t.Fatalf("notice=%+v payload=%+v", n, p)
	}
	if p.KeyName != key.Name || p.UpstreamName != "plus" || p.PlatformKeyID != key.ID || p.UpstreamID != key.UpstreamID {
		t.Fatalf("names=%+v", p)
	}
	if n.ReadAt != nil {
		t.Fatal("new notice should be unread")
	}
	if n.Kind != domain.NoticeKindRateChange {
		t.Fatalf("kind=%s", n.Kind)
	}
	if n.Summary == "" {
		t.Fatal("summary should be rendered")
	}
}

func TestRecordRateChangeDirectionDown(t *testing.T) {
	s, key := seedRateKey(t, 1, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 1, 0.08, domain.RateChangeManual); err != nil {
		t.Fatal(err)
	}
	_, p := lastRateNotice(t, s)
	if p.Direction != domain.RateChangeDown {
		t.Fatalf("direction=%s", p.Direction)
	}
}

func TestRecordRateChangeSkipsSameRate(t *testing.T) {
	s, key := seedRateKey(t, 0.5, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 0.5, 0.5, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRateChange(context.Background(), key, 0.5, 0.5+1e-7, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 0 {
		t.Fatalf("count=%d", rateNoticeCount(t, s))
	}
}

func TestRecordRateChangeSkipsFirstBillingFromDefault(t *testing.T) {
	s, key := seedRateKey(t, 1, nil)
	if err := s.RecordRateChange(context.Background(), key, 1, 0.08, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 0 {
		t.Fatalf("first billing sync should not write, count=%d", rateNoticeCount(t, s))
	}
}

func TestRecordRateChangeKeepsManualFromDefault(t *testing.T) {
	s, key := seedRateKey(t, 1, nil)
	if err := s.RecordRateChange(context.Background(), key, 1, 0.08, domain.RateChangeManual); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 1 {
		t.Fatal("manual edit from default 1 should write")
	}
}

func TestRecordRateChangeKeepsLaterBilling(t *testing.T) {
	s, key := seedRateKey(t, 0.08, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 0.08, 0.1, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 1 {
		t.Fatal("later billing change should write")
	}
}

func TestApplyBillingRateSkipsFirstThenRecords(t *testing.T) {
	s, key := seedRateKey(t, 1, nil)
	now := time.Now()
	if err := s.applyBillingRate(context.Background(), key, 0.08, now); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 0 {
		t.Fatal("first billing apply should skip notice")
	}
	var stored domain.PlatformKey
	if err := s.DB.First(&stored, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RateMultiplier != 0.08 || stored.RateSyncedAt == nil {
		t.Fatalf("key not synced: %+v", stored)
	}

	if err := s.applyBillingRate(context.Background(), key, 0.12, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rateNoticeCount(t, s) != 1 {
		t.Fatalf("second billing apply should write, count=%d", rateNoticeCount(t, s))
	}
	_, p := lastRateNotice(t, s)
	if p.OldRate != 0.08 || p.NewRate != 0.12 || p.Direction != domain.RateChangeUp {
		t.Fatalf("payload=%+v", p)
	}
}

func TestPurgeNoticesKeepsRecent(t *testing.T) {
	s, _ := seedRateKey(t, 1, timePtr(time.Now()))
	old := domain.Notice{
		Kind: domain.NoticeKindRateChange, Source: domain.RateChangeBilling,
		Summary:   "old",
		Payload:   `{"key_name":"old"}`,
		CreatedAt: time.Now().Add(-91 * 24 * time.Hour),
	}
	fresh := domain.Notice{
		Kind: domain.NoticeKindModelChange, Source: domain.NoticeSourceModelsSync,
		Summary:   "fresh",
		Payload:   `{"group_name":"g"}`,
		CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := s.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeNotices(context.Background(), NoticeRetention)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d want 1", n)
	}
	var left []domain.Notice
	if err := s.DB.Find(&left).Error; err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Summary != "fresh" {
		t.Fatalf("left=%+v", left)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

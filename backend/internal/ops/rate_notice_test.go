package ops

import (
	"context"
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

func noticeCount(t *testing.T, s *Service) int {
	t.Helper()
	var n int64
	if err := s.DB.Model(&domain.RateChangeNotice{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func lastNotice(t *testing.T, s *Service) domain.RateChangeNotice {
	t.Helper()
	var n domain.RateChangeNotice
	if err := s.DB.Order("id DESC").First(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecordRateChangeWritesOnMove(t *testing.T) {
	s, key := seedRateKey(t, 0.5, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 0.5, 0.8, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if noticeCount(t, s) != 1 {
		t.Fatalf("count=%d", noticeCount(t, s))
	}
	n := lastNotice(t, s)
	if n.OldRate != 0.5 || n.NewRate != 0.8 || n.Direction != domain.RateChangeUp || n.Source != domain.RateChangeBilling {
		t.Fatalf("notice=%+v", n)
	}
	if n.KeyName != key.Name || n.UpstreamName != "plus" || n.PlatformKeyID != key.ID || n.UpstreamID != key.UpstreamID {
		t.Fatalf("names=%+v", n)
	}
	if n.ReadAt != nil {
		t.Fatal("new notice should be unread")
	}
}

func TestRecordRateChangeDirectionDown(t *testing.T) {
	s, key := seedRateKey(t, 1, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 1, 0.08, domain.RateChangeManual); err != nil {
		t.Fatal(err)
	}
	n := lastNotice(t, s)
	if n.Direction != domain.RateChangeDown {
		t.Fatalf("direction=%s", n.Direction)
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
	if noticeCount(t, s) != 0 {
		t.Fatalf("count=%d", noticeCount(t, s))
	}
}

func TestRecordRateChangeSkipsFirstBillingFromDefault(t *testing.T) {
	s, key := seedRateKey(t, 1, nil)
	if err := s.RecordRateChange(context.Background(), key, 1, 0.08, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if noticeCount(t, s) != 0 {
		t.Fatalf("first billing sync should not write, count=%d", noticeCount(t, s))
	}
}

func TestRecordRateChangeKeepsManualFromDefault(t *testing.T) {
	s, key := seedRateKey(t, 1, nil)
	if err := s.RecordRateChange(context.Background(), key, 1, 0.08, domain.RateChangeManual); err != nil {
		t.Fatal(err)
	}
	if noticeCount(t, s) != 1 {
		t.Fatal("manual edit from default 1 should write")
	}
}

func TestRecordRateChangeKeepsLaterBilling(t *testing.T) {
	s, key := seedRateKey(t, 0.08, timePtr(time.Now()))
	if err := s.RecordRateChange(context.Background(), key, 0.08, 0.1, domain.RateChangeBilling); err != nil {
		t.Fatal(err)
	}
	if noticeCount(t, s) != 1 {
		t.Fatal("later billing change should write")
	}
}

func TestApplyBillingRateSkipsFirstThenRecords(t *testing.T) {
	s, key := seedRateKey(t, 1, nil)
	now := time.Now()
	if err := s.applyBillingRate(context.Background(), key, 0.08, now); err != nil {
		t.Fatal(err)
	}
	if noticeCount(t, s) != 0 {
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
	if noticeCount(t, s) != 1 {
		t.Fatalf("second billing apply should write, count=%d", noticeCount(t, s))
	}
	n := lastNotice(t, s)
	if n.OldRate != 0.08 || n.NewRate != 0.12 || n.Direction != domain.RateChangeUp {
		t.Fatalf("notice=%+v", n)
	}
}

func TestPurgeRateChangeNoticesKeepsRecent(t *testing.T) {
	s, key := seedRateKey(t, 1, timePtr(time.Now()))
	old := domain.RateChangeNotice{
		PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
		KeyName: "old", UpstreamName: "plus",
		OldRate: 1, NewRate: 0.5, Direction: domain.RateChangeDown, Source: domain.RateChangeBilling,
		CreatedAt: time.Now().Add(-91 * 24 * time.Hour),
	}
	fresh := domain.RateChangeNotice{
		PlatformKeyID: key.ID, UpstreamID: key.UpstreamID,
		KeyName: "fresh", UpstreamName: "plus",
		OldRate: 0.5, NewRate: 0.6, Direction: domain.RateChangeUp, Source: domain.RateChangeManual,
		CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := s.DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeRateChangeNotices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d want 1", n)
	}
	var left []domain.RateChangeNotice
	if err := s.DB.Find(&left).Error; err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].KeyName != "fresh" {
		t.Fatalf("left=%+v", left)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

package dashboard

import (
	"strings"
	"testing"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func alertTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.AlertSettings{}, &domain.Notice{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFilterAlertItems(t *testing.T) {
	h1, h2, hHigh := 0.0, 4.9, 5.1
	items := []UrgentItem{
		{ProviderID: 1, Name: "depleted", HoursLeft: &h1},
		{ProviderID: 2, Name: "below", HoursLeft: &h2},
		{ProviderID: 3, Name: "above", HoursLeft: &hHigh},
		{ProviderID: 4, Name: "insufficient", Insufficient: true},
	}
	got := filterAlertItems(items, 5)
	if len(got) != 2 {
		t.Fatalf("targets = %d, want 2", len(got))
	}
	if got[0].ProviderID != 1 || got[1].ProviderID != 2 {
		t.Fatalf("providers = %d,%d, want 1,2", got[0].ProviderID, got[1].ProviderID)
	}
}

func TestSilenceDue(t *testing.T) {
	now := time.Now()
	state := map[uint]time.Time{1: now.Add(-2 * time.Hour), 2: now.Add(-7 * time.Hour)}
	targets := []alertTarget{{UrgentItem: UrgentItem{ProviderID: 1}, Hours: 3}, {UrgentItem: UrgentItem{ProviderID: 2}, Hours: 2}, {UrgentItem: UrgentItem{ProviderID: 3}, Hours: 1}}
	due := silenceDue(state, targets, 6*time.Hour, now)
	if len(due) != 2 {
		t.Fatalf("due = %d, want 2", len(due))
	}
	if due[0].ProviderID != 2 || due[1].ProviderID != 3 {
		t.Fatalf("providers = %d,%d, want 2,3", due[0].ProviderID, due[1].ProviderID)
	}
}

func TestSilenceStateRoundTrip(t *testing.T) {
	state := map[uint]time.Time{7: time.Unix(1700000000, 0).UTC(), 9: time.Now().UTC().Truncate(time.Second)}
	raw := saveSilenceState(state)
	back := loadSilenceState(raw)
	if len(back) != 2 {
		t.Fatalf("roundtrip size = %d, want 2", len(back))
	}
	if !back[7].Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("provider 7 = %v, want unix 1700000000", back[7])
	}
	if got := loadSilenceState(""); len(got) != 0 {
		t.Fatalf("empty state = %v, want empty", got)
	}
	if got := loadSilenceState("not json"); len(got) != 0 {
		t.Fatalf("garbage state = %v, want empty", got)
	}
}

func TestBuildAlertMessage(t *testing.T) {
	bal, consumed := 3.5, 1.2
	title, desp := buildAlertMessage([]alertTarget{
		{UrgentItem: UrgentItem{ProviderID: 1, Name: "A", BalanceUSD: &bal, Consumed24hUSD: &consumed}, Hours: 2.5},
	})
	if !strings.Contains(title, "A") {
		t.Fatalf("title %q should name the single provider", title)
	}
	if !strings.Contains(desp, "2.5 小时") || !strings.Contains(desp, "$3.50") || !strings.Contains(desp, "$1.20") {
		t.Fatalf("desp missing details:\n%s", desp)
	}
	depleted, _ := buildAlertMessage([]alertTarget{
		{UrgentItem: UrgentItem{ProviderID: 2, Name: "B"}, Hours: 0},
	})
	if !strings.Contains(depleted, "B") || !strings.Contains(depleted, "已耗尽") {
		t.Fatalf("depleted title %q should flag the provider", depleted)
	}
	// Multi-provider case switches to the aggregate title.
	title2, _ := buildAlertMessage([]alertTarget{
		{UrgentItem: UrgentItem{ProviderID: 1, Name: "A"}, Hours: 2.5},
		{UrgentItem: UrgentItem{ProviderID: 2, Name: "B"}, Hours: 0},
	})
	if !strings.Contains(title2, "2 个") {
		t.Fatalf("aggregate title %q should count providers", title2)
	}
}

func TestLoadAlertSettingsCreatesDefaults(t *testing.T) {
	db := alertTestDB(t)
	s := &Service{db: db}
	cfg := s.LoadAlertSettings()
	if cfg.ID != 1 || cfg.Enabled || cfg.HoursThreshold != 5 || cfg.SilenceHours != 6 {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	cfg.Enabled = true
	cfg.HoursThreshold = 8
	if _, err := s.SaveAlertSettings(cfg); err != nil {
		t.Fatal(err)
	}
	reloaded := s.LoadAlertSettings()
	if !reloaded.Enabled || reloaded.HoursThreshold != 8 {
		t.Fatalf("save/reload mismatch: %+v", reloaded)
	}
	// Out-of-range values are clamped back on load.
	_ = db.Model(&domain.AlertSettings{}).Where("id = 1").Updates(map[string]any{"hours_threshold": 500, "silence_hours": 0}).Error
	clamped := s.LoadAlertSettings()
	if clamped.HoursThreshold != 5 || clamped.SilenceHours != 6 {
		t.Fatalf("clamp failed: %+v", clamped)
	}
}

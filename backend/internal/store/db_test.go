package store

import (
	"path/filepath"
	"testing"
	"time"

	"simple-up-manage/internal/domain"
)

// Legacy (pre-refactor) models, used only to build an old-style database.
type legacyUpstream struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:128;not null"`
	BaseURL   string `gorm:"size:512;not null"`
	Kind      string `gorm:"size:32;not null"`
	Protocols string `gorm:"size:128;not null"`
	Status    string `gorm:"size:16;not null;default:enabled"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyUpstream) TableName() string { return "upstreams" }

type legacyUpstreamGroup struct {
	ID             uint    `gorm:"primaryKey"`
	UpstreamID     uint    `gorm:"index;not null"`
	Name           string  `gorm:"size:128;not null"`
	RateMultiplier float64 `gorm:"type:decimal(12,6);not null;default:1"`
	ModelPrices    string  `gorm:"type:text"`
	SyncedAt       *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time

	Upstream *legacyUpstream `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
}

func (legacyUpstreamGroup) TableName() string { return "upstream_groups" }

type legacyPlatformKey struct {
	ID           uint   `gorm:"primaryKey"`
	UpstreamID   uint   `gorm:"index;not null"`
	Name         string `gorm:"size:128;not null"`
	EncryptedKey string `gorm:"type:text;not null"`
	KeyPreview   string `gorm:"size:64"`
	GroupID      *uint  `gorm:"index"`
	Status       string `gorm:"size:16;not null;default:enabled"`
	HealthStatus string `gorm:"size:32;not null;default:healthy"`
	CreatedAt    time.Time
	UpdatedAt    time.Time

	Upstream *legacyUpstream      `gorm:"foreignKey:UpstreamID;constraint:OnDelete:CASCADE"`
	Group    *legacyUpstreamGroup `gorm:"foreignKey:GroupID;constraint:OnDelete:SET NULL"`
}

func (legacyPlatformKey) TableName() string { return "platform_keys" }

func TestParseDatabaseURL(t *testing.T) {
	dsn, driver := parseDatabaseURL("sqlite://data/local.db")
	if driver != "sqlite" || filepath.ToSlash(dsn) != "data/local.db" {
		t.Fatalf("sqlite: dsn=%q driver=%q", dsn, driver)
	}
	_, driver = parseDatabaseURL("postgres://sum:sum@127.0.0.1:5432/db?sslmode=disable")
	if driver != "postgres" {
		t.Fatalf("postgres driver=%q", driver)
	}
}

// TestMigrateUpstreamGroupsToKeys builds the pre-refactor schema (upstream_groups
// + platform_keys.group_id with FK/index) and checks the one-time migration
// copies rates onto keys and drops the legacy structures on SQLite.
func TestMigrateUpstreamGroupsToKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := Open("sqlite://" + filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	// Let GORM create the legacy schema so the DDL (quoting, constraint names,
	// index names) matches what real pre-refactor databases contain.
	if err := db.AutoMigrate(&legacyUpstream{}, &legacyUpstreamGroup{}, &legacyPlatformKey{}); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	stmts := []string{
		`INSERT INTO upstreams (id, name, base_url, kind, protocols) VALUES (1, 'sub', 'https://a', 'sub2api', 'openai'), (2, 'na', 'https://b', 'new_api', 'openai')`,
		`INSERT INTO upstream_groups (id, upstream_id, name, rate_multiplier, synced_at) VALUES (10, 1, 'k1', 0.35, '2026-01-02 03:04:05'), (11, 2, 'vip', 0.8, NULL)`,
		`INSERT INTO platform_keys (id, upstream_id, name, encrypted_key, group_id) VALUES (100, 1, 'k1', 'x', 10), (101, 2, 'k2', 'x', 11), (102, 2, 'k3', 'x', NULL)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if db.Migrator().HasTable("upstream_groups") {
		t.Fatal("upstream_groups should be dropped")
	}
	var cols []struct{ Name string }
	if err := db.Raw("PRAGMA table_info(platform_keys)").Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	has := map[string]bool{}
	for _, c := range cols {
		has[c.Name] = true
	}
	if has["group_id"] || !has["rate_multiplier"] || !has["billing_group"] {
		t.Fatalf("columns after migration: %v", has)
	}
	type row struct {
		ID             uint
		RateMultiplier float64
		BillingGroup   string
		RateSyncedAt   *string
	}
	var rows []row
	if err := db.Raw("SELECT id, rate_multiplier, billing_group, rate_synced_at FROM platform_keys ORDER BY id").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%+v", rows)
	}
	if rows[0].RateMultiplier != 0.35 || rows[0].BillingGroup != "" || rows[0].RateSyncedAt == nil {
		t.Fatalf("sub2api key: %+v", rows[0])
	}
	if rows[1].RateMultiplier != 0.8 || rows[1].BillingGroup != "vip" {
		t.Fatalf("new-api key: %+v", rows[1])
	}
	if rows[2].RateMultiplier != 1 {
		t.Fatalf("ungrouped key should default to 1: %+v", rows[2])
	}
	var idx []struct{ Name string }
	_ = db.Raw("PRAGMA index_list(platform_keys)").Scan(&idx).Error
	found := false
	for _, i := range idx {
		if i.Name == "idx_platform_keys_upstream_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("upstream_id index should be restored, got %+v", idx)
	}
	// Running again must be a no-op.
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestOpenSQLiteMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	db, err := Open("sqlite://" + filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()
}

func TestMigrateKeyConcurrencyBalanceToUpstream(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pre-balance.db")
	db, err := Open("sqlite://" + filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	if err := db.AutoMigrate(&legacyUpstream{}); err != nil {
		t.Fatalf("legacy upstreams: %v", err)
	}
	if err := db.Exec(`CREATE TABLE platform_keys (
		id INTEGER PRIMARY KEY,
		upstream_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		encrypted_key TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'enabled',
		concurrency INTEGER NOT NULL DEFAULT 0,
		last_balance REAL,
		last_balance_at DATETIME,
		health_status TEXT NOT NULL DEFAULT 'healthy'
	)`).Error; err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`INSERT INTO upstreams (id, name, base_url, kind, protocols) VALUES (1, 'p', 'https://a', 'sub2api', 'openai')`,
		`INSERT INTO platform_keys (id, upstream_id, name, encrypted_key, concurrency, last_balance, last_balance_at) VALUES
			(1, 1, 'k1', 'x', 2, 10.5, '2026-01-01 00:00:00'),
			(2, 1, 'k2', 'x', 5, 3.25, '2026-01-02 00:00:00')`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var up domain.Upstream
	if err := db.First(&up, 1).Error; err != nil {
		t.Fatal(err)
	}
	if up.Concurrency != 5 {
		t.Fatalf("concurrency=%d want 5", up.Concurrency)
	}
	if up.LastBalance == nil || *up.LastBalance != 3.25 {
		t.Fatalf("balance=%v want 3.25", up.LastBalance)
	}
	if up.LastBalanceAt == nil {
		t.Fatal("last_balance_at should be copied")
	}
}

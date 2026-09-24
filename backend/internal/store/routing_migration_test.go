package store

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"path/filepath"
	"simple-up-manage/internal/domain"
	"testing"
)

func TestLegacyDownRequiresRecoveryOnlyOnce(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.PlatformKey{}); err != nil {
		t.Fatal(err)
	}
	k := domain.PlatformKey{Name: "legacy", EncryptedKey: "test", HealthStatus: domain.HealthDown}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var r domain.RoutingCircuit
	if err := db.First(&r).Error; err != nil {
		t.Fatal(err)
	}
	if !r.Open || r.PlatformKeyID != k.ID || r.Reason != "legacy_health_unverified" {
		t.Fatal(r)
	}
	r.Open = false
	if err := db.Save(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	db.First(&r)
	if r.Open {
		t.Fatal("restart recreated legacy gate")
	}
}

func TestRoutingGenerationMigrationPreservesGate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "generation.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	defer conn.Close()
	if err := db.Exec(`CREATE TABLE routing_circuits (scope TEXT PRIMARY KEY, platform_key_id INTEGER, open NUMERIC, reason TEXT)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO routing_circuits(scope,platform_key_id,open,reason) VALUES ('key:1',1,1,'authentication_failure')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	var row domain.RoutingCircuit
	if err := db.First(&row, "scope = ?", "key:1").Error; err != nil {
		t.Fatal(err)
	}
	if row.Generation != 0 || !row.Open || row.Reason != "authentication_failure" {
		t.Fatalf("migration changed gate: %+v", row)
	}
	if err := db.Model(&row).Update("generation", 7).Error; err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&row, "scope = ?", "key:1").Error; err != nil {
		t.Fatal(err)
	}
	if row.Generation != 7 || !row.Open {
		t.Fatalf("restart reset generation: %+v", row)
	}
}

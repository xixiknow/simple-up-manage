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

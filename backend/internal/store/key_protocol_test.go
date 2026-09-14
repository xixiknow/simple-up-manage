package store

import (
	"path/filepath"
	"simple-up-manage/internal/domain"
	"testing"
)

func TestKeyProtocolMigrationInheritsProvider(t *testing.T) {
	db, err := Open("sqlite://" + filepath.ToSlash(filepath.Join(t.TempDir(), "protocol.db")))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "fixture", BaseURL: "https://example.test", Kind: domain.KindOpenAICompat, Protocols: "openai,anthropic"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	k := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: "fixture"}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&domain.PlatformKey{}, "Protocols"); err != nil {
		t.Fatal(err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Preload("Upstream").First(&k, k.ID).Error; err != nil {
		t.Fatal(err)
	}
	if k.Protocols != "" || len(k.EffectiveProtocols()) != 2 {
		t.Fatalf("legacy key no longer inherits: %+v", k)
	}
}

package store

import (
	"path/filepath"
	"testing"

	"simple-up-manage/internal/domain"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCleanupIntelStaleReferences(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "intel_cleanup.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := AutoMigrate(db); err != nil {
		t.Fatal(err)
	}

	upstream := domain.Upstream{Name: "u", BaseURL: "http://u", Kind: domain.KindNewAPI, Status: domain.StatusEnabled}
	if err := db.Create(&upstream).Error; err != nil {
		t.Fatal(err)
	}
	key := domain.PlatformKey{UpstreamID: upstream.ID, Name: "k", EncryptedKey: "enc", Status: domain.StatusEnabled}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	group := domain.RouteGroup{Name: "g", Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	livePlan := domain.IntelTestPlan{Name: "live", RouteGroupID: group.ID, Model: "m", Enabled: true}
	deadPlan := domain.IntelTestPlan{Name: "dead-group", RouteGroupID: 999, Model: "m", Enabled: true}
	if err := db.Create(&livePlan).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&deadPlan).Error; err != nil {
		t.Fatal(err)
	}
	states := []domain.IntelQuarantineState{
		{PlanID: livePlan.ID, PlatformKeyID: key.ID, Status: domain.IntelQuarantineQuarantined}, // keep: group exists
		{PlanID: deadPlan.ID, PlatformKeyID: key.ID, Status: domain.IntelQuarantineQuarantined}, // drop: group gone
		{PlanID: 888, PlatformKeyID: key.ID, Status: domain.IntelQuarantineQuarantined},         // drop: plan gone
	}
	if err := db.Create(&states).Error; err != nil {
		t.Fatal(err)
	}
	joins := []domain.RouteGroupKey{
		{RouteGroupID: group.ID, PlatformKeyID: key.ID}, // keep
		{RouteGroupID: 999, PlatformKeyID: key.ID},      // drop: group gone
		{RouteGroupID: group.ID, PlatformKeyID: 777},    // drop: key gone
	}
	if err := db.Create(&joins).Error; err != nil {
		t.Fatal(err)
	}

	if err := cleanupIntelStaleReferences(db); err != nil {
		t.Fatal(err)
	}

	var stateCount int64
	if err := db.Model(&domain.IntelQuarantineState{}).Count(&stateCount).Error; err != nil {
		t.Fatal(err)
	}
	if stateCount != 1 {
		t.Fatalf("quarantine states left = %d, want 1 (live plan only)", stateCount)
	}
	var joinCount int64
	if err := db.Model(&domain.RouteGroupKey{}).Count(&joinCount).Error; err != nil {
		t.Fatal(err)
	}
	if joinCount != 1 {
		t.Fatalf("route_group_keys left = %d, want 1 (live member only)", joinCount)
	}
	var stillEnabled int64
	if err := db.Model(&domain.IntelTestPlan{}).Where("enabled = ?", true).Count(&stillEnabled).Error; err != nil {
		t.Fatal(err)
	}
	if stillEnabled != 1 {
		t.Fatalf("enabled plans = %d, want 1 (dead-group plan must be disabled)", stillEnabled)
	}
	var dead domain.IntelTestPlan
	if err := db.First(&dead, deadPlan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if dead.Enabled {
		t.Fatalf("plan %d on a deleted group must be disabled", dead.ID)
	}
}

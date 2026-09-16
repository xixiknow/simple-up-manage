package store

import (
	"context"
	"fmt"
	"time"

	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
)

func migrateDashboard(db *gorm.DB) error {
	if err := db.AutoMigrate(
		&dashboard.CatalogVersion{},
		&dashboard.CatalogVersionPrice{},
		&dashboard.RequestFact{},
		&dashboard.AttemptFact{},
		&dashboard.EventLedger{},
		&dashboard.MinuteAgg{},
		&dashboard.DayAgg{},
		&dashboard.Gap{},
		&dashboard.Settings{},
		&dashboard.Meta{},
	); err != nil {
		return err
	}
	if err := backfillProbeEnabled(db); err != nil {
		return err
	}
	if err := seedDashboardSettings(db); err != nil {
		return err
	}
	if _, err := dashboard.EnsureCatalogVersion(context.Background(), db); err != nil {
		return err
	}
	return nil
}

func rejectMultiConsumerRouteGroups(db *gorm.DB) error {
	if !db.Migrator().HasTable(&domain.ConsumerRouteGroup{}) {
		return nil
	}
	var rows []struct {
		ConsumerKeyID uint
		N             int64
	}
	if err := db.Model(&domain.ConsumerRouteGroup{}).
		Select("consumer_key_id, COUNT(*) AS n").
		Group("consumer_key_id").
		Having("COUNT(*) > 1").
		Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ConsumerKeyID)
	}
	return fmt.Errorf("consumer_route_groups has multi-group bindings for consumer_key_ids=%v; refuse to migrate", ids)
}

func backfillProbeEnabled(db *gorm.DB) error {
	key := &domain.PlatformKey{}
	if !db.Migrator().HasColumn(key, "probe_enabled") {
		return nil
	}
	return db.Model(key).Where("probe_enabled IS NULL").Update("probe_enabled", true).Error
}

func seedDashboardSettings(db *gorm.DB) error {
	cfg := dashboard.DefaultSettings()
	if err := db.Where("id = ?", 1).FirstOrCreate(&cfg).Error; err != nil {
		return err
	}
	var meta dashboard.Meta
	if err := db.First(&meta, 1).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			meta = dashboard.Meta{ID: 1, AvailableFrom: time.Now().UTC()}
			return db.Create(&meta).Error
		}
		return err
	}
	return nil
}

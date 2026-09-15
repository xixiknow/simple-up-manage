package store

import (
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"simple-up-manage/internal/domain"
	"time"
)

func migrateRoutingHealth(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&domain.RoutingMigration{ID: 1})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		var keys []domain.PlatformKey
		if err := tx.Select("id").Where("health_status = ?", domain.HealthDown).Find(&keys).Error; err != nil {
			return err
		}
		for _, k := range keys {
			r := domain.RoutingCircuit{Scope: fmt.Sprintf("key:%d", k.ID), PlatformKeyID: k.ID, Open: true, Until: time.Now(), Reason: "legacy_health_unverified"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

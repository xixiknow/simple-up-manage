package store

import (
	"encoding/json"
	"fmt"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
)

// migrateRateChangeNoticesToNotices copies legacy rate_change_notices rows
// into the unified notices table once, then drops the legacy table. The whole
// move is one transaction, so a failed drop cannot duplicate rows on retry.
// Fresh installs have no legacy table and skip straight through.
func migrateRateChangeNoticesToNotices(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable("rate_change_notices") {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		type legacyRow struct {
			ID            uint
			PlatformKeyID uint
			UpstreamID    uint
			KeyName       string
			UpstreamName  string
			OldRate       float64
			NewRate       float64
			Direction     string
			Source        string
			ReadAt        *time.Time
			CreatedAt     time.Time
		}
		var rows []legacyRow
		if err := tx.Table("rate_change_notices").Order("id ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy rate notices: %w", err)
		}
		for _, r := range rows {
			payload, err := json.Marshal(map[string]any{
				"platform_key_id": r.PlatformKeyID,
				"upstream_id":     r.UpstreamID,
				"key_name":        r.KeyName,
				"upstream_name":   r.UpstreamName,
				"old_rate":        r.OldRate,
				"new_rate":        r.NewRate,
				"direction":       r.Direction,
			})
			if err != nil {
				return fmt.Errorf("encode notice payload %d: %w", r.ID, err)
			}
			n := domain.Notice{
				Kind:      domain.NoticeKindRateChange,
				Source:    r.Source,
				Summary:   fmt.Sprintf("账号 %s 倍率 ×%s → ×%s", r.KeyName, domain.FormatRate(r.OldRate), domain.FormatRate(r.NewRate)),
				Payload:   string(payload),
				ReadAt:    r.ReadAt,
				CreatedAt: r.CreatedAt,
			}
			if err := tx.Create(&n).Error; err != nil {
				return fmt.Errorf("copy rate notice %d: %w", r.ID, err)
			}
		}
		if err := tx.Migrator().DropTable("rate_change_notices"); err != nil {
			return fmt.Errorf("drop rate_change_notices: %w", err)
		}
		return nil
	})
}

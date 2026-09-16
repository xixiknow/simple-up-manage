package dashboard

import (
	"context"
	"strings"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
)

type ModelPrice struct {
	Input           float64
	Output          float64
	CacheReadCoeff  float64
	CacheWriteCoeff float64
	OK              bool
}

func PublishCatalogVersion(ctx context.Context, db *gorm.DB) (uint, error) {
	if db == nil {
		return 0, nil
	}
	var rows []domain.CatalogModel
	if err := db.WithContext(ctx).Find(&rows).Error; err != nil {
		return 0, err
	}
	meta := domain.CatalogMeta{ID: 1, Source: "models.dev"}
	_ = db.WithContext(ctx).First(&meta, 1).Error
	ver := CatalogVersion{
		Source:      meta.Source,
		ModelCount:  len(rows),
		PublishedAt: time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ver).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		prices := make([]CatalogVersionPrice, 0, len(rows))
		for _, r := range rows {
			prices = append(prices, CatalogVersionPrice{
				VersionID:       ver.ID,
				Vendor:          r.Vendor,
				ModelID:         r.ModelID,
				InputCost:       r.InputCost,
				OutputCost:      r.OutputCost,
				CacheReadCoeff:  0.1,
				CacheWriteCoeff: 1.25,
			})
		}
		return tx.CreateInBatches(prices, 100).Error
	}); err != nil {
		return 0, err
	}
	return ver.ID, nil
}

func EnsureCatalogVersion(ctx context.Context, db *gorm.DB) (uint, error) {
	var ver CatalogVersion
	err := db.WithContext(ctx).Order("id DESC").First(&ver).Error
	if err == nil {
		return ver.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	var n int64
	if err := db.WithContext(ctx).Model(&domain.CatalogModel{}).Count(&n).Error; err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	return PublishCatalogVersion(ctx, db)
}

func CurrentCatalogVersion(db *gorm.DB) uint {
	if db == nil {
		return 0
	}
	var ver CatalogVersion
	if err := db.Order("id DESC").First(&ver).Error; err != nil {
		return 0
	}
	return ver.ID
}

func LookupVersionPrice(db *gorm.DB, versionID uint, model string) ModelPrice {
	id := catalogModelID(model)
	if db == nil || versionID == 0 || id == "" {
		return ModelPrice{CacheReadCoeff: 0.1, CacheWriteCoeff: 1.25}
	}
	var row CatalogVersionPrice
	err := db.Where("version_id = ? AND LOWER(model_id) = ? AND (input_cost > 0 OR output_cost > 0)", versionID, id).
		Order("vendor").First(&row).Error
	if err != nil {
		return ModelPrice{CacheReadCoeff: 0.1, CacheWriteCoeff: 1.25}
	}
	cr, cw := row.CacheReadCoeff, row.CacheWriteCoeff
	if cr <= 0 {
		cr = 0.1
	}
	if cw <= 0 {
		cw = 1.25
	}
	return ModelPrice{Input: row.InputCost, Output: row.OutputCost, CacheReadCoeff: cr, CacheWriteCoeff: cw, OK: true}
}

func catalogModelID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		id = id[i+1:]
	}
	return id
}

// BaseCostUSD is catalog price × tokens / 1e6, without key or sale multipliers.
func BaseCostUSD(price ModelPrice, protocol string, input, cacheRead, cacheWrite, output int64, usageKnown bool) *float64 {
	if !price.OK {
		return nil
	}
	if !usageKnown {
		return nil
	}
	uncached := input
	if protocol == domain.ProtocolOpenAI {
		uncached = max(0, input-cacheRead-cacheWrite)
	}
	usd := (float64(uncached)*price.Input +
		float64(cacheRead)*price.Input*price.CacheReadCoeff +
		float64(cacheWrite)*price.Input*price.CacheWriteCoeff +
		float64(output)*price.Output) / 1e6
	v := round8(usd)
	return &v
}

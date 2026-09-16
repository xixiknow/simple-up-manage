package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Open(databaseURL string) (*gorm.DB, error) {
	dsn, driver := parseDatabaseURL(databaseURL)
	cfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)}

	var (
		db  *gorm.DB
		err error
	)
	switch driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil && filepath.Dir(dsn) != "." {
			return nil, fmt.Errorf("create sqlite dir: %w", err)
		}
		db, err = gorm.Open(sqlite.Open(dsn), cfg)
		if err != nil {
			return nil, err
		}
		if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			return nil, err
		}
		return db, nil
	default:
		db, err = gorm.Open(postgres.Open(dsn), cfg)
		return db, err
	}
}

func parseDatabaseURL(raw string) (dsn, driver string) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "sqlite://"):
		return filepath.Clean(strings.TrimPrefix(raw, "sqlite://")), "sqlite"
	case strings.HasPrefix(raw, "file:"):
		return raw, "sqlite"
	case strings.HasSuffix(raw, ".db") || strings.HasSuffix(raw, ".sqlite") || strings.HasSuffix(raw, ".sqlite3"):
		return filepath.Clean(raw), "sqlite"
	default:
		return raw, "postgres"
	}
}

func AutoMigrate(db *gorm.DB) error {
	if err := rejectMultiConsumerRouteGroups(db); err != nil {
		return err
	}
	m := db.Migrator()
	up := &domain.Upstream{}
	backfillBalance := m.HasTable(up) && !m.HasColumn(up, "last_balance")
	backfillConc := m.HasTable(up) && !m.HasColumn(up, "concurrency")
	if err := db.AutoMigrate(
		&domain.Upstream{},
		&domain.PlatformKey{},
		&domain.ConsumerKey{},
		&domain.RouteGroup{},
		&domain.RouteGroupKey{},
		&domain.ConsumerRouteGroup{},
		&domain.RequestLog{},
		&domain.RequestAttempt{},
		&domain.LogBody{},
		&domain.RoutingCircuit{},
		&domain.RoutingObservation{},
		&domain.RoutingBudget{},
		&domain.RoutingMigration{},
		&domain.KeyModelCooldown{},
		&domain.ProbeLog{},
		&domain.RateChangeNotice{},
		&domain.SchedulerSettings{},
		&domain.CatalogModel{},
		&domain.CatalogMeta{},
	); err != nil {
		return err
	}
	if err := migrateRoutingHealth(db); err != nil {
		return err
	}
	if err := migrateRequestLogState(db); err != nil {
		return err
	}
	if err := db.Migrator().DropTable("price_thresholds"); err != nil {
		return err
	}
	// Global price thresholds are gone; leftover health tags would otherwise
	// stick until an admin action triggers RecomputeHealth.
	if err := db.Model(&domain.PlatformKey{}).
		Where("health_status = ?", "price_out_of_range").
		Update("health_status", domain.HealthHealthy).Error; err != nil {
		return err
	}
	if err := migrateKeyConcurrencyBalanceToUpstream(db, backfillConc, backfillBalance); err != nil {
		return err
	}
	if err := migrateUpstreamGroupsToKeys(db); err != nil {
		return err
	}
	return migrateDashboard(db)
}

func migrateRequestLogState(db *gorm.DB) error {
	if err := db.Model(&domain.RequestLog{}).Where("in_flight IS NULL").Update("in_flight", false).Error; err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var after uint
		for {
			var rows []domain.RequestLog
			if err := tx.Select("id", "request_body", "request_headers", "request_body_trunc", "stream_known", "completed_at", "in_flight", "created_at", "duration_ms").Where("id > ? AND (stream_known = ? OR (completed_at IS NULL AND in_flight = ?))", after, false, false).Order("id").Limit(100).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, row := range rows {
				after = row.ID
				if row.CompletedAt == nil && !row.InFlight {
					ended := row.CreatedAt.Add(time.Duration(row.DurationMs) * time.Millisecond).UTC()
					if err := tx.Model(&domain.RequestLog{}).Where("id = ?", row.ID).Update("completed_at", ended).Error; err != nil {
						return err
					}
				}
				if row.StreamKnown {
					continue
				}
				if row.RequestBodyTrunc {
					continue
				}
				var headers map[string]json.RawMessage
				_ = json.Unmarshal([]byte(row.RequestHeaders), &headers)
				var contentType string
				for name, value := range headers {
					if strings.EqualFold(name, "content-type") {
						_ = json.Unmarshal(value, &contentType)
					}
				}
				stream, known := domain.RequestStream(contentType, []byte(row.RequestBody))
				if err := tx.Model(&domain.RequestLog{}).Where("id = ?", row.ID).Updates(map[string]any{"stream": stream, "stream_known": known}).Error; err != nil {
					return err
				}
			}
		}
	})
}

// migrateKeyConcurrencyBalanceToUpstream copies per-key concurrency / balance
// onto the provider once, when those columns are first added to upstreams.
func migrateKeyConcurrencyBalanceToUpstream(db *gorm.DB, backfillConc, backfillBalance bool) error {
	if !backfillConc && !backfillBalance {
		return nil
	}
	if backfillConc {
		type concRow struct {
			UpstreamID uint
			MaxConc    int
		}
		var rows []concRow
		if err := db.Table("platform_keys").
			Select("upstream_id, MAX(concurrency) AS max_conc").
			Group("upstream_id").
			Scan(&rows).Error; err != nil {
			return fmt.Errorf("read key concurrency: %w", err)
		}
		for _, r := range rows {
			if r.MaxConc <= 0 {
				continue
			}
			if err := db.Model(&domain.Upstream{}).Where("id = ?", r.UpstreamID).
				Update("concurrency", r.MaxConc).Error; err != nil {
				return fmt.Errorf("copy concurrency to upstream %d: %w", r.UpstreamID, err)
			}
		}
	}
	if backfillBalance {
		type balRow struct {
			UpstreamID    uint
			LastBalance   *float64
			LastBalanceAt *time.Time
		}
		var rows []balRow
		if err := db.Raw(`
			SELECT k.upstream_id, k.last_balance, k.last_balance_at
			FROM platform_keys k
			INNER JOIN (
				SELECT upstream_id, MAX(last_balance_at) AS last_at
				FROM platform_keys
				WHERE last_balance_at IS NOT NULL
				GROUP BY upstream_id
			) t ON k.upstream_id = t.upstream_id AND k.last_balance_at = t.last_at
		`).Scan(&rows).Error; err != nil {
			return fmt.Errorf("read key balances: %w", err)
		}
		seen := map[uint]struct{}{}
		for _, r := range rows {
			if _, ok := seen[r.UpstreamID]; ok {
				continue
			}
			seen[r.UpstreamID] = struct{}{}
			if err := db.Model(&domain.Upstream{}).Where("id = ?", r.UpstreamID).Updates(map[string]any{
				"last_balance":    r.LastBalance,
				"last_balance_at": r.LastBalanceAt,
			}).Error; err != nil {
				return fmt.Errorf("copy balance to upstream %d: %w", r.UpstreamID, err)
			}
		}
	}
	return nil
}

// migrateUpstreamGroupsToKeys is a one-time migration for databases created
// before rate multipliers moved onto platform keys. It copies each key's group
// rate / sync time / group name onto the key, then drops the legacy
// upstream_groups table and platform_keys.group_id column.
func migrateUpstreamGroupsToKeys(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable("upstream_groups") {
		return nil
	}
	pk := &domain.PlatformKey{}
	if m.HasColumn(pk, "group_id") {
		if err := db.Transaction(func(tx *gorm.DB) error {
			type row struct {
				KeyID          uint
				RateMultiplier float64
				SyncedAt       *time.Time
				Name           string
				Kind           string
			}
			var rows []row
			if err := tx.Table("platform_keys AS k").
				Select("k.id AS key_id, g.rate_multiplier, g.synced_at, g.name, u.kind").
				Joins("JOIN upstream_groups g ON g.id = k.group_id").
				Joins("JOIN upstreams u ON u.id = k.upstream_id").
				Scan(&rows).Error; err != nil {
				return fmt.Errorf("read legacy groups: %w", err)
			}
			for _, r := range rows {
				updates := map[string]any{
					"rate_multiplier": r.RateMultiplier,
					"rate_synced_at":  r.SyncedAt,
				}
				if r.Kind == domain.KindNewAPI {
					updates["billing_group"] = r.Name
				}
				if err := tx.Model(pk).Where("id = ?", r.KeyID).Updates(updates).Error; err != nil {
					return fmt.Errorf("copy rate to key %d: %w", r.KeyID, err)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		// The FK / index must go before the column can be dropped (SQLite
		// rejects dropping a column that is referenced by a constraint).
		if m.HasConstraint(pk, "fk_platform_keys_group") {
			if err := m.DropConstraint(pk, "fk_platform_keys_group"); err != nil {
				return fmt.Errorf("drop fk_platform_keys_group: %w", err)
			}
		}
		if m.HasIndex(pk, "idx_platform_keys_group_id") {
			if err := m.DropIndex(pk, "idx_platform_keys_group_id"); err != nil {
				return fmt.Errorf("drop idx_platform_keys_group_id: %w", err)
			}
		}
		if err := m.DropColumn(pk, "group_id"); err != nil {
			return fmt.Errorf("drop platform_keys.group_id: %w", err)
		}
		// SQLite recreates the table for the steps above and loses its indexes;
		// a second AutoMigrate pass restores them (no-op elsewhere).
		if err := db.AutoMigrate(pk); err != nil {
			return fmt.Errorf("re-migrate platform_keys: %w", err)
		}
	}
	if err := m.DropTable("upstream_groups"); err != nil {
		return fmt.Errorf("drop upstream_groups: %w", err)
	}
	return nil
}

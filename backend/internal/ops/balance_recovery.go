package ops

import (
	"context"
	"time"

	"gorm.io/gorm"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/routinghealth"
)

// Provider balance is shared by its keys. Clear only explicit quota gates that
// predate this balance check, leaving newer failures and other causes intact.
func (s *Service) releaseBalanceCooldowns(ctx context.Context, upstreamID uint, checkedAfter time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ids []uint
		if err := tx.Model(&domain.PlatformKey{}).Where("upstream_id = ?", upstreamID).Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
		for _, id := range ids {
			var gates []domain.RoutingCircuit
			if err := tx.Where("platform_key_id = ? AND open = ? AND reason = ? AND opened_at <= ?", id, true, "key_quota_exhausted", checkedAfter).Order("scope").Find(&gates).Error; err != nil {
				return err
			}
			// Match routing admission's key-before-model lock order.
			for i := range gates {
				if gates[i].Scope == routinghealth.KeyScope(id) {
					gates[0], gates[i] = gates[i], gates[0]
					break
				}
			}
			for _, gate := range gates {
				now := time.Now()
				if err := tx.Model(&domain.RoutingCircuit{}).Where("scope = ? AND open = ? AND reason = ? AND opened_at <= ?", gate.Scope, true, "key_quota_exhausted", checkedAfter).Updates(map[string]any{
					"open": false, "opened_at": now, "until": time.Time{}, "reason": "", "failures": 0, "failure_times": nil, "backoff_sec": 0,
					"lease": "", "lease_until": time.Time{}, "check_lease": "", "check_until": nil, "check_ok": false, "check_error": "", "check_at": nil, "next_check_at": nil, "check_backoff": 0,
				}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

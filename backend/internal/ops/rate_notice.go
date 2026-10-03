package ops

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"simple-up-manage/internal/domain"
)

const rateChangeEpsilon = 1e-6

func rateChanged(oldRate, newRate float64) bool {
	return math.Abs(oldRate-newRate) > rateChangeEpsilon
}

func skipFirstBillingSync(key *domain.PlatformKey, oldRate float64) bool {
	if key == nil {
		return false
	}
	return key.RateSyncedAt == nil && math.Abs(oldRate-1) <= rateChangeEpsilon
}

// rateChangeSummary renders the one-line inbox description. Payload carries
// the structured fields; Summary is what the list shows at a glance.
func rateChangeSummary(keyName string, oldRate, newRate float64) string {
	return fmt.Sprintf("账号 %s 倍率 ×%s → ×%s", keyName, domain.FormatRate(oldRate), domain.FormatRate(newRate))
}

// RecordRateChange inserts a notice when the multiplier actually moved.
// Billing's first sync from the default 1 (rate_synced_at still nil) is skipped;
// later billing moves and every manual edit are recorded.
func (s *Service) RecordRateChange(ctx context.Context, key *domain.PlatformKey, oldRate, newRate float64, source string) error {
	if s == nil || s.DB == nil || key == nil {
		return nil
	}
	if !rateChanged(oldRate, newRate) {
		return nil
	}
	if source == domain.RateChangeBilling && skipFirstBillingSync(key, oldRate) {
		return nil
	}
	direction := domain.RateChangeDown
	if newRate > oldRate {
		direction = domain.RateChangeUp
	}
	upstreamName := ""
	if key.Upstream != nil {
		upstreamName = key.Upstream.Name
	}
	payload := map[string]any{
		"platform_key_id": key.ID,
		"upstream_id":     key.UpstreamID,
		"key_name":        key.Name,
		"upstream_name":   upstreamName,
		"old_rate":        oldRate,
		"new_rate":        newRate,
		"direction":       direction,
	}
	return s.RecordNotice(ctx, domain.NoticeKindRateChange, source, rateChangeSummary(key.Name, oldRate, newRate), payload)
}

func (s *Service) applyBillingRate(ctx context.Context, key *domain.PlatformKey, rate float64, now time.Time) error {
	if key == nil {
		return nil
	}
	// Snapshot before Updates: GORM may copy map values (including
	// rate_synced_at) back onto the struct, which would hide the first-sync skip.
	before := *key
	if err := s.DB.WithContext(ctx).Model(key).Updates(billingSyncedUpdates(key, rate, now)).Error; err != nil {
		return err
	}
	if err := s.RecordRateChange(ctx, &before, before.RateMultiplier, rate, domain.RateChangeBilling); err != nil {
		log.Printf("rate-change notice: %v", err)
	}
	key.RateMultiplier = rate
	ts := now
	key.RateSyncedAt = &ts
	return nil
}

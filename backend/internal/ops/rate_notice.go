package ops

import (
	"context"
	"log"
	"math"
	"time"

	"simple-up-manage/internal/domain"
)

const rateChangeEpsilon = 1e-6

// RateChangeNoticeRetention is how long price-change rows are kept. The
// log-retention job deletes older notices on the same cadence as request logs.
const RateChangeNoticeRetention = 90 * 24 * time.Hour

func rateChanged(oldRate, newRate float64) bool {
	return math.Abs(oldRate-newRate) > rateChangeEpsilon
}

func skipFirstBillingSync(key *domain.PlatformKey, oldRate float64) bool {
	if key == nil {
		return false
	}
	return key.RateSyncedAt == nil && math.Abs(oldRate-1) <= rateChangeEpsilon
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
	n := domain.RateChangeNotice{
		PlatformKeyID: key.ID,
		UpstreamID:    key.UpstreamID,
		KeyName:       key.Name,
		UpstreamName:  upstreamName,
		OldRate:       oldRate,
		NewRate:       newRate,
		Direction:     direction,
		Source:        source,
	}
	return s.DB.WithContext(ctx).Create(&n).Error
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

// PurgeRateChangeNotices deletes notices older than 90 days.
func (s *Service) PurgeRateChangeNotices(ctx context.Context) (int64, error) {
	cut := time.Now().Add(-RateChangeNoticeRetention)
	res := s.DB.WithContext(ctx).Where("created_at < ?", cut).Delete(&domain.RateChangeNotice{})
	return res.RowsAffected, res.Error
}

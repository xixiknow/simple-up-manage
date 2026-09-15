package ops

import (
	"context"
	"time"

	"simple-up-manage/internal/domain"
)

// PurgeRequestLogs deletes request logs older than retention. Returns the
// number of rows removed.
func (s *Service) PurgeRequestLogs(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	cut := time.Now().Add(-retention)
	if s.DB.Migrator().HasTable(&domain.RoutingObservation{}) {
		if err := s.DB.WithContext(ctx).Where("created_at < ?", cut).Delete(&domain.RoutingObservation{}).Error; err != nil {
			return 0, err
		}
		if err := s.DB.WithContext(ctx).Where("updated_at < ?", cut).Delete(&domain.RoutingBudget{}).Error; err != nil {
			return 0, err
		}
		if err := s.DB.WithContext(ctx).Where("updated_at < ? AND open = ?", cut, false).Delete(&domain.RoutingCircuit{}).Error; err != nil {
			return 0, err
		}
	}
	if s.DB.Migrator().HasTable(&domain.RequestAttempt{}) {
		if err := s.DB.WithContext(ctx).Where("completed_at < ?", cut).Delete(&domain.RequestAttempt{}).Error; err != nil {
			return 0, err
		}
	}
	res := s.DB.WithContext(ctx).Where("created_at < ?", cut).Delete(&domain.RequestLog{})
	return res.RowsAffected, res.Error
}

const staleInFlightAge = 6 * time.Minute

// FinalizeStaleInFlightLogs marks abandoned in-flight rows as failed so the
// UI does not tick forever after a crash or killed stream. Age is the gateway
// overall timeout (300s) plus a short buffer.
func (s *Service) FinalizeStaleInFlightLogs(ctx context.Context) (int64, error) {
	cut := time.Now().Add(-staleInFlightAge)
	res := s.DB.WithContext(ctx).Model(&domain.RequestLog{}).
		Where("in_flight = ? AND created_at < ?", true, cut).
		Updates(map[string]any{
			"in_flight":     false,
			"completed_at":  time.Now().UTC(),
			"success":       false,
			"error_message": "stale in-flight request",
			"duration_ms":   int(staleInFlightAge / time.Millisecond),
		})
	return res.RowsAffected, res.Error
}

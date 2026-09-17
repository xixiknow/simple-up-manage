package ops

import (
	"context"
	"errors"
	"gorm.io/gorm"
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
	if err := s.Archives.Purge(ctx, cut); err != nil {
		return 0, err
	}
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
	var rows []domain.RequestLog
	if err := s.DB.WithContext(ctx).Select("id", "created_at", "log_revision", "ttft_ms").Where("in_flight = ? AND created_at < ?", true, cut).Order("id").Limit(100).Find(&rows).Error; err != nil {
		return 0, err
	}
	var count int64
	for _, row := range rows {
		updates := map[string]any{
			"in_flight":     false,
			"completed_at":  time.Now().UTC(),
			"success":       false,
			"error_message": "stale in-flight request",
			"ttft_status":   gorm.Expr("CASE WHEN ttft_ms > 0 THEN 'measured' ELSE 'interrupted' END"),
			"duration_ms":   int(staleInFlightAge / time.Millisecond),
		}
		if s.DB.Migrator().HasTable(&domain.RequestAttempt{}) {
			var attempt domain.RequestAttempt
			err := s.DB.WithContext(ctx).Where("request_log_id = ?", row.ID).Order("completed_at DESC, id DESC").First(&attempt).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return count, err
			}
			if err == nil && attempt.Result == "success" {
				updates["success"], updates["error_message"] = true, ""
				updates["failure_scope"], updates["failure_action"] = "", ""
				updates["completed_at"], updates["status_code"] = attempt.CompletedAt, attempt.StatusCode
				updates["duration_ms"] = max(0, int(attempt.CompletedAt.Sub(row.CreatedAt).Milliseconds()))
				updates["platform_key_id"] = attempt.PlatformKeyID
				updates["input_tokens"], updates["output_tokens"] = attempt.InputTokens, attempt.OutputTokens
				updates["cache_read_tokens"], updates["cache_creation_tokens"] = attempt.CacheReadTokens, attempt.CacheCreationTokens
				updates["ttft_status"], updates["ttft_event"] = attempt.TTFTStatus, attempt.TTFTEvent
				updates["ttft_ms"] = 0
				if attempt.TTFTMs > 0 {
					updates["ttft_ms"] = max(1, int(attempt.StartedAt.Sub(row.CreatedAt).Milliseconds())+attempt.TTFTMs)
				}
			}
		}
		res := s.DB.WithContext(ctx).Model(&domain.RequestLog{}).Where("id = ? AND in_flight = ? AND log_revision = ?", row.ID, true, row.LogRevision).Updates(updates)
		if res.Error != nil {
			return count, res.Error
		}
		count += res.RowsAffected
	}
	return count, nil
}

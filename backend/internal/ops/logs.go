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
	res := s.DB.WithContext(ctx).Where("created_at < ?", cut).Delete(&domain.RequestLog{})
	return res.RowsAffected, res.Error
}

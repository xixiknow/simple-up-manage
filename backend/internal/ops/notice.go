package ops

import (
	"context"
	"encoding/json"
	"time"

	"simple-up-manage/internal/domain"
)

// NoticeRetention is how long notice rows are kept. The log-retention job
// deletes older notices on the same cadence as request logs.
const NoticeRetention = 90 * 24 * time.Hour

// RecordNotice inserts one unified-inbox row. Summary is truncated to the
// column width so over-long model lists cannot fail the insert.
func (s *Service) RecordNotice(ctx context.Context, kind, source, summary string, payload any) error {
	if s == nil || s.DB == nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	n := domain.Notice{
		Kind:    kind,
		Source:  source,
		Summary: truncate(summary, 512),
		Payload: string(data),
	}
	return s.DB.WithContext(ctx).Create(&n).Error
}

// PurgeNotices deletes notices older than the given age.
func (s *Service) PurgeNotices(ctx context.Context, olderThan time.Duration) (int64, error) {
	cut := time.Now().Add(-olderThan)
	res := s.DB.WithContext(ctx).Where("created_at < ?", cut).Delete(&domain.Notice{})
	return res.RowsAffected, res.Error
}

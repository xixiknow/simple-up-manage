package ops

import (
	"context"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"
)

// Only scheduled diagnostics use this gate; manual checks remain available.
// Diagnostic failures never modify business circuits or operator switches.
func (s *Service) diagnosticReady(ctx context.Context, keyID uint, kind string, now time.Time) (bool, error) {
	var rows []domain.ProbeLog
	if err := s.DB.WithContext(ctx).Select("success", "status_code", "error_message", "retry_after", "created_at").Where("platform_key_id = ? AND kind = ?", keyID, kind).Order("created_at DESC, id DESC").Limit(6).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 || rows[0].Success {
		return true, nil
	}
	last := rows[0]
	failures := 0
	for _, row := range rows {
		if row.Success {
			break
		}
		failures++
	}
	delay := min(time.Minute*time.Duration(1<<uint(failures-1)), 15*time.Minute)
	switch upstream.ParseError([]byte(last.ErrorMessage)).Kind() {
	case "credential_disabled", "authentication":
		delay = time.Hour
	case "quota":
		delay = 15 * time.Minute
	case "capability":
		delay = 30 * time.Minute
	default:
		if last.StatusCode == 401 {
			delay = time.Hour
		}
	}
	delay = max(delay, upstream.RetryAfter(last.RetryAfter, last.CreatedAt))
	return !last.CreatedAt.Add(delay).After(now), nil
}

package ops

import (
	"context"
	"gorm.io/gorm"
	"simple-up-manage/internal/domain"
	"strings"
)

// ExplainRouteRejections distinguishes membership from request matching without
// changing the authorization rules used for business traffic.
func ExplainRouteRejections(ctx context.Context, db *gorm.DB, consumer uint, protocol, model string) (map[uint]string, error) {
	var groups []domain.RouteGroup
	err := db.WithContext(ctx).Where("id IN (?)", db.Model(&domain.ConsumerRouteGroup{}).Select("route_group_id").Where("consumer_key_id = ?", consumer)).Find(&groups).Error
	if err != nil {
		return nil, err
	}
	out := map[uint]string{}
	for _, g := range groups {
		reason := "route_model_mismatch"
		switch {
		case g.Status != domain.StatusEnabled:
			reason = "route_group_disabled"
		case !g.MatchesProtocol(protocol):
			reason = "route_protocol_mismatch"
		case strings.TrimSpace(model) == "" && len(g.Models) > 0:
			reason = "model_required"
		}
		var links []domain.RouteGroupKey
		if err := db.WithContext(ctx).Where("route_group_id = ?", g.ID).Find(&links).Error; err != nil {
			return nil, err
		}
		for _, l := range links {
			out[l.PlatformKeyID] = reason
		}
	}
	return out, nil
}

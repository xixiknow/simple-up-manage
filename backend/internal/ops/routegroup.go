package ops

import (
	"context"
	"strings"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
)

// RouteGroupMatches reports whether a route group should serve a request for the
// given protocol and model. Empty protocol / model patterns on the group match all.
func RouteGroupMatches(g *domain.RouteGroup, protocol, model string) bool {
	if g == nil || g.Status != domain.StatusEnabled {
		return false
	}
	if !g.MatchesProtocol(protocol) {
		return false
	}
	if len(g.Models) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, pattern := range g.Models {
		if MatchModel(pattern, model) || (model == "" && pattern == "*") {
			return true
		}
	}
	return false
}

// RateInRange reports whether a key rate sits inside the route group's optional
// reference range [min, max). Unset bounds are unrestricted.
func RateInRange(g *domain.RouteGroup, rate float64) bool {
	if g == nil {
		return true
	}
	if g.RateMin != nil && rate < *g.RateMin {
		return false
	}
	if g.RateMax != nil && rate >= *g.RateMax {
		return false
	}
	return true
}

// ResolveAllowKeys returns the set of platform key IDs a consumer may use for a
// request. bound=false means the consumer has no route groups and every key is
// allowed (the caller should treat allow as nil). bound=true with an empty set
// means the consumer is bound but nothing matches.
//
// A member key is allowed when at least one matching group holds it with its
// rate_multiplier inside the group's rate range. Keys that are members of a
// matching group but whose rate has drifted outside every such group's range
// are returned in drift so the picker can report them as skipped.
func ResolveAllowKeys(ctx context.Context, db *gorm.DB, consumerID uint, protocol, model string) (allow, drift map[uint]struct{}, bound bool, err error) {
	var links []domain.ConsumerRouteGroup
	if err = db.WithContext(ctx).Where("consumer_key_id = ?", consumerID).Find(&links).Error; err != nil {
		return nil, nil, false, err
	}
	if len(links) == 0 {
		return nil, nil, false, nil
	}
	groupIDs := make([]uint, 0, len(links))
	for _, l := range links {
		groupIDs = append(groupIDs, l.RouteGroupID)
	}
	return resolveGroupAllowKeys(ctx, db, groupIDs, protocol, model)
}

// ResolveSnapshotAllowKeys keeps the request's group binding fixed across body reads.
// A missing/deleted bound group yields an empty allowed set, never global routing.
func ResolveSnapshotAllowKeys(ctx context.Context, db *gorm.DB, groupID *uint, protocol, model string) (allow, drift map[uint]struct{}, bound bool, err error) {
	if groupID == nil {
		return nil, nil, false, nil
	}
	return resolveGroupAllowKeys(ctx, db, []uint{*groupID}, protocol, model)
}

func resolveGroupAllowKeys(ctx context.Context, db *gorm.DB, groupIDs []uint, protocol, model string) (allow, drift map[uint]struct{}, bound bool, err error) {
	var groups []domain.RouteGroup
	if err = db.WithContext(ctx).Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
		return nil, nil, true, err
	}
	matched := map[uint]*domain.RouteGroup{}
	matchedIDs := make([]uint, 0, len(groups))
	for i := range groups {
		if RouteGroupMatches(&groups[i], protocol, model) {
			matched[groups[i].ID] = &groups[i]
			matchedIDs = append(matchedIDs, groups[i].ID)
		}
	}
	allow = map[uint]struct{}{}
	drift = map[uint]struct{}{}
	if len(matchedIDs) == 0 {
		return allow, drift, true, nil
	}
	var members []domain.RouteGroupKey
	if err = db.WithContext(ctx).Where("route_group_id IN ?", matchedIDs).Find(&members).Error; err != nil {
		return nil, nil, true, err
	}
	if len(members) == 0 {
		return allow, drift, true, nil
	}
	keyIDs := make([]uint, 0, len(members))
	for _, m := range members {
		keyIDs = append(keyIDs, m.PlatformKeyID)
	}
	var rates []struct {
		ID             uint
		RateMultiplier float64
	}
	if err = db.WithContext(ctx).Model(&domain.PlatformKey{}).Select("id, rate_multiplier").Where("id IN ?", keyIDs).Scan(&rates).Error; err != nil {
		return nil, nil, true, err
	}
	rateByKey := make(map[uint]float64, len(rates))
	for _, r := range rates {
		rateByKey[r.ID] = r.RateMultiplier
	}
	for _, m := range members {
		rate, ok := rateByKey[m.PlatformKeyID]
		if !ok {
			continue
		}
		if RateInRange(matched[m.RouteGroupID], rate) {
			allow[m.PlatformKeyID] = struct{}{}
		} else {
			drift[m.PlatformKeyID] = struct{}{}
		}
	}
	for id := range allow {
		delete(drift, id)
	}
	return allow, drift, true, nil
}

// RouteGroupsForKeys returns, for each platform key, the route groups it belongs to.
func RouteGroupsForKeys(ctx context.Context, db *gorm.DB, keyIDs []uint) (map[uint][]domain.RouteGroup, error) {
	out := map[uint][]domain.RouteGroup{}
	if len(keyIDs) == 0 {
		return out, nil
	}
	var members []domain.RouteGroupKey
	if err := db.WithContext(ctx).Where("platform_key_id IN ?", keyIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return out, nil
	}
	groupIDs := map[uint]struct{}{}
	for _, m := range members {
		groupIDs[m.RouteGroupID] = struct{}{}
	}
	ids := make([]uint, 0, len(groupIDs))
	for id := range groupIDs {
		ids = append(ids, id)
	}
	var groups []domain.RouteGroup
	if err := db.WithContext(ctx).Where("id IN ?", ids).Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]domain.RouteGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	for _, m := range members {
		if g, ok := byID[m.RouteGroupID]; ok {
			out[m.PlatformKeyID] = append(out[m.PlatformKeyID], g)
		}
	}
	return out, nil
}

// RouteGroupsForConsumers returns, for each consumer key, its bound route groups.
func RouteGroupsForConsumers(ctx context.Context, db *gorm.DB, consumerIDs []uint) (map[uint][]domain.RouteGroup, error) {
	out := map[uint][]domain.RouteGroup{}
	if len(consumerIDs) == 0 {
		return out, nil
	}
	var links []domain.ConsumerRouteGroup
	if err := db.WithContext(ctx).Where("consumer_key_id IN ?", consumerIDs).Find(&links).Error; err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return out, nil
	}
	groupIDs := map[uint]struct{}{}
	for _, l := range links {
		groupIDs[l.RouteGroupID] = struct{}{}
	}
	ids := make([]uint, 0, len(groupIDs))
	for id := range groupIDs {
		ids = append(ids, id)
	}
	var groups []domain.RouteGroup
	if err := db.WithContext(ctx).Where("id IN ?", ids).Order("id ASC").Find(&groups).Error; err != nil {
		return nil, err
	}
	byID := make(map[uint]domain.RouteGroup, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	for _, l := range links {
		if g, ok := byID[l.RouteGroupID]; ok {
			out[l.ConsumerKeyID] = append(out[l.ConsumerKeyID], g)
		}
	}
	return out, nil
}

func isModelGlob(pattern string) bool {
	return strings.Contains(pattern, "*")
}

// ListVisibleModels is the id set returned by GET /v1/models.
//
// Unbound consumers see the union of every enabled key's fetched list.
// Bound consumers see the union of models on the route groups those keys
// belong to: exact group entries are included as-is, globs expand against the
// key's fetched list, and an empty group list means the key's full fetched list.
func ListVisibleModels(bound bool, groups []domain.RouteGroup, keys []domain.PlatformKey, memberOf map[uint][]uint) []string {
	groupByID := map[uint]*domain.RouteGroup{}
	for i := range groups {
		if groups[i].Status != domain.StatusEnabled {
			continue
		}
		groupByID[groups[i].ID] = &groups[i]
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	addLast := func(k *domain.PlatformKey) {
		for _, id := range k.LastModels {
			add(id)
		}
	}
	addFromGroup := func(g *domain.RouteGroup, k *domain.PlatformKey) {
		if len(g.Models) == 0 {
			addLast(k)
			return
		}
		for _, pattern := range g.Models {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if isModelGlob(pattern) {
				for _, id := range k.LastModels {
					if MatchModel(pattern, id) {
						add(id)
					}
				}
				continue
			}
			add(pattern)
		}
	}

	for i := range keys {
		k := &keys[i]
		if k.Status != domain.StatusEnabled {
			continue
		}
		if k.Upstream == nil || k.Upstream.Status != domain.StatusEnabled {
			continue
		}
		if !bound {
			addLast(k)
			continue
		}
		gids := memberOf[k.ID]
		if len(gids) == 0 {
			continue
		}
		for _, gid := range gids {
			g := groupByID[gid]
			if g == nil || !RateInRange(g, k.RateMultiplier) {
				continue
			}
			addFromGroup(g, k)
		}
	}
	return out
}

package ops

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"simple-up-manage/internal/domain"
)

// modelsSyncConcurrency bounds parallel /v1/models fetches in the scheduled sync.
const modelsSyncConcurrency = 8

// ModelSyncResult summarizes one scheduled refresh pass.
type ModelSyncResult struct {
	OK             int `json:"ok"`
	Failed         int `json:"failed"`
	GroupsNotified int `json:"groups_notified"`
	Added          int `json:"added"`
	Removed        int `json:"removed"`
}

// SyncAllModels refreshes every enabled key's cached model list (GET
// /v1/models), then reports per route group which models became available or
// disappeared as model_change Notices.
//
// A failed fetch keeps the key's previous list, so a temporarily down upstream
// never reads as a model removal; the change surfaces on a later pass.
func (s *Service) SyncAllModels(ctx context.Context) (ModelSyncResult, error) {
	res := ModelSyncResult{}
	oldByID, err := s.enabledKeyModels(ctx)
	if err != nil {
		return res, fmt.Errorf("list current keys: %w", err)
	}
	res.OK, res.Failed = s.refreshKeyModels(ctx)
	newByID, err := s.enabledKeyModels(ctx)
	if err != nil {
		return res, fmt.Errorf("list refreshed keys: %w", err)
	}
	added, removed, notified, err := s.noticeGroupModelDiff(ctx, oldByID, newByID)
	if err != nil {
		return res, err
	}
	res.GroupsNotified, res.Added, res.Removed = notified, added, removed
	return res, nil
}

// enabledKeyModels snapshots id → key for every enabled key on an enabled
// upstream. Only the fields the group diff needs are read.
func (s *Service) enabledKeyModels(ctx context.Context) (map[uint]domain.PlatformKey, error) {
	var keys []domain.PlatformKey
	err := s.DB.WithContext(ctx).
		Model(&domain.PlatformKey{}).
		Joins("JOIN upstreams ON upstreams.id = platform_keys.upstream_id").
		Where("platform_keys.status = ?", domain.StatusEnabled).
		Where("upstreams.status = ?", domain.StatusEnabled).
		Select("platform_keys.id, platform_keys.upstream_id, platform_keys.status, platform_keys.rate_multiplier, platform_keys.last_models").
		Find(&keys).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint]domain.PlatformKey, len(keys))
	for _, k := range keys {
		out[k.ID] = k
	}
	return out, nil
}

// refreshKeyModels re-fetches /v1/models for every enabled key concurrently.
func (s *Service) refreshKeyModels(ctx context.Context) (int, int) {
	var ids []uint
	err := s.DB.WithContext(ctx).
		Model(&domain.PlatformKey{}).
		Joins("JOIN upstreams ON upstreams.id = platform_keys.upstream_id").
		Where("platform_keys.status = ?", domain.StatusEnabled).
		Where("upstreams.status = ?", domain.StatusEnabled).
		Order("platform_keys.id ASC").
		Pluck("platform_keys.id", &ids).Error
	if err != nil {
		log.Printf("models sync: list keys: %v", err)
		return 0, 0
	}
	pending := make(chan uint, len(ids))
	for _, id := range ids {
		pending <- id
	}
	close(pending)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, fail := 0, 0
	workers := min(modelsSyncConcurrency, len(pending))
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range pending {
				if ctx.Err() != nil {
					mu.Lock()
					fail++
					mu.Unlock()
					continue
				}
				out, err := s.FetchModels(ctx, id)
				mu.Lock()
				if err != nil || out == nil || !out.Success {
					fail++
				} else {
					ok++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return ok, fail
}

// noticeGroupModelDiff diffs each enabled group's servable model set between
// the pre- and post-sync key snapshots and writes one Notice per changed group.
func (s *Service) noticeGroupModelDiff(ctx context.Context, oldByID, newByID map[uint]domain.PlatformKey) (added, removed, notified int, err error) {
	var groups []domain.RouteGroup
	if err = s.DB.WithContext(ctx).Where("status = ?", domain.StatusEnabled).Find(&groups).Error; err != nil {
		return 0, 0, 0, err
	}
	if len(groups) == 0 {
		return 0, 0, 0, nil
	}
	var members []domain.RouteGroupKey
	if err = s.DB.WithContext(ctx).Find(&members).Error; err != nil {
		return 0, 0, 0, err
	}
	membersByGroup := map[uint][]uint{}
	for _, m := range members {
		membersByGroup[m.RouteGroupID] = append(membersByGroup[m.RouteGroupID], m.PlatformKeyID)
	}
	for i := range groups {
		g := &groups[i]
		ids := membersByGroup[g.ID]
		oldSet := groupModelSet(g, ids, oldByID)
		newSet := groupModelSet(g, ids, newByID)
		add := diffModelSets(newSet, oldSet)
		rem := diffModelSets(oldSet, newSet)
		if len(add) == 0 && len(rem) == 0 {
			continue
		}
		added += len(add)
		removed += len(rem)
		payload := map[string]any{
			"route_group_id": g.ID,
			"group_name":     g.Name,
			"added":          add,
			"removed":        rem,
		}
		if err := s.RecordNotice(ctx, domain.NoticeKindModelChange, domain.NoticeSourceModelsSync, modelChangeSummary(g.Name, add, rem), payload); err != nil {
			return added, removed, notified, err
		}
		notified++
	}
	return added, removed, notified, nil
}

// groupModelSet is the group's servable model set given its member keys —
// the same expansion ListVisibleModels performs for GET /v1/models: key and
// upstream status are already filtered in SQL, rate drift excludes the key,
// an empty group pattern list exposes the key's whole fetched list, exact
// patterns pass as-is and globs expand against the fetched list.
func groupModelSet(g *domain.RouteGroup, memberIDs []uint, keyByID map[uint]domain.PlatformKey) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range memberIDs {
		k, ok := keyByID[id]
		if !ok || !RateInRange(g, k.RateMultiplier) {
			continue
		}
		if len(g.Models) == 0 {
			for _, m := range k.LastModels {
				addModelID(out, m)
			}
			continue
		}
		for _, pattern := range g.Models {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if isModelGlob(pattern) {
				for _, m := range k.LastModels {
					if MatchModel(pattern, m) {
						addModelID(out, m)
					}
				}
				continue
			}
			addModelID(out, pattern)
		}
	}
	return out
}

func addModelID(set map[string]struct{}, id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	set[id] = struct{}{}
}

// diffModelSets returns the sorted ids in from that are absent in sub.
func diffModelSets(from, sub map[string]struct{}) []string {
	out := make([]string, 0, len(from))
	for id := range from {
		if _, ok := sub[id]; ok {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// modelChangeSummary renders the inbox line, capping the inline id list.
func modelChangeSummary(group string, added, removed []string) string {
	switch {
	case len(added) > 0 && len(removed) > 0:
		return fmt.Sprintf("分组 %s 新增 %s，移除 %s", group, modelListPreview(added), modelListPreview(removed))
	case len(added) > 0:
		return fmt.Sprintf("分组 %s 新增 %s", group, modelListPreview(added))
	case len(removed) > 0:
		return fmt.Sprintf("分组 %s 移除 %s", group, modelListPreview(removed))
	default:
		return ""
	}
}

func modelListPreview(ids []string) string {
	const maxInline = 5
	if len(ids) > maxInline {
		return fmt.Sprintf("%d 个模型（%s 等）", len(ids), strings.Join(ids[:maxInline], "、"))
	}
	return fmt.Sprintf("%d 个模型（%s）", len(ids), strings.Join(ids, "、"))
}

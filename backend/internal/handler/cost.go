package handler

import (
	"strings"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"
)

func catalogModelID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		id = id[i+1:]
	}
	return id
}

// attachLogCosts backfills the list amount for legacy rows that never settled
// a cost. New logs carry their own estimate (itemized billing × upstream
// rate), so this only ever touches pre-billing-upgrade history with the old
// simple formula.
func (h *Admin) attachLogCosts(out []logDTO) {
	if len(out) == 0 {
		return
	}
	need := false
	keyIDs := map[uint]struct{}{}
	for i := range out {
		if out[i].CostUSD == nil {
			need = true
		}
		if out[i].PlatformKeyID != nil {
			keyIDs[*out[i].PlatformKeyID] = struct{}{}
		}
	}
	if !need {
		return
	}
	var catalog []domain.CatalogModel
	_ = h.DB.Find(&catalog).Error
	prices := map[string][2]float64{}
	for _, row := range catalog {
		key := strings.ToLower(row.ModelID)
		if prev, exists := prices[key]; exists && prev[0]+prev[1] > 0 && row.InputCost+row.OutputCost == 0 {
			continue
		}
		prices[key] = [2]float64{row.InputCost, row.OutputCost}
	}
	rates := map[uint]float64{}
	if len(keyIDs) > 0 {
		ids := make([]uint, 0, len(keyIDs))
		for id := range keyIDs {
			ids = append(ids, id)
		}
		var keys []domain.PlatformKey
		_ = h.DB.Select("id, rate_multiplier").Where("id IN ?", ids).Find(&keys).Error
		for _, k := range keys {
			rates[k.ID] = k.RateMultiplier
		}
	}
	for i := range out {
		if out[i].CostUSD != nil {
			continue
		}
		p, ok := prices[catalogModelID(out[i].Model)]
		if !ok || (p[0] == 0 && p[1] == 0) {
			continue
		}
		rate := 1.0
		if out[i].PlatformKeyID != nil {
			if r, ok := rates[*out[i].PlatformKeyID]; ok && r > 0 {
				rate = r
			}
		}
		out[i].CostUSD = upstream.EstimateCostUSD(p[0], p[1], rate, upstream.TokenUsage{
			InputTokens:         out[i].InputTokens,
			OutputTokens:        out[i].OutputTokens,
			CacheReadTokens:     out[i].CacheReadTokens,
			CacheCreationTokens: out[i].CacheCreationTokens,
		})
	}
}

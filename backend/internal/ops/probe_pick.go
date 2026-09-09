package ops

import (
	"strings"

	"simple-up-manage/internal/domain"
)

type ProbeTarget struct {
	Vendor   string
	Model    string
	Protocol string
}

func PickProbeTarget(key *domain.PlatformKey, cfg domain.SchedulerSettings, catalog []domain.CatalogModel) ProbeTarget {
	cfg.Normalize()
	fallback := ProbeTarget{
		Vendor:   domain.VendorOpenAI,
		Model:    cfg.ProbeOpenAIModel,
		Protocol: domain.ProtocolOpenAI,
	}
	if key != nil && key.Upstream != nil && key.Upstream.Supports(domain.ProtocolAnthropic) {
		fallback = ProbeTarget{
			Vendor:   domain.VendorAnthropic,
			Model:    cfg.ProbeAnthropicModel,
			Protocol: domain.ProtocolAnthropic,
		}
	}

	configured := cfg.ConfiguredProbeModels()
	last := lastModelSet(key)
	if len(last) == 0 {
		return fallback
	}

	type hit struct {
		vendor string
		model  string
		cost   float64
		order  int
	}
	var hits []hit
	costs := catalogCosts(catalog)
	for i, v := range domain.ProbeVendors() {
		model := configured[v.ID]
		if model == "" {
			continue
		}
		if _, ok := last[strings.ToLower(model)]; !ok {
			continue
		}
		hits = append(hits, hit{
			vendor: v.ID,
			model:  model,
			cost:   costs[v.ID+"/"+strings.ToLower(model)],
			order:  i,
		})
	}
	if len(hits) > 0 {
		best := hits[0]
		for _, h := range hits[1:] {
			if cheaperHit(h.cost, h.order, best.cost, best.order) {
				best = h
			}
		}
		return ProbeTarget{Vendor: best.vendor, Model: best.model, Protocol: domain.VendorProtocol(best.vendor)}
	}

	votes := map[string]int{}
	for id := range last {
		v := InferVendor(id, catalog)
		if v != "" {
			votes[v]++
		}
	}
	bestVendor := ""
	bestVotes := 0
	bestOrder := 99
	for i, v := range domain.ProbeVendors() {
		n := votes[v.ID]
		if n > bestVotes || (n == bestVotes && n > 0 && i < bestOrder) {
			bestVendor = v.ID
			bestVotes = n
			bestOrder = i
		}
	}
	if bestVendor != "" {
		if model := configured[bestVendor]; model != "" {
			return ProbeTarget{Vendor: bestVendor, Model: model, Protocol: domain.VendorProtocol(bestVendor)}
		}
	}
	return fallback
}

func InferVendor(model string, catalog []domain.CatalogModel) string {
	id := normalizeModelID(model)
	if id == "" {
		return ""
	}
	for _, m := range catalog {
		if strings.EqualFold(m.ModelID, id) {
			return m.Vendor
		}
	}
	switch {
	case strings.HasPrefix(id, "claude"):
		return domain.VendorAnthropic
	case strings.HasPrefix(id, "grok"):
		return domain.VendorGrok
	case strings.HasPrefix(id, "glm") || strings.HasPrefix(id, "chatglm"):
		return domain.VendorZhipu
	case strings.HasPrefix(id, "kimi") || strings.HasPrefix(id, "moonshot"):
		return domain.VendorMoonshot
	case strings.HasPrefix(id, "deepseek"):
		return domain.VendorDeepseek
	case strings.HasPrefix(id, "gpt") || strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") ||
		strings.HasPrefix(id, "o4") || strings.HasPrefix(id, "chatgpt"):
		return domain.VendorOpenAI
	default:
		return ""
	}
}

func lastModelSet(key *domain.PlatformKey) map[string]struct{} {
	out := map[string]struct{}{}
	if key == nil {
		return out
	}
	for _, id := range key.LastModels {
		id = normalizeModelID(id)
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

func catalogCosts(catalog []domain.CatalogModel) map[string]float64 {
	out := map[string]float64{}
	for _, m := range catalog {
		out[m.Vendor+"/"+strings.ToLower(m.ModelID)] = m.Cost()
	}
	return out
}

func cheaperHit(costA float64, orderA int, costB float64, orderB int) bool {
	if costA == costB {
		return orderA < orderB
	}
	if costA == 0 && costB > 0 {
		return false
	}
	if costB == 0 && costA > 0 {
		return true
	}
	return costA < costB
}

func normalizeModelID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		id = id[i+1:]
	}
	return id
}

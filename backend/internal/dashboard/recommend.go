package dashboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"simple-up-manage/internal/domain"
)

type RecommendationsDTO struct {
	Meta           MetaDTO       `json:"meta"`
	Urgent         []UrgentItem  `json:"urgent"`
	Invest         []InvestItem  `json:"invest"`
	Watch          []WatchItem   `json:"watch"`
	DemandBoards   []DemandBoard `json:"demand_boards"`
	CommonCoverage *float64      `json:"common_coverage"`
	Note           string        `json:"note"`
}

type UrgentItem struct {
	ProviderID      uint     `json:"provider_id"`
	Name            string   `json:"name"`
	BalanceUSD      *float64 `json:"balance_usd"`
	HoursLeft       *float64 `json:"hours_left"`
	Consumed24hUSD  *float64 `json:"consumed_24h_usd"`
	Coverage        *float64 `json:"coverage"`
	Reason          string   `json:"reason"`
	Insufficient    bool     `json:"insufficient"`
	ZeroConsumption bool     `json:"zero_consumption"`
	HealthNote      string   `json:"health_note,omitempty"`
}

type InvestItem struct {
	ProviderID     uint     `json:"provider_id"`
	Name           string   `json:"name"`
	ProfitPerCost  *float64 `json:"profit_per_cost"`
	Margin         *float64 `json:"margin"`
	Coverage       *float64 `json:"coverage"`
	Samples        int64    `json:"samples"`
	SuccessRate    *float64 `json:"success_rate"`
	TTFTp95MS      *float64 `json:"ttft_p95_ms"`
	CostMultiplier *float64 `json:"cost_multiplier"`
	BalanceUSD     *float64 `json:"balance_usd"`
	Reason         string   `json:"reason"`
}

type WatchItem struct {
	ProviderID uint   `json:"provider_id"`
	Name       string `json:"name"`
	Reason     string `json:"reason"`
	DemandKey  string `json:"demand_key,omitempty"`
}

type DemandBoard struct {
	Key    string       `json:"key"`
	Label  string       `json:"label"`
	Weight float64      `json:"weight"`
	Items  []InvestItem `json:"items"`
}

func (s *Service) Recommendations(ctx context.Context, staleAfter time.Duration) (RecommendationsDTO, error) {
	now := time.Now().UTC()
	r24 := Range{From: now.Add(-24 * time.Hour), To: now}
	r7 := Range{From: now.Add(-7 * 24 * time.Hour), To: now}
	meta := s.meta(r7)
	cfg := s.LoadSettings()
	out := RecommendationsDTO{
		Meta:   meta,
		Note:   "仅按本网关流量估算，不把其他渠道用量或充值变化当作已知。推荐不自动改路由、不自动充值。",
		Urgent: []UrgentItem{}, Invest: []InvestItem{}, Watch: []WatchItem{}, DemandBoards: []DemandBoard{},
	}
	var err error
	out.Urgent, err = s.urgent(ctx, r24, cfg, staleAfter)
	if err != nil {
		return RecommendationsDTO{}, err
	}
	invest, watch, boards, cov, err := s.invest(ctx, r7, cfg)
	if err != nil {
		return RecommendationsDTO{}, err
	}
	out.Invest, out.Watch, out.DemandBoards, out.CommonCoverage = invest, watch, boards, cov
	return out, nil
}

func (s *Service) urgent(ctx context.Context, r Range, cfg Settings, staleAfter time.Duration) ([]UrgentItem, error) {
	bal := s.currentBalance(ctx, staleAfter)
	coverage := s.meta(r).DataQuality.Complete
	var items []UrgentItem
	for _, p := range bal.Providers {
		if !p.Enabled || p.Unlimited || p.Unknown || p.BalanceUSD == nil {
			continue
		}
		att, err := s.sumRange(ctx, r, FamilyAttempt, DimProvider, p.ID)
		if err != nil {
			return nil, err
		}
		it := UrgentItem{ProviderID: p.ID, Name: p.Name, BalanceUSD: p.BalanceUSD}
		if att.ConsumptionUSD != 0 {
			it.Consumed24hUSD = ptrFloat(round8(att.ConsumptionUSD))
		}
		complete := coverage && r.To.Sub(r.From) >= 24*time.Hour && att.UnknownConsumption == 0 && !p.Stale && p.BalanceAt != nil
		if att.RequestsCompleted+att.ProviderSuccess+att.ProviderFailure > 0 {
			den := att.ProviderSuccess + att.ProviderFailure + att.UnknownConsumption
			if den > 0 {
				it.Coverage = ratio(att.ProviderSuccess+att.ProviderFailure, den)
			}
		}
		if !complete || p.Stale || att.UnknownConsumption > 0 {
			it.Insufficient = true
			it.Reason = "预测依据不足"
			items = append(items, it)
			continue
		}
		if att.ConsumptionUSD <= 0 {
			it.ZeroConsumption = true
			it.Reason = "暂无消耗"
			items = append(items, it)
			continue
		}
		hourly := att.ConsumptionUSD / 24
		if hourly <= 0 {
			it.ZeroConsumption = true
			it.Reason = "暂无消耗"
			items = append(items, it)
			continue
		}
		hours := *p.BalanceUSD / hourly
		if hours < 0 {
			hours = 0
		}
		it.HoursLeft = &hours
		if hours <= 0 {
			it.Reason = "余额已耗尽"
		} else {
			it.Reason = fmt.Sprintf("预计可支撑 %.1f 小时（阈值 %d 小时）", hours, cfg.RenewalHorizonHours)
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		ih, jh := 1e18, 1e18
		if items[i].HoursLeft != nil {
			ih = *items[i].HoursLeft
		} else if items[i].Insufficient {
			ih = 1e17
		}
		if items[j].HoursLeft != nil {
			jh = *items[j].HoursLeft
		} else if items[j].Insufficient {
			jh = 1e17
		}
		if ih != jh {
			return ih < jh
		}
		return items[i].ProviderID < items[j].ProviderID
	})
	horizon := float64(cfg.RenewalHorizonHours)
	filtered := items[:0]
	for _, it := range items {
		if it.HoursLeft != nil && *it.HoursLeft > horizon && *it.HoursLeft > 0 {
			continue
		}
		filtered = append(filtered, it)
	}
	return filtered, nil
}

func (s *Service) invest(ctx context.Context, r Range, cfg Settings) ([]InvestItem, []WatchItem, []DemandBoard, *float64, error) {
	requestWindow := s.db.WithContext(ctx).Model(&RequestFact{}).
		Where("completed_at >= ? AND completed_at < ? AND source = ?", r.From, r.To, domain.SourceBusiness)
	var attempts []AttemptFact
	// Financial contributions follow the completed request, including its attempts
	// before the window. Quality samples retain their own completion-time window.
	if err := s.db.WithContext(ctx).Where("request_uuid IN (?) AND source = ?", requestWindow.Select("uuid"), domain.SourceBusiness).Find(&attempts).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	var reqs []RequestFact
	if err := s.db.WithContext(ctx).Where("completed_at >= ? AND completed_at < ? AND source = ?", r.From, r.To, domain.SourceBusiness).Find(&reqs).Error; err != nil {
		return nil, nil, nil, nil, err
	}
	reqBy := map[string]RequestFact{}
	for _, f := range reqs {
		reqBy[f.UUID] = f
	}
	type demandKey struct {
		Group                 uint
		Model, Protocol, Path string
		Stream                bool
	}
	type cell struct {
		provider              uint
		name                  string
		success, fail, ttftN  int64
		ttftHist              []int64
		coveredReq            map[string]struct{}
		allReq                map[string]struct{}
		profit, cost, revenue Amount
		rateSum               float64
		rateN                 int64
	}
	cells := map[demandKey]map[uint]*cell{}
	demandBase := map[demandKey]float64{}
	providers := map[uint]string{}
	for _, f := range reqs {
		if f.Success && f.BaseCostUSD != nil && f.RouteGroupID != nil {
			dk := demandKey{Group: *f.RouteGroupID, Model: f.Model, Protocol: f.Protocol, Path: f.Path, Stream: f.Stream}
			demandBase[dk] += *f.BaseCostUSD
		}
	}
	for _, a := range attempts {
		f, ok := reqBy[a.RequestUUID]
		if !ok || f.RouteGroupID == nil || !a.HTTPSent {
			continue
		}
		dk := demandKey{Group: *f.RouteGroupID, Model: f.Model, Protocol: a.Protocol, Path: a.Path, Stream: a.Stream}
		if cells[dk] == nil {
			cells[dk] = map[uint]*cell{}
		}
		c := cells[dk][a.ProviderID]
		if c == nil {
			c = &cell{provider: a.ProviderID, name: a.ProviderName, coveredReq: map[string]struct{}{}, allReq: map[string]struct{}{}, ttftHist: emptyHist()}
			cells[dk][a.ProviderID] = c
		}
		providers[a.ProviderID] = a.ProviderName
		c.allReq[a.RequestUUID] = struct{}{}
		if !a.CompletedAt.Before(r.From) && a.CompletedAt.Before(r.To) {
			if a.Result == "success" {
				c.success++
			} else if a.Result == "upstream_failure" {
				c.fail++
			}
			if a.TTFTStatus == "measured" && a.Result == "success" {
				c.ttftHist = addHist(c.ttftHist, observeTTFT(a.TTFTMs))
				c.ttftN++
			}
		}
		c.rateSum += a.CostMultiplier
		c.rateN++
		if f.Covered {
			_, counted := c.coveredReq[a.RequestUUID]
			c.coveredReq[a.RequestUUID] = struct{}{}
			if !counted && f.Success && f.FinalProviderID != nil && *f.FinalProviderID == a.ProviderID && f.RevenueUSD != nil {
				c.revenue = c.revenue.Add(AmountFromFloat(*f.RevenueUSD))
			}
			if a.EstimatedCostUSD != nil {
				c.cost = c.cost.Add(AmountFromFloat(*a.EstimatedCostUSD))
			}
		}
	}
	for _, m := range cells {
		for _, c := range m {
			c.profit = c.revenue.Sub(c.cost)
		}
	}

	type cand struct {
		dk    demandKey
		c     *cell
		ok    bool
		watch string
	}
	var qualified []cand
	watch := []WatchItem{}
	for dk, m := range cells {
		for _, c := range m {
			samples := c.success + c.fail
			sr := ratio(c.success, samples)
			p95, _, _ := quantileMS(c.ttftHist, 0.95)
			ok := samples >= int64(cfg.MinQualitySamples) && sr != nil && *sr >= cfg.MinSuccessRate && c.ttftN >= int64(cfg.MinTTFTSamples) && p95 != nil && *p95 <= float64(cfg.MaxTTFTP95Ms)
			reason := ""
			if !ok {
				if c.ttftN < int64(cfg.MinTTFTSamples) {
					reason = "缺少首字证据，待观察"
				} else if samples < int64(cfg.MinQualitySamples) {
					reason = "有效质量样本不足"
				} else if sr == nil || *sr < cfg.MinSuccessRate {
					reason = "成功率未达标"
				} else {
					reason = "P95 超过门槛"
				}
				watch = append(watch, WatchItem{ProviderID: c.provider, Name: c.name, Reason: reason, DemandKey: fmt.Sprintf("g%d/%s/%s", dk.Group, dk.Protocol, dk.Model)})
			}
			qualified = append(qualified, cand{dk: dk, c: c, ok: ok, watch: reason})
		}
	}

	// per-demand boards of qualified
	byDemand := map[demandKey][]InvestItem{}
	for _, q := range qualified {
		if !q.ok {
			continue
		}
		cov := ratio(int64(len(q.c.coveredReq)), int64(len(q.c.allReq)))
		if cov == nil || *cov < cfg.MinFinanceCoverage || q.c.cost == 0 {
			watch = append(watch, WatchItem{ProviderID: q.c.provider, Name: q.c.name, Reason: "财务覆盖不足或成本为零，不输出确定排名", DemandKey: fmt.Sprintf("g%d/%s/%s", q.dk.Group, q.dk.Protocol, q.dk.Model)})
			continue
		}
		ppc := q.c.profit.Float() / q.c.cost.Float()
		item := InvestItem{
			ProviderID: q.c.provider, Name: q.c.name, ProfitPerCost: &ppc,
			Margin: ratioAmount(q.c.profit.Float(), q.c.revenue.Float()), Coverage: cov,
			Samples: q.c.success + q.c.fail, SuccessRate: ratio(q.c.success, q.c.success+q.c.fail),
		}
		p95, _, _ := quantileMS(q.c.ttftHist, 0.95)
		item.TTFTp95MS = p95
		if q.c.rateN > 0 {
			v := q.c.rateSum / float64(q.c.rateN)
			item.CostMultiplier = &v
		}
		item.Reason = "质量达标，按单位预估成本毛利排序"
		byDemand[q.dk] = append(byDemand[q.dk], item)
	}
	for k := range byDemand {
		sort.Slice(byDemand[k], func(i, j int) bool {
			a, b := byDemand[k][i], byDemand[k][j]
			if a.ProfitPerCost != nil && b.ProfitPerCost != nil && *a.ProfitPerCost != *b.ProfitPerCost {
				return *a.ProfitPerCost > *b.ProfitPerCost
			}
			if a.Samples != b.Samples {
				return a.Samples > b.Samples
			}
			return a.ProviderID < b.ProviderID
		})
	}

	// common demand = intersection of demands that have at least one qualified candidate
	var common []demandKey
	first := true
	var set map[demandKey]struct{}
	providersOK := map[uint]map[demandKey]struct{}{}
	for dk, items := range byDemand {
		for _, it := range items {
			if providersOK[it.ProviderID] == nil {
				providersOK[it.ProviderID] = map[demandKey]struct{}{}
			}
			providersOK[it.ProviderID][dk] = struct{}{}
		}
	}
	for pid, ds := range providersOK {
		_ = pid
		if first {
			set = map[demandKey]struct{}{}
			for d := range ds {
				set[d] = struct{}{}
			}
			first = false
			continue
		}
		for d := range set {
			if _, ok := ds[d]; !ok {
				delete(set, d)
			}
		}
	}
	for d := range set {
		common = append(common, d)
	}

	var totalBase, commonBase float64
	for dk, w := range demandBase {
		if w <= 0 {
			continue
		}
		totalBase += w
		for _, c := range common {
			if c == dk {
				commonBase += w
			}
		}
	}
	var cov *float64
	if totalBase > 0 {
		v := commonBase / totalBase
		cov = &v
	}
	boards := []DemandBoard{}
	for dk, items := range byDemand {
		w := 0.0
		if totalBase > 0 {
			w = demandBase[dk] / totalBase
		}
		boards = append(boards, DemandBoard{Key: fmt.Sprintf("g%d/%s/%s/%s/%t", dk.Group, dk.Protocol, dk.Model, dk.Path, dk.Stream), Label: fmt.Sprintf("g%d/%s/%s/%s (stream=%t)", dk.Group, dk.Protocol, dk.Model, dk.Path, dk.Stream), Weight: w, Items: items})
	}
	sort.Slice(boards, func(i, j int) bool { return boards[i].Key < boards[j].Key })

	var invest []InvestItem
	if cov != nil && *cov >= cfg.MinCommonDemandCoverage && len(common) > 0 {
		score := map[uint]float64{}
		metaP := map[uint]InvestItem{}
		for _, dk := range common {
			w := 0.0
			if commonBase > 0 {
				w = demandBase[dk] / commonBase
			}
			for _, it := range byDemand[dk] {
				if it.ProfitPerCost == nil {
					continue
				}
				score[it.ProviderID] += w * *it.ProfitPerCost
				metaP[it.ProviderID] = it
			}
		}
		for pid, sc := range score {
			it := metaP[pid]
			it.ProfitPerCost = &sc
			it.Reason = "共同需求加权预估性价比"
			invest = append(invest, it)
		}
		sort.Slice(invest, func(i, j int) bool {
			if invest[i].ProfitPerCost != nil && invest[j].ProfitPerCost != nil && *invest[i].ProfitPerCost != *invest[j].ProfitPerCost {
				return *invest[i].ProfitPerCost > *invest[j].ProfitPerCost
			}
			return invest[i].ProviderID < invest[j].ProviderID
		})
	}

	bal := s.currentBalance(ctx, 2*time.Minute)
	byBal := map[uint]ProviderBalance{}
	for _, p := range bal.Providers {
		byBal[p.ID] = p
	}
	for i := range invest {
		if p, ok := byBal[invest[i].ProviderID]; ok {
			invest[i].BalanceUSD = p.BalanceUSD
		}
	}
	for pid, name := range providers {
		seen := false
		for _, it := range invest {
			if it.ProviderID == pid {
				seen = true
			}
		}
		for _, it := range watch {
			if it.ProviderID == pid {
				seen = true
			}
		}
		if !seen {
			watch = append(watch, WatchItem{ProviderID: pid, Name: name, Reason: "没有足够数据，待观察"})
		}
	}
	return invest, watch, boards, cov, nil
}

package dashboard

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"simple-up-manage/internal/domain"
)

var (
	ErrBadRange = errors.New("invalid time range")
)

type Range struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type DataQuality struct {
	Complete     bool             `json:"complete"`
	Gaps         []GapDTO         `json:"gaps,omitempty"`
	Unmeasured   map[string]int64 `json:"unmeasured,omitempty"`
	Overflow     bool             `json:"overflow,omitempty"`
	QueueDepth   int              `json:"queue_depth,omitempty"`
	WindowWarmup bool             `json:"window_warmup,omitempty"`
}

type GapDTO struct {
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	Reason string    `json:"reason"`
}

type MetaDTO struct {
	GeneratedAt   time.Time   `json:"generated_at"`
	AvailableFrom time.Time   `json:"available_from"`
	Timezone      string      `json:"timezone"`
	Range         Range       `json:"range"`
	DataQuality   DataQuality `json:"data_quality"`
}

type FinanceDTO struct {
	KnownRevenueUSD         *float64 `json:"known_revenue_usd"`
	KnownEstimatedCostUSD   *float64 `json:"known_estimated_cost_usd"`
	ConsumptionUSD          *float64 `json:"consumption_usd"`
	ReportedConsumptionUSD  *float64 `json:"reported_consumption_usd"`
	EstimatedConsumptionUSD *float64 `json:"estimated_consumption_usd"`
	UnknownConsumption      int64    `json:"unknown_consumption"`
	CoveredRevenueUSD       *float64 `json:"covered_revenue_usd"`
	CoveredCostUSD          *float64 `json:"covered_cost_usd"`
	EstimatedProfitUSD      *float64 `json:"estimated_profit_usd"`
	Margin                  *float64 `json:"margin"`
	Coverage                *float64 `json:"coverage"`
	KnownPartial            bool     `json:"known_partial"`
}

type BalanceDTO struct {
	TotalKnownUSD   *float64          `json:"total_known_usd"`
	EnabledKnownUSD *float64          `json:"enabled_known_usd"`
	UnlimitedCount  int               `json:"unlimited_count"`
	UnknownCount    int               `json:"unknown_count"`
	DisabledCount   int               `json:"disabled_count"`
	StaleCount      int               `json:"stale_count"`
	Providers       []ProviderBalance `json:"providers"`
	RefreshedAt     *time.Time        `json:"refreshed_at"`
}

type ProviderBalance struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	BalanceUSD *float64   `json:"balance_usd"`
	BalanceAt  *time.Time `json:"balance_at"`
	Kind       string     `json:"kind"`
	Unlimited  bool       `json:"unlimited"`
	Unknown    bool       `json:"unknown"`
	Stale      bool       `json:"stale"`
}

type OverviewDTO struct {
	Meta                MetaDTO    `json:"meta"`
	RequestsStarted     int64      `json:"requests_started"`
	RequestsCompleted   int64      `json:"requests_completed"`
	RequestsSuccess     int64      `json:"requests_success"`
	SuccessRate         *float64   `json:"success_rate"`
	RetryRate           *float64   `json:"retry_rate"`
	ProviderSuccessRate *float64   `json:"provider_success_rate"`
	TTFTp50MS           *float64   `json:"ttft_p50_ms"`
	TTFTp95MS           *float64   `json:"ttft_p95_ms"`
	TTFTSamples         int64      `json:"ttft_samples"`
	TTFTApprox          bool       `json:"ttft_approx"`
	TTFTOverflow        bool       `json:"ttft_overflow"`
	InflightMean        *float64   `json:"inflight_mean"`
	InflightPeak        int        `json:"inflight_peak"`
	Finance             FinanceDTO `json:"finance"`
	Balance             BalanceDTO `json:"balance"`
}

type TrendPoint struct {
	Bucket              time.Time `json:"bucket"`
	Complete            bool      `json:"complete"`
	RequestsStarted     int64     `json:"requests_started"`
	RequestsCompleted   int64     `json:"requests_completed"`
	FailureRate         *float64  `json:"failure_rate"`
	SuccessRate         *float64  `json:"success_rate"`
	RetryRate           *float64  `json:"retry_rate"`
	ProviderSuccessRate *float64  `json:"provider_success_rate"`
	TTFTp50MS           *float64  `json:"ttft_p50_ms"`
	TTFTp95MS           *float64  `json:"ttft_p95_ms"`
	TTFTSamples         int64     `json:"ttft_samples"`
	InflightMean        *float64  `json:"inflight_mean"`
	InflightPeak        int       `json:"inflight_peak"`
	ConsumptionUSD      *float64  `json:"consumption_usd"`
	CoveredProfitUSD    *float64  `json:"covered_profit_usd"`
	KnownRevenueUSD     *float64  `json:"known_revenue_usd"`
}

type TrendsDTO struct {
	Meta        MetaDTO      `json:"meta"`
	Granularity string       `json:"granularity"`
	Points      []TrendPoint `json:"points"`
}

type RankingRow struct {
	ID                 uint     `json:"id"`
	Name               string   `json:"name"`
	RequestsCompleted  int64    `json:"requests_completed"`
	SuccessRate        *float64 `json:"success_rate"`
	KnownRevenueUSD    *float64 `json:"known_revenue_usd"`
	CoveredRevenueUSD  *float64 `json:"covered_revenue_usd"`
	CoveredCostUSD     *float64 `json:"covered_cost_usd"`
	EstimatedProfitUSD *float64 `json:"estimated_profit_usd"`
	Margin             *float64 `json:"margin"`
	Coverage           *float64 `json:"coverage"`
	ConsumptionUSD     *float64 `json:"consumption_usd"`
	Excluded           bool     `json:"excluded"`
	ExcludeReason      string   `json:"exclude_reason,omitempty"`
}

type RankingsDTO struct {
	Meta      MetaDTO      `json:"meta"`
	Dimension string       `json:"dimension"`
	Items     []RankingRow `json:"items"`
	Total     int64        `json:"total"`
	Page      int          `json:"page"`
	PageSize  int          `json:"page_size"`
}

func ParseRange(fromS, toS string) (Range, error) {
	if strings.TrimSpace(fromS) == "" || strings.TrimSpace(toS) == "" {
		return Range{}, fmt.Errorf("%w: from and to are required", ErrBadRange)
	}
	from, err := time.Parse(time.RFC3339, fromS)
	if err != nil {
		from, err = time.Parse(time.RFC3339Nano, fromS)
		if err != nil {
			return Range{}, fmt.Errorf("%w: from must be RFC3339", ErrBadRange)
		}
	}
	to, err := time.Parse(time.RFC3339, toS)
	if err != nil {
		to, err = time.Parse(time.RFC3339Nano, toS)
		if err != nil {
			return Range{}, fmt.Errorf("%w: to must be RFC3339", ErrBadRange)
		}
	}
	if !to.After(from) {
		return Range{}, fmt.Errorf("%w: to must be greater than from", ErrBadRange)
	}
	return Range{From: from.UTC(), To: to.UTC()}, nil
}

func ValidateGranularity(r Range, gran string) error {
	gran = strings.ToLower(strings.TrimSpace(gran))
	switch gran {
	case "minute", "hour":
		if time.Since(r.From) > MinuteRetention {
			return fmt.Errorf("%w: minute/hour queries are limited to the last 30 days", ErrBadRange)
		}
	case "day":
		loc := shanghaiLoc()
		fromIn := r.From.In(loc)
		toIn := r.To.In(loc)
		if fromIn.Hour() != 0 || fromIn.Minute() != 0 || fromIn.Second() != 0 || fromIn.Nanosecond() != 0 {
			return fmt.Errorf("%w: day queries must start on Asia/Shanghai midnight", ErrBadRange)
		}
		if toIn.Hour() != 0 || toIn.Minute() != 0 || toIn.Second() != 0 || toIn.Nanosecond() != 0 {
			return fmt.Errorf("%w: day queries must end on Asia/Shanghai midnight", ErrBadRange)
		}
	default:
		return fmt.Errorf("%w: granularity must be minute, hour, or day", ErrBadRange)
	}
	return nil
}

func (s *Service) meta(r Range) MetaDTO {
	q := DataQuality{Complete: true, Unmeasured: map[string]int64{}}
	if s != nil && s.settle != nil {
		q.Overflow = s.settle.Overflow()
		q.QueueDepth = s.settle.QueueDepth()
		if q.Overflow || q.QueueDepth > 0 {
			q.Complete = false
		}
		var gaps []Gap
		if err := s.db.Where("ended_at >= ? AND started_at < ?", r.From, r.To).Order("id DESC").Limit(50).Find(&gaps).Error; err != nil {
			q.Complete = false
		}
		for _, g := range gaps {
			q.Gaps = append(q.Gaps, GapDTO{From: g.StartedAt, To: g.EndedAt, Reason: g.Reason})
			q.Complete = false
		}
	}
	avail := s.availableFrom()
	if r.From.Before(avail) {
		q.Complete = false
	}
	return MetaDTO{
		GeneratedAt:   time.Now().UTC(),
		AvailableFrom: avail,
		Timezone:      "Asia/Shanghai",
		Range:         r,
		DataQuality:   q,
	}
}

func (s *Service) availableFrom() time.Time {
	var m Meta
	if err := s.db.First(&m, 1).Error; err != nil {
		return time.Now().UTC()
	}
	return m.AvailableFrom
}

func (s *Service) Overview(ctx context.Context, r Range, balanceStale time.Duration) (OverviewDTO, error) {
	meta := s.meta(r)
	req, err := s.sumRange(ctx, r, FamilyRequest, DimGlobal, 0)
	if err != nil {
		return OverviewDTO{}, err
	}
	att, err := s.sumRange(ctx, r, FamilyAttempt, DimGlobal, 0)
	if err != nil {
		return OverviewDTO{}, err
	}
	out := OverviewDTO{Meta: meta, RequestsStarted: req.RequestsStarted, RequestsCompleted: req.RequestsCompleted, RequestsSuccess: req.RequestsSuccess, InflightPeak: req.InflightPeak}
	out.SuccessRate = ratio(req.RequestsSuccess, req.RequestsCompleted)
	out.RetryRate = ratio(req.RequestsRetried, req.RequestsCompleted)
	out.ProviderSuccessRate = ratio(att.ProviderSuccess, att.ProviderSuccess+att.ProviderFailure)
	p50, n, over := quantileMS(parseHist(req.TTFTHistJSON), 0.50)
	p95, _, over95 := quantileMS(parseHist(req.TTFTHistJSON), 0.95)
	out.TTFTp50MS, out.TTFTp95MS, out.TTFTSamples = p50, p95, n
	out.TTFTApprox = n > 0
	out.TTFTOverflow = over || over95
	dur := r.To.Sub(r.From).Seconds()
	if dur > 0 && req.InflightIntegralSec > 0 {
		v := req.InflightIntegralSec / dur
		out.InflightMean = &v
	}
	out.Finance = financeFrom(req, att, req.RequestsCompleted)
	out.Meta.DataQuality.Unmeasured = map[string]int64{
		ReasonUnbound:      req.UnboundCount,
		ReasonMissingSale:  req.MissingSaleCount,
		ReasonMissingPrice: req.MissingPriceCount,
		ReasonMissingUsage: req.MissingUsageCount,
		ReasonInterrupted:  req.InterruptedCount,
	}
	out.Balance = s.currentBalance(ctx, balanceStale)
	return out, nil
}

func (s *Service) Trends(ctx context.Context, r Range, gran string) (TrendsDTO, error) {
	if err := ValidateGranularity(r, gran); err != nil {
		return TrendsDTO{}, err
	}
	meta := s.meta(r)
	out := TrendsDTO{Meta: meta, Granularity: gran, Points: []TrendPoint{}}
	step := time.Minute
	useDay := gran == "day"
	if gran == "hour" {
		step = time.Hour
	}
	if useDay {
		step = 24 * time.Hour
	}
	avail := s.availableFrom()
	cursor := r.From
	if useDay {
		cursor = shanghaiDay(r.From)
	} else {
		cursor = cursor.UTC().Truncate(step)
	}
	for cursor.Before(r.To) {
		next := cursor.Add(step)
		if useDay {
			next = shanghaiDay(cursor).AddDate(0, 0, 1)
		}
		to := next
		if to.After(r.To) {
			to = r.To
		}
		from := cursor
		if from.Before(r.From) {
			from = r.From
		}
		req, err := s.sumRange(ctx, Range{From: from, To: to}, FamilyRequest, DimGlobal, 0)
		if err != nil {
			return TrendsDTO{}, err
		}
		att, err := s.sumRange(ctx, Range{From: from, To: to}, FamilyAttempt, DimGlobal, 0)
		if err != nil {
			return TrendsDTO{}, err
		}
		pt := TrendPoint{
			Bucket:            cursor.UTC(),
			Complete:          !from.Before(avail) && !to.After(time.Now()),
			RequestsStarted:   req.RequestsStarted,
			RequestsCompleted: req.RequestsCompleted,
			InflightPeak:      req.InflightPeak,
		}
		pt.SuccessRate = ratio(req.RequestsSuccess, req.RequestsCompleted)
		if req.RequestsCompleted > 0 {
			v := 1 - *pt.SuccessRate
			pt.FailureRate = &v
		}
		pt.RetryRate = ratio(req.RequestsRetried, req.RequestsCompleted)
		pt.ProviderSuccessRate = ratio(att.ProviderSuccess, att.ProviderSuccess+att.ProviderFailure)
		p50, n, _ := quantileMS(parseHist(req.TTFTHistJSON), 0.50)
		p95, _, _ := quantileMS(parseHist(req.TTFTHistJSON), 0.95)
		pt.TTFTp50MS, pt.TTFTp95MS, pt.TTFTSamples = p50, p95, n
		dur := to.Sub(from).Seconds()
		if dur > 0 && req.InflightIntegralSec > 0 {
			v := req.InflightIntegralSec / dur
			pt.InflightMean = &v
		}
		if att.ConsumptionUSD != 0 {
			pt.ConsumptionUSD = ptrFloat(round8(att.ConsumptionUSD))
		}
		if req.CoveredCount > 0 {
			pt.CoveredProfitUSD = ptrFloat(round8(req.CoveredRevenueUSD - req.CoveredCostUSD))
		}
		if req.KnownRevenueUSD != 0 {
			pt.KnownRevenueUSD = ptrFloat(round8(req.KnownRevenueUSD))
		}
		out.Points = append(out.Points, pt)
		cursor = next
	}
	return out, nil
}

func (s *Service) Rankings(ctx context.Context, r Range, dim string, page, pageSize int) (RankingsDTO, error) {
	dim = strings.ToLower(strings.TrimSpace(dim))
	if dim != DimGroup && dim != DimProvider {
		return RankingsDTO{}, fmt.Errorf("%w: dimension must be group or provider", ErrBadRange)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	meta := s.meta(r)
	rows, err := s.listDim(ctx, r, FamilyRequest, dim)
	if err != nil {
		return RankingsDTO{}, err
	}
	attRows, err := s.listDim(ctx, r, FamilyAttempt, dim)
	if err != nil {
		return RankingsDTO{}, err
	}
	attBy := map[uint]MinuteAgg{}
	for _, a := range attRows {
		attBy[a.DimID] = a
	}
	names := s.dimNames(dim, rows)
	items := make([]RankingRow, 0, len(rows))
	for _, row := range rows {
		att := attBy[row.DimID]
		item := RankingRow{
			ID:                row.DimID,
			Name:              names[row.DimID],
			RequestsCompleted: row.RequestsCompleted,
			SuccessRate:       ratio(row.RequestsSuccess, row.RequestsCompleted),
		}
		if dim == DimProvider {
			item.SuccessRate = ratio(att.ProviderSuccess, att.ProviderSuccess+att.ProviderFailure)
		}
		if row.KnownRevenueUSD != 0 {
			item.KnownRevenueUSD = ptrFloat(round8(row.KnownRevenueUSD))
		}
		if row.CoveredCount == 0 {
			item.Excluded = true
			item.ExcludeReason = "未纳入完整测算"
		} else {
			item.CoveredRevenueUSD = ptrFloat(round8(row.CoveredRevenueUSD))
			item.CoveredCostUSD = ptrFloat(round8(row.CoveredCostUSD))
			profit := round8(row.CoveredRevenueUSD - row.CoveredCostUSD)
			item.EstimatedProfitUSD = &profit
			item.Margin = ratioAmount(row.CoveredRevenueUSD-row.CoveredCostUSD, row.CoveredRevenueUSD)
			item.Coverage = ratio(row.CoveredCount, row.RequestsCompleted)
		}
		if att.ConsumptionUSD != 0 {
			item.ConsumptionUSD = ptrFloat(round8(att.ConsumptionUSD))
		}
		items = append(items, item)
	}
	sortRankings(items)
	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return RankingsDTO{Meta: meta, Dimension: dim, Items: items[start:end], Total: total, Page: page, PageSize: pageSize}, nil
}

func sortRankings(items []RankingRow) {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if rankingLess(items[j], items[i]) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

func rankingLess(a, b RankingRow) bool {
	if a.Excluded != b.Excluded {
		return !a.Excluded
	}
	ap, bp := -math.MaxFloat64, -math.MaxFloat64
	if a.EstimatedProfitUSD != nil {
		ap = *a.EstimatedProfitUSD
	}
	if b.EstimatedProfitUSD != nil {
		bp = *b.EstimatedProfitUSD
	}
	if ap != bp {
		return ap > bp
	}
	return a.ID < b.ID
}

func (s *Service) sumRange(ctx context.Context, r Range, family, dim string, id uint) (MinuteAgg, error) {
	out := MinuteAgg{TTFTHistJSON: histJSON(emptyHist())}
	rows, err := s.rangeRows(ctx, r, family, dim, &id)
	if err != nil {
		return out, err
	}
	for _, m := range rows {
		out = mergeMinute(out, m)
	}
	return out, nil
}

func (s *Service) listDim(ctx context.Context, r Range, family, dim string) ([]MinuteAgg, error) {
	mins, err := s.rangeRows(ctx, r, family, dim, nil)
	if err != nil {
		return nil, err
	}
	by := map[uint]MinuteAgg{}
	for _, m := range mins {
		by[m.DimID] = mergeMinute(by[m.DimID], m)
	}
	out := make([]MinuteAgg, 0, len(by))
	for id, v := range by {
		v.DimID = id
		out = append(out, v)
	}
	return out, nil
}

// Complete Shanghai days use durable daily aggregates. Only the partial edges
// use minute buckets, so neither today's values nor old retained days are lost.
func (s *Service) rangeRows(ctx context.Context, r Range, family, dim string, id *uint) ([]MinuteAgg, error) {
	dayFrom, dayTo := shanghaiDay(r.From), shanghaiDay(r.To)
	if dayFrom.Before(r.From) {
		dayFrom = dayFrom.AddDate(0, 0, 1)
	}
	var edges []Range
	var rows []MinuteAgg
	q := s.db.WithContext(ctx).Where("family = ? AND dim = ?", family, dim)
	if id != nil {
		q = q.Where("dim_id = ?", *id)
	}
	q = q.Session(&gorm.Session{})
	if dayFrom.Before(dayTo) {
		var days []DayAgg
		if err := q.Where("bucket >= ? AND bucket < ?", dayFrom, dayTo).Find(&days).Error; err != nil {
			return nil, err
		}
		for _, d := range days {
			rows = append(rows, minuteFromDay(d))
		}
		if r.From.Before(dayFrom) {
			edges = append(edges, Range{From: r.From, To: dayFrom})
		}
		if dayTo.Before(r.To) {
			edges = append(edges, Range{From: dayTo, To: r.To})
		}
	} else {
		edges = append(edges, r)
	}
	for _, edge := range edges {
		if edge.From.Before(time.Now().Add(-MinuteRetention)) {
			return nil, fmt.Errorf("%w: older ranges require Asia/Shanghai midnight boundaries", ErrBadRange)
		}
		var minutes []MinuteAgg
		if err := q.Where("bucket >= ? AND bucket < ?", edge.From.UTC().Truncate(time.Minute), edge.To.UTC()).Find(&minutes).Error; err != nil {
			return nil, err
		}
		rows = append(rows, minutes...)
	}
	return rows, nil
}

func (s *Service) dimNames(dim string, rows []MinuteAgg) map[uint]string {
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.DimID)
	}
	out := map[uint]string{}
	if len(ids) == 0 {
		return out
	}
	if dim == DimGroup {
		var gs []domain.RouteGroup
		_ = s.db.Select("id", "name").Where("id IN ?", ids).Find(&gs).Error
		for _, g := range gs {
			out[g.ID] = g.Name
		}
		var facts []RequestFact
		_ = s.db.Select("route_group_id, route_group_name").Where("route_group_id IN ?", ids).Group("route_group_id, route_group_name").Find(&facts).Error
		for _, f := range facts {
			if f.RouteGroupID != nil && out[*f.RouteGroupID] == "" {
				out[*f.RouteGroupID] = f.RouteGroupName
			}
		}
	} else {
		var us []domain.Upstream
		_ = s.db.Select("id", "name").Where("id IN ?", ids).Find(&us).Error
		for _, u := range us {
			out[u.ID] = u.Name
		}
		var facts []AttemptFact
		_ = s.db.Select("provider_id, provider_name").Where("provider_id IN ?", ids).Group("provider_id, provider_name").Find(&facts).Error
		for _, f := range facts {
			if out[f.ProviderID] == "" {
				out[f.ProviderID] = f.ProviderName
			}
		}
	}
	for _, id := range ids {
		if out[id] == "" {
			out[id] = fmt.Sprintf("#%d", id)
		}
	}
	return out
}

func mergeMinute(a, b MinuteAgg) MinuteAgg {
	if a.TTFTHistJSON == "" {
		a.TTFTHistJSON = histJSON(emptyHist())
	}
	a.RequestsStarted += b.RequestsStarted
	a.RequestsCompleted += b.RequestsCompleted
	a.RequestsSuccess += b.RequestsSuccess
	a.RequestsRetried += b.RequestsRetried
	a.ProviderSuccess += b.ProviderSuccess
	a.ProviderFailure += b.ProviderFailure
	a.InflightIntegralSec += b.InflightIntegralSec
	if b.InflightPeak > a.InflightPeak {
		a.InflightPeak = b.InflightPeak
	}
	a.TTFTHistJSON = histJSON(addHist(parseHist(a.TTFTHistJSON), parseHist(b.TTFTHistJSON)))
	a.TTFTSamples += b.TTFTSamples
	a.KnownRevenueUSD = round8(a.KnownRevenueUSD + b.KnownRevenueUSD)
	a.KnownCostUSD = round8(a.KnownCostUSD + b.KnownCostUSD)
	a.ConsumptionUSD = round8(a.ConsumptionUSD + b.ConsumptionUSD)
	a.ReportedConsumptionUSD = round8(a.ReportedConsumptionUSD + b.ReportedConsumptionUSD)
	a.EstimatedConsumptionUSD = round8(a.EstimatedConsumptionUSD + b.EstimatedConsumptionUSD)
	a.UnknownConsumption += b.UnknownConsumption
	a.CoveredRevenueUSD = round8(a.CoveredRevenueUSD + b.CoveredRevenueUSD)
	a.CoveredCostUSD = round8(a.CoveredCostUSD + b.CoveredCostUSD)
	a.CoveredCount += b.CoveredCount
	a.UnboundCount += b.UnboundCount
	a.MissingSaleCount += b.MissingSaleCount
	a.MissingPriceCount += b.MissingPriceCount
	a.MissingUsageCount += b.MissingUsageCount
	a.InterruptedCount += b.InterruptedCount
	return a
}

func minuteFromDay(d DayAgg) MinuteAgg {
	return MinuteAgg{
		Family: d.Family, Dim: d.Dim, DimID: d.DimID,
		RequestsStarted: d.RequestsStarted, RequestsCompleted: d.RequestsCompleted, RequestsSuccess: d.RequestsSuccess,
		RequestsRetried: d.RequestsRetried, ProviderSuccess: d.ProviderSuccess, ProviderFailure: d.ProviderFailure,
		InflightIntegralSec: d.InflightIntegralSec, InflightPeak: d.InflightPeak, TTFTHistJSON: d.TTFTHistJSON, TTFTSamples: d.TTFTSamples,
		KnownRevenueUSD: d.KnownRevenueUSD, KnownCostUSD: d.KnownCostUSD, ConsumptionUSD: d.ConsumptionUSD,
		ReportedConsumptionUSD: d.ReportedConsumptionUSD, EstimatedConsumptionUSD: d.EstimatedConsumptionUSD,
		UnknownConsumption: d.UnknownConsumption, CoveredRevenueUSD: d.CoveredRevenueUSD, CoveredCostUSD: d.CoveredCostUSD,
		CoveredCount: d.CoveredCount, UnboundCount: d.UnboundCount, MissingSaleCount: d.MissingSaleCount,
		MissingPriceCount: d.MissingPriceCount, MissingUsageCount: d.MissingUsageCount, InterruptedCount: d.InterruptedCount,
	}
}

func financeFrom(req, att MinuteAgg, completed int64) FinanceDTO {
	f := FinanceDTO{UnknownConsumption: att.UnknownConsumption, KnownPartial: att.UnknownConsumption > 0 || req.CoveredCount < completed}
	if req.KnownRevenueUSD != 0 || req.RequestsCompleted > 0 {
		f.KnownRevenueUSD = ptrFloat(round8(req.KnownRevenueUSD))
	}
	if req.KnownCostUSD != 0 {
		f.KnownEstimatedCostUSD = ptrFloat(round8(req.KnownCostUSD))
	}
	if att.ConsumptionUSD != 0 {
		f.ConsumptionUSD = ptrFloat(round8(att.ConsumptionUSD))
	}
	if att.ReportedConsumptionUSD != 0 {
		f.ReportedConsumptionUSD = ptrFloat(round8(att.ReportedConsumptionUSD))
	}
	if att.EstimatedConsumptionUSD != 0 {
		f.EstimatedConsumptionUSD = ptrFloat(round8(att.EstimatedConsumptionUSD))
	}
	if req.CoveredCount > 0 {
		f.CoveredRevenueUSD = ptrFloat(round8(req.CoveredRevenueUSD))
		f.CoveredCostUSD = ptrFloat(round8(req.CoveredCostUSD))
		profit := round8(req.CoveredRevenueUSD - req.CoveredCostUSD)
		f.EstimatedProfitUSD = &profit
		f.Margin = ratioAmount(req.CoveredRevenueUSD-req.CoveredCostUSD, req.CoveredRevenueUSD)
		f.Coverage = ratio(req.CoveredCount, completed)
	} else if completed > 0 {
		z := 0.0
		f.Coverage = &z
	}
	return f
}

func ratio(n, d int64) *float64 {
	if d <= 0 {
		return nil
	}
	v := float64(n) / float64(d)
	return &v
}

func ratioAmount(n, d float64) *float64 {
	if d == 0 {
		return nil
	}
	v := n / d
	return &v
}

func (s *Service) currentBalance(ctx context.Context, staleAfter time.Duration) BalanceDTO {
	var ups []domain.Upstream
	_ = s.db.WithContext(ctx).Find(&ups).Error
	out := BalanceDTO{Providers: []ProviderBalance{}}
	var total, enabled float64
	var latest *time.Time
	now := time.Now()
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	for _, u := range ups {
		p := ProviderBalance{ID: u.ID, Name: u.Name, Enabled: u.Status == domain.StatusEnabled, Kind: u.Kind, BalanceUSD: u.LastBalance, BalanceAt: u.LastBalanceAt}
		if u.LastBalance == nil && u.LastBalanceAt != nil {
			p.Unlimited = true
			out.UnlimitedCount++
		} else if u.LastBalance == nil {
			p.Unknown = true
			out.UnknownCount++
		} else {
			total += *u.LastBalance
			if p.Enabled {
				enabled += *u.LastBalance
			}
		}
		if !p.Enabled {
			out.DisabledCount++
		}
		if u.LastBalanceAt != nil {
			if latest == nil || u.LastBalanceAt.After(*latest) {
				t := *u.LastBalanceAt
				latest = &t
			}
			if now.Sub(*u.LastBalanceAt) > staleAfter {
				p.Stale = true
				out.StaleCount++
			}
		}
		out.Providers = append(out.Providers, p)
	}
	if out.UnknownCount+out.UnlimitedCount < len(ups) {
		out.TotalKnownUSD = ptrFloat(round8(total))
		out.EnabledKnownUSD = ptrFloat(round8(enabled))
	}
	out.RefreshedAt = latest
	return out
}

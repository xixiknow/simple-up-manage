package dashboard

import (
	"context"
	"math"
	"regexp"
	"strings"
	"time"

	"simple-up-manage/internal/domain"

	"gorm.io/gorm"
)

// Legacy cache coefficients applied whenever the price card lacks an absolute
// per-token cache price (models.dev rows and pre-extension snapshots).
const (
	LegacyCacheReadCoeff  = 0.1
	LegacyCacheWriteCoeff = 1.25
)

// ModelPrice is the billing price card resolved for one request.
//
// Scale conventions: Input/Output are USD per million tokens (models.dev
// convention, kept for the legacy coefficient path); every absolute price
// field (cache / priority) is USD per token.
type ModelPrice struct {
	Input  float64
	Output float64
	// Coefficients over the input price, used when the matching absolute price
	// is absent (cache read = input × CacheReadCoeff, cache write = input ×
	// CacheWriteCoeff).
	CacheReadCoeff  float64
	CacheWriteCoeff float64
	// Matched is the snapshot model_id that answered the lookup, which can
	// differ from the requested model when an alias candidate matched.
	Matched string
	OK      bool

	// Absolute per-token prices from the LiteLLM card; nil falls back to the
	// coefficient path for that item.
	CacheRead    *float64
	CacheWrite5m *float64
	CacheWrite1h *float64

	// Priority service-tier prices (per token for cache fields, per million
	// for input/output — mirroring the base fields' scales).
	InputPriority        *float64
	OutputPriority       *float64
	CacheReadPriority    *float64
	CacheWrite5mPriority *float64

	// Long-context ladder: total prompt tokens above the threshold scale
	// input (and cache) prices by LongCtxInputMult and output prices by
	// LongCtxOutputMult.
	LongCtxThreshold  int64
	LongCtxInputMult  float64
	LongCtxOutputMult float64
}

// UsageTokens carries the protocol-native usage a request produced.
// Input keeps the upstream's own semantics (Anthropic excludes cache tokens,
// OpenAI prompt_tokens includes them); ComputeCost normalizes per protocol.
type UsageTokens struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite   int64
	CacheWrite5m int64
	CacheWrite1h int64
	ServiceTier  string
	UsageKnown   bool
}

type CostOptions struct {
	Protocol string
	Model    string
	// At prices time-dependent rules (peak hours); zero means now.
	At     time.Time
	Effort string
}

// LongCtxDetail reports whether the long-context ladder fired for a request.
type LongCtxDetail struct {
	Threshold        int64   `json:"threshold"`
	TotalTokens      int64   `json:"total_tokens"`
	Applied          bool    `json:"applied"`
	InputMultiplier  float64 `json:"input_multiplier"`
	OutputMultiplier float64 `json:"output_multiplier"`
}

// CostDetail is the billing receipt persisted on request logs (JSON column)
// and rendered by the request-records cost popover. Prices are USD per token;
// the frontend converts to per-MTok for display.
type CostDetail struct {
	Source   string `json:"source"` // estimated | reported
	Model    string `json:"model,omitempty"`
	Matched  string `json:"matched,omitempty"`
	Protocol string `json:"protocol,omitempty"`

	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWriteTokens   int64 `json:"cache_write_tokens"`
	CacheWrite5mTokens int64 `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"`

	InputPrice        float64 `json:"input_price"`
	OutputPrice       float64 `json:"output_price"`
	CacheReadPrice    float64 `json:"cache_read_price"`
	CacheWrite5mPrice float64 `json:"cache_write_5m_price"`
	CacheWrite1hPrice float64 `json:"cache_write_1h_price"`
	CacheWriteMode    string  `json:"cache_write_mode,omitempty"` // breakdown | coefficient

	InputCost      float64 `json:"input_cost"`
	OutputCost     float64 `json:"output_cost"`
	CacheReadCost  float64 `json:"cache_read_cost"`
	CacheWriteCost float64 `json:"cache_write_cost"`

	ServiceTier      string         `json:"service_tier,omitempty"`
	TierMultiplier   float64        `json:"tier_multiplier,omitempty"`
	LongCtx          *LongCtxDetail `json:"long_ctx,omitempty"`
	TimeMultiplier   float64        `json:"time_multiplier,omitempty"`
	Effort           string         `json:"effort,omitempty"`
	EffortMultiplier float64        `json:"effort_multiplier,omitempty"`

	Total float64 `json:"total"`
	// RateMultiplier is the upstream key multiplier; Final is what the request
	// actually cost (Total × rate, or the upstream-reported amount).
	RateMultiplier float64 `json:"rate_multiplier"`
	Final          float64 `json:"final"`
}

type CostResult struct {
	// TotalUSD is nil when the model has no price card or usage is unknown.
	TotalUSD *float64
	Detail   *CostDetail
}

const (
	CostSourceReported  = "reported"
	CostSourceEstimated = "estimated"
	CostSourceNone      = "none"
)

func PublishCatalogVersion(ctx context.Context, db *gorm.DB) (uint, error) {
	if db == nil {
		return 0, nil
	}
	var catalogRows []domain.CatalogModel
	if err := db.WithContext(ctx).Find(&catalogRows).Error; err != nil {
		return 0, err
	}
	var litellmRows []domain.LiteLLMPrice
	if err := db.WithContext(ctx).Find(&litellmRows).Error; err != nil {
		return 0, err
	}
	prices := mergeCatalogPrices(catalogRows, litellmRows)
	meta := domain.CatalogMeta{ID: 1, Source: "models.dev"}
	_ = db.WithContext(ctx).First(&meta, 1).Error
	source := firstNonEmpty(meta.Source, "models.dev")
	if len(litellmRows) > 0 {
		source = "models.dev+litellm"
	}
	ver := CatalogVersion{
		Source:      source,
		ModelCount:  len(prices),
		PublishedAt: time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ver).Error; err != nil {
			return err
		}
		if len(prices) == 0 {
			return nil
		}
		for i := range prices {
			prices[i].VersionID = ver.ID
		}
		return tx.CreateInBatches(prices, 100).Error
	}); err != nil {
		return 0, err
	}
	return ver.ID, nil
}

// mergeCatalogPrices builds the snapshot rows: models.dev input/output prices
// first, then one LiteLLM row per lowercased model key overriding any
// colliding entry. One row per model_id keeps the version lookup deterministic.
func mergeCatalogPrices(catalogRows []domain.CatalogModel, litellmRows []domain.LiteLLMPrice) []CatalogVersionPrice {
	chosen := map[string]CatalogVersionPrice{}
	for _, r := range catalogRows {
		id := strings.ToLower(strings.TrimSpace(r.ModelID))
		if id == "" {
			continue
		}
		if _, ok := chosen[id]; ok {
			continue
		}
		chosen[id] = CatalogVersionPrice{
			Vendor:          r.Vendor,
			ModelID:         r.ModelID,
			InputCost:       r.InputCost,
			OutputCost:      r.OutputCost,
			CacheReadCoeff:  LegacyCacheReadCoeff,
			CacheWriteCoeff: LegacyCacheWriteCoeff,
		}
	}
	for _, r := range litellmRows {
		id := strings.ToLower(strings.TrimSpace(r.ModelKey))
		if id == "" {
			continue
		}
		row := CatalogVersionPrice{
			Vendor:                    r.Vendor,
			ModelID:                   r.ModelKey,
			InputCost:                 r.InputPricePerToken * 1e6,
			OutputCost:                r.OutputPricePerToken * 1e6,
			CacheReadCoeff:            LegacyCacheReadCoeff,
			CacheWriteCoeff:           LegacyCacheWriteCoeff,
			CacheReadPrice:            r.CacheReadPricePerToken,
			CacheWrite5mPrice:         r.CacheWrite5mPricePerToken,
			CacheWrite1hPrice:         r.CacheWrite1hPricePerToken,
			InputPriorityPrice:        r.InputPriorityPerToken,
			OutputPriorityPrice:       r.OutputPriorityPerToken,
			CacheReadPriorityPrice:    r.CacheReadPriorityPerToken,
			CacheWrite5mPriorityPrice: r.CacheWrite5mPriorityPerToken,
		}
		if r.LongCtxThreshold > 0 {
			t := r.LongCtxThreshold
			in, out := r.LongCtxInputMult, r.LongCtxOutputMult
			row.LongCtxThreshold = &t
			row.LongCtxInputMult = &in
			row.LongCtxOutputMult = &out
		}
		chosen[id] = row
	}
	out := make([]CatalogVersionPrice, 0, len(chosen))
	for _, row := range chosen {
		out = append(out, row)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func EnsureCatalogVersion(ctx context.Context, db *gorm.DB) (uint, error) {
	var ver CatalogVersion
	err := db.WithContext(ctx).Order("id DESC").First(&ver).Error
	if err == nil {
		return ver.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	var n int64
	if err := db.WithContext(ctx).Model(&domain.CatalogModel{}).Count(&n).Error; err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	return PublishCatalogVersion(ctx, db)
}

func CurrentCatalogVersion(db *gorm.DB) uint {
	if db == nil {
		return 0
	}
	var ver CatalogVersion
	if err := db.Order("id DESC").First(&ver).Error; err != nil {
		return 0
	}
	return ver.ID
}

var (
	priceDateSuffixPattern = regexp.MustCompile(`-\d{8}$`)
	priceDigitDashPattern  = regexp.MustCompile(`-(\d)-(\d)`)
)

// LookupVersionPrice resolves the price card for a model within a snapshot
// version. Candidates try the exact id first, then progressively looser
// aliases (provider prefix, date suffix, digit-dot spelling) so LiteLLM cards
// keep answering for versioned request model names.
func LookupVersionPrice(db *gorm.DB, versionID uint, model string) ModelPrice {
	fallback := ModelPrice{CacheReadCoeff: LegacyCacheReadCoeff, CacheWriteCoeff: LegacyCacheWriteCoeff}
	if db == nil || versionID == 0 || strings.TrimSpace(model) == "" {
		return fallback
	}
	for _, candidate := range priceLookupCandidates(model) {
		var row CatalogVersionPrice
		err := db.Where("version_id = ? AND LOWER(model_id) = ? AND (input_cost > 0 OR output_cost > 0)", versionID, candidate).
			Order("vendor").First(&row).Error
		if err != nil {
			continue
		}
		return modelPriceFromRow(row)
	}
	return fallback
}

func priceLookupCandidates(model string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	base := catalogModelID(model)
	full := strings.ToLower(strings.TrimSpace(model))
	add(base)
	add(full)
	for _, c := range []string{base, full} {
		add(priceDateSuffixPattern.ReplaceAllString(c, ""))
	}
	// claude-opus-4-5 → claude-opus-4.5 style spelling variants (first
	// digit-digit group only; LiteLLM mixes dash and dot version conventions).
	for _, c := range []string{base, full} {
		if m := priceDigitDashPattern.FindStringSubmatchIndex(c); m != nil {
			dotted := c[:m[0]] + "-" + c[m[2]:m[3]] + "." + c[m[4]:m[5]] + c[m[1]:]
			add(dotted)
			add(priceDateSuffixPattern.ReplaceAllString(dotted, ""))
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func modelPriceFromRow(row CatalogVersionPrice) ModelPrice {
	cr, cw := row.CacheReadCoeff, row.CacheWriteCoeff
	if cr <= 0 {
		cr = LegacyCacheReadCoeff
	}
	if cw <= 0 {
		cw = LegacyCacheWriteCoeff
	}
	p := ModelPrice{
		Input:                row.InputCost,
		Output:               row.OutputCost,
		CacheReadCoeff:       cr,
		CacheWriteCoeff:      cw,
		Matched:              row.ModelID,
		OK:                   true,
		CacheRead:            row.CacheReadPrice,
		CacheWrite5m:         row.CacheWrite5mPrice,
		CacheWrite1h:         row.CacheWrite1hPrice,
		InputPriority:        row.InputPriorityPrice,
		OutputPriority:       row.OutputPriorityPrice,
		CacheReadPriority:    row.CacheReadPriorityPrice,
		CacheWrite5mPriority: row.CacheWrite5mPriorityPrice,
	}
	if row.LongCtxThreshold != nil {
		p.LongCtxThreshold = *row.LongCtxThreshold
	}
	if row.LongCtxInputMult != nil {
		p.LongCtxInputMult = *row.LongCtxInputMult
	}
	if row.LongCtxOutputMult != nil {
		p.LongCtxOutputMult = *row.LongCtxOutputMult
	}
	return p
}

func catalogModelID(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(id, "/"); i >= 0 && i < len(id)-1 {
		id = id[i+1:]
	}
	return id
}

// BaseCostUSD is the legacy coefficient-based estimate kept for old snapshots
// and tests: catalog price × tokens / 1e6, without key or sale multipliers.
func BaseCostUSD(price ModelPrice, protocol string, input, cacheRead, cacheWrite, output int64, usageKnown bool) *float64 {
	legacy := price
	legacy.CacheRead, legacy.CacheWrite5m, legacy.CacheWrite1h = nil, nil, nil
	legacy.InputPriority, legacy.OutputPriority = nil, nil
	legacy.CacheReadPriority, legacy.CacheWrite5mPriority = nil, nil
	legacy.LongCtxThreshold, legacy.LongCtxInputMult, legacy.LongCtxOutputMult = 0, 0, 0
	res := ComputeCost(legacy, CostOptions{Protocol: protocol}, UsageTokens{
		Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, UsageKnown: usageKnown,
	})
	return res.TotalUSD
}

// ComputeCost prices one request item-by-item from the resolved card:
// input / output / cache-read / cache-write (5m/1h tiers when the card has
// them), then layers the long-context ladder, service-tier pricing, peak
// hours and reasoning-effort rules on top. Missing per-item prices fall back
// to the legacy cache coefficients, so pre-extension snapshots keep working.
func ComputeCost(price ModelPrice, opt CostOptions, u UsageTokens) *CostResult {
	if !price.OK || !u.UsageKnown {
		return &CostResult{}
	}
	inPrice, outPrice, readPrice, writeMode, w5, w1, writeAggPrice := resolveItemPrices(price, u.ServiceTier)
	tierMult := tierMultiplierFor(price, u.ServiceTier)

	longCtx := &LongCtxDetail{Threshold: price.LongCtxThreshold, InputMultiplier: 1, OutputMultiplier: 1}
	if price.LongCtxThreshold > 0 {
		total := totalContextTokens(opt.Protocol, u)
		longCtx.TotalTokens = total
		if total > price.LongCtxThreshold {
			inMult := orOne(price.LongCtxInputMult)
			outMult := orOne(price.LongCtxOutputMult)
			inPrice *= inMult
			outPrice *= outMult
			readPrice *= inMult
			w5 *= inMult
			w1 *= inMult
			writeAggPrice *= inMult
			longCtx.Applied = true
			longCtx.InputMultiplier = inMult
			longCtx.OutputMultiplier = outMult
		}
	}

	uncached := u.Input
	if opt.Protocol == domain.ProtocolOpenAI {
		uncached = max(0, uncached-u.CacheRead-u.CacheWrite)
	}
	inputCost := float64(uncached) * inPrice
	outputCost := float64(u.Output) * outPrice
	readCost := float64(u.CacheRead) * readPrice
	var writeCost float64
	if writeMode == "breakdown" {
		n5, n1 := normalizeCacheWrite(u)
		if n5 == 0 && n1 == 0 && u.CacheWrite > 0 {
			// API reported cache-write tokens without ephemeral details:
			// bill everything at the 5m price (sub2api fallback).
			n5, n1 = u.CacheWrite, 0
		}
		writeCost = float64(n5)*w5 + float64(n1)*w1
	} else {
		writeCost = float64(u.CacheWrite) * writeAggPrice
	}

	detail := &CostDetail{
		Model: opt.Model, Matched: price.Matched, Protocol: opt.Protocol,
		InputTokens: u.Input, OutputTokens: u.Output,
		CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrite,
		CacheWrite5mTokens: u.CacheWrite5m, CacheWrite1hTokens: u.CacheWrite1h,
		InputPrice: inPrice, OutputPrice: outPrice, CacheReadPrice: readPrice,
		CacheWrite5mPrice: w5, CacheWrite1hPrice: w1, CacheWriteMode: writeMode,
		InputCost: inputCost, OutputCost: outputCost, CacheReadCost: readCost, CacheWriteCost: writeCost,
		ServiceTier: strings.TrimSpace(u.ServiceTier), LongCtx: longCtx,
	}
	if tierMult != 1 {
		detail.TierMultiplier = tierMult
		inputCost *= tierMult
		outputCost *= tierMult
		readCost *= tierMult
		writeCost *= tierMult
		detail.InputCost, detail.OutputCost = inputCost, outputCost
		detail.CacheReadCost, detail.CacheWriteCost = readCost, writeCost
	}

	timeMult := TimeMultiplier(opt.Model, opt.At)
	if timeMult != 1 {
		detail.TimeMultiplier = timeMult
		inputCost *= timeMult
		outputCost *= timeMult
		readCost *= timeMult
		detail.InputCost, detail.OutputCost, detail.CacheReadCost = inputCost, outputCost, readCost
	}
	effortMult := EffortMultiplier(opt.Model, opt.Effort)
	if effortMult != 1 {
		detail.Effort = strings.TrimSpace(opt.Effort)
		detail.EffortMultiplier = effortMult
		inputCost *= effortMult
		outputCost *= effortMult
		readCost *= effortMult
		writeCost *= effortMult
		detail.InputCost, detail.OutputCost = inputCost, outputCost
		detail.CacheReadCost, detail.CacheWriteCost = readCost, writeCost
	}

	total := inputCost + outputCost + readCost + writeCost
	if math.IsNaN(total) || math.IsInf(total, 0) {
		return &CostResult{}
	}
	detail.Total = round8(total)
	v := detail.Total
	return &CostResult{TotalUSD: &v, Detail: detail}
}

// resolveItemPrices picks per-token unit prices for one request: absolute
// card prices win, legacy coefficients fill the gaps, and priority-tier
// substitution applies when the card has priority prices.
func resolveItemPrices(price ModelPrice, tier string) (in, out, read float64, writeMode string, w5, w1, writeAgg float64) {
	in = price.Input / 1e6
	out = price.Output / 1e6
	read = in * orCoeff(price.CacheReadCoeff, LegacyCacheReadCoeff)
	if price.CacheRead != nil {
		read = *price.CacheRead
	}
	writeAgg = in * orCoeff(price.CacheWriteCoeff, LegacyCacheWriteCoeff)
	writeMode = "coefficient"
	w5, w1 = writeAgg, writeAgg

	hasBreakdown := price.CacheWrite5m != nil || price.CacheWrite1h != nil
	if hasBreakdown {
		w5 = orFirst(price.CacheWrite5m, price.CacheWrite1h)
		w1 = w5
		if p := price.CacheWrite1h; p != nil && *p > w5 {
			// 1h tier only applies when it actually costs more than 5m;
			// otherwise the card is treated as 5m-only (sub2api guard).
			w1 = *p
		}
		writeMode = "breakdown"
	}

	if isPriorityTier(tier) && hasPriorityPrices(price) {
		if price.InputPriority != nil {
			in = *price.InputPriority / 1e6
		}
		if price.OutputPriority != nil {
			out = *price.OutputPriority / 1e6
		}
		if price.CacheReadPriority != nil {
			read = *price.CacheReadPriority
		}
		if price.CacheWrite5mPriority != nil {
			writeAgg = *price.CacheWrite5mPriority
		} else {
			// Priority cache write ≈ priority input × coefficient when the
			// card does not price it separately (matches LiteLLM data).
			writeAgg = in * orCoeff(price.CacheWriteCoeff, LegacyCacheWriteCoeff)
		}
	}
	return in, out, read, writeMode, w5, w1, writeAgg
}

// tierMultiplierFor returns the flat multiplier for tiers without dedicated
// priority prices (priority/fast ×2) and flex ×0.5.
func tierMultiplierFor(price ModelPrice, tier string) float64 {
	switch normalizeTier(tier) {
	case "priority", "fast", "ultrafast":
		if hasPriorityPrices(price) {
			return 1
		}
		return 2
	case "flex":
		return 0.5
	default:
		return 1
	}
}

func hasPriorityPrices(price ModelPrice) bool {
	return price.InputPriority != nil || price.OutputPriority != nil ||
		price.CacheReadPriority != nil || price.CacheWrite5mPriority != nil
}

func isPriorityTier(tier string) bool {
	switch normalizeTier(tier) {
	case "priority", "fast", "ultrafast":
		return true
	default:
		return false
	}
}

func normalizeTier(tier string) string {
	return strings.ToLower(strings.TrimSpace(tier))
}

// totalContextTokens counts the prompt size a long-context ladder compares
// against the threshold. OpenAI prompt_tokens already include cached tokens;
// Anthropic reports them separately.
func totalContextTokens(protocol string, u UsageTokens) int64 {
	if protocol == domain.ProtocolOpenAI {
		return u.Input
	}
	return u.Input + u.CacheRead + u.CacheWrite
}

// normalizeCacheWrite caps contradictory 5m/1h details at the aggregate while
// keeping their reported ratio (ported from sub2api).
func normalizeCacheWrite(u UsageTokens) (int64, int64) {
	n5, n1, agg := u.CacheWrite5m, u.CacheWrite1h, u.CacheWrite
	if n5 < 0 {
		n5 = 0
	}
	if n1 < 0 {
		n1 = 0
	}
	if agg <= 0 || (n5 <= agg && n1 <= agg-n5) {
		return n5, n1
	}
	detailTotal := float64(n5) + float64(n1)
	if detailTotal <= 0 {
		return 0, 0
	}
	scaled := int64(math.Round(float64(agg) * float64(n5) / detailTotal))
	if scaled >= agg {
		n5 = agg
	} else {
		n5 = scaled
	}
	return n5, agg - n5
}

func orOne(v float64) float64 {
	if v <= 0 {
		return 1
	}
	return v
}

func orCoeff(v, fallback float64) float64 {
	if v <= 0 {
		return fallback
	}
	return v
}

func orFirst(a, b *float64) float64 {
	if a != nil {
		return *a
	}
	if b != nil {
		return *b
	}
	return 0
}

// ReportedCostDetail builds the receipt for a request whose cost the upstream
// reported itself: card prices become reference rows and Final carries the
// reported amount instead of Total × rate.
func ReportedCostDetail(res *CostResult, rate, reported float64) *CostDetail {
	if res == nil || res.Detail == nil {
		return nil
	}
	detail := *res.Detail
	detail.Source = CostSourceReported
	detail.RateMultiplier = rate
	detail.Final = round8(reported)
	return &detail
}

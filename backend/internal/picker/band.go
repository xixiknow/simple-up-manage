package picker

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/metrics"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type BandPicker struct {
	db      *gorm.DB
	rdb     *redis.Client
	metrics *metrics.Store
	mu      sync.RWMutex
	cfg     domain.SchedulerSettings
	sticky  map[string]stickyEntry
	filled  sync.Map
	runtime *runtimeState
}

type stickyEntry struct {
	KeyID  uint
	Expiry time.Time
}

type failureStats struct {
	KeyID     uint  `gorm:"column:platform_key_id"`
	Failures  int64 `gorm:"column:failures"`
	Successes int64 `gorm:"column:successes"`
}

type runtimeState struct {
	mu               sync.Mutex
	keyInflight      map[uint]int
	providerInflight map[uint]int
	rpm              map[uint][]time.Time
}

func NewBand(db *gorm.DB, rdb *redis.Client) *BandPicker {
	cfg := loadSettings(db)
	store := metrics.NewStore(rdb, time.Duration(cfg.WindowMinutes)*time.Minute, cfg.WindowMaxSamples)
	return &BandPicker{
		db:      db,
		rdb:     rdb,
		metrics: store,
		cfg:     cfg,
		sticky:  map[string]stickyEntry{},
		runtime: &runtimeState{keyInflight: map[uint]int{}, providerInflight: map[uint]int{}, rpm: map[uint][]time.Time{}},
	}
}

func loadSettings(db *gorm.DB) domain.SchedulerSettings {
	row := domain.DefaultSchedulerSettings()
	row.ID = 1
	_ = db.FirstOrCreate(&row, domain.SchedulerSettings{ID: 1}).Error
	row.Normalize()
	return row
}

func (p *BandPicker) Settings() domain.SchedulerSettings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

func (p *BandPicker) Reload() {
	cfg := loadSettings(p.db)
	p.mu.Lock()
	p.cfg = cfg
	p.metrics.Configure(time.Duration(cfg.WindowMinutes)*time.Minute, cfg.WindowMaxSamples)
	p.mu.Unlock()
}

func (p *BandPicker) UpdateSettings(ctx context.Context, next domain.SchedulerSettings) error {
	next.Normalize()
	var row domain.SchedulerSettings
	if err := p.db.WithContext(ctx).First(&row).Error; err != nil {
		next.ID = 0
		if err := p.db.WithContext(ctx).Create(&next).Error; err != nil {
			return err
		}
		row = next
	} else {
		next.ID = row.ID
		if err := p.db.WithContext(ctx).Save(&next).Error; err != nil {
			return err
		}
		row = next
	}
	p.mu.Lock()
	p.cfg = row
	p.metrics.Configure(time.Duration(row.WindowMinutes)*time.Minute, row.WindowMaxSamples)
	p.mu.Unlock()
	return nil
}

func (p *BandPicker) Observe(ctx context.Context, keyID uint, model string, success bool, input, cacheRead, cacheCreate int64, ttftMs int) {
	p.metrics.Observe(ctx, metrics.Observation{
		KeyID:               keyID,
		Model:               strings.TrimSpace(model),
		Success:             success,
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheCreate,
		TTFTMs:              ttftMs,
		At:                  time.Now(),
	})
}

func (p *BandPicker) Pick(ctx context.Context, req Request) (*domain.PlatformKey, *domain.Upstream, error) {
	cands, err := p.evaluate(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	for i := range cands {
		if cands[i].Selected && cands[i].Key != nil && cands[i].Upstream != nil {
			return cands[i].Key, cands[i].Upstream, nil
		}
	}
	return nil, nil, ErrNoUpstream
}

func (p *BandPicker) Explain(ctx context.Context, req Request) ([]Candidate, error) {
	return p.evaluate(ctx, req)
}

func (p *BandPicker) evaluate(ctx context.Context, req Request) ([]Candidate, error) {
	cfg := p.Settings()
	var keys []domain.PlatformKey
	if err := p.db.WithContext(ctx).
		Preload("Upstream").
		Order("id ASC").
		Find(&keys).Error; err != nil {
		return nil, err
	}

	excluded := map[uint]struct{}{}
	for _, id := range req.Exclude {
		excluded[id] = struct{}{}
	}
	excludedProviders := map[uint]struct{}{}
	for _, id := range req.ExcludeProviders {
		excludedProviders[id] = struct{}{}
	}
	excludedKeyModels := map[uint]struct{}{}
	for _, id := range req.ExcludeKeyModels {
		excludedKeyModels[id] = struct{}{}
	}

	out := make([]Candidate, 0, len(keys))
	failureByKey := p.recentFailureStats(ctx, keys, cfg)
	modelCooldowns := p.activeModelCooldowns(ctx, keys, req.Model)
	var eligible []int
	for i := range keys {
		c := p.inspect(ctx, &keys[i], req, cfg, excluded, excludedProviders, excludedKeyModels, failureByKey, modelCooldowns)
		out = append(out, c)
		if c.Eligible {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) == 0 {
		return out, nil
	}

	best := out[eligible[0]].Quality
	for _, idx := range eligible {
		if out[idx].Quality > best {
			best = out[idx].Quality
		}
	}
	floor := best * (1 - cfg.Epsilon)
	var band []int
	for _, idx := range eligible {
		if out[idx].Quality+1e-9 >= floor {
			out[idx].InBand = true
			band = append(band, idx)
		}
	}
	mode := effectiveRankingMode(cfg.RankingMode, req.Session)
	for _, idx := range band {
		out[idx].RankingMode = mode
	}
	if mode == "cache_affinity" {
		if stickyID := p.stickyKey(ctx, req, cfg); stickyID > 0 {
			for _, idx := range band {
				if out[idx].KeyID == stickyID {
					out[idx].Selected, out[idx].AffinityHit = true, true
					return out, nil
				}
			}
		}
	}
	sort.SliceStable(band, func(i, j int) bool { return p.candidateLess(out[band[i]], out[band[j]], mode, req) })
	if len(band) > 0 {
		out[band[0]].Selected = true
		out[band[0]].AffinityHit = mode == "cache_affinity" && strings.TrimSpace(req.Session) != ""
	}
	return out, nil
}

func (p *BandPicker) inspect(ctx context.Context, key *domain.PlatformKey, req Request, cfg domain.SchedulerSettings, excluded, excludedProviders, excludedKeyModels map[uint]struct{}, failureByKey map[uint]failureStats, modelCooldowns map[uint]struct{}) Candidate {
	c := Candidate{
		Key:          key,
		Upstream:     key.Upstream,
		KeyID:        key.ID,
		KeyName:      key.Name,
		KeyPreview:   key.KeyPreview,
		UpstreamID:   key.UpstreamID,
		HealthStatus: key.HealthStatus,
		LastBalance:  key.LastBalance,
	}
	if key.Upstream != nil {
		c.UpstreamName = key.Upstream.Name
		c.LastBalance = key.Upstream.LastBalance
	}
	c.Rate = key.RateMultiplier
	c.RankingMode = effectiveRankingMode(cfg.RankingMode, req.Session)
	c.RPMLimit, c.MaxConcurrency = key.RPMLimit, key.MaxConcurrency
	if key.Upstream != nil {
		c.ProviderConcurrency = key.Upstream.Concurrency
	}
	c.CurrentRPM, c.KeyInflight, c.ProviderInflight = p.resourceSnapshot(key.ID, key.UpstreamID)
	if req.AllowKeys != nil {
		if _, ok := req.AllowKeys[key.ID]; !ok {
			if _, drifted := req.DriftKeys[key.ID]; drifted {
				c.SkipReason = "route_rate_drift"
			} else {
				c.SkipReason = "not_in_route_group"
			}
			return c
		}
	}
	if reason := hardReject(key, req.Protocol, req.Model, cfg.ModelFilterEnabled(), excluded); reason != "" {
		c.SkipReason = reason
		return c
	}
	if _, ok := excludedProviders[key.UpstreamID]; ok {
		c.SkipReason = "provider_excluded"
		return c
	}
	if _, ok := excludedKeyModels[key.ID]; ok {
		c.SkipReason = "key_model_excluded"
		return c
	}
	if _, ok := modelCooldowns[key.ID]; ok {
		c.SkipReason = "key_model_cooldown"
		return c
	}
	if key.RPMLimit > 0 && c.CurrentRPM >= key.RPMLimit {
		c.SkipReason = "key_rpm_exceeded"
		return c
	}
	if key.MaxConcurrency > 0 && c.KeyInflight >= key.MaxConcurrency {
		c.SkipReason = "key_concurrency_exceeded"
		return c
	}
	if key.Upstream != nil && key.Upstream.Concurrency > 0 && c.ProviderInflight >= key.Upstream.Concurrency {
		c.SkipReason = "provider_concurrency_exceeded"
		return c
	}
	if stats, ok := failureByKey[key.ID]; ok && stats.Successes == 0 && stats.Failures >= int64(cfg.FailureThreshold) {
		c.SkipReason = "recent_failure_cooldown"
		return c
	}

	w := p.window(ctx, key.ID, req.Model, cfg)
	c.SuccessRate = w.SuccessRate
	c.CacheRate = w.CacheRate
	c.TTFTp50 = w.TTFTp50
	c.Samples = w.Samples
	c.Quality = Quality(ScoreInputs{
		Success:    w.SuccessRate,
		Samples:    w.Samples,
		Cache:      w.CacheRate,
		HasCache:   w.HasCache,
		LatencyP50: w.TTFTp50,
		HasLatency: w.HasLatency,
	}, cfg)
	c.EffectiveCost = effectiveCost(c.Rate, w.CacheRate)
	c.Eligible = true
	return c
}

func effectiveRankingMode(configured, session string) string {
	if configured == "adaptive" {
		if strings.TrimSpace(session) != "" {
			return "cache_affinity"
		}
		return "load_balance"
	}
	return configured
}

func affinityScore(req Request, keyID uint) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d", req.Protocol, req.Model, strings.TrimSpace(req.Session), keyID)
	return h.Sum64()
}

func (p *BandPicker) candidateLess(a, b Candidate, mode string, req Request) bool {
	switch mode {
	case "fixed_order":
		return a.KeyID < b.KeyID
	case "cache_affinity":
		ha, hb := affinityScore(req, a.KeyID), affinityScore(req, b.KeyID)
		if ha != hb {
			return ha > hb
		}
	case "load_balance":
		if a.KeyInflight != b.KeyInflight {
			return a.KeyInflight < b.KeyInflight
		}
		if a.ProviderInflight != b.ProviderInflight {
			return a.ProviderInflight < b.ProviderInflight
		}
	}
	if a.EffectiveCost != b.EffectiveCost {
		return a.EffectiveCost < b.EffectiveCost
	}
	return a.KeyID < b.KeyID
}

func (p *BandPicker) activeModelCooldowns(ctx context.Context, keys []domain.PlatformKey, model string) map[uint]struct{} {
	out := map[uint]struct{}{}
	model = strings.TrimSpace(model)
	if model == "" || len(keys) == 0 {
		return out
	}
	ids := make([]uint, 0, len(keys))
	for i := range keys {
		ids = append(ids, keys[i].ID)
	}
	var rows []domain.KeyModelCooldown
	_ = p.db.WithContext(ctx).Where("platform_key_id IN ? AND model = ? AND cooldown_until > ?", ids, model, time.Now()).Find(&rows).Error
	for _, row := range rows {
		out[row.PlatformKeyID] = struct{}{}
	}
	return out
}

// recentFailureStats loads the short-window circuit counters used by
// Aether-style schedulers. A recent success clears the guard; this prevents a
// burst of transient failures from repeatedly entering the request path.
func (p *BandPicker) recentFailureStats(ctx context.Context, keys []domain.PlatformKey, cfg domain.SchedulerSettings) map[uint]failureStats {
	stats := make(map[uint]failureStats)
	if cfg.FailureThreshold <= 0 || cfg.FailureWindowSec <= 0 || len(keys) == 0 {
		return stats
	}
	ids := make([]uint, 0, len(keys))
	for i := range keys {
		ids = append(ids, keys[i].ID)
	}
	since := time.Now().Add(-time.Duration(cfg.FailureWindowSec) * time.Second)
	var rows []failureStats
	p.db.WithContext(ctx).Model(&domain.RequestLog{}).
		Select("platform_key_id, SUM(CASE WHEN success = ? THEN 1 ELSE 0 END) AS failures, SUM(CASE WHEN success = ? THEN 1 ELSE 0 END) AS successes", false, true).
		Where("platform_key_id IN ? AND created_at >= ? AND in_flight = ?", ids, since, false).
		Where("failure_action IS NULL OR failure_action <> ?", "exclude_busy_resource").
		Group("platform_key_id").Scan(&rows)
	for _, row := range rows {
		stats[row.KeyID] = row
	}
	return stats
}

func hardReject(key *domain.PlatformKey, protocol, model string, filterByModels bool, excluded map[uint]struct{}) string {
	if _, skip := excluded[key.ID]; skip {
		return "excluded"
	}
	if key.Status != domain.StatusEnabled {
		return "key_disabled"
	}
	if key.Upstream == nil || key.Upstream.Status != domain.StatusEnabled {
		return "upstream_disabled"
	}
	if key.Upstream.CooldownUntil != nil && key.Upstream.CooldownUntil.After(time.Now()) {
		return "provider_cooldown"
	}
	if !key.Upstream.Supports(protocol) {
		return "protocol_mismatch"
	}
	if filterByModels && !key.SupportsModel(model) {
		return "model_not_supported"
	}
	now := time.Now()
	if key.CooldownUntil != nil && key.CooldownUntil.After(now) {
		return "cooldown"
	}
	if key.Upstream != nil && key.Upstream.LastBalance != nil && *key.Upstream.LastBalance <= 0 {
		return "low_balance"
	}
	switch key.HealthStatus {
	case domain.HealthDisabled:
		return "disabled"
	case domain.HealthDown:
		return "down"
	case domain.HealthLowBalance:
		return "low_balance"
	}
	return ""
}

func (p *BandPicker) window(ctx context.Context, keyID uint, model string, cfg domain.SchedulerSettings) metrics.Window {
	w := p.metrics.Snapshot(ctx, keyID, model)
	if w.Samples < cfg.MinSamples && model != "" {
		if alt := p.metrics.Snapshot(ctx, keyID, ""); alt.Samples > w.Samples {
			w = alt
		}
	}
	if w.Samples == 0 {
		p.backfillOnce(ctx, keyID, model, cfg)
		w = p.metrics.Snapshot(ctx, keyID, model)
		if w.Samples == 0 && model != "" {
			w = p.metrics.Snapshot(ctx, keyID, "")
		}
	}
	return w
}

func (p *BandPicker) backfillOnce(ctx context.Context, keyID uint, model string, cfg domain.SchedulerSettings) {
	token := fmt.Sprintf("%d:%s", keyID, model)
	if _, loaded := p.filled.LoadOrStore(token, time.Now()); loaded {
		return
	}
	limit := cfg.WindowMaxSamples
	if limit <= 0 {
		limit = 50
	}
	since := time.Now().Add(-time.Duration(cfg.WindowMinutes) * time.Minute)
	q := p.db.WithContext(ctx).Where("platform_key_id = ? AND created_at >= ? AND in_flight = ?", keyID, since, false)
	if strings.TrimSpace(model) != "" {
		q = q.Where("model = ?", model)
	}
	var logs []domain.RequestLog
	if err := q.Order("id DESC").Limit(limit).Find(&logs).Error; err != nil || len(logs) == 0 {
		if strings.TrimSpace(model) == "" {
			return
		}
		if err := p.db.WithContext(ctx).
			Where("platform_key_id = ? AND created_at >= ? AND in_flight = ?", keyID, since, false).
			Order("id DESC").Limit(limit).Find(&logs).Error; err != nil || len(logs) == 0 {
			return
		}
	}
	items := make([]metrics.Observation, 0, len(logs))
	for i := len(logs) - 1; i >= 0; i-- {
		lg := logs[i]
		items = append(items, metrics.Observation{
			KeyID:               keyID,
			Model:               lg.Model,
			Success:             lg.Success,
			InputTokens:         lg.InputTokens,
			CacheReadTokens:     lg.CacheReadTokens,
			CacheCreationTokens: lg.CacheCreationTokens,
			TTFTMs:              lg.TTFTMs,
			At:                  lg.CreatedAt,
		})
	}
	p.metrics.Backfill(ctx, items)
}

func (p *BandPicker) stickyEnabled(protocol string, cfg domain.SchedulerSettings) bool {
	switch protocol {
	case domain.ProtocolAnthropic:
		return cfg.StickyAnthropic
	case domain.ProtocolOpenAI:
		return cfg.StickyOpenAI
	default:
		return false
	}
}

func stickyRedisKey(session string) string {
	return "sum:sticky:" + session
}

func (p *BandPicker) stickyKey(ctx context.Context, req Request, cfg domain.SchedulerSettings) uint {
	if strings.TrimSpace(req.Session) == "" || !p.stickyEnabled(req.Protocol, cfg) {
		return 0
	}
	if p.rdb != nil {
		if raw, err := p.rdb.Get(ctx, stickyRedisKey(req.Session)).Result(); err == nil {
			var id uint
			_, _ = fmt.Sscanf(raw, "%d", &id)
			return id
		}
	}
	p.mu.RLock()
	ent, ok := p.sticky[req.Session]
	p.mu.RUnlock()
	if ok && ent.Expiry.After(time.Now()) {
		return ent.KeyID
	}
	return 0
}

func (p *BandPicker) SetSticky(ctx context.Context, protocol, session string, keyID uint) {
	cfg := p.Settings()
	if strings.TrimSpace(session) == "" || keyID == 0 || !p.stickyEnabled(protocol, cfg) {
		return
	}
	ttl := time.Duration(cfg.StickyTTLSec) * time.Second
	if p.rdb != nil {
		_ = p.rdb.Set(ctx, stickyRedisKey(session), fmt.Sprintf("%d", keyID), ttl).Err()
	}
	p.mu.Lock()
	p.sticky[session] = stickyEntry{KeyID: keyID, Expiry: time.Now().Add(ttl)}
	p.mu.Unlock()
}

func (p *BandPicker) Cooldown(ctx context.Context, keyID uint) {
	cfg := p.Settings()
	until := time.Now().Add(time.Duration(cfg.CooldownSec) * time.Second)
	_ = p.db.WithContext(ctx).Model(&domain.PlatformKey{}).Where("id = ?", keyID).Updates(map[string]any{
		"cooldown_until": until,
		"health_status":  domain.HealthCooldown,
	}).Error
}

func (p *BandPicker) MarkLowBalance(ctx context.Context, keyID uint) {
	var k domain.PlatformKey
	if err := p.db.WithContext(ctx).Select("id", "upstream_id").First(&k, keyID).Error; err != nil {
		return
	}
	zero := 0.0
	now := time.Now()
	_ = p.db.WithContext(ctx).Model(&domain.Upstream{}).Where("id = ?", k.UpstreamID).Updates(map[string]any{
		"last_balance":    zero,
		"last_balance_at": now,
		"health_status":   domain.HealthLowBalance,
		"last_error":      "upstream quota exhausted",
	}).Error
	_ = p.db.WithContext(ctx).Model(&domain.PlatformKey{}).Where("upstream_id = ?", k.UpstreamID).
		Update("health_status", domain.HealthLowBalance).Error
}

func (p *BandPicker) resourceSnapshot(keyID, providerID uint) (rpm, keyInflight, providerInflight int) {
	now := time.Now()
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	window := p.runtime.rpm[keyID]
	cut := now.Add(-time.Minute)
	keep := window[:0]
	for _, at := range window {
		if at.After(cut) {
			keep = append(keep, at)
		}
	}
	p.runtime.rpm[keyID] = keep
	return len(keep), p.runtime.keyInflight[keyID], p.runtime.providerInflight[providerID]
}

func (p *BandPicker) TryAcquire(key *domain.PlatformKey, up *domain.Upstream) (bool, string) {
	if key == nil || up == nil {
		return false, "key"
	}
	now := time.Now()
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	cut := now.Add(-time.Minute)
	window := p.runtime.rpm[key.ID]
	keep := window[:0]
	for _, at := range window {
		if at.After(cut) {
			keep = append(keep, at)
		}
	}
	if key.RPMLimit > 0 && len(keep) >= key.RPMLimit {
		p.runtime.rpm[key.ID] = keep
		return false, "key"
	}
	if key.MaxConcurrency > 0 && p.runtime.keyInflight[key.ID] >= key.MaxConcurrency {
		return false, "key"
	}
	if up.Concurrency > 0 && p.runtime.providerInflight[up.ID] >= up.Concurrency {
		return false, "provider"
	}
	p.runtime.rpm[key.ID] = append(keep, now)
	p.runtime.keyInflight[key.ID]++
	p.runtime.providerInflight[up.ID]++
	return true, ""
}

func (p *BandPicker) Release(key *domain.PlatformKey, up *domain.Upstream) {
	if key == nil || up == nil {
		return
	}
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	if n := p.runtime.keyInflight[key.ID] - 1; n > 0 {
		p.runtime.keyInflight[key.ID] = n
	} else {
		delete(p.runtime.keyInflight, key.ID)
	}
	if n := p.runtime.providerInflight[up.ID] - 1; n > 0 {
		p.runtime.providerInflight[up.ID] = n
	} else {
		delete(p.runtime.providerInflight, up.ID)
	}
}

func (p *BandPicker) CooldownProvider(ctx context.Context, upstreamID uint, reason string) {
	until := time.Now().Add(time.Duration(p.Settings().CooldownSec) * time.Second)
	_ = p.db.WithContext(ctx).Model(&domain.Upstream{}).Where("id = ?", upstreamID).Updates(map[string]any{
		"cooldown_until": until, "health_status": domain.HealthCooldown, "last_error": reason,
	}).Error
}

func (p *BandPicker) CooldownKeyModel(ctx context.Context, keyID uint, model, reason string, status int) {
	model = strings.TrimSpace(model)
	if keyID == 0 || model == "" {
		return
	}
	row := domain.KeyModelCooldown{PlatformKeyID: keyID, Model: model}
	until := time.Now().Add(time.Duration(p.Settings().FailureWindowSec) * time.Second)
	_ = p.db.WithContext(ctx).Where("platform_key_id = ? AND model = ?", keyID, model).
		Assign(domain.KeyModelCooldown{CooldownUntil: until, Reason: reason, StatusCode: status}).FirstOrCreate(&row).Error
}

func (p *BandPicker) RecordProviderSuccess(ctx context.Context, upstreamID uint) {
	if upstreamID == 0 {
		return
	}
	_ = p.db.WithContext(ctx).Model(&domain.Upstream{}).Where("id = ?", upstreamID).Updates(map[string]any{
		"health_status": domain.HealthHealthy, "cooldown_until": nil, "last_error": "",
	}).Error
}

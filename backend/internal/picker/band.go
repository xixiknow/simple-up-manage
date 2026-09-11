package picker

import (
	"context"
	"fmt"
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
}

type stickyEntry struct {
	KeyID  uint
	Expiry time.Time
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

	out := make([]Candidate, 0, len(keys))
	var eligible []int
	for i := range keys {
		c := p.inspect(ctx, &keys[i], req, cfg, excluded)
		out = append(out, c)
		if c.Eligible {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) == 0 {
		return out, nil
	}

	if stickyID := p.stickyKey(ctx, req, cfg); stickyID > 0 {
		for i := range eligible {
			idx := eligible[i]
			if out[idx].KeyID == stickyID {
				out[idx].Selected = true
				out[idx].InBand = true
				return out, nil
			}
		}
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
	sort.SliceStable(band, func(i, j int) bool {
		a, b := out[band[i]], out[band[j]]
		if a.EffectiveCost != b.EffectiveCost {
			return a.EffectiveCost < b.EffectiveCost
		}
		return a.KeyID < b.KeyID
	})
	if len(band) > 0 {
		out[band[0]].Selected = true
	}
	return out, nil
}

func (p *BandPicker) inspect(ctx context.Context, key *domain.PlatformKey, req Request, cfg domain.SchedulerSettings, excluded map[uint]struct{}) Candidate {
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
	case domain.HealthCooldown:
		return "cooldown"
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
	}).Error
	_ = p.db.WithContext(ctx).Model(&domain.PlatformKey{}).Where("upstream_id = ?", k.UpstreamID).
		Update("health_status", domain.HealthLowBalance).Error
}

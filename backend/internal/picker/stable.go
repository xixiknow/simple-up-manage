package picker

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/redis/go-redis/v9"
	"simple-up-manage/internal/domain"
)

type stableBinding struct {
	Key        uint  `json:"key"`
	Expires    int64 `json:"expires"`
	Challenger uint  `json:"challenger"`
	Since      int64 `json:"since"`
}

type responseSession struct {
	Session string
	Expires int64
}
type stableState struct {
	Bindings  map[string]stableBinding   `json:"bindings"`
	Responses map[string]responseSession `json:"responses"`
	Explored  map[uint]int64             `json:"explored"`
	Expires   int64                      `json:"expires"`
}

func (s *stableState) prepare(now int64) {
	if s.Expires <= now {
		*s = stableState{}
	}
	if s.Bindings == nil {
		s.Bindings = map[string]stableBinding{}
	}
	if s.Responses == nil {
		s.Responses = map[string]responseSession{}
	}
	if s.Explored == nil {
		s.Explored = map[uint]int64{}
	}
	for k, v := range s.Bindings {
		if v.Expires <= now {
			delete(s.Bindings, k)
		}
	}
	for k, v := range s.Responses {
		if v.Expires <= now {
			delete(s.Responses, k)
		}
	}
}

func routeScope(req Request) string {
	var ids []uint
	for id := range req.AllowKeys {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	b, _ := json.Marshal([]any{req.ConsumerID, req.Protocol, req.Model, req.Path, req.Stream, req.AllowKeys != nil, ids})
	return sessionDigest(string(b))
}

// WATCH serializes decisions across processes. Preview reads the same state but
// never advances counters, confirmations or bindings.
func (p *BandPicker) withStableState(ctx context.Context, scope string, mutate bool, fn func(*stableState)) error {
	p.stableMu.Lock()
	defer p.stableMu.Unlock()
	now := time.Now().UnixMilli()
	ttl := time.Duration(max(p.Settings().StickyTTLSec, 900)) * time.Second
	if p.rdb != nil {
		redisCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		key := "sum:v2:stable:" + scope
		var conflicted bool
		for i := 0; i < 5; i++ {
			var cached stableState
			err := p.rdb.Watch(redisCtx, func(tx *redis.Tx) error {
				var state stableState
				raw, err := tx.Get(redisCtx, key).Bytes()
				if err != nil && err != redis.Nil {
					return err
				}
				if len(raw) > 0 {
					if err = json.Unmarshal(raw, &state); err != nil {
						return err
					}
				}
				state.prepare(now)
				fn(&state)
				cached = state
				if !mutate {
					return nil
				}
				state.Expires = now + ttl.Milliseconds()
				cached = state
				b, err := json.Marshal(state)
				if err != nil {
					return err
				}
				_, err = tx.TxPipelined(redisCtx, func(pipe redis.Pipeliner) error { pipe.Set(redisCtx, key, b, ttl); return nil })
				return err
			}, key)
			if err == nil {
				if mutate {
					p.stableStates[scope] = cached
				}
				return nil
			}
			if err == redis.TxFailedErr {
				conflicted = true
				continue
			}
			conflicted = false
			break
		}
		if conflicted {
			return redis.TxFailedErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	state := p.stableStates[scope]
	// Maps need copying: a read-only preview must not alter cached state.
	b, _ := json.Marshal(state)
	state = stableState{}
	_ = json.Unmarshal(b, &state)
	state.prepare(now)
	fn(&state)
	if mutate {
		state.Expires = now + ttl.Milliseconds()
		p.stableStates[scope] = state
		for k, s := range p.stableStates {
			if s.Expires <= now {
				delete(p.stableStates, k)
			}
		}
	}
	return nil
}

func (p *BandPicker) attemptCandidates(ctx context.Context, req Request, cands []Candidate) (map[uint][]domain.RequestAttempt, error) {
	var ids []uint
	for _, c := range cands {
		if c.Eligible {
			ids = append(ids, c.KeyID)
		}
	}
	byKey := map[uint][]domain.RequestAttempt{}
	if len(ids) == 0 {
		return byKey, nil
	}
	cfg := p.Settings()
	q := p.db.WithContext(ctx).Model(&domain.RequestAttempt{}).
		Select("*, ROW_NUMBER() OVER (PARTITION BY platform_key_id ORDER BY completed_at DESC, id DESC) AS sample_rank").
		Where("platform_key_id IN ? AND protocol = ? AND model = ? AND path = ? AND stream = ? AND stats_version = ? AND completed_at > ? AND result IN ?", ids, req.Protocol, req.Model, req.Path, req.Stream, domain.AttemptStatsVersion, time.Now().Add(-time.Duration(cfg.WindowMinutes)*time.Minute), []string{"success", "upstream_failure"})
	var rows []domain.RequestAttempt
	if err := p.db.WithContext(ctx).Table("(?) AS recent", q).Where("sample_rank <= ?", cfg.WindowMaxSamples).Order("completed_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		byKey[r.PlatformKeyID] = append(byKey[r.PlatformKeyID], r)
	}
	for i := range cands {
		c := &cands[i]
		if !c.Eligible {
			continue
		}
		var ok int
		var lats []int
		var in, cr, cc int64
		for _, r := range byKey[c.KeyID] {
			c.LastSampleAt = max(c.LastSampleAt, r.CompletedAt.UnixMilli())
			if r.Result != "success" {
				continue
			}
			ok++
			if r.TTFTMs > 0 {
				lats = append(lats, r.TTFTMs)
			}
			in += r.InputTokens
			cr += r.CacheReadTokens
			cc += r.CacheCreationTokens
		}
		c.Samples = len(byKey[c.KeyID])
		c.LatencySamples = len(lats)
		if c.Samples > 0 {
			c.SuccessRate = float64(ok) / float64(c.Samples)
		}
		c.Quality = (float64(ok) + 3.5) / float64(c.Samples+5)
		if len(lats) > 0 {
			sort.Ints(lats)
			c.TTFTp50 = lats[(len(lats)-1)/2]
		}
		if in+cr+cc > 0 {
			c.CacheRate = float64(cr) / float64(in+cr+cc)
		}
		c.EffectiveCost = effectiveCost(c.Rate, c.CacheRate)
	}
	return byKey, nil
}

func candidatePool(cands []Candidate, minSamples int) ([]int, bool) {
	bestSuccess := 0.0
	for _, c := range cands {
		if c.Eligible && c.Samples >= minSamples {
			bestSuccess = math.Max(bestSuccess, c.SuccessRate)
		}
	}
	var pool []int
	for i := range cands {
		c := &cands[i]
		c.Reliable = c.Eligible && c.Samples >= minSamples && c.SuccessRate >= 0.9 && c.SuccessRate+0.05+1e-9 >= bestSuccess
		if c.Reliable {
			pool = append(pool, i)
		}
	}
	degraded := len(pool) == 0
	if degraded {
		best := -1.0
		for _, c := range cands {
			if c.Eligible {
				best = math.Max(best, c.Quality)
			}
		}
		for i, c := range cands {
			if c.Eligible && c.Quality+1e-9 >= best {
				pool = append(pool, i)
			}
		}
	}
	return pool, degraded
}

func fastestCandidate(cands []Candidate, pool []int, minSamples int) int {
	fastest := math.MaxInt
	for _, i := range pool {
		c := cands[i]
		if c.LatencySamples >= minSamples {
			fastest = min(fastest, c.TTFTp50)
		}
	}
	chosen := -1
	for _, i := range pool {
		c := cands[i]
		if fastest != math.MaxInt && (c.LatencySamples < minSamples || float64(c.TTFTp50-fastest) > math.Max(500, float64(fastest)*.1)) {
			continue
		}
		if chosen < 0 || c.EffectiveCost < cands[chosen].EffectiveCost || (c.EffectiveCost == cands[chosen].EffectiveCost && c.KeyID < cands[chosen].KeyID) {
			chosen = i
		}
	}
	return chosen
}

func (p *BandPicker) stableDecision(ctx context.Context, req Request, mutate bool) (*domain.PlatformKey, *domain.Upstream, Decision, error) {
	cands, err := p.evaluate(ctx, req)
	d := Decision{Scope: routeScope(req), SessionSource: req.SessionSource, SessionHash: req.Session}
	if err != nil {
		return nil, nil, d, err
	}
	for _, c := range cands {
		if c.Selected && c.Recovery {
			d.Candidates = cands
			d.SelectedKeyID = c.KeyID
			d.Reason = "recovery_validation"
			d.Exploration = true
			return c.Key, c.Upstream, d, nil
		}
	}
	rows, err := p.attemptCandidates(ctx, req, cands)
	if err != nil {
		return nil, nil, d, err
	}
	cfg := p.Settings()
	pool, degraded := candidatePool(cands, cfg.MinSamples)
	for _, i := range pool {
		cands[i].InBand = true
	}
	d.Candidates = cands
	d.Degraded = degraded
	best := fastestCandidate(cands, pool, cfg.MinSamples)
	if best < 0 {
		return nil, nil, d, ErrNoUpstream
	}
	now := time.Now().UnixMilli()
	chosen := best
	err = p.withStableState(ctx, d.Scope, mutate, func(state *stableState) {
		chosen = best
		d.Exploration = false
		d.Reason = "initial_selection"
		binding, hasBinding := state.Bindings[req.Session]
		d.PreviousKeyID = binding.Key
		current := -1
		for i, c := range cands {
			if c.KeyID == binding.Key {
				current = i
				break
			}
		}
		if hasBinding && current >= 0 && cands[current].Eligible {
			c, b := cands[current], cands[best]
			if c.Reliable || (degraded && c.Quality+1e-9 >= b.Quality) {
				chosen = current
				d.Reason = "reuse"
				improved := best != current && b.LatencySamples >= cfg.MinSamples && c.LatencySamples >= cfg.MinSamples && b.SuccessRate >= c.SuccessRate && c.TTFTp50-b.TTFTp50 >= cfg.SwitchImprovementMs && float64(c.TTFTp50-b.TTFTp50) >= float64(c.TTFTp50)*cfg.SwitchImprovementRatio
				if improved {
					if binding.Challenger != b.KeyID {
						binding.Challenger = b.KeyID
						binding.Since = now
					}
					newSamples := 0
					for _, r := range rows[b.KeyID] {
						if r.Result == "success" && r.TTFTMs > 0 && r.CompletedAt.UnixMilli() > binding.Since {
							newSamples++
						}
					}
					if now-binding.Since >= int64(cfg.SwitchConfirmSec)*1000 && newSamples >= 3 {
						chosen = best
						d.Reason = "latency_improved"
					}
				} else {
					binding.Challenger = 0
					binding.Since = 0
				}
				if mutate {
					state.Bindings[req.Session] = binding
				}
			} else {
				d.Reason = "reliability_dropped"
			}
		} else if hasBinding {
			d.Reason = "binding_unavailable"
			if current >= 0 {
				d.Reason = cands[current].SkipReason
			}
		}
		if len(req.Exclude)+len(req.ExcludeProviders)+len(req.ExcludeKeyModels) > 0 {
			d.Reason = "failover"
			return
		}
		if (req.Session == "" || !hasBinding) && cfg.ExplorationRatio > 0 {
			if req.ExplorationSlot {
				explore := -1
				for i, c := range cands {
					if !c.Eligible || i == chosen || now-state.Explored[c.KeyID] < 60000 {
						continue
					}
					if explore < 0 || c.LastSampleAt < cands[explore].LastSampleAt || (c.LastSampleAt == cands[explore].LastSampleAt && c.KeyID < cands[explore].KeyID) {
						explore = i
					}
				}
				if explore >= 0 {
					chosen = explore
					d.Exploration = true
					d.Reason = "exploration"
					if mutate {
						state.Explored[cands[chosen].KeyID] = now
					}
				}
			}
		}
	})
	if err != nil {
		return nil, nil, d, err
	}
	cands[chosen].Selected = true
	cands[chosen].DecisionReason = d.Reason
	d.SelectedKeyID = cands[chosen].KeyID
	d.Candidates = cands
	return cands[chosen].Key, cands[chosen].Upstream, d, nil
}

func (p *BandPicker) PickDecision(ctx context.Context, req Request) (*domain.PlatformKey, *domain.Upstream, Decision, error) {
	var prepErr error
	req, prepErr = p.prepareBudget(ctx, req, true)
	if prepErr != nil {
		return nil, nil, Decision{}, prepErr
	}
	if p.Settings().RankingMode == "stable_latency" {
		return p.stableDecision(ctx, req, true)
	}
	cands, err := p.evaluate(ctx, req)
	d := Decision{Reason: "legacy_ranking", Scope: routeScope(req), SessionHash: req.Session, SessionSource: req.SessionSource, Candidates: cands}
	if err != nil {
		return nil, nil, d, err
	}
	for _, c := range cands {
		if c.Selected {
			d.SelectedKeyID = c.KeyID
			if c.Recovery {
				d.Reason = "recovery_validation"
				d.Exploration = true
			}
			return c.Key, c.Upstream, d, nil
		}
	}
	return nil, nil, d, ErrNoUpstream
}

func (p *BandPicker) CommitSuccess(ctx context.Context, req Request, d Decision, key uint, responseID string) {
	if d.Reason == "recovery_validation" {
		return
	}
	if p.Settings().RankingMode != "stable_latency" {
		p.SetSticky(ctx, req.Protocol, req.Session, key)
		return
	}
	_ = p.withStableState(ctx, routeScope(req), true, func(state *stableState) {
		now := time.Now().UnixMilli()
		ttl := 900
		if req.Session != "" {
			ttl = p.Settings().StickyTTLSec
		}
		if !d.Exploration || req.Session != "" {
			old, exists := state.Bindings[req.Session]
			// A late completion cannot overwrite a newer successful decision.
			if !exists || old.Key == key || old.Key == d.PreviousKeyID {
				if old.Key != key {
					old = stableBinding{Key: key}
				}
				old.Expires = now + int64(ttl)*1000
				state.Bindings[req.Session] = old
			}
		}
		if responseID != "" {
			session := req.Session
			if session == "" {
				session = sessionDigest("response:" + responseID)
				state.Bindings[session] = stableBinding{Key: key, Expires: now + int64(p.Settings().StickyTTLSec)*1000}
			}
			state.Responses[sessionDigest(responseID)] = responseSession{session, now + int64(p.Settings().StickyTTLSec)*1000}
		}
	})
}

func (p *BandPicker) ResolvePrevious(ctx context.Context, req Request, id string) string {
	var session string
	_ = p.withStableState(ctx, routeScope(req), false, func(state *stableState) { session = state.Responses[sessionDigest(id)].Session })
	return session
}

var _ DecisionPicker = (*BandPicker)(nil)

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Observation struct {
	KeyID               uint      `json:"key_id"`
	Model               string    `json:"model"`
	Success             bool      `json:"success"`
	InputTokens         int64     `json:"input_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	TTFTMs              int       `json:"ttft_ms"`
	At                  time.Time `json:"at"`
}

type Window struct {
	SuccessRate float64 `json:"success_rate"`
	CacheRate   float64 `json:"cache_rate"`
	HasCache    bool    `json:"has_cache"`
	TTFTp50     int     `json:"ttft_p50"`
	HasLatency  bool    `json:"has_latency"`
	Samples     int     `json:"samples"`
}

type Store struct {
	rdb        *redis.Client
	mu         sync.Mutex
	mem        map[string][]Observation
	maxSamples int
	window     time.Duration
}

func NewStore(rdb *redis.Client, window time.Duration, maxSamples int) *Store {
	if window <= 0 {
		window = 15 * time.Minute
	}
	if maxSamples <= 0 {
		maxSamples = 50
	}
	return &Store{
		rdb:        rdb,
		mem:        map[string][]Observation{},
		maxSamples: maxSamples,
		window:     window,
	}
}

func (s *Store) Configure(window time.Duration, maxSamples int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if window > 0 {
		s.window = window
	}
	if maxSamples > 0 {
		s.maxSamples = maxSamples
	}
}

func (s *Store) config() (time.Duration, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.window, s.maxSamples
}

func bucket(keyID uint, model string) string {
	if model == "" {
		return fmt.Sprintf("%d:_", keyID)
	}
	return fmt.Sprintf("%d:%s", keyID, model)
}

func redisKey(keyID uint, model string) string {
	return "sum:w:" + bucket(keyID, model)
}

func (s *Store) Observe(ctx context.Context, obs Observation) {
	window, maxSamples := s.config()
	if obs.At.IsZero() {
		obs.At = time.Now()
	}
	s.pushMem(bucket(obs.KeyID, obs.Model), obs)
	if obs.Model != "" {
		s.pushMem(bucket(obs.KeyID, ""), obs)
	}
	if s.rdb == nil {
		return
	}
	raw, err := json.Marshal(obs)
	if err != nil {
		return
	}
	pipe := s.rdb.Pipeline()
	k1 := redisKey(obs.KeyID, obs.Model)
	pipe.LPush(ctx, k1, raw)
	pipe.LTrim(ctx, k1, 0, int64(maxSamples-1))
	pipe.Expire(ctx, k1, window*4)
	if obs.Model != "" {
		k2 := redisKey(obs.KeyID, "")
		pipe.LPush(ctx, k2, raw)
		pipe.LTrim(ctx, k2, 0, int64(maxSamples-1))
		pipe.Expire(ctx, k2, window*4)
	}
	_, _ = pipe.Exec(ctx)
}

func (s *Store) Backfill(ctx context.Context, items []Observation) {
	for _, obs := range items {
		s.Observe(ctx, obs)
	}
}

func (s *Store) Snapshot(ctx context.Context, keyID uint, model string) Window {
	if s.rdb != nil {
		if w, ok := s.snapshotRedis(ctx, keyID, model); ok {
			return w
		}
	}
	window, maxSamples := s.config()
	return summarize(s.memCopy(bucket(keyID, model)), window, maxSamples)
}

func (s *Store) pushMem(key string, obs Observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem[key] = append(s.mem[key], obs)
	if len(s.mem[key]) > s.maxSamples*2 {
		s.mem[key] = s.mem[key][len(s.mem[key])-s.maxSamples:]
	}
}

func (s *Store) memCopy(key string) []Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.mem[key]
	out := make([]Observation, len(src))
	copy(out, src)
	return out
}

func (s *Store) snapshotRedis(ctx context.Context, keyID uint, model string) (Window, bool) {
	window, maxSamples := s.config()
	raws, err := s.rdb.LRange(ctx, redisKey(keyID, model), 0, int64(maxSamples-1)).Result()
	if err != nil || len(raws) == 0 {
		return Window{}, false
	}
	items := make([]Observation, 0, len(raws))
	for _, raw := range raws {
		var obs Observation
		if json.Unmarshal([]byte(raw), &obs) == nil {
			items = append(items, obs)
		}
	}
	return summarize(items, window, maxSamples), true
}

func summarize(items []Observation, window time.Duration, maxSamples int) Window {
	cut := time.Now().Add(-window)
	filtered := make([]Observation, 0, len(items))
	for _, it := range items {
		if it.At.IsZero() || it.At.After(cut) {
			filtered = append(filtered, it)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].At.After(filtered[j].At) })
	if len(filtered) > maxSamples {
		filtered = filtered[:maxSamples]
	}
	if len(filtered) == 0 {
		return Window{}
	}
	var okN int
	var in, cr, cc int64
	ttfts := make([]int, 0, len(filtered))
	for _, it := range filtered {
		if it.Success {
			okN++
		}
		in += it.InputTokens
		cr += it.CacheReadTokens
		cc += it.CacheCreationTokens
		if it.Success && it.TTFTMs > 0 {
			ttfts = append(ttfts, it.TTFTMs)
		}
	}
	w := Window{
		SuccessRate: float64(okN) / float64(len(filtered)),
		Samples:     len(filtered),
	}
	den := float64(in + cr + cc)
	if den > 0 {
		w.CacheRate = float64(cr) / den
		w.HasCache = true
	}
	if len(ttfts) > 0 {
		sort.Ints(ttfts)
		w.TTFTp50 = ttfts[(len(ttfts)-1)/2]
		w.HasLatency = true
	}
	return w
}

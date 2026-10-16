package ops

import (
	"context"
	"net/http"
	"sort"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"
)

// KeyWindowStats is the list-side observation window for channel scoring.
type KeyWindowStats struct {
	Samples    int
	Success    float64
	Cache      float64
	HasCache   bool
	LatencyP50 int
	HasLatency bool
}

type windowEvent struct {
	at         time.Time
	success    bool
	latency    int
	in, cr, cc int64
	hasTok     bool
}

// KeyWindowStatsMap aggregates request logs plus light/deep probes per key.
func (s *Service) KeyWindowStatsMap(ctx context.Context, keyIDs []uint, window time.Duration, maxSamples int) map[uint]KeyWindowStats {
	out := make(map[uint]KeyWindowStats, len(keyIDs))
	if len(keyIDs) == 0 || window <= 0 {
		return out
	}
	if maxSamples <= 0 {
		maxSamples = 50
	}
	since := time.Now().Add(-window)
	byKey := make(map[uint][]windowEvent, len(keyIDs))

	var reqs []domain.RequestLog
	_ = s.DB.WithContext(ctx).
		Select("platform_key_id, protocol, success, failure_action, ttft_ms, duration_ms, input_tokens, cache_read_tokens, cache_creation_tokens, created_at").
		Where("platform_key_id IN ? AND created_at >= ? AND in_flight = ? AND (success = ? OR failure_action IS NULL OR failure_action NOT IN ?)", keyIDs, since, false, true, domain.AvailabilityFailureActions()).
		Find(&reqs).Error
	for _, r := range reqs {
		if r.PlatformKeyID == nil {
			continue
		}
		lat := r.TTFTMs
		if lat <= 0 {
			lat = r.DurationMs
		}
		// RequestLog keeps protocol-native usage (OpenAI prompt_tokens include
		// cached tokens); normalize before aggregating so the cached tokens are
		// not counted twice in the cache-rate denominator.
		n := upstream.NormalizeUsage(r.Protocol, upstream.TokenUsage{InputTokens: r.InputTokens, CacheReadTokens: r.CacheReadTokens, CacheCreationTokens: r.CacheCreationTokens})
		tok := n.InputTokens + n.CacheReadTokens + n.CacheCreationTokens
		byKey[*r.PlatformKeyID] = append(byKey[*r.PlatformKeyID], windowEvent{
			at: r.CreatedAt, success: r.Success, latency: lat,
			in: n.InputTokens, cr: n.CacheReadTokens, cc: n.CacheCreationTokens,
			hasTok: tok > 0,
		})
	}

	var probes []domain.ProbeLog
	_ = s.DB.WithContext(ctx).
		Select("platform_key_id, success, status_code, error_message, latency_ms, created_at").
		Where("platform_key_id IN ? AND created_at >= ? AND kind IN ?", keyIDs, since, []string{domain.ProbeLight, domain.ProbeDeep}).
		Find(&probes).Error
	for _, p := range probes {
		if !p.Success && availabilityProbeFailure(p.StatusCode, p.ErrorMessage) {
			// Arrears/credential probe failures are availability state, not
			// quality: leave them out so a recharged key's console score
			// recovers immediately.
			continue
		}
		byKey[p.PlatformKeyID] = append(byKey[p.PlatformKeyID], windowEvent{
			at: p.CreatedAt, success: p.Success, latency: p.LatencyMs,
		})
	}

	for _, id := range keyIDs {
		events := byKey[id]
		if len(events) == 0 {
			continue
		}
		sort.Slice(events, func(i, j int) bool { return events[i].at.After(events[j].at) })
		if len(events) > maxSamples {
			events = events[:maxSamples]
		}
		out[id] = summarizeWindow(events)
	}
	return out
}

// availabilityProbeFailure reports whether a failed probe row is a billing or
// credential state rather than a quality signal: an explicit 402, or a
// quota-classified error body on the auth/quota status codes. Unparseable
// bodies stay counted (conservative: timeouts and 5xx keep punishing).
func availabilityProbeFailure(statusCode int, errorMessage string) bool {
	if statusCode == http.StatusPaymentRequired {
		return true
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests {
		return upstream.ParseError([]byte(errorMessage)).Kind() == "quota"
	}
	return false
}

func summarizeWindow(events []windowEvent) KeyWindowStats {
	var okN int
	var in, cr, cc int64
	lats := make([]int, 0, len(events))
	for _, e := range events {
		if e.success {
			okN++
		}
		if e.latency > 0 {
			lats = append(lats, e.latency)
		}
		if e.hasTok {
			in += e.in
			cr += e.cr
			cc += e.cc
		}
	}
	st := KeyWindowStats{
		Samples: len(events),
		Success: float64(okN) / float64(len(events)),
	}
	den := float64(in + cr + cc)
	if den > 0 {
		st.Cache = float64(cr) / den
		st.HasCache = true
	}
	if len(lats) > 0 {
		sort.Ints(lats)
		st.LatencyP50 = lats[(len(lats)-1)/2]
		st.HasLatency = true
	}
	return st
}

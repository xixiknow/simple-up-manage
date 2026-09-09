package picker

import "simple-up-manage/internal/domain"

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ScoreInputs are the observed terms for qualityScore. Missing optional
// terms (no cache traffic, no latency samples) are omitted and remaining
// weights are renormalized.
type ScoreInputs struct {
	Success    float64
	Samples    int
	Cache      float64
	HasCache   bool
	LatencyP50 int
	HasLatency bool
}

func qualityScore(success, cache float64, ttftP50 int, samples int, cfg domain.SchedulerSettings) float64 {
	return Quality(ScoreInputs{
		Success:    success,
		Samples:    samples,
		Cache:      cache,
		HasCache:   true,
		LatencyP50: ttftP50,
		HasLatency: true,
	}, cfg)
}

// Quality is the scheduler quality in [0,1]. Terms without observations are
// dropped and the remaining weights are renormalized to sum to 1.
func Quality(in ScoreInputs, cfg domain.SchedulerSettings) float64 {
	success := in.Success
	if in.Samples < cfg.MinSamples {
		success = cfg.PriorSuccess
	}
	var num, den float64
	if cfg.WeightSuccess > 0 {
		num += cfg.WeightSuccess * clamp01(success)
		den += cfg.WeightSuccess
	}
	if in.HasLatency && cfg.WeightTTFT > 0 {
		lat := 1.0
		if cfg.TTFTCapMs > 0 {
			lat = 1 - clamp01(float64(in.LatencyP50)/float64(cfg.TTFTCapMs))
		}
		num += cfg.WeightTTFT * lat
		den += cfg.WeightTTFT
	}
	if in.HasCache && cfg.WeightCache > 0 {
		num += cfg.WeightCache * clamp01(in.Cache)
		den += cfg.WeightCache
	}
	if den <= 0 {
		return 0
	}
	return num / den
}

func effectiveCost(rate, cacheRate float64) float64 {
	if rate < 0 {
		rate = 0
	}
	return rate * (1 - 0.5*clamp01(cacheRate))
}

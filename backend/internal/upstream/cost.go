package upstream

import "math"

const (
	cacheReadFactor  = 0.1
	cacheWriteFactor = 1.25
)

// EstimateCostUSD prices a request from catalog $/1M-token rates.
// Upstream-reported CostUSD is left untouched by callers; this only fills gaps.
//
// Cache accounting:
//   - OpenAI-style: input_tokens already includes cached_tokens → subtract before
//     billing the uncached remainder at full input price.
//   - Anthropic-style: input_tokens is uncached → keep it, add cache tokens.
// Cache read is billed at 10% of input; cache write at 125% (Anthropic / common
// aggregator convention). rate is the platform key multiplier (0.08 = 8%).
func EstimateCostUSD(inputPerMillion, outputPerMillion, rate float64, u TokenUsage) *float64 {
	if inputPerMillion == 0 && outputPerMillion == 0 {
		return nil
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 {
		return nil
	}
	if rate <= 0 {
		rate = 1
	}
	uncached := u.InputTokens
	if u.CacheReadTokens > 0 && uncached >= u.CacheReadTokens {
		uncached -= u.CacheReadTokens
	}
	usd := (float64(uncached)*inputPerMillion +
		float64(u.CacheReadTokens)*inputPerMillion*cacheReadFactor +
		float64(u.CacheCreationTokens)*inputPerMillion*cacheWriteFactor +
		float64(u.OutputTokens)*outputPerMillion) / 1e6 * rate
	usd = math.Round(usd*1e8) / 1e8
	return &usd
}

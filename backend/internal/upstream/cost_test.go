package upstream

import "testing"

func TestEstimateCostUSDOpenAICacheIncluded(t *testing.T) {
	u := TokenUsage{InputTokens: 4387, OutputTokens: 14, CacheReadTokens: 3840}
	got := EstimateCostUSD(4, 20, 0.08, u)
	if got == nil {
		t.Fatal("expected cost")
	}
	// uncached 547 * 4 + cache 3840 * 4 * 0.1 + out 14 * 20 = 4004
	// 4004 / 1e6 * 0.08 = 0.00032032
	if *got != 0.00032032 {
		t.Fatalf("got %v", *got)
	}
}

func TestEstimateCostUSDAnthropicCacheSeparate(t *testing.T) {
	u := TokenUsage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 80, CacheCreationTokens: 40}
	got := EstimateCostUSD(3, 15, 1, u)
	if got == nil {
		t.Fatal("expected cost")
	}
	// input 20*3 + cache read 80*3*0.1 + cache write 40*3*1.25 + out 10*15
	// = 60 + 24 + 150 + 150 = 384 / 1e6
	if *got != 0.000384 {
		t.Fatalf("got %v", *got)
	}
}

func TestEstimateCostUSDNilWhenNoTokensOrPrice(t *testing.T) {
	if EstimateCostUSD(0, 0, 1, TokenUsage{InputTokens: 10}) != nil {
		t.Fatal("zero price")
	}
	if EstimateCostUSD(3, 15, 1, TokenUsage{}) != nil {
		t.Fatal("zero tokens")
	}
}

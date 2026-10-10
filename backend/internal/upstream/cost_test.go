package upstream

import (
	"testing"

	"simple-up-manage/internal/domain"
)

func TestEstimateCostUSDOpenAICacheIncluded(t *testing.T) {
	u := TokenUsage{InputTokens: 4387, OutputTokens: 14, CacheReadTokens: 3840}
	got := EstimateCostUSD(domain.ProtocolOpenAI, 4, 20, 0.08, u)
	if got == nil {
		t.Fatal("expected cost")
	}
	// uncached 547 * 4 + cache 3840 * 4 * 0.1 + out 14 * 20 = 4004
	// 4004 / 1e6 * 0.08 = 0.00032032
	if *got != 0.00032032 {
		t.Fatalf("got %v", *got)
	}
}

func TestEstimateCostUSDOpenAISubtractsCacheWrite(t *testing.T) {
	u := TokenUsage{InputTokens: 8, CacheReadTokens: 5, CacheCreationTokens: 1}
	got := EstimateCostUSD(domain.ProtocolOpenAI, 4, 20, 1, u)
	if got == nil {
		t.Fatal("expected cost")
	}
	// uncached (8-5-1) * 4 + read 5 * (4*0.1) + write 1 * (4*1.25)
	// = 8 + 2 + 5 = 15 / 1e6
	if *got != 0.000015 {
		t.Fatalf("got %v", *got)
	}
}

func TestEstimateCostUSDAnthropicCacheSeparate(t *testing.T) {
	u := TokenUsage{InputTokens: 20, OutputTokens: 10, CacheReadTokens: 80, CacheCreationTokens: 40}
	got := EstimateCostUSD(domain.ProtocolAnthropic, 3, 15, 1, u)
	if got == nil {
		t.Fatal("expected cost")
	}
	// input 20*3 + cache read 80*3*0.1 + cache write 40*3*1.25 + out 10*15
	// = 60 + 24 + 150 + 150 = 384 / 1e6
	if *got != 0.000384 {
		t.Fatalf("got %v", *got)
	}
}

func TestEstimateCostUSDEmptyProtocolKeepsAnthropicConvention(t *testing.T) {
	// Pre-protocol legacy rows: input is Anthropic-style (uncached), so a
	// cache_read larger than input must not zero out the input term.
	u := TokenUsage{InputTokens: 20, CacheReadTokens: 80}
	got := EstimateCostUSD("", 3, 15, 1, u)
	if got == nil {
		t.Fatal("expected cost")
	}
	// input 20*3 + read 80*3*0.1 = 60 + 24 = 84 / 1e6
	if *got != 0.000084 {
		t.Fatalf("got %v", *got)
	}
}

func TestEstimateCostUSDNilWhenNoTokensOrPrice(t *testing.T) {
	if EstimateCostUSD(domain.ProtocolAnthropic, 0, 0, 1, TokenUsage{InputTokens: 10}) != nil {
		t.Fatal("zero price")
	}
	if EstimateCostUSD(domain.ProtocolAnthropic, 3, 15, 1, TokenUsage{}) != nil {
		t.Fatal("zero tokens")
	}
}

func TestNormalizeUsage(t *testing.T) {
	openai := TokenUsage{InputTokens: 8, CacheReadTokens: 5, CacheCreationTokens: 1}
	got := NormalizeUsage(domain.ProtocolOpenAI, openai)
	if got.InputTokens != 2 || got.CacheReadTokens != 5 || got.CacheCreationTokens != 1 {
		t.Fatalf("openai normalize: %+v", got)
	}
	if openai.InputTokens != 8 {
		t.Fatal("normalize must not mutate the caller's value")
	}

	anthropic := TokenUsage{InputTokens: 10, CacheReadTokens: 2}
	if got := NormalizeUsage(domain.ProtocolAnthropic, anthropic); got.InputTokens != 10 {
		t.Fatalf("anthropic input must stay uncached: %+v", got)
	}
}

package upstream

import (
	"testing"
)

func TestParseUsageAnthropicCacheCreationBreakdown(t *testing.T) {
	body := []byte(`{
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_read_input_tokens": 2000,
			"cache_creation_input_tokens": 800,
			"cache_creation": {
				"ephemeral_5m_input_tokens": 500,
				"ephemeral_1h_input_tokens": 300
			}
		}
	}`)
	u := ParseUsageJSON(body)
	if !u.UsageKnown || u.InputTokens != 100 || u.OutputTokens != 50 {
		t.Fatalf("usage = %+v", u)
	}
	if u.CacheReadTokens != 2000 || u.CacheCreationTokens != 800 {
		t.Fatalf("cache tokens = %+v", u)
	}
	if u.CacheCreation5mTokens != 500 || u.CacheCreation1hTokens != 300 {
		t.Fatalf("cache breakdown = %+v", u)
	}
	if u.ServiceTier != "" {
		t.Fatalf("service tier = %q", u.ServiceTier)
	}
}

func TestParseUsageOpenAIServiceTier(t *testing.T) {
	u := ParseUsageJSON([]byte(`{
		"usage": {
			"prompt_tokens": 900,
			"completion_tokens": 70,
			"service_tier": "priority",
			"prompt_tokens_details": {"cached_tokens": 600}
		}
	}`))
	if u.InputTokens != 900 || u.CacheReadTokens != 600 {
		t.Fatalf("usage = %+v", u)
	}
	if u.ServiceTier != "priority" {
		t.Fatalf("service tier = %q", u.ServiceTier)
	}
}

func TestParseUsageResponsesEnvelopeServiceTier(t *testing.T) {
	u := ParseUsageJSON([]byte(`{"response": {"usage": {"input_tokens": 10, "output_tokens": 5, "service_tier": "flex"}}}`))
	if u.ServiceTier != "flex" || u.InputTokens != 10 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestMergeUsageCarriesNewFields(t *testing.T) {
	dst := TokenUsage{}
	MergeUsage(&dst, TokenUsage{InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 10, CacheCreation5mTokens: 6, CacheCreation1hTokens: 4, ServiceTier: "priority", UsageKnown: true})
	MergeUsage(&dst, TokenUsage{CacheReadTokens: 7})
	if dst.InputTokens != 1 || dst.OutputTokens != 2 {
		t.Fatalf("merge = %+v", dst)
	}
	if dst.CacheReadTokens != 7 {
		t.Fatalf("cache read merge = %+v", dst)
	}
	if dst.CacheCreationTokens != 10 || dst.CacheCreation5mTokens != 6 || dst.CacheCreation1hTokens != 4 {
		t.Fatalf("cache merge = %+v", dst)
	}
	if dst.ServiceTier != "priority" || !dst.UsageKnown {
		t.Fatalf("tier/known = %+v", dst)
	}
}

package upstream

import "testing"

func TestParseBalance(t *testing.T) {
	b := ParseBalance([]byte(`{"quota":{"remaining":12.5,"used":1,"limit":20}}`))
	if b.Remaining == nil || *b.Remaining != 12.5 {
		t.Fatalf("quota.remaining: %+v", b)
	}
	b = ParseBalance([]byte(`{"remaining":3.2,"unit":"USD"}`))
	if b.Remaining == nil || *b.Remaining != 3.2 {
		t.Fatalf("remaining: %+v", b)
	}
	b = ParseBalance([]byte(`{"balance":9}`))
	if b.Remaining == nil || *b.Remaining != 9 {
		t.Fatalf("balance: %+v", b)
	}
}

func TestParseBilling(t *testing.T) {
	v, ok := ParseBillingMultiplier([]byte(`{"effective_rate_multiplier":0.45}`))
	if !ok || v != 0.45 {
		t.Fatalf("got %v %v", v, ok)
	}
}

func TestParseNewAPIBilling(t *testing.T) {
	limit, unlimited, ok := ParseSubscriptionLimit([]byte(`{"object":"billing_subscription","hard_limit_usd":12.5,"soft_limit_usd":12.5,"system_hard_limit_usd":12.5,"access_until":0}`))
	if !ok || unlimited || limit != 12.5 {
		t.Fatalf("subscription: %v %v %v", limit, unlimited, ok)
	}
	_, unlimited, ok = ParseSubscriptionLimit([]byte(`{"hard_limit_usd":100000000}`))
	if !ok || !unlimited {
		t.Fatalf("unlimited: %v %v", unlimited, ok)
	}
	if _, _, ok := ParseSubscriptionLimit([]byte(`{"error":{"message":"boom","type":"upstream_error"}}`)); ok {
		t.Fatal("error body must not parse as ok")
	}
	used, ok := ParseBillingUsage([]byte(`{"object":"list","total_usage":250}`))
	if !ok || used != 2.5 {
		t.Fatalf("usage: %v %v", used, ok)
	}
	rem, unlimited, ok := ParseNewAPITokenUsage([]byte(`{"code":true,"data":{"object":"token_usage","total_available":1000000,"unlimited_quota":false}}`))
	if !ok || unlimited || rem != 2 {
		t.Fatalf("token usage: %v %v %v", rem, unlimited, ok)
	}
	_, unlimited, ok = ParseNewAPITokenUsage([]byte(`{"data":{"total_available":0,"unlimited_quota":true}}`))
	if !ok || !unlimited {
		t.Fatalf("token unlimited: %v %v", unlimited, ok)
	}
	rem, unlimited, ok = ParseNewAPITokenUsage([]byte(`{"success":true,"message":"","data":{"object":"token_usage","total_available":2500000,"unlimited_quota":false}}`))
	if !ok || unlimited || rem != 5 {
		t.Fatalf("success wrapper: %v %v %v", rem, unlimited, ok)
	}
	rem, unlimited, ok = ParseNewAPITokenUsage([]byte(`{"object":"token_usage","total_available":1500000,"unlimited_quota":false}`))
	if !ok || unlimited || rem != 3 {
		t.Fatalf("unwrapped: %v %v %v", rem, unlimited, ok)
	}
	if _, _, ok = ParseNewAPITokenUsage([]byte(`{"success":false,"message":"No Authorization header"}`)); ok {
		t.Fatal("failed body must not parse as ok")
	}
	if got := MaybeRawQuotaToUSD(12.5); got != 12.5 {
		t.Fatalf("usd limit: %v", got)
	}
	if got := MaybeRawQuotaToUSD(1000000); got != 2 {
		t.Fatalf("token-scale limit: %v", got)
	}
	gr, ok := ParseNewAPIGroupRatio([]byte(`{"success":true,"data":[],"group_ratio":{"default":1,"vip":0.8}}`))
	if !ok || gr["vip"] != 0.8 || gr["default"] != 1 {
		t.Fatalf("group ratio: %v %v", gr, ok)
	}
}

func TestParseUsageJSON(t *testing.T) {
	u := ParseUsageJSON([]byte(`{"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}`))
	if u.InputTokens != 10 || u.OutputTokens != 4 || u.CacheReadTokens != 2 || u.CacheCreationTokens != 1 {
		t.Fatalf("%+v", u)
	}
	u = ParseUsageJSON([]byte(`{"usage":{"prompt_tokens":8,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":5}}}`))
	if u.InputTokens != 8 || u.OutputTokens != 3 || u.CacheReadTokens != 5 {
		t.Fatalf("%+v", u)
	}
	u = ParseUsageJSON([]byte(`{"usage":{"input_tokens":4387,"output_tokens":14,"input_tokens_details":{"cached_tokens":3840,"cache_write_tokens":12}}}`))
	if u.InputTokens != 4387 || u.OutputTokens != 14 || u.CacheReadTokens != 3840 || u.CacheCreationTokens != 12 {
		t.Fatalf("responses usage: %+v", u)
	}
}

func TestJoinEndpoint(t *testing.T) {
	got := JoinEndpoint("https://host.example/v1/", "/v1/messages")
	if got != "https://host.example/v1/messages" {
		t.Fatalf("got %s", got)
	}
}

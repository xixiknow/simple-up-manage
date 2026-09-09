package ops

import (
	"testing"

	"simple-up-manage/internal/domain"
)

func testCatalog() []domain.CatalogModel {
	return []domain.CatalogModel{
		{Vendor: domain.VendorOpenAI, ModelID: "gpt-4o-mini", InputCost: 0.15, OutputCost: 0.6},
		{Vendor: domain.VendorAnthropic, ModelID: "claude-haiku-4-5", InputCost: 1, OutputCost: 5},
		{Vendor: domain.VendorGrok, ModelID: "grok-3-mini", InputCost: 0.3, OutputCost: 0.5},
		{Vendor: domain.VendorZhipu, ModelID: "glm-4.5-flash", InputCost: 0.1, OutputCost: 0.1},
		{Vendor: domain.VendorMoonshot, ModelID: "kimi-k2-turbo-preview", InputCost: 0.15, OutputCost: 2.5},
		{Vendor: domain.VendorDeepseek, ModelID: "deepseek-chat", InputCost: 0.14, OutputCost: 0.28},
	}
}

func TestPickProbeTargetProtocolFallback(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	openaiUp := &domain.Upstream{Protocols: domain.ProtocolOpenAI}
	got := PickProbeTarget(&domain.PlatformKey{Upstream: openaiUp}, cfg, nil)
	if got.Vendor != domain.VendorOpenAI || got.Model != cfg.ProbeOpenAIModel {
		t.Fatalf("openai fallback %+v", got)
	}

	anthUp := &domain.Upstream{Protocols: domain.ProtocolAnthropic}
	got = PickProbeTarget(&domain.PlatformKey{Upstream: anthUp}, cfg, nil)
	if got.Vendor != domain.VendorAnthropic || got.Protocol != domain.ProtocolAnthropic {
		t.Fatalf("anthropic fallback %+v", got)
	}
}

func TestPickProbeTargetUsesConfiguredModelOnKey(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	cfg.ProbeDeepseekModel = "deepseek-chat"
	cfg.ProbeOpenAIModel = "gpt-4o-mini"
	key := &domain.PlatformKey{
		Upstream:   &domain.Upstream{Protocols: domain.ProtocolOpenAI},
		LastModels: domain.JSONStrings{"deepseek-chat", "gpt-4o"},
	}
	got := PickProbeTarget(key, cfg, testCatalog())
	if got.Vendor != domain.VendorDeepseek || got.Model != "deepseek-chat" {
		t.Fatalf("expected deepseek-chat, got %+v", got)
	}
}

func TestPickProbeTargetPrefersCheapestConfiguredHit(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	cfg.ProbeOpenAIModel = "gpt-4o-mini"
	cfg.ProbeDeepseekModel = "deepseek-chat"
	cfg.ProbeZhipuModel = "glm-4.5-flash"
	key := &domain.PlatformKey{
		Upstream:   &domain.Upstream{Protocols: domain.ProtocolOpenAI},
		LastModels: domain.JSONStrings{"gpt-4o-mini", "deepseek-chat", "glm-4.5-flash"},
	}
	got := PickProbeTarget(key, cfg, testCatalog())
	if got.Vendor != domain.VendorZhipu || got.Model != "glm-4.5-flash" {
		t.Fatalf("expected cheapest zhipu hit, got %+v", got)
	}
}

func TestPickProbeTargetInfersVendorWhenProbeIdMissing(t *testing.T) {
	cfg := domain.DefaultSchedulerSettings()
	cfg.ProbeMoonshotModel = "kimi-k2-turbo-preview"
	key := &domain.PlatformKey{
		Upstream:   &domain.Upstream{Protocols: domain.ProtocolOpenAI},
		LastModels: domain.JSONStrings{"moonshot-v1-8k", "kimi-k2"},
	}
	got := PickProbeTarget(key, cfg, testCatalog())
	if got.Vendor != domain.VendorMoonshot || got.Model != "kimi-k2-turbo-preview" {
		t.Fatalf("expected moonshot probe, got %+v", got)
	}
}

func TestInferVendor(t *testing.T) {
	cat := testCatalog()
	cases := map[string]string{
		"openai/gpt-4o-mini": "openai",
		"claude-sonnet-4":    domain.VendorAnthropic,
		"grok-4":             domain.VendorGrok,
		"GLM-4":              domain.VendorZhipu,
		"kimi-k3":            domain.VendorMoonshot,
		"deepseek-reasoner":  domain.VendorDeepseek,
		"unknown-model":      "",
	}
	for id, want := range cases {
		if got := InferVendor(id, cat); got != want {
			t.Fatalf("%s: got %q want %q", id, got, want)
		}
	}
}

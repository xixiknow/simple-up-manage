package picker

import (
	"testing"

	"simple-up-manage/internal/domain"
)

func TestHardRejectModelFilter(t *testing.T) {
	up := &domain.Upstream{Status: domain.StatusEnabled, Protocols: "openai"}
	key := &domain.PlatformKey{
		Status:       domain.StatusEnabled,
		Upstream:     up,
		HealthStatus: domain.HealthHealthy,
		LastModels:   domain.JSONStrings{"gpt-4o", "GPT-Image-1"},
	}
	none := map[uint]struct{}{}

	if r := hardReject(key, domain.ProtocolOpenAI, "gpt-4o", true, none); r != "" {
		t.Fatalf("listed model should pass, got %q", r)
	}
	if r := hardReject(key, domain.ProtocolOpenAI, "gpt-image-1", true, none); r != "" {
		t.Fatalf("match should be case-insensitive, got %q", r)
	}
	if r := hardReject(key, domain.ProtocolOpenAI, "claude-3", true, none); r != "model_not_supported" {
		t.Fatalf("unlisted model should be rejected, got %q", r)
	}
	if r := hardReject(key, domain.ProtocolOpenAI, "claude-3", false, none); r != "" {
		t.Fatalf("filter disabled should pass, got %q", r)
	}
	if r := hardReject(key, domain.ProtocolOpenAI, "", true, none); r != "" {
		t.Fatalf("empty model should pass, got %q", r)
	}

	key.LastModels = nil
	if r := hardReject(key, domain.ProtocolOpenAI, "anything", true, none); r != "" {
		t.Fatalf("empty list should not filter, got %q", r)
	}
}

func TestHardRejectUpstreamBalance(t *testing.T) {
	zero := 0.0
	up := &domain.Upstream{Status: domain.StatusEnabled, Protocols: "openai"}
	key := &domain.PlatformKey{Status: domain.StatusEnabled, Upstream: up, HealthStatus: domain.HealthHealthy}
	if r := hardReject(key, domain.ProtocolOpenAI, "m", false, nil); r != "" {
		t.Fatalf("no balance should pass, got %q", r)
	}
	up.LastBalance = &zero
	if r := hardReject(key, domain.ProtocolOpenAI, "m", false, nil); r != "low_balance" {
		t.Fatalf("provider balance 0: got %q", r)
	}
}

func TestSchedulerFilterByModelsDefault(t *testing.T) {
	var s domain.SchedulerSettings
	s.Normalize()
	if !s.ModelFilterEnabled() {
		t.Fatal("filter_by_models should default to true")
	}
	off := false
	s.FilterByModels = &off
	s.Normalize()
	if s.ModelFilterEnabled() {
		t.Fatal("explicit false must be preserved by Normalize")
	}
}

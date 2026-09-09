package handler

import (
	"testing"

	"simple-up-manage/internal/domain"
)

func TestGroupAcceptsProtocolOpenAIOnly(t *testing.T) {
	groups := []domain.RouteGroup{{
		Name:     "GPT-0.1分组",
		Protocol: domain.ProtocolOpenAI,
		Models:   domain.JSONStrings{"gpt-*"},
		Status:   domain.StatusEnabled,
	}}
	if !groupAcceptsProtocol(groups, false, domain.ProtocolOpenAI) {
		t.Fatal("openai group should accept openai")
	}
	if groupAcceptsProtocol(groups, false, domain.ProtocolAnthropic) {
		t.Fatal("openai-only group should not accept anthropic")
	}
}

func TestPickTestModelUsesMatchingProbe(t *testing.T) {
	groups := []domain.RouteGroup{{
		Protocol: domain.ProtocolOpenAI,
		Models:   domain.JSONStrings{"gpt-*"},
		Status:   domain.StatusEnabled,
	}}
	got := pickTestModel(groups, false, domain.ProtocolOpenAI, "gpt-5.6-sol")
	if got != "gpt-5.6-sol" {
		t.Fatalf("got %q", got)
	}
}

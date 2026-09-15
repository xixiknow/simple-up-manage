package ops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"simple-up-manage/internal/domain"
	"testing"
)

func TestKeyProtocolProbes(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Api-Key") != "fixture" || r.Header.Get("Anthropic-Version") == "" {
			t.Error("missing anthropic authentication")
		}
		if r.URL.Path != "/v1/models" && r.URL.Path != "/v1/messages" {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[],"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer server.Close()
	s := New(testDB(t), nil, nil)
	k := &domain.PlatformKey{Protocols: "anthropic", Upstream: &domain.Upstream{Protocols: "openai,anthropic", BaseURL: server.URL}, LastModels: domain.JSONStrings{"gpt-4o-mini"}}
	if target := PickProbeTarget(k, domain.DefaultSchedulerSettings(), testCatalog()); target.Protocol != domain.ProtocolAnthropic {
		t.Fatalf("wrong fallback: %+v", target)
	}
	if _, err := s.getModels(context.Background(), k, "fixture"); err != nil {
		t.Fatal(err)
	}
	if out := s.lightProbe(context.Background(), k, "fixture"); !out.Success {
		t.Fatalf("light: %+v", out)
	}
	if out := s.deepProbe(context.Background(), k, "fixture"); !out.Success {
		t.Fatalf("deep: %+v", out)
	}
	k.Upstream.Protocols = "openai"
	before := calls
	if out := s.deepProbe(context.Background(), k, "fixture"); out.Error == "" {
		t.Fatal("missing protocol error")
	}
	if out := s.lightProbe(context.Background(), k, "fixture"); out.Error == "" {
		t.Fatal("missing protocol error")
	}
	if _, err := s.getModels(context.Background(), k, "fixture"); err == nil {
		t.Fatal("missing protocol error")
	}
	if calls != before {
		t.Fatal("invalid protocol sent network request")
	}
}

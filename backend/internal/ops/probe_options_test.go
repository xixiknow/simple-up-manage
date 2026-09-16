package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
)

func TestCustomProbeModelPromptAndProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, path string
		stream               bool
	}{
		{"chat", domain.ProtocolOpenAI, "/v1/chat/completions", false},
		{"responses", domain.ProtocolOpenAI, "/v1/responses", true},
		{"anthropic", domain.ProtocolAnthropic, "/v1/messages", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prompt := "who are you?\n请介绍你自己。"
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != tc.path {
					t.Errorf("path=%s, want %s", r.URL.Path, tc.path)
				}
				var body struct {
					Model    string `json:"model"`
					Input    string `json:"input"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "custom-model" || body.Stream != tc.stream {
					t.Errorf("body=%+v", body)
				}
				if tc.path == "/v1/responses" {
					if body.Input != prompt {
						t.Errorf("input=%q", body.Input)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"))
				} else {
					if len(body.Messages) != 1 || body.Messages[0].Content != prompt {
						t.Errorf("messages=%+v", body.Messages)
					}
					w.Header().Set("Content-Type", "application/json")
					if tc.protocol == domain.ProtocolAnthropic {
						if r.Header.Get("x-api-key") != "fixture" {
							t.Error("missing Anthropic auth")
						}
						_, _ = w.Write([]byte(`{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"OK"}]}`))
					} else {
						_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
					}
				}
			}))
			defer server.Close()
			db := testDB(t)
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("fixture")
			up := domain.Upstream{Name: "p", BaseURL: server.URL, Protocols: "openai,anthropic"}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "k", EncryptedKey: secret}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			// An unrelated observed model must never override a manual choice.
			for _, a := range []domain.RequestAttempt{
				{ID: "other", Model: "other-model", Path: "/v1/chat/completions", Protocol: domain.ProtocolOpenAI},
				{ID: "chosen", Model: "custom-model", Path: tc.path, Protocol: tc.protocol, Stream: tc.stream},
			} {
				a.PlatformKeyID, a.StatsVersion, a.CompletedAt = key.ID, domain.AttemptStatsVersion, time.Now()
				if err := db.Create(&a).Error; err != nil {
					t.Fatal(err)
				}
			}
			s := New(db, enc, nil)
			out, err := s.ProbeKey(context.Background(), key.ID, true, ProbeOptions{Model: "custom-model", Prompt: &prompt, Protocol: tc.protocol})
			if err != nil || out == nil || !out.Success {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if calls.Load() != 1 || out.Model != "custom-model" {
				t.Fatalf("calls=%d out=%+v", calls.Load(), out)
			}
			var stored domain.ProbeLog
			if err := db.First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Model != out.Model || stored.Path != tc.path {
				t.Fatalf("probe log=%+v", stored)
			}
		})
	}
}

func TestCustomProbeBatchAndDisabledKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		if string(body["model"]) != `"manual"` || !strings.Contains(string(body["messages"]), "who are you") {
			t.Errorf("body=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	db := testDB(t)
	enc, _ := crypto.New(strings.Repeat("01", 32))
	secret, _ := enc.Encrypt("fixture")
	up := domain.Upstream{Name: "p", BaseURL: server.URL, Protocols: "openai"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"on", "off"} {
		k := domain.PlatformKey{UpstreamID: up.ID, Name: name, EncryptedKey: secret}
		if err := db.Create(&k).Error; err != nil {
			t.Fatal(err)
		}
		if name == "off" {
			if err := db.Model(&k).Update("probe_enabled", false).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	prompt := "who are you"
	s := New(db, enc, nil)
	ok, fail, skipped, reasons := s.ProbeFilteredDetail(context.Background(), true, &up.ID, 0, ProbeOptions{Model: "manual", Prompt: &prompt})
	if ok != 1 || fail != 0 || skipped != 1 || calls.Load() != 1 || reasons[domain.ProbeSkipDisabled] != 1 {
		t.Fatalf("ok=%d failed=%d skipped=%d calls=%d reasons=%v", ok, fail, skipped, calls.Load(), reasons)
	}
	var key domain.PlatformKey
	db.Where("name = ?", "on").First(&key)
	out, err := s.ProbeKey(context.Background(), key.ID, true, ProbeOptions{Protocol: domain.ProtocolAnthropic})
	if err != nil || out == nil || !out.Skipped || calls.Load() != 1 {
		t.Fatalf("protocol mismatch: out=%+v err=%v calls=%d", out, err, calls.Load())
	}
}

func TestProbeOptionsValidation(t *testing.T) {
	empty, large, valid := " \n ", strings.Repeat("你", 4001), "who are you"
	for _, tc := range []struct {
		options     ProbeOptions
		deep, valid bool
	}{
		{ProbeOptions{}, false, true}, {ProbeOptions{}, true, true},
		{ProbeOptions{Prompt: &valid}, true, true}, {ProbeOptions{Prompt: &empty}, true, false},
		{ProbeOptions{Prompt: &large}, true, false}, {ProbeOptions{Protocol: "invalid"}, true, false},
		{ProbeOptions{Model: strings.Repeat("m", 257)}, true, false}, {ProbeOptions{Prompt: &valid}, false, false},
	} {
		if err := tc.options.Validate(tc.deep); (err == nil) != tc.valid {
			t.Fatalf("deep=%v want valid=%v err=%v", tc.deep, tc.valid, err)
		}
	}
}

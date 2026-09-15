package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
)

func TestProbeDoesNotChangeBusinessState(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "success"}[success], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if success {
					w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
				} else {
					w.WriteHeader(400)
					w.Write([]byte(`{"error":{"code":"invalid_request"}}`))
				}
			}))
			defer server.Close()
			db := testDB(t)
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("fixture")
			up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Protocols: "openai"}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(time.Minute)
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret, HealthStatus: domain.HealthHealthy, CooldownUntil: &until, LastError: "business-error", LastModels: domain.JSONStrings{"m"}}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			circuit := domain.RoutingCircuit{Scope: "test", PlatformKeyID: key.ID, Open: true, Until: until, Reason: "business_failure"}
			if err := db.Create(&circuit).Error; err != nil {
				t.Fatal(err)
			}
			s := New(db, enc, nil)
			out, err := s.ProbeKey(context.Background(), key.ID, true)
			if err != nil || out.Success != success {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			var after domain.PlatformKey
			if err := db.First(&after, key.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.HealthStatus != key.HealthStatus || after.LastError != key.LastError || !after.CooldownUntil.Equal(until) || len(after.LastModels) != 1 {
				t.Fatalf("probe changed key: %+v", after)
			}
			var gate domain.RoutingCircuit
			db.First(&gate, "scope = ?", "test")
			if !gate.Open || !gate.Until.Equal(until) {
				t.Fatal("probe changed business gate")
			}
			var n int64
			db.Model(&domain.RequestAttempt{}).Count(&n)
			if n != 0 {
				t.Fatal("probe entered business statistics")
			}
		})
	}
}

func TestProbeUsesObservedResponsesStreamAndStrictValidation(t *testing.T) {
	valid := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("wrong endpoint: %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true || body["model"] != "observed-model" || body["input"] != "Reply OK." {
			t.Errorf("wrong synthetic request: %+v", body)
		}
		if !valid {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("Hi! What can I help you with?"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()
	db := testDB(t)
	up := &domain.Upstream{BaseURL: server.URL, Protocols: "openai"}
	key := &domain.PlatformKey{ID: 1, Upstream: up, Protocols: "openai"}
	a := domain.RequestAttempt{ID: "traffic", PlatformKeyID: 1, Protocol: "openai", Model: "observed-model", Path: "/v1/responses", Stream: true, StatsVersion: 2, CompletedAt: time.Now()}
	if err := db.Create(&a).Error; err != nil {
		t.Fatal(err)
	}
	s := New(db, nil, nil)
	out := s.deepProbe(context.Background(), key, "test")
	if !out.Success || out.Path != a.Path || !out.Stream {
		t.Fatalf("response probe: %+v", out)
	}
	valid = false
	out = s.deepProbe(context.Background(), key, "test")
	if out.Success {
		t.Fatal("plain greeting passed probe")
	}
}

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
	"simple-up-manage/internal/routinghealth"
)

func TestRecoveryWorkerUsesExactDimensionWithoutClosingCircuit(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/v1/responses" || body["model"] != "gpt-fixture" || body["stream"] != true || body["input"] != "Reply OK." {
					t.Errorf("wrong recovery dimension: %s %+v", r.URL.Path, body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n"))
			}))
			defer server.Close()
			db := testDB(t)
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("fixture")
			up := domain.Upstream{Name: "recovery", BaseURL: server.URL, Protocols: "openai", Status: domain.StatusEnabled}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "recovery", EncryptedKey: secret, Protocols: "openai", Status: domain.StatusEnabled, ProbeEnabled: &enabled}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			d := routinghealth.Dimension{KeyID: key.ID, Protocol: "openai", Model: "gpt-fixture", Path: "/v1/responses", Stream: true}
			gate := domain.RoutingCircuit{Scope: d.Scope(), PlatformKeyID: key.ID, Open: true, Until: time.Now().Add(-time.Minute), Protocol: d.Protocol, Model: d.Model, Path: d.Path, Stream: d.Stream}
			if err := db.Create(&gate).Error; err != nil {
				t.Fatal(err)
			}
			s := New(db, enc, nil)
			for i := 0; i < 2; i++ {
				if err := s.CheckRecoveries(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			var after domain.RoutingCircuit
			if err := db.First(&after, "scope = ?", gate.Scope).Error; err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if enabled {
				want = 1
			}
			if calls.Load() != want || !after.Open || after.CheckOK != enabled {
				t.Fatalf("calls=%d circuit=%+v", calls.Load(), after)
			}
			var n int64
			if err := db.Model(&domain.RequestAttempt{}).Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatal("synthetic check entered business samples")
			}
		})
	}
}

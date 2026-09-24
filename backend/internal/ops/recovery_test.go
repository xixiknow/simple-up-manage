package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/routinghealth"
)

func TestRecoveryWorkerUsesExactDimensionAndClosesCircuit(t *testing.T) {
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
			if calls.Load() != want || after.Open == enabled || after.CheckOK != enabled {
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

func TestRecoveryPoolRefillsAndStopsAcrossInstances(t *testing.T) {
	const total = 10
	started := make(chan int, total)
	var active, maxActive atomic.Int32
	release := make([]chan struct{}, total)
	for i := range release {
		release[i] = make(chan struct{})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		id, err := strconv.Atoi(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if err != nil || id < 0 || id >= total {
			t.Error("unexpected credential")
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		started <- id
		select {
		case <-r.Context().Done():
			return
		case <-release[id]:
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"r","object":"response","status":"completed","output":[]}`)
	}))
	defer server.Close()
	db := testDB(t)
	enc, _ := crypto.New(strings.Repeat("01", 32))
	up := domain.Upstream{Name: "pool", BaseURL: server.URL, Protocols: "openai", Status: domain.StatusEnabled}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		secret, _ := enc.Encrypt(strconv.Itoa(i))
		enabled := true
		key := domain.PlatformKey{UpstreamID: up.ID, Name: fmt.Sprint(i), EncryptedKey: secret, Protocols: "openai", Status: domain.StatusEnabled, ProbeEnabled: &enabled}
		if err := db.Create(&key).Error; err != nil {
			t.Fatal(err)
		}
		dim := routinghealth.Dimension{KeyID: key.ID, Protocol: "openai", Model: "m", Path: "/v1/responses"}
		gate := domain.RoutingCircuit{Scope: dim.Scope(), PlatformKeyID: key.ID, Open: true, Until: time.Now().Add(-time.Minute), Protocol: dim.Protocol, Model: dim.Model, Path: dim.Path}
		if i == 0 {
			future := time.Now().Add(5 * time.Minute)
			gate.CheckOK = true
			gate.NextCheckAt = &future
		} // historical readiness must be rechecked now
		if err := db.Create(&gate).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := New(db, enc, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	defer func() {
		cancel()
		for i := 0; i < 2; i++ {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("pool failed to stop")
			}
		}
	}()
	for i := 0; i < 2; i++ {
		go func() { s.RunRecoveries(ctx, 8); done <- struct{}{} }()
	}
	seen := map[int]bool{}
	first := -1
	for i := 0; i < 8; i++ {
		select {
		case id := <-started:
			if seen[id] {
				t.Fatalf("duplicate probe: %d", id)
			}
			seen[id] = true
			if first < 0 {
				first = id
			}
		case <-time.After(4 * time.Second):
			t.Fatal("pool did not fill eight slots")
		}
	}
	select {
	case id := <-started:
		t.Fatalf("ninth started without capacity: %d", id)
	case <-time.After(100 * time.Millisecond):
	}
	close(release[first])
	select {
	case id := <-started:
		if seen[id] {
			t.Fatalf("duplicate refill %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("completed slot waited for slow batch")
	}
	if maxActive.Load() > 8 {
		t.Fatalf("global concurrency exceeded: %d", maxActive.Load())
	}
	cancel()
	// Deferred drain ensures cancellation completes before the database/server closes.
}

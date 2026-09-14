package ops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestProbeTimeoutIndependent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		deep    bool
		seconds int
		delay   time.Duration
		success bool
	}{
		{"light beyond ten seconds", false, 30, 10500 * time.Millisecond, true},
		{"deep beyond ten seconds", true, 30, 10500 * time.Millisecond, true},
		{"light configured timeout", false, 1, 2 * time.Second, false},
		{"deep configured timeout", true, 1, 2 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(tc.delay):
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[],"choices":[{"message":{"content":"hi"}}]}`))
			}))
			defer server.Close()
			db := testDB(t)
			cfg := domain.DefaultSchedulerSettings()
			cfg.ID = 1
			cfg.ProbeTimeoutSec = tc.seconds
			if err := db.Save(&cfg).Error; err != nil {
				t.Fatal(err)
			}
			enc, err := crypto.New(strings.Repeat("01", 32))
			if err != nil {
				t.Fatal(err)
			}
			secret, err := enc.Encrypt("fixture")
			if err != nil {
				t.Fatal(err)
			}
			up := domain.Upstream{Name: "fixture", BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: "openai"}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			k := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", EncryptedKey: secret}
			if err := db.Create(&k).Error; err != nil {
				t.Fatal(err)
			}
			s := New(db, enc, nil)
			out, err := s.ProbeKey(context.Background(), k.ID, tc.deep)
			if err != nil || out.Success != tc.success {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if !tc.success && !strings.Contains(out.Error, "probe timed out after 1 seconds") {
				t.Fatalf("timeout: %+v", out)
			}
			var log domain.ProbeLog
			if err := db.First(&log).Error; err != nil {
				t.Fatal(err)
			}
			if log.Success != tc.success {
				t.Fatalf("log: %+v", log)
			}
		})
	}
}

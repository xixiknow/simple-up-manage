package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/ops"

	"github.com/gin-gonic/gin"
)

func TestManualProbeEndpointsForwardOptions(t *testing.T) {
	for _, mode := range []string{"key", "run-key", "provider", "all"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "manual-model" || len(body.Messages) != 1 || body.Messages[0].Content != "who are you" {
					t.Errorf("received=%+v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			db := logTestDB(t)
			enc, _ := crypto.New(strings.Repeat("01", 32))
			secret, _ := enc.Encrypt("fixture")
			up := domain.Upstream{Name: "p", BaseURL: server.URL, Protocols: "openai"}
			if err := db.Create(&up).Error; err != nil {
				t.Fatal(err)
			}
			key := domain.PlatformKey{UpstreamID: up.ID, Name: "k", EncryptedKey: secret}
			if err := db.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			admin := &Admin{DB: db, Ops: ops.New(db, enc, nil)}
			router := gin.New()
			router.POST("/keys/:id/probe", admin.ProbeKey)
			router.POST("/probes/run", admin.RunProbes)
			path := "/probes/run"
			payload := map[string]any{"deep": true, "model": "manual-model", "prompt": "who are you", "protocol": "openai"}
			switch mode {
			case "key":
				path = fmt.Sprintf("/keys/%d/probe", key.ID)
			case "run-key":
				payload["key_id"] = key.ID
			case "provider":
				payload["upstream_id"] = up.ID
			}
			raw, _ := json.Marshal(payload)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != 200 || calls.Load() != 1 {
				t.Fatalf("status=%d calls=%d response=%s", rec.Code, calls.Load(), rec.Body.String())
			}
		})
	}
}

func TestManualProbeEndpointsRejectInvalidOptions(t *testing.T) {
	admin := &Admin{} // Invalid input must be rejected before touching the service.
	router := gin.New()
	router.POST("/keys/:id/probe", admin.ProbeKey)
	router.POST("/probes/run", admin.RunProbes)
	for _, path := range []string{"/keys/1/probe", "/probes/run"} {
		for _, body := range []string{`{`, `{"deep":true,"prompt":"  "}`, `{"deep":false,"prompt":"hi"}`, `{"deep":true,"protocol":"invalid"}`, `{"deep":true,"model":12}`} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			if rec.Code != 400 {
				t.Fatalf("path=%s body=%s status=%d", path, body, rec.Code)
			}
		}
	}
}

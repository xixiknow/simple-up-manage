package handler

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/ops"
	"strings"
	"testing"
)

func TestCreateKeyProtocols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) }))
	defer server.Close()
	db := logTestDB(t)
	enc, err := crypto.New(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "provider", BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: "openai,anthropic"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	h := &Admin{DB: db, Enc: enc, Ops: ops.New(db, enc, nil)}
	r := gin.New()
	r.POST("/upstreams/:id/keys", h.CreateUpstreamKey)
	for _, extra := range []string{"", `,"protocols":[]`, `,"protocols":["anthropic"]`, `,"protocols":["openai","anthropic"]`} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upstreams/1/keys", strings.NewReader(`{"name_tag":"fixture","api_key":"secret"`+extra+`}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != 201 {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data keyDTO `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		want := 2
		if extra == `,"protocols":["anthropic"]` {
			want = 1
		}
		if len(envelope.Data.EffectiveProtocols) != want {
			t.Fatalf("effective protocols: %s", w.Body.String())
		}
	}
}

func TestKeyProtocolAPI(t *testing.T) {
	db := logTestDB(t)
	up := domain.Upstream{Name: "provider", BaseURL: "https://example.test", Kind: domain.KindOpenAICompat, Protocols: "openai,anthropic"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	k := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture", NameTag: "fixture", EncryptedKey: "fixture"}
	if err := db.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	h := &Admin{DB: db, Ops: ops.New(db, nil, nil)}
	r := gin.New()
	r.PUT("/keys/:id", h.UpdateKey)
	r.PUT("/upstreams/:id", h.UpdateUpstream)
	for _, tc := range []struct {
		path, body string
		code       int
		want       string
	}{
		{"/keys/1", `{"protocols":["anthropic"]}`, 200, "anthropic"},
		{"/keys/1", `{"status":"disabled"}`, 200, "anthropic"},
		{"/upstreams/1", `{"protocols":["openai"]}`, 400, "anthropic"},
		{"/keys/1", `{"protocols":["invalid"]}`, 400, "anthropic"},
		{"/keys/1", `{"protocols":[]}`, 200, ""},
		{"/upstreams/1", `{"protocols":["openai"]}`, 200, ""},
		{"/keys/1", `{"protocols":["anthropic"]}`, 400, ""},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
		if tc.path == "/upstreams/1" && tc.code == 400 && !strings.Contains(w.Body.String(), "fixture") {
			t.Fatal("missing affected key")
		}
		if err := db.First(&k, k.ID).Error; err != nil {
			t.Fatal(err)
		}
		if k.Protocols != tc.want {
			t.Fatalf("protocols=%q want=%q", k.Protocols, tc.want)
		}
	}
}

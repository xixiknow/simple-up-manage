package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteCaddyfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	original := ":8080 {\n\treverse_proxy sum-app-blue:8080 {\n\t\tflush_interval -1\n\t}\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := rewriteCaddyfile(path, []string{"sum-app-blue", "sum-app-green"}, "sum-app-green")
	if err != nil || !changed {
		t.Fatalf("rewrite: changed=%v err=%v", changed, err)
	}
	next, _ := os.ReadFile(path)
	if want := "reverse_proxy sum-app-green:8080"; !strings.Contains(string(next), want) {
		t.Fatalf("rewritten file missing %q:\n%s", want, next)
	}
	if strings.Contains(string(next), "sum-app-blue") {
		t.Fatalf("old upstream still present:\n%s", next)
	}

	// Second rewrite to the same target is a no-op.
	changed, err = rewriteCaddyfile(path, []string{"sum-app-blue", "sum-app-green"}, "sum-app-green")
	if err != nil || changed {
		t.Fatalf("idempotent rewrite: changed=%v err=%v", changed, err)
	}
}

func TestRewriteCaddyfileIgnoresUnrelatedHosts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Caddyfile")
	original := "other.example.com:8080 {\n\treverse_proxy other-app:9000\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := rewriteCaddyfile(path, []string{"sum-app-blue", "sum-app-green"}, "sum-app-green")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatalf("unrelated config must not change")
	}
}

func TestCloneEnv(t *testing.T) {
	env := []string{"A=1", "COLOR=blue", "PEER_CONTAINER=sum-app-green", "EMPTY="}
	got := cloneEnv(env, map[string]string{"COLOR": "green", "PEER_CONTAINER": "sum-app-blue", "NEW": "x", "EMPTY": ""})
	joined := ""
	for _, kv := range got {
		joined += kv + "\n"
	}
	if !strings.Contains(joined, "A=1") || !strings.Contains(joined, "COLOR=green") || !strings.Contains(joined, "PEER_CONTAINER=sum-app-blue") || !strings.Contains(joined, "NEW=x") {
		t.Fatalf("unexpected env: %#v", got)
	}
	if strings.Contains(joined, "EMPTY=") {
		t.Fatalf("overridden empty value should be dropped: %#v", got)
	}
}

func TestCloneLabelsDropsCompose(t *testing.T) {
	labels := map[string]string{
		"com.docker.compose.service":      "app-blue",
		"com.docker.compose.project":      "sum",
		"org.opencontainers.image.source": "https://github.com/xixiklow/simple-up-manage",
	}
	out := cloneLabels(labels)
	if _, ok := out["com.docker.compose.service"]; ok {
		t.Fatalf("compose label survived: %#v", out)
	}
	if out["org.opencontainers.image.source"] != labels["org.opencontainers.image.source"] {
		t.Fatalf("app label lost: %#v", out)
	}
}

func TestRepoFromImage(t *testing.T) {
	img := &imageInspect{RepoDigests: []string{
		"ghcr.io/xixiklow/simple-up-manage@sha256:abc123",
	}}
	if got := repoFromImage(img, "fallback"); got != "ghcr.io/xixiklow/simple-up-manage" {
		t.Fatalf("repoFromImage = %q", got)
	}
	if got := repoFromImage(&imageInspect{}, "ghcr.io/x/y:latest"); got != "ghcr.io/x/y:latest" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestPickDigest(t *testing.T) {
	digests := []string{"ghcr.io/xixiklow/simple-up-manage@sha256:aaaa", "other@sha256:bbbb"}
	if got := pickDigest(digests, "ghcr.io/xixiklow/simple-up-manage"); got != "sha256:aaaa" {
		t.Fatalf("pickDigest = %q", got)
	}
	if got := pickDigest(digests, "missing"); got != "" {
		t.Fatalf("pickDigest missing = %q", got)
	}
}

func TestSplitRepo(t *testing.T) {
	base, path, host := splitRepo("ghcr.io/xixiklow/simple-up-manage")
	if base != "https://ghcr.io" || path != "xixiklow/simple-up-manage" || host != "ghcr.io" {
		t.Fatalf("splitRepo = %q %q %q", base, path, host)
	}
}

// newFakeRegistry serves a minimal OCI registry: the latest tag resolves to the
// given index or plain manifest, and the referenced child manifests / config
// blobs are served from the fixtures map.
func newFakeRegistry(latest any, extra map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "test-token"})
		case r.URL.Path == "/v2/x/y/manifests/latest":
			_ = json.NewEncoder(w).Encode(latest)
		default:
			body, ok := extra[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
}

func TestConfigVersionLabelFromIndex(t *testing.T) {
	// buildx pushes an OCI index whose entries include provenance attestations
	// (unknown/unknown); the amd64 child manifest must be fetched to reach the
	// config blob digest — the old code requested the manifest digest from the
	// blobs endpoint and always came up empty.
	latest := map[string]any{"manifests": []map[string]any{
		{"digest": "sha256:amd64", "platform": map[string]string{"architecture": "amd64", "os": "linux"}},
		{"digest": "sha256:attest", "platform": map[string]string{"architecture": "unknown", "os": "unknown"}},
	}}
	extra := map[string]any{
		"/v2/x/y/manifests/sha256:amd64": map[string]any{"config": map[string]string{"digest": "sha256:cfg"}},
		"/v2/x/y/blobs/sha256:cfg":       map[string]any{"config": map[string]any{"Labels": map[string]string{"org.opencontainers.image.version": "1.2.3"}}},
	}
	srv := newFakeRegistry(latest, extra)
	defer srv.Close()

	r := &registryClient{base: srv.URL, repoPath: "x/y", http: srv.Client()}
	ver, err := r.configVersionLabel(context.Background(), "latest")
	if err != nil {
		t.Fatalf("configVersionLabel: %v", err)
	}
	if ver != "1.2.3" {
		t.Fatalf("version label = %q, want 1.2.3", ver)
	}
}

func TestConfigVersionLabelFromPlainManifest(t *testing.T) {
	latest := map[string]any{"config": map[string]string{"digest": "sha256:cfg"}}
	extra := map[string]any{
		"/v2/x/y/blobs/sha256:cfg": map[string]any{"config": map[string]any{"Labels": map[string]string{"org.opencontainers.image.version": "2.0.0"}}},
	}
	srv := newFakeRegistry(latest, extra)
	defer srv.Close()

	r := &registryClient{base: srv.URL, repoPath: "x/y", http: srv.Client()}
	ver, err := r.configVersionLabel(context.Background(), "latest")
	if err != nil {
		t.Fatalf("configVersionLabel: %v", err)
	}
	if ver != "2.0.0" {
		t.Fatalf("version label = %q, want 2.0.0", ver)
	}
}

func TestCapability(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c := Config{SocketPath: sock, Color: "blue", PeerName: "sum-app-green", CaddyContainer: "sum-proxy", CaddyfilePath: "/app/caddy/Caddyfile", DatabaseURL: "postgres://x"}
	if !c.capability().OK {
		t.Fatalf("valid config rejected: %s", c.capability().Reason)
	}
	c.DatabaseURL = "sqlite://data/local.db"
	if c.capability().OK {
		t.Fatalf("sqlite must be rejected")
	}
	c.DatabaseURL = "postgres://x"
	c.Color = ""
	if c.capability().OK {
		t.Fatalf("missing color must be rejected")
	}
	c.Color = "blue"
	c.SocketPath = filepath.Join(t.TempDir(), "missing.sock")
	if c.capability().OK {
		t.Fatalf("missing socket must be rejected")
	}
}

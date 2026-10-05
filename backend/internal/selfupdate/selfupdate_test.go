package selfupdate

import (
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

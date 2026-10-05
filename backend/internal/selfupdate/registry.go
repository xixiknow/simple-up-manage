package selfupdate

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	latestTag    = "latest"
	versionLabel = "org.opencontainers.image.version"
	ghcrRegistry = "ghcr.io"
)

// manifestAccept lists the manifest media types the registry may answer with,
// newest (OCI index) first — required for ghcr to answer HEAD/GET at all.
const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// registryClient checks the remote registry for a newer image. Anonymous pull
// tokens cover public images; a mounted docker config.json covers private ones.
type registryClient struct {
	base     string // https://ghcr.io
	repoPath string // xixiklow/simple-up-manage
	auth     *dockerAuth
	http     *http.Client
}

type dockerAuth struct {
	username      string
	password      string
	identityToken string
}

// splitRepo turns "ghcr.io/xixiklow/simple-up-manage" into a registry base URL
// and the repository path scoped tokens are issued for.
func splitRepo(repo string) (base, repoPath, host string) {
	i := strings.Index(repo, "/")
	head := repo
	rest := ""
	if i >= 0 {
		head, rest = repo[:i], repo[i+1:]
	}
	if i < 0 || !(strings.Contains(head, ".") || strings.Contains(head, ":") || head == "localhost") {
		return "https://registry-1.docker.io", repo, "docker.io"
	}
	return "https://" + head, rest, head
}

func loadDockerAuth(path, registry string) *dockerAuth {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return nil
	}
	entry, ok := cfg.Auths[registry]
	if !ok {
		return nil
	}
	a := &dockerAuth{identityToken: entry.IdentityToken}
	if entry.Auth != "" {
		if decoded, err := base64.StdEncoding.DecodeString(entry.Auth); err == nil {
			if i := bytes.IndexByte(decoded, ':'); i >= 0 {
				a.username, a.password = string(decoded[:i]), string(decoded[i+1:])
			}
		}
	}
	return a
}

// registryAuthPayload encodes credentials for the engine's X-Registry-Auth
// header so daemon-side pulls can reach private registries.
func registryAuthPayload(authFile, registry string) string {
	a := loadDockerAuth(authFile, registry)
	if a == nil || (a.username == "" && a.identityToken == "") {
		return ""
	}
	cfg := struct {
		Username      string `json:"username,omitempty"`
		Password      string `json:"password,omitempty"`
		IdentityToken string `json:"identitytoken,omitempty"`
		ServerAddress string `json:"serveraddress,omitempty"`
	}{a.username, a.password, a.identityToken, registry}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func newRegistryClient(repo, authFile string) *registryClient {
	base, repoPath, host := splitRepo(repo)
	return &registryClient{
		base:     base,
		repoPath: repoPath,
		auth:     loadDockerAuth(authFile, host),
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

func (r *registryClient) token(ctx context.Context) (string, error) {
	q := url.Values{}
	q.Set("service", strings.TrimPrefix(r.base, "https://"))
	q.Set("scope", "repository:"+r.repoPath+":pull")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/token?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if r.auth != nil {
		if r.auth.username != "" {
			req.SetBasicAuth(r.auth.username, r.auth.password)
		} else if r.auth.identityToken != "" {
			req.Header.Set("Authorization", "Bearer "+r.auth.identityToken)
		}
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry token: HTTP %d: %s", resp.StatusCode, limitBody(resp.Body))
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Token == "" {
		out.Token = out.AccessToken
	}
	if out.Token == "" {
		return "", fmt.Errorf("registry token: empty token")
	}
	return out.Token, nil
}

func (r *registryClient) manifestURL(ref string) string {
	return r.base + "/v2/" + r.repoPath + "/manifests/" + ref
}

// headDigest returns the registry digest of the given tag (usually latest).
func (r *registryClient) headDigest(ctx context.Context, tag string) (string, error) {
	token, err := r.token(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, r.manifestURL(tag), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", manifestAccept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry head: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry head %s: HTTP %d: %s", tag, resp.StatusCode, limitBody(resp.Body))
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry head %s: missing Docker-Content-Digest header", tag)
	}
	return strings.ToLower(digest), nil
}

// configVersionLabel resolves the OCI version label (the git short sha our CI
// stamps in) of the image a tag currently points at. Best effort: any failure
// returns an error the caller treats as "target version unknown".
func (r *registryClient) configVersionLabel(ctx context.Context, tag string) (string, error) {
	token, err := r.token(ctx)
	if err != nil {
		return "", err
	}
	auth := func(req *http.Request) {
		req.Header.Set("Accept", manifestAccept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.manifestURL(tag), nil)
	if err != nil {
		return "", err
	}
	auth(req)
	resp, err := r.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("registry manifest %s: HTTP %d", tag, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	configDigest, err := resolveConfigDigest(raw)
	if err != nil {
		return "", err
	}

	blobReq, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/v2/"+r.repoPath+"/blobs/"+configDigest, nil)
	if err != nil {
		return "", err
	}
	auth(blobReq)
	blobResp, err := r.http.Do(blobReq)
	if err != nil {
		return "", err
	}
	defer blobResp.Body.Close()
	if blobResp.StatusCode >= 300 {
		return "", fmt.Errorf("registry blob %s: HTTP %d", configDigest, blobResp.StatusCode)
	}
	var imgCfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.NewDecoder(io.LimitReader(blobResp.Body, 1<<20)).Decode(&imgCfg); err != nil {
		return "", err
	}
	return imgCfg.Config.Labels[versionLabel], nil
}

// resolveConfigDigest digs the image config blob digest out of either a
// manifest list (picking linux/amd64) or a plain manifest.
func resolveConfigDigest(raw []byte) (string, error) {
	var index struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform *struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return "", err
	}
	if len(index.Manifests) == 0 {
		return "", fmt.Errorf("registry manifest: no manifests")
	}
	if index.Manifests[0].Platform == nil {
		return index.Manifests[0].Digest, nil
	}
	pick := ""
	for _, m := range index.Manifests {
		if m.Platform != nil && m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			pick = m.Digest
			break
		}
	}
	if pick == "" {
		return "", fmt.Errorf("registry manifest: no linux/amd64 entry")
	}
	return pick, nil
}

// repoFromImage picks the registry repo ("ghcr.io/xixiklow/simple-up-manage")
// from an image's repo digests, falling back to the container's original ref.
func repoFromImage(img *imageInspect, fallback string) string {
	for _, d := range img.RepoDigests {
		if i := strings.Index(d, "@"); i > 0 && strings.HasPrefix(d, "ghcr.io/") {
			return d[:i]
		}
	}
	for _, d := range img.RepoDigests {
		if i := strings.Index(d, "@"); i > 0 {
			return d[:i]
		}
	}
	return fallback
}

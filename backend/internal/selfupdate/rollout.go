package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"simple-up-manage/internal/buildinfo"
)

// run executes the blue-green rollout. Callers must hold s.mu for its whole
// duration (StartUpdate releases it via defer).
//
// Sequence: pull new image → recreate the peer container from the new image
// (cloning this container's config) → wait for its /health to report the
// expected version → rewrite the shared Caddyfile and reload Caddy → record
// state → mark done and SIGTERM this (old) container so the existing drain
// path finishes its 310s graceful shutdown.
func (s *Service) run(target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	self, err := s.inspectSelf(ctx)
	if err != nil {
		s.fail("定位自身容器失败", err, target)
		return
	}
	selfName := strings.TrimPrefix(self.Name, "/")
	peerName := s.cfg.PeerName
	peerColor := otherColor(s.cfg.Color)

	imgSelf, err := s.docker.inspectImage(ctx, self.Image)
	if err != nil {
		s.fail("读取自身镜像信息失败", err, target)
		return
	}
	repo := repoFromImage(imgSelf, self.Config.Image)

	var tag, expected string
	var strict bool
	switch {
	case target == "" || target == "latest":
		tag = latestTag
		expected = s.lastCheck().TargetVersion
	case target == "previous":
		state := s.loadState()
		if state == nil || state.PreviousVersion == "" {
			s.fail("没有可回滚的历史版本", nil, target)
			return
		}
		tag, expected, strict = state.PreviousVersion, state.PreviousVersion, true
	default:
		tag, expected, strict = target, target, true
	}

	s.setPhase("pulling", fmt.Sprintf("拉取镜像 %s:%s", repo, tag), target)
	if err := s.docker.pullImage(ctx, repo, tag, s.logTail); err != nil {
		s.fail("拉取镜像失败", err, target)
		return
	}
	img, err := s.docker.inspectImage(ctx, repo+":"+tag)
	if err != nil {
		s.fail("确认新镜像失败", err, target)
		return
	}

	s.setPhase("creating", fmt.Sprintf("创建新容器 %s（%s）", peerName, peerColor), target)
	if err := s.docker.removeContainer(ctx, peerName, true); err != nil {
		log.Printf("self-update: 清理旧 %s 容器: %v", peerName, err)
	}
	createReq := s.cloneCreateRequest(self, img, peerColor, selfName)
	peerID, err := s.docker.createContainer(ctx, peerName, createReq)
	if err != nil {
		s.fail("创建新容器失败", err, target)
		return
	}
	if err := s.docker.startContainer(ctx, peerID); err != nil {
		s.fail("启动新容器失败", err, target)
		_ = s.docker.removeContainer(ctx, peerName, true)
		return
	}

	s.setPhase("waiting", fmt.Sprintf("等待新实例就绪（最长 %s）", s.cfg.WaitTimeout), target)
	if err := s.waitPeer(ctx, peerName, expected, strict); err != nil {
		s.fail("新实例未就绪，已放弃切换（旧实例继续服务）", err, target)
		_ = s.docker.stopContainer(ctx, peerID, 10)
		_ = s.docker.removeContainer(ctx, peerName, true)
		return
	}

	s.setPhase("switching", "切换 Caddy 上游至 "+peerName, target)
	if _, err := rewriteCaddyfile(s.cfg.CaddyfilePath, []string{selfName, peerName}, peerName); err != nil {
		s.fail("改写 Caddyfile 失败", err, target)
		return
	}
	if out, code, err := s.docker.execInContainer(ctx, s.cfg.CaddyContainer, []string{"/usr/bin/caddy", "reload", "--config", "/etc/caddy/Caddyfile"}); err != nil || code != 0 {
		s.fail("Caddy reload 失败", fmt.Errorf("exit=%d out=%s err=%v", code, strings.TrimSpace(out), err), target)
		return
	}

	if err := s.saveState(StateFile{
		ActiveColor:     peerColor,
		CurrentVersion:  firstNonEmpty(expected, tag),
		PreviousVersion: buildinfo.Version,
		UpdatedAt:       time.Now(),
	}); err != nil {
		log.Printf("self-update: 保存 state 失败: %v", err)
	}

	s.setPhase("done", "切换完成，旧实例将在后台排空退出", target)
	time.Sleep(time.Second) // let the reload settle before this container gets SIGTERM
	go func() {
		// Stopping ourselves: the call normally never returns because the
		// drain in cmd/server tears the process down — that is the point.
		for attempt := 1; attempt <= 3; attempt++ {
			if err := s.docker.stopContainer(context.Background(), self.ID, int(s.cfg.StopTimeout.Seconds())); err == nil {
				return
			} else {
				log.Printf("self-update: 停止旧实例失败 (attempt %d): %v", attempt, err)
				time.Sleep(5 * time.Second)
			}
		}
		log.Printf("self-update: 严重警告: 旧实例(%s)未成功停止，其定时任务仍在运行，请人工执行 docker stop %s", selfName, selfName)
	}()
}

// waitPeer polls the peer's /health until it reports ready. With strict mode
// the reported version must match the expected sha.
func (s *Service) waitPeer(ctx context.Context, peerName, expected string, strict bool) error {
	url := fmt.Sprintf("http://%s:%s/health", peerName, s.cfg.Port)
	deadline := time.Now().Add(s.cfg.WaitTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(url)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var h struct {
					Status  string `json:"status"`
					Version string `json:"version"`
				}
				if json.Unmarshal(body, &h) == nil && h.Status == "ok" {
					if !strict || expected == "" || strings.EqualFold(strings.TrimSpace(h.Version), expected) {
						log.Printf("self-update: peer ready version=%s", h.Version)
						return nil
					}
					lastErr = fmt.Errorf("版本不匹配: 新实例 %s，期望 %s", h.Version, expected)
				} else {
					lastErr = errors.New("health 响应异常")
				}
			} else {
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("等待超时（最后错误: %v）", lastErr)
}

func (s *Service) cloneCreateRequest(self *containerInspect, img *imageInspect, peerColor, selfName string) *createRequest {
	env := cloneEnv(self.Config.Env, map[string]string{
		"COLOR":          peerColor,
		"PEER_CONTAINER": selfName,
	})
	labels := cloneLabels(self.Config.Labels)
	labels["sum.role"] = "app"
	labels["sum.color"] = peerColor

	networkMode := self.HostConfig.NetworkMode
	if networkMode == "" || networkMode == "default" {
		// compose usually sets the project network here; fall back to the
		// first attached network so the peer rejoins the same one.
		for name := range self.NetworkSettings.Networks {
			networkMode = name
			break
		}
	}

	hc := &createHostConfig{
		Binds:       self.HostConfig.Binds,
		NetworkMode: networkMode,
		ExtraHosts:  self.HostConfig.ExtraHosts,
	}
	for _, m := range self.Mounts {
		if m.Type != "volume" || m.Name == "" || m.Destination == "" {
			continue // binds are covered by HostConfig.Binds
		}
		hc.Mounts = append(hc.Mounts, mountConfig{Type: "volume", Source: m.Name, Target: m.Destination, ReadOnly: m.ReadOnly})
	}
	if self.HostConfig.RestartPolicy != nil && self.HostConfig.RestartPolicy.Name != "" {
		hc.RestartPolicy = &restartPolicy{Name: self.HostConfig.RestartPolicy.Name}
	}
	if self.HostConfig.LogConfig != nil && self.HostConfig.LogConfig.Type != "" {
		hc.LogConfig = &logConfig{Type: self.HostConfig.LogConfig.Type, Config: self.HostConfig.LogConfig.Config}
	}
	return &createRequest{Image: img.ID, Env: env, Labels: labels, Healthcheck: self.Config.Healthcheck, HostConfig: hc}
}

// cloneEnv copies env and applies key overrides.
func cloneEnv(env []string, overrides map[string]string) []string {
	out := make([]string, 0, len(env))
	seen := make(map[string]bool, len(env))
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if replacement, override := overrides[key]; override {
			if replacement == "" {
				seen[key] = true
				continue
			}
			out = append(out, key+"="+replacement)
			seen[key] = true
			continue
		}
		out = append(out, kv)
	}
	for key, value := range overrides {
		if !seen[key] && value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// cloneLabels copies labels, dropping compose management labels so the
// engine-created peer is not fought over by future `docker compose` runs.
func cloneLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+2)
	for k, v := range labels {
		if strings.HasPrefix(k, "com.docker.compose.") {
			continue
		}
		out[k] = v
	}
	return out
}

// rewriteCaddyfile swaps the active upstream in the shared Caddyfile to the
// peer container name. names are the two blue/green container names; any of
// them followed by an optional :port is replaced. Idempotent: returns
// changed=false when the file already points at the peer.
func rewriteCaddyfile(path string, names []string, peerName string) (bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, regexp.QuoteMeta(n))
	}
	re, err := regexp.Compile(`(` + strings.Join(quoted, "|") + `)(:\d+)?`)
	if err != nil {
		return false, err
	}
	next := re.ReplaceAllStringFunc(string(raw), func(m string) string {
		port := ":8080"
		if i := strings.LastIndex(m, ":"); i >= 0 {
			port = m[i:]
		}
		return peerName + port
	})
	if next == string(raw) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func (s *Service) fail(message string, err error, target string) {
	if err != nil {
		message = fmt.Sprintf("%s: %v", message, err)
	}
	s.setPhase("failed", message, target)
}

func (s *Service) lastCheck() CheckResult {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	return s.lastCheckRes
}

func otherColor(c string) string {
	if c == "blue" {
		return "green"
	}
	return "blue"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

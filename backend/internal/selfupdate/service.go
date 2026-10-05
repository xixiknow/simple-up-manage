package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"simple-up-manage/internal/buildinfo"

	"github.com/redis/go-redis/v9"
)

const (
	statusKey    = "selfupdate:status"
	lockKey      = "selfupdate:lock"
	logTailLimit = 12
)

// Config comes from the deployment environment (compose.prod.yml).
type Config struct {
	SocketPath     string
	Color          string // blue | green
	Port           string // app listen port, from LISTEN
	PeerName       string // peer container name, e.g. sum-app-green
	CaddyContainer string
	CaddyfilePath  string
	StatePath      string
	AuthFilePath   string // docker config.json for private registry pulls
	StopTimeout    time.Duration
	WaitTimeout    time.Duration
	DatabaseURL    string
}

func ConfigFromEnv(databaseURL string) Config {
	color := strings.ToLower(strings.TrimSpace(os.Getenv("COLOR")))
	port := "8080"
	if i := strings.LastIndex(os.Getenv("LISTEN"), ":"); i >= 0 && i < len(os.Getenv("LISTEN"))-1 {
		port = os.Getenv("LISTEN")[i+1:]
	}
	return Config{
		SocketPath:     envDefault("DOCKER_SOCKET", "/var/run/docker.sock"),
		Color:          color,
		Port:           port,
		PeerName:       strings.TrimSpace(os.Getenv("PEER_CONTAINER")),
		CaddyContainer: strings.TrimSpace(os.Getenv("CADDY_CONTAINER")),
		CaddyfilePath:  strings.TrimSpace(os.Getenv("CADDYFILE_PATH")),
		StatePath:      envDefault("STATE_FILE", "/app/caddy/state.json"),
		AuthFilePath:   os.Getenv("REGISTRY_AUTH_FILE"),
		StopTimeout:    330 * time.Second,
		WaitTimeout:    150 * time.Second,
		DatabaseURL:    databaseURL,
	}
}

func envDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

type capability struct {
	OK     bool
	Reason string
}

func (c Config) capability() capability {
	if c.Color != "blue" && c.Color != "green" {
		return capability{false, "未在蓝绿部署中运行（缺少 COLOR 环境变量）"}
	}
	if c.PeerName == "" || c.CaddyContainer == "" || c.CaddyfilePath == "" {
		return capability{false, "缺少 PEER_CONTAINER / CADDY_CONTAINER / CADDYFILE_PATH 配置"}
	}
	if _, err := os.Stat(c.SocketPath); err != nil {
		return capability{false, "未挂载 docker socket，无法编排容器"}
	}
	if !strings.HasPrefix(c.DatabaseURL, "postgres://") && !strings.HasPrefix(c.DatabaseURL, "postgresql://") {
		return capability{false, "仅 PostgreSQL 存储支持双实例更新"}
	}
	return capability{true, ""}
}

// CheckResult is the outcome of comparing the running image against the
// registry's latest tag.
type CheckResult struct {
	Repo            string    `json:"repo"`
	Current         string    `json:"current"`
	Latest          string    `json:"latest"`
	TargetVersion   string    `json:"target_version"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at"`
	Error           string    `json:"error,omitempty"`
}

// Status is the rollout progress. Phases: idle, pulling, creating, waiting,
// switching, done, failed.
type Status struct {
	Phase     string     `json:"phase"`
	Message   string     `json:"message,omitempty"`
	Target    string     `json:"target,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
	LogTail   []string   `json:"log_tail,omitempty"`
}

func (s Status) Active() bool {
	switch s.Phase {
	case "pulling", "creating", "waiting", "switching":
		return true
	}
	return false
}

// VersionInfo is the admin API payload for GET /system/version.
type VersionInfo struct {
	Version           string     `json:"version"`
	Color             string     `json:"color"`
	Latest            string     `json:"latest,omitempty"`
	TargetVersion     string     `json:"target_version,omitempty"`
	UpdateAvailable   bool       `json:"update_available"`
	CheckedAt         *time.Time `json:"checked_at,omitempty"`
	CheckError        string     `json:"check_error,omitempty"`
	CanSelfUpdate     bool       `json:"can_self_update"`
	UnsupportedReason string     `json:"unsupported_reason,omitempty"`
	RollbackTo        string     `json:"rollback_to,omitempty"`
}

// StateFile records the active color and version history across switches.
// It lives on the shared ./caddy host mount so the successor instance can
// offer rollback.
type StateFile struct {
	ActiveColor     string    `json:"active_color"`
	CurrentVersion  string    `json:"current_version"`
	PreviousVersion string    `json:"previous_version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Service struct {
	cfg    Config
	cap    capability
	rdb    *redis.Client
	docker *dockerClient

	mu        sync.Mutex
	status    Status
	lockToken string
	// runMu guards one rollout at a time. s.mu must never be held across the
	// rollout goroutine: setPhase/Status re-lock it, and Go mutexes are not
	// reentrant — holding it here deadlocked the rollout at its first phase
	// change and hung every update request and status poller behind it.
	runMu sync.Mutex

	checkMu      sync.Mutex
	lastCheckRes CheckResult
}

func New(cfg Config, rdb *redis.Client) *Service {
	cap := cfg.capability()
	s := &Service{cfg: cfg, cap: cap, rdb: rdb}
	if cap.OK {
		s.docker = newDockerClient(cfg.SocketPath)
		s.docker.auth = registryAuthPayload(cfg.AuthFilePath, ghcrRegistry)
	}
	s.status = Status{Phase: "idle", UpdatedAt: time.Now()}
	return s
}

func (s *Service) Capability() (bool, string) { return s.cap.OK, s.cap.Reason }

// Info assembles the payload for the frontend version display.
func (s *Service) Info() VersionInfo {
	info := VersionInfo{
		Version:           buildinfo.Version,
		Color:             s.cfg.Color,
		CanSelfUpdate:     s.cap.OK,
		UnsupportedReason: s.cap.Reason,
	}
	s.checkMu.Lock()
	lastRes := s.lastCheckRes
	s.checkMu.Unlock()
	info.Latest = lastRes.Latest
	info.TargetVersion = lastRes.TargetVersion
	info.UpdateAvailable = lastRes.UpdateAvailable
	if lastRes.Error != "" {
		info.CheckError = lastRes.Error
	}
	if !lastRes.CheckedAt.IsZero() {
		info.CheckedAt = &lastRes.CheckedAt
	}
	if state := s.loadState(); state != nil && state.PreviousVersion != "" && state.PreviousVersion != buildinfo.Version {
		info.RollbackTo = state.PreviousVersion
	}
	return info
}

// CheckNow compares the running image digest against the registry's latest.
func (s *Service) CheckNow(ctx context.Context) CheckResult {
	res := CheckResult{CheckedAt: time.Now()}
	if err := s.runCheck(ctx, &res); err != nil {
		res.Error = err.Error()
	}
	s.checkMu.Lock()
	s.lastCheckRes = res
	s.checkMu.Unlock()
	log.Printf("self-update check: available=%v current=%.12s latest=%.12s target=%q err=%q",
		res.UpdateAvailable, res.Current, res.Latest, res.TargetVersion, res.Error)
	return res
}

func (s *Service) runCheck(ctx context.Context, res *CheckResult) error {
	if !s.cap.OK {
		return errors.New(s.cap.Reason)
	}
	self, err := s.inspectSelf(ctx)
	if err != nil {
		return err
	}
	img, err := s.docker.inspectImage(ctx, self.Image)
	if err != nil {
		return fmt.Errorf("inspect self image: %w", err)
	}
	repo := repoFromImage(img, self.Config.Image)
	res.Repo = repo
	res.Current = pickDigest(img.RepoDigests, repo)

	reg := newRegistryClient(repo, s.cfg.AuthFilePath)
	latest, err := reg.headDigest(ctx, latestTag)
	if err != nil {
		return err
	}
	res.Latest = latest
	res.UpdateAvailable = latest != "" && latest != res.Current
	// Target version is best effort; it only feeds the confirm dialog.
	if ver, err := reg.configVersionLabel(ctx, latestTag); err == nil {
		res.TargetVersion = ver
	}
	return nil
}

func pickDigest(digests []string, repo string) string {
	for _, d := range digests {
		if strings.HasPrefix(d, repo+"@") {
			return strings.ToLower(d[len(repo)+1:])
		}
	}
	return ""
}

func (s *Service) inspectSelf(ctx context.Context) (*containerInspect, error) {
	id := os.Getenv("HOSTNAME")
	if id == "" {
		return nil, errors.New("HOSTNAME 未设置，无法定位自身容器")
	}
	self, err := s.docker.inspectContainer(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("inspect self: %w", err)
	}
	return self, nil
}

// StartChecker runs an immediate (deferred past startup) and hourly registry
// check until stop is closed.
func (s *Service) StartChecker(stop <-chan struct{}) {
	if !s.cap.OK {
		return
	}
	go func() {
		select {
		case <-time.After(20 * time.Second):
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			s.CheckNow(ctx)
			cancel()
		case <-stop:
			return
		}
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				s.CheckNow(ctx)
				cancel()
			case <-stop:
				return
			}
		}
	}()
}

// StartUpdate validates the request and kicks off the rollout in the
// background. The runMu TryLock plus a Redis lock prevent concurrent rollouts
// across instances. The goroutine must not inherit s.mu — see the runMu comment.
func (s *Service) StartUpdate(target string) error {
	if !s.cap.OK {
		return fmt.Errorf("self-update 不可用: %s", s.cap.Reason)
	}
	if !s.runMu.TryLock() {
		return errors.New("已有更新正在进行中")
	}
	s.mu.Lock()
	s.status = Status{Phase: "idle", UpdatedAt: time.Now()}
	s.mu.Unlock()
	go func() {
		defer s.runMu.Unlock()
		defer s.releaseLock()
		if !s.acquireLock() {
			s.setPhase("failed", "另一个实例正在执行更新", target)
			return
		}
		s.run(target)
	}()
	return nil
}

func (s *Service) acquireLock() bool {
	if s.rdb == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	token := s.cfg.Color + ":" + fmt.Sprint(time.Now().UnixNano())
	ok, err := s.rdb.SetNX(ctx, lockKey, token, 15*time.Minute).Result()
	if err != nil {
		log.Printf("self-update: redis lock unavailable (%v), proceeding under local lock", err)
		return true
	}
	if !ok {
		return false
	}
	s.lockToken = token
	return true
}

func (s *Service) releaseLock() {
	if s.rdb == nil || s.lockToken == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if v, err := s.rdb.Get(ctx, lockKey).Result(); err == nil && v == s.lockToken {
		s.rdb.Del(ctx, lockKey)
	}
	s.lockToken = ""
}

// Status reports rollout progress. Redis wins over local state so the poller
// keeps seeing progress after traffic has switched to the successor instance.
func (s *Service) Status() Status {
	if s.rdb != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		raw, err := s.rdb.Get(ctx, statusKey).Result()
		cancel()
		if err == nil {
			var st Status
			if json.Unmarshal([]byte(raw), &st) == nil && st.Phase != "" {
				if !st.Active() && time.Since(st.UpdatedAt) > time.Hour {
					return s.localStatus()
				}
				return st
			}
		}
	}
	return s.localStatus()
}

func (s *Service) localStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) setPhase(phase, message, target string) {
	s.mu.Lock()
	now := time.Now()
	if s.status.Phase == "idle" || s.status.StartedAt == nil {
		s.status.StartedAt = &now
	}
	s.status.Phase = phase
	s.status.Message = message
	s.status.Target = target
	s.status.UpdatedAt = now
	st := s.status
	s.mu.Unlock()
	log.Printf("self-update: phase=%s %s", phase, message)
	s.publishStatus(st)
}

func (s *Service) logTail(line string) {
	if line == "" {
		return
	}
	s.mu.Lock()
	s.status.LogTail = append(s.status.LogTail, line)
	if len(s.status.LogTail) > logTailLimit {
		s.status.LogTail = s.status.LogTail[len(s.status.LogTail)-logTailLimit:]
	}
	s.mu.Unlock()
}

func (s *Service) publishStatus(st Status) {
	if s.rdb == nil {
		return
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.rdb.Set(ctx, statusKey, raw, 24*time.Hour).Err(); err != nil {
		log.Printf("self-update: publish status: %v", err)
	}
}

func (s *Service) loadState() *StateFile {
	raw, err := os.ReadFile(s.cfg.StatePath)
	if err != nil {
		return nil
	}
	var st StateFile
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	return &st
}

func (s *Service) saveState(st StateFile) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.cfg.StatePath, raw, 0o644)
}

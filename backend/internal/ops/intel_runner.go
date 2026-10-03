package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"

	"gorm.io/gorm"
)

const (
	intelMaxParallel      = 8
	intelResultsPerPlan   = 1000
	intelResultAge        = 30 * 24 * time.Hour
	intelStaleRunAge      = 15 * time.Minute
	intelPelicanTimeout   = 180 * time.Second
	intelCandyTimeout     = 90 * time.Second
	intelPelicanMaxTokens = 8192
	intelCandyMaxTokens   = 1024
)

// ErrIntelRunInProgress reports that the plan already has an active run.
var ErrIntelRunInProgress = errors.New("intel test run already in progress")

// intelRunning guards one concurrent run per plan across Service clones and
// overlapping triggers (ticker + manual button).
var intelRunning sync.Map

// RunIntelPlan starts one execution of a plan asynchronously and returns the
// created run. The actual samples run on context.Background so the job ticker's
// 9-minute window can never kill a long pelican generation; progress is polled
// through the runs endpoint.
func (s *Service) RunIntelPlan(planID uint) (*domain.IntelTestRun, error) {
	var plan domain.IntelTestPlan
	if err := s.DB.First(&plan, planID).Error; err != nil {
		return nil, err
	}
	s.finalizeStaleIntelRuns()
	if _, busy := intelRunning.LoadOrStore(planID, struct{}{}); busy {
		return nil, ErrIntelRunInProgress
	}
	var running int64
	if err := s.DB.Model(&domain.IntelTestRun{}).Where("plan_id = ? AND status = ?", planID, domain.IntelRunRunning).Count(&running).Error; err != nil {
		intelRunning.Delete(planID)
		return nil, err
	}
	if running > 0 {
		intelRunning.Delete(planID)
		return nil, ErrIntelRunInProgress
	}
	keys, err := s.intelPlanKeys(&plan)
	if err != nil {
		intelRunning.Delete(planID)
		return nil, err
	}
	// Full runs always carry the plan's quarantined keys so manual triggers
	// advance their recovery evaluation too.
	if states, err := s.intelQuarantinedStates(planID, false); err == nil {
		keys = appendIntelQuarantineKeys(keys, s.intelQuarantineKeys(&plan, states))
	}
	now := time.Now()
	run := domain.IntelTestRun{PlanID: planID, Status: domain.IntelRunRunning, Scope: domain.IntelRunScopeFull, Total: len(keys), StartedAt: now}
	if err := s.DB.Create(&run).Error; err != nil {
		intelRunning.Delete(planID)
		return nil, err
	}
	go s.runIntelSamples(plan, run.ID, keys, domain.IntelRunScopeFull)
	return &run, nil
}

// RunIntelQuarantine retests the quarantined keys of a plan whose backoff
// timer is due. It creates no run when nothing is due and returns (nil, nil).
func (s *Service) RunIntelQuarantine(planID uint) (*domain.IntelTestRun, error) {
	var plan domain.IntelTestPlan
	if err := s.DB.First(&plan, planID).Error; err != nil {
		return nil, err
	}
	if !plan.QuarantineEnabled || plan.QuestionKind != domain.IntelQuestionCandy {
		return nil, nil
	}
	s.finalizeStaleIntelRuns()
	if _, busy := intelRunning.LoadOrStore(planID, struct{}{}); busy {
		return nil, ErrIntelRunInProgress
	}
	states, err := s.intelQuarantinedStates(planID, true)
	if err != nil {
		intelRunning.Delete(planID)
		return nil, err
	}
	keys := s.intelQuarantineKeys(&plan, states)
	if len(keys) == 0 {
		// Nothing testable is due (key deleted / disabled): push the due
		// schedule out so the scanner stops re-firing every tick.
		next := time.Now().Add(time.Duration(intelQuarantineBaseSec) * time.Second)
		_ = s.DB.Model(&domain.IntelQuarantineState{}).
			Where("plan_id = ? AND status = ?", planID, domain.IntelQuarantineQuarantined).
			Where("next_test_at IS NULL OR next_test_at <= ?", time.Now()).
			Update("next_test_at", next).Error
		intelRunning.Delete(planID)
		return nil, nil
	}
	now := time.Now()
	run := domain.IntelTestRun{PlanID: planID, Status: domain.IntelRunRunning, Scope: domain.IntelRunScopeQuarantine, Total: len(keys), StartedAt: now}
	if err := s.DB.Create(&run).Error; err != nil {
		intelRunning.Delete(planID)
		return nil, err
	}
	go s.runIntelSamples(plan, run.ID, keys, domain.IntelRunScopeQuarantine)
	return &run, nil
}

// intelPlanKeys resolves the enabled member keys of the plan's group that
// support the plan's model.
func (s *Service) intelPlanKeys(plan *domain.IntelTestPlan) ([]domain.PlatformKey, error) {
	var keys []domain.PlatformKey
	err := s.DB.
		Joins("JOIN route_group_keys rgk ON rgk.platform_key_id = platform_keys.id").
		Preload("Upstream").
		Where("rgk.route_group_id = ? AND platform_keys.status = ?", plan.RouteGroupID, domain.StatusEnabled).
		Find(&keys).Error
	if err != nil {
		return nil, err
	}
	out := keys[:0]
	for i := range keys {
		k := &keys[i]
		if k.Upstream == nil || k.Upstream.Status != domain.StatusEnabled {
			continue
		}
		if !k.SupportsModel(plan.Model) {
			continue
		}
		out = append(out, *k)
	}
	return out, nil
}

func (s *Service) runIntelSamples(plan domain.IntelTestPlan, runID uint, keys []domain.PlatformKey, scope string) {
	defer intelRunning.Delete(plan.ID)
	ctx := context.Background()
	prompt := IntelQuestionText(plan.QuestionKind, plan.Prompt)
	successVerdict := domain.IntelSuccessVerdict(plan.QuestionKind)

	parallel := plan.Parallel
	if parallel < 1 {
		parallel = 1
	}
	if parallel > intelMaxParallel {
		parallel = intelMaxParallel
	}
	if parallel > len(keys) {
		parallel = len(keys)
	}

	queue := make(chan *domain.PlatformKey)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, success := 0, 0
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range queue {
				res := s.intelSample(ctx, plan, runID, key, prompt)
				s.applyIntelOutcome(&plan, key, &res)
				mu.Lock()
				done++
				if res.Verdict == successVerdict {
					success++
				}
				mu.Unlock()
			}
		}()
	}
	for i := range keys {
		queue <- &keys[i]
	}
	close(queue)
	wg.Wait()

	now := time.Now()
	if err := s.DB.Model(&domain.IntelTestRun{}).Where("id = ?", runID).Updates(map[string]any{
		"status": domain.IntelRunFinished, "done": done, "success": success, "finished_at": now,
	}).Error; err != nil {
		log.Printf("intel run %d finalize: %v", runID, err)
	}
	// Quarantine retests run on their own per-key backoff schedule and must
	// not advance the plan's full-run cadence.
	if scope == domain.IntelRunScopeFull {
		updates := map[string]any{"last_run_at": now, "next_run_at": nil}
		if plan.Enabled && plan.IntervalMinutes >= domain.IntelMinIntervalMinutes {
			updates["next_run_at"] = now.Add(time.Duration(plan.IntervalMinutes) * time.Minute)
		}
		if err := s.DB.Model(&domain.IntelTestPlan{}).Where("id = ?", plan.ID).Updates(updates).Error; err != nil {
			log.Printf("intel plan %d schedule: %v", plan.ID, err)
		}
	}
	s.pruneIntelPlan(plan.ID)
}

// intelSample probes one key with the plan question and persists the judged
// result bound to group-provider-key-model.
func (s *Service) intelSample(ctx context.Context, plan domain.IntelTestPlan, runID uint, key *domain.PlatformKey, prompt string) domain.IntelTestResult {
	res := domain.IntelTestResult{
		RunID: runID, PlanID: plan.ID, RouteGroupID: plan.RouteGroupID,
		UpstreamID: key.UpstreamID, PlatformKeyID: key.ID,
		KeyName: key.Name, Model: plan.Model, QuestionKind: plan.QuestionKind,
		CreatedAt: time.Now(),
	}
	if key.Upstream != nil {
		res.UpstreamName = key.Upstream.Name
	}
	var text string
	save := func() domain.IntelTestResult {
		if err := s.DB.Create(&res).Error; err != nil {
			log.Printf("intel result create: %v", err)
			return res
		}
		if res.HasOutput {
			out := domain.IntelTestOutput{ResultID: res.ID, OutputText: text, CreatedAt: res.CreatedAt}
			if err := s.DB.Create(&out).Error; err != nil {
				log.Printf("intel output create: %v", err)
			}
		}
		return res
	}
	fail := func(err error) domain.IntelTestResult {
		res.Verdict = domain.IntelVerdictError
		res.ErrorMessage = err.Error()
		return save()
	}

	protocol, err := intelPickProtocol(plan, key)
	if err != nil {
		return fail(err)
	}
	res.Protocol = protocol
	apiKey, err := s.decrypt(key)
	if err != nil {
		return fail(err)
	}

	timeout := intelCandyTimeout
	maxTokens := intelCandyMaxTokens
	if plan.QuestionKind == domain.IntelQuestionPelican {
		timeout = intelPelicanTimeout
		maxTokens = intelPelicanMaxTokens
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	text, statusCode, tokens, err := s.intelRequest(reqCtx, key, apiKey, protocol, plan.Model, prompt, maxTokens)
	res.StatusCode = statusCode
	res.LatencyMs = int(time.Since(started).Milliseconds())
	res.Tokens = tokens
	if err != nil {
		return fail(err)
	}

	switch plan.QuestionKind {
	case domain.IntelQuestionPelican:
		if !IntelPelicanValid(text) {
			res.Verdict = domain.IntelVerdictInvalid
			if strings.TrimSpace(text) == "" {
				res.ErrorMessage = "模型返回空输出"
			} else {
				res.ErrorMessage = "模型未返回 HTML/SVG"
			}
			break
		}
		if len(text) > intelMaxOutputBytes {
			res.Verdict = domain.IntelVerdictInvalid
			res.ErrorMessage = "输出超过 2MB 上限，不保存截断的作品"
			break
		}
		res.Verdict = domain.IntelVerdictSuccess
		res.OutputSize = len(text)
		res.HasOutput = text != ""
	default: // candy
		trimmed := strings.TrimSpace(text)
		res.AnswerPreview = intelTruncateRunes(trimmed, 200)
		if IntelCandyCorrect(trimmed) {
			res.Verdict = domain.IntelVerdictCorrect
		} else {
			res.Verdict = domain.IntelVerdictIncorrect
			res.ErrorMessage = "答案不匹配（期望 21）"
		}
	}
	return save()
}

// intelRequest posts the question via the chat-completions / messages API and
// returns the flat text output.
func (s *Service) intelRequest(ctx context.Context, key *domain.PlatformKey, apiKey, protocol, model, prompt string, maxTokens int) (string, int, int, error) {
	messages := []map[string]string{{"role": "user", "content": prompt}}
	var path string
	var body map[string]any
	var extra http.Header
	if protocol == domain.ProtocolAnthropic {
		path = "/v1/messages"
		body = map[string]any{"model": model, "max_tokens": maxTokens, "stream": false, "messages": messages}
		extra = upstream.AnthropicHeaders(apiKey)
	} else {
		path = "/v1/chat/completions"
		body = map[string]any{"model": model, "max_tokens": maxTokens, "stream": false, "messages": messages}
	}
	// The shared client times out at 10s; clone with headroom above the caller's
	// per-sample context deadline so long pelican generations survive.
	client := s.Client.WithTimeout(intelRequestClientTimeout(ctx))
	res, err := client.PostJSON(ctx, key.Upstream.BaseURL, path, apiKey, body, extra)
	if err != nil {
		return "", 0, 0, err
	}
	if res.Status < 200 || res.Status >= 300 {
		snippet := intelTruncateRunes(strings.TrimSpace(string(res.Body)), 300)
		return "", res.Status, 0, fmt.Errorf("HTTP %d: %s", res.Status, snippet)
	}
	if protocol == domain.ProtocolAnthropic {
		var out struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil {
			return "", res.Status, 0, fmt.Errorf("响应解析失败: %w", err)
		}
		var text strings.Builder
		for _, part := range out.Content {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
		return text.String(), res.Status, out.Usage.InputTokens + out.Usage.OutputTokens, nil
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return "", res.Status, 0, fmt.Errorf("响应解析失败: %w", err)
	}
	text := ""
	if len(out.Choices) > 0 {
		switch v := out.Choices[0].Message.Content.(type) {
		case string:
			text = v
		case []any:
			var parts strings.Builder
			for _, item := range v {
				if obj, ok := item.(map[string]any); ok {
					if t, ok := obj["text"].(string); ok {
						parts.WriteString(t)
					}
				}
			}
			text = parts.String()
		}
	}
	return text, res.Status, out.Usage.TotalTokens, nil
}

func intelRequestClientTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline) + 15*time.Second
	}
	return 5 * time.Minute
}

func intelPickProtocol(plan domain.IntelTestPlan, key *domain.PlatformKey) (string, error) {
	if plan.Protocol != "" {
		if key.SupportsProtocol(plan.Protocol) {
			return plan.Protocol, nil
		}
		return "", fmt.Errorf("该 Key 不支持协议 %s", plan.Protocol)
	}
	if key.SupportsProtocol(domain.ProtocolOpenAI) {
		return domain.ProtocolOpenAI, nil
	}
	if key.SupportsProtocol(domain.ProtocolAnthropic) {
		return domain.ProtocolAnthropic, nil
	}
	return "", errors.New("key has no effective protocol")
}

// RunDueIntelPlans fires due scheduled plans; called from the minute ticker.
func (s *Service) RunDueIntelPlans() {
	s.finalizeStaleIntelRuns()
	var plans []domain.IntelTestPlan
	now := time.Now()
	if err := s.DB.Where("enabled = ? AND interval_minutes >= ? AND (next_run_at IS NULL OR next_run_at <= ?)", true, domain.IntelMinIntervalMinutes, now).Find(&plans).Error; err != nil {
		return
	}
	for i := range plans {
		plan := plans[i]
		if _, busy := intelRunning.Load(plan.ID); busy {
			continue
		}
		if _, err := s.RunIntelPlan(plan.ID); err != nil {
			if !errors.Is(err, ErrIntelRunInProgress) {
				log.Printf("intel plan %d scheduled run: %v", plan.ID, err)
			}
		} else {
			log.Printf("intel plan %d (%s %s) scheduled run started", plan.ID, plan.QuestionKind, plan.Model)
		}
	}
	// Quarantine retests: plans with a due quarantined key, regardless of the
	// plan's own full-run cadence.
	for _, planID := range s.runIntelQuarantineDue() {
		if _, busy := intelRunning.Load(planID); busy {
			continue
		}
		if _, err := s.RunIntelQuarantine(planID); err != nil && !errors.Is(err, ErrIntelRunInProgress) {
			log.Printf("intel plan %d quarantine run: %v", planID, err)
		}
	}
}

// finalizeStaleIntelRuns closes run rows left "running" by a crash or restart.
func (s *Service) finalizeStaleIntelRuns() {
	cutoff := time.Now().Add(-intelStaleRunAge)
	if err := s.DB.Model(&domain.IntelTestRun{}).
		Where("status = ? AND started_at < ?", domain.IntelRunRunning, cutoff).
		Updates(map[string]any{"status": domain.IntelRunFinished, "finished_at": time.Now()}).Error; err != nil {
		log.Printf("intel stale runs: %v", err)
	}
}

// pruneIntelPlan caps each plan's history: newest 1000 results, nothing older
// than 30 days, and no orphaned outputs.
func (s *Service) pruneIntelPlan(planID uint) {
	cutoff := time.Now().Add(-intelResultAge)
	if err := s.DB.Where("plan_id = ? AND created_at < ?", planID, cutoff).Delete(&domain.IntelTestResult{}).Error; err != nil {
		log.Printf("intel prune age: %v", err)
	}
	var threshold domain.IntelTestResult
	err := s.DB.Where("plan_id = ?", planID).Order("id DESC").Offset(intelResultsPerPlan).First(&threshold).Error
	if err == nil {
		if err := s.DB.Where("plan_id = ? AND id <= ?", planID, threshold.ID).Delete(&domain.IntelTestResult{}).Error; err != nil {
			log.Printf("intel prune count: %v", err)
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("intel prune threshold: %v", err)
	}
	if err := s.DB.Exec("DELETE FROM intel_test_outputs WHERE result_id NOT IN (SELECT id FROM intel_test_results)").Error; err != nil {
		log.Printf("intel prune outputs: %v", err)
	}
}

// DeleteIntelPlanData removes every run / result / output of a plan. Any key
// still quarantined by the plan is re-added to its group first so deletion
// never strands keys outside their group.
func (s *Service) DeleteIntelPlanData(planID uint) error {
	s.RestoreIntelPlanQuarantined(planID)
	return s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", planID).Delete(&domain.IntelQuarantineState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("result_id IN (SELECT id FROM intel_test_results WHERE plan_id = ?)", planID).
			Delete(&domain.IntelTestOutput{}).Error; err != nil {
			return err
		}
		if err := tx.Where("plan_id = ?", planID).Delete(&domain.IntelTestResult{}).Error; err != nil {
			return err
		}
		return tx.Where("plan_id = ?", planID).Delete(&domain.IntelTestRun{}).Error
	})
}

func intelTruncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

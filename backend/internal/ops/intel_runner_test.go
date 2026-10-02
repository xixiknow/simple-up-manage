package ops

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
)

// newIntelEnv builds a test service with one openai upstream + key, joined into
// a route group, plus an httptest upstream whose replies come from answers.
func newIntelEnv(t *testing.T, protocols, answer string, delay time.Duration) (*Service, *domain.RouteGroup) {
	t.Helper()
	db := testDB(t)
	enc, err := crypto.New(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/chat/completions") && !strings.HasPrefix(r.URL.Path, "/v1/messages") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" && r.Header.Get("X-Api-Key") != "fixture-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		text := answer
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/messages") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"usage":   map[string]int{"input_tokens": 5, "output_tokens": 7},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": text}}},
			"usage":   map[string]int{"total_tokens": 12},
		})
	}))
	t.Cleanup(server.Close)

	up := domain.Upstream{Name: "fixture-provider", BaseURL: server.URL, Kind: domain.KindOpenAICompat, Protocols: protocols}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	secret, err := enc.Encrypt("fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	key := domain.PlatformKey{UpstreamID: up.ID, Name: "fixture-provider-main", EncryptedKey: secret, Status: domain.StatusEnabled, Protocols: protocols}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	group := domain.RouteGroup{Name: "fixture-group", Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.RouteGroupKey{RouteGroupID: group.ID, PlatformKeyID: key.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return New(db, enc, nil), &group
}

func (s *Service) intelPlan(t *testing.T, groupID uint, kind, model string) *domain.IntelTestPlan {
	t.Helper()
	plan := domain.IntelTestPlan{
		Name: "fixture-plan", RouteGroupID: groupID, Model: model, QuestionKind: kind,
		Parallel: 4, Enabled: false,
	}
	if err := s.DB.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return &plan
}

// waitIntelRun polls until the run is finished and its plan leaves the running
// guard, so package-level state never leaks between tests.
func waitIntelRun(t *testing.T, s *Service, planID, runID uint) domain.IntelTestRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var run domain.IntelTestRun
		if err := s.DB.First(&run, runID).Error; err != nil {
			t.Fatal(err)
		}
		_, busy := intelRunning.Load(planID)
		if run.Status == domain.IntelRunFinished && !busy {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d did not finish: %+v busy=%v", runID, run, busy)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitIntelIdle waits until the plan leaves the package-level running guard.
func waitIntelIdle(t *testing.T, planID uint) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, busy := intelRunning.Load(planID); !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("plan %d still marked as running", planID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIntelRunCandyEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name          string
		answer        string
		wantVerdict   string
		wantSuccess   int
		wantErrSubstr string
	}{
		{name: "correct answer", answer: "答案是21", wantVerdict: domain.IntelVerdictCorrect, wantSuccess: 1},
		{name: "wrong answer", answer: "42", wantVerdict: domain.IntelVerdictIncorrect, wantErrSubstr: "答案不匹配"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, group := newIntelEnv(t, "openai", tc.answer, 0)
			plan := s.intelPlan(t, group.ID, domain.IntelQuestionCandy, "gpt-fixture")
			run, err := s.RunIntelPlan(plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.Total != 1 {
				t.Fatalf("run total = %d, want 1", run.Total)
			}
			finished := waitIntelRun(t, s, plan.ID, run.ID)
			run = &finished
			if run.Done != 1 || run.Success != tc.wantSuccess {
				t.Fatalf("run done=%d success=%d; want done=1 success=%d", run.Done, run.Success, tc.wantSuccess)
			}
			var results []domain.IntelTestResult
			if err := s.DB.Where("run_id = ?", run.ID).Find(&results).Error; err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 {
				t.Fatalf("results = %d, want 1", len(results))
			}
			res := results[0]
			if res.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q (%s), want %q", res.Verdict, res.ErrorMessage, tc.wantVerdict)
			}
			if res.RouteGroupID != group.ID || res.UpstreamID == 0 || res.PlatformKeyID == 0 {
				t.Fatalf("binding incomplete: group=%d upstream=%d key=%d", res.RouteGroupID, res.UpstreamID, res.PlatformKeyID)
			}
			if res.KeyName != "fixture-provider-main" || res.UpstreamName != "fixture-provider" {
				t.Fatalf("name snapshots wrong: key=%q upstream=%q", res.KeyName, res.UpstreamName)
			}
			if res.Protocol != domain.ProtocolOpenAI {
				t.Fatalf("protocol = %q, want openai", res.Protocol)
			}
			if tc.wantVerdict == domain.IntelVerdictCorrect && res.AnswerPreview != "答案是21" {
				t.Fatalf("answer preview = %q", res.AnswerPreview)
			}
			if tc.wantErrSubstr != "" && !strings.Contains(res.ErrorMessage, tc.wantErrSubstr) {
				t.Fatalf("error message = %q, want substring %q", res.ErrorMessage, tc.wantErrSubstr)
			}
			var updated domain.IntelTestPlan
			if err := s.DB.First(&updated, plan.ID).Error; err != nil {
				t.Fatal(err)
			}
			if updated.LastRunAt == nil {
				t.Fatal("plan last_run_at not set")
			}
			if updated.NextRunAt != nil {
				t.Fatal("manual plan should have no next_run_at")
			}
		})
	}
}

func TestIntelRunPelicanStoresOutput(t *testing.T) {
	html := "<!DOCTYPE html><html><body><svg viewBox=\"0 0 10 10\"></svg></body></html>"
	s, group := newIntelEnv(t, "openai", html, 0)
	plan := s.intelPlan(t, group.ID, domain.IntelQuestionPelican, "gpt-fixture")
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitIntelRun(t, s, plan.ID, run.ID)
	run = &finished
	if run.Success != 1 {
		t.Fatalf("run success = %d, want 1", run.Success)
	}
	var res domain.IntelTestResult
	if err := s.DB.Where("run_id = ?", run.ID).First(&res).Error; err != nil {
		t.Fatal(err)
	}
	if res.Verdict != domain.IntelVerdictSuccess || !res.HasOutput || res.OutputSize == 0 {
		t.Fatalf("verdict=%q has_output=%v size=%d", res.Verdict, res.HasOutput, res.OutputSize)
	}
	var out domain.IntelTestOutput
	if err := s.DB.Where("result_id = ?", res.ID).First(&out).Error; err != nil {
		t.Fatalf("output row missing: %v", err)
	}
	if out.OutputText != html {
		t.Fatalf("output text mismatch: %q", out.OutputText)
	}
}

func TestIntelRunPelicanRejectsNonHTML(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "抱歉我不会画画", 0)
	plan := s.intelPlan(t, group.ID, domain.IntelQuestionPelican, "gpt-fixture")
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntelRun(t, s, plan.ID, run.ID)
	var res domain.IntelTestResult
	if err := s.DB.Where("run_id = ?", run.ID).First(&res).Error; err != nil {
		t.Fatal(err)
	}
	if res.Verdict != domain.IntelVerdictInvalid || !strings.Contains(res.ErrorMessage, "HTML/SVG") {
		t.Fatalf("verdict=%q error=%q, want invalid with HTML/SVG hint", res.Verdict, res.ErrorMessage)
	}
	if res.HasOutput {
		t.Fatal("invalid output must not be persisted")
	}
}

func TestIntelRunAnthropicProtocol(t *testing.T) {
	s, group := newIntelEnv(t, "anthropic", "21", 0)
	plan := s.intelPlan(t, group.ID, domain.IntelQuestionCandy, "claude-fixture")
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitIntelRun(t, s, plan.ID, run.ID)
	var res domain.IntelTestResult
	if err := s.DB.Where("run_id = ?", run.ID).First(&res).Error; err != nil {
		t.Fatal(err)
	}
	if res.Verdict != domain.IntelVerdictCorrect || res.Protocol != domain.ProtocolAnthropic {
		t.Fatalf("verdict=%q protocol=%q, want correct/anthropic", res.Verdict, res.Protocol)
	}
	if res.Tokens != 12 {
		t.Fatalf("tokens = %d, want 12", res.Tokens)
	}
}

func TestIntelRunInProgressGuard(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 300*time.Millisecond)
	plan := s.intelPlan(t, group.ID, domain.IntelQuestionCandy, "gpt-fixture")
	if _, err := s.RunIntelPlan(plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunIntelPlan(plan.ID); err == nil {
		t.Fatal("second overlapping run should fail")
	}
	var runs []domain.IntelTestRun
	if err := s.DB.Where("plan_id = ?", plan.ID).Find(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs created = %d, want 1", len(runs))
	}
	waitIntelIdle(t, plan.ID)
}

func TestIntelRunSkipsUnsupportedKeys(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	var key domain.PlatformKey
	if err := s.DB.Where("name = ?", "fixture-provider-main").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&key).Update("last_models", domain.JSONStrings{"other-model"}).Error; err != nil {
		t.Fatal(err)
	}
	plan := s.intelPlan(t, group.ID, domain.IntelQuestionCandy, "gpt-fixture")
	run, err := s.RunIntelPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	finished := waitIntelRun(t, s, plan.ID, run.ID)
	run = &finished
	if run.Total != 0 || run.Done != 0 {
		t.Fatalf("run total=%d done=%d, want 0/0 (key does not support model)", run.Total, run.Done)
	}
	var count int64
	if err := s.DB.Model(&domain.IntelTestResult{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("results = %d, want 0", count)
	}
}

func TestIntelRunDueSchedulesNextRun(t *testing.T) {
	s, group := newIntelEnv(t, "openai", "21", 0)
	plan := s.intelPlan(t, group.ID, domain.IntelQuestionCandy, "gpt-fixture")
	past := time.Now().Add(-time.Hour)
	if err := s.DB.Model(plan).Updates(map[string]any{"enabled": true, "interval_minutes": 15, "next_run_at": past}).Error; err != nil {
		t.Fatal(err)
	}
	s.RunDueIntelPlans()
	deadline := time.Now().Add(5 * time.Second)
	var run domain.IntelTestRun
	for {
		err := s.DB.Where("plan_id = ?", plan.ID).Order("id DESC").First(&run).Error
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduled run never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	finished := waitIntelRun(t, s, plan.ID, run.ID)
	run = finished
	var updated domain.IntelTestPlan
	if err := s.DB.First(&updated, plan.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.After(time.Now()) {
		t.Fatalf("next_run_at = %v, want future", updated.NextRunAt)
	}
	// Manual-only plans must never be picked up by the scheduler.
	if err := s.DB.Model(plan).Updates(map[string]any{"enabled": true, "interval_minutes": 0, "next_run_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	s.RunDueIntelPlans()
	time.Sleep(100 * time.Millisecond)
	var runs int64
	if err := s.DB.Model(&domain.IntelTestRun{}).Where("plan_id = ?", plan.ID).Count(&runs).Error; err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("runs = %d, want 1 (manual plan must not schedule)", runs)
	}
}

package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"simple-up-manage/internal/domain"

	"github.com/gin-gonic/gin"
)

func TestIntelPlanSummary(t *testing.T) {
	db := logTestDB(t)
	group := domain.RouteGroup{Name: "g1", Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "provider-a", BaseURL: "https://example.com", Kind: domain.KindOpenAICompat, Protocols: "openai"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	keyA := domain.PlatformKey{UpstreamID: up.ID, Name: "provider-a-fast", Status: domain.StatusEnabled}
	keyB := domain.PlatformKey{UpstreamID: up.ID, Name: "provider-a-slow", Status: domain.StatusEnabled}
	if err := db.Create(&keyA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&keyB).Error; err != nil {
		t.Fatal(err)
	}
	plan := domain.IntelTestPlan{RouteGroupID: group.ID, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Parallel: 4, Enabled: true}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	run := domain.IntelTestRun{PlanID: plan.ID, Status: domain.IntelRunFinished, Total: 4, Done: 4, Success: 2, StartedAt: time.Now(), FinishedAt: ptrTime(time.Now())}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	rows := []domain.IntelTestResult{
		{RunID: run.ID, PlanID: plan.ID, RouteGroupID: group.ID, UpstreamID: up.ID, PlatformKeyID: keyA.ID, KeyName: keyA.Name, UpstreamName: up.Name, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Verdict: domain.IntelVerdictCorrect, LatencyMs: 100, AnswerPreview: "21", Tokens: 10, CreatedAt: time.Now().Add(-3 * time.Minute)},
		{RunID: run.ID, PlanID: plan.ID, RouteGroupID: group.ID, UpstreamID: up.ID, PlatformKeyID: keyA.ID, KeyName: keyA.Name, UpstreamName: up.Name, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Verdict: domain.IntelVerdictCorrect, LatencyMs: 200, AnswerPreview: "21", Tokens: 10, CreatedAt: time.Now().Add(-2 * time.Minute)},
		{RunID: run.ID, PlanID: plan.ID, RouteGroupID: group.ID, UpstreamID: up.ID, PlatformKeyID: keyA.ID, KeyName: keyA.Name, UpstreamName: up.Name, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Verdict: domain.IntelVerdictIncorrect, LatencyMs: 50, AnswerPreview: "42", Tokens: 10, CreatedAt: time.Now().Add(-time.Minute)},
		{RunID: run.ID, PlanID: plan.ID, RouteGroupID: group.ID, UpstreamID: up.ID, PlatformKeyID: keyB.ID, KeyName: keyB.Name, UpstreamName: up.Name, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Verdict: domain.IntelVerdictError, LatencyMs: 0, ErrorMessage: "HTTP 401", Tokens: 0, CreatedAt: time.Now()},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/summary", nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatUint(uint64(plan.ID), 10)}}
	admin.IntelPlanSummary(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Items []struct {
				PlatformKeyID uint    `json:"platform_key_id"`
				Samples       int64   `json:"samples"`
				Success       int64   `json:"success"`
				Accuracy      float64 `json:"accuracy"`
				AvgLatencyMs  float64 `json:"avg_latency_ms"`
				LastVerdict   string  `json:"last_verdict"`
				LastAnswer    string  `json:"last_answer"`
				History       []struct {
					Verdict string `json:"verdict"`
				} `json:"history"`
			} `json:"items"`
			SuccessVerdict string `json:"success_verdict"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.SuccessVerdict != domain.IntelVerdictCorrect {
		t.Fatalf("success_verdict = %q", response.Data.SuccessVerdict)
	}
	if len(response.Data.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(response.Data.Items))
	}
	first := response.Data.Items[0]
	second := response.Data.Items[1]
	if first.PlatformKeyID != keyA.ID || first.Samples != 3 || first.Success != 2 {
		t.Fatalf("top row wrong: %+v", first)
	}
	if first.Accuracy < 66.6 || first.Accuracy > 66.7 {
		t.Fatalf("accuracy = %f, want ~66.7", first.Accuracy)
	}
	if first.AvgLatencyMs != 150 {
		t.Fatalf("avg latency = %f, want 150 (success rows only)", first.AvgLatencyMs)
	}
	if first.LastVerdict != domain.IntelVerdictIncorrect || first.LastAnswer != "42" {
		t.Fatalf("last verdict/answer = %q/%q", first.LastVerdict, first.LastAnswer)
	}
	if len(first.History) != 3 {
		t.Fatalf("history = %d entries, want 3", len(first.History))
	}
	if second.PlatformKeyID != keyB.ID || second.Success != 0 || len(second.History) != 1 {
		t.Fatalf("second row wrong: %+v", second)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

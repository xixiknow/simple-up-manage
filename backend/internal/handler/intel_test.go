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

func TestIntelPlanSummaryWindowedAccuracy(t *testing.T) {
	db := logTestDB(t)
	group := domain.RouteGroup{Name: "g-win", Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "provider-win", BaseURL: "https://example.com", Kind: domain.KindOpenAICompat, Protocols: "openai"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	keyA := domain.PlatformKey{UpstreamID: up.ID, Name: "win-key", Status: domain.StatusEnabled}
	keyB := domain.PlatformKey{UpstreamID: up.ID, Name: "error-key", Status: domain.StatusEnabled}
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
	newResult := func(key *domain.PlatformKey, verdict string) domain.IntelTestResult {
		return domain.IntelTestResult{
			RunID: 1, PlanID: plan.ID, RouteGroupID: group.ID, UpstreamID: up.ID,
			PlatformKeyID: key.ID, KeyName: key.Name, UpstreamName: up.Name,
			Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy,
			Verdict: verdict, LatencyMs: 100, AnswerPreview: "21", CreatedAt: time.Now(),
		}
	}
	// Key A: two oldest wrong answers, then ten correct ones. The rolling
	// window must report a perfect 100% over the newest 10 effective samples
	// instead of the all-time 10/12.
	for i := 0; i < 2; i++ {
		row := newResult(&keyA, domain.IntelVerdictIncorrect)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		row := newResult(&keyA, domain.IntelVerdictCorrect)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Key B: only a transport error — no effective samples, but still listed.
	rowB := newResult(&keyB, domain.IntelVerdictError)
	if err := db.Create(&rowB).Error; err != nil {
		t.Fatal(err)
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
				PlatformKeyID uint       `json:"platform_key_id"`
				Samples       int64      `json:"samples"`
				Success       int64      `json:"success"`
				Accuracy      float64    `json:"accuracy"`
				AvgLatencyMs  float64    `json:"avg_latency_ms"`
				History       []struct{} `json:"history"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(response.Data.Items))
	}
	byKey := map[uint]struct {
		Samples  int64
		Success  int64
		Accuracy float64
		Avg      float64
		HistLen  int
	}{}
	for _, item := range response.Data.Items {
		byKey[item.PlatformKeyID] = struct {
			Samples  int64
			Success  int64
			Accuracy float64
			Avg      float64
			HistLen  int
		}{item.Samples, item.Success, item.Accuracy, item.AvgLatencyMs, len(item.History)}
	}
	a := byKey[keyA.ID]
	if a.Samples != 10 || a.Success != 10 || a.Accuracy != 100 {
		t.Fatalf("key A windowed stats = %+v, want 10/10 = 100%%", a)
	}
	if a.Avg != 100 {
		t.Fatalf("key A avg latency = %f, want 100", a.Avg)
	}
	if a.HistLen != 12 {
		t.Fatalf("key A history = %d, want 12 (errors included)", a.HistLen)
	}
	b := byKey[keyB.ID]
	if b.Samples != 0 || b.Success != 0 {
		t.Fatalf("key B stats = %+v, want zero effective samples", b)
	}
	if b.HistLen != 1 {
		t.Fatalf("key B history = %d, want 1", b.HistLen)
	}
}

func TestIntelPlanListWindowedStats(t *testing.T) {
	db := logTestDB(t)
	group := domain.RouteGroup{Name: "g-list", Status: domain.StatusEnabled}
	if err := db.Create(&group).Error; err != nil {
		t.Fatal(err)
	}
	up := domain.Upstream{Name: "provider-list", BaseURL: "https://example.com", Kind: domain.KindOpenAICompat, Protocols: "openai"}
	if err := db.Create(&up).Error; err != nil {
		t.Fatal(err)
	}
	key := domain.PlatformKey{UpstreamID: up.ID, Name: "list-key", Status: domain.StatusEnabled}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	plan := domain.IntelTestPlan{RouteGroupID: group.ID, Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy, Parallel: 4, Enabled: true}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	newResult := func(verdict string) domain.IntelTestResult {
		return domain.IntelTestResult{
			RunID: 1, PlanID: plan.ID, RouteGroupID: group.ID, UpstreamID: up.ID,
			PlatformKeyID: key.ID, KeyName: key.Name, UpstreamName: up.Name,
			Model: "gpt-x", QuestionKind: domain.IntelQuestionCandy,
			Verdict: verdict, LatencyMs: 200, AnswerPreview: "21", CreatedAt: time.Now(),
		}
	}
	for i := 0; i < 2; i++ {
		row := newResult(domain.IntelVerdictIncorrect)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		row := newResult(domain.IntelVerdictCorrect)
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	admin := &Admin{DB: db}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/plans", nil)
	admin.ListIntelPlans(c)
	if recorder.Code != 200 {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			Items []struct {
				ID    uint `json:"id"`
				Stats struct {
					Samples  int64   `json:"samples"`
					Success  int64   `json:"success"`
					Accuracy float64 `json:"accuracy"`
				} `json:"stats"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(response.Data.Items))
	}
	st := response.Data.Items[0].Stats
	if st.Samples != 10 || st.Success != 10 || st.Accuracy != 100 {
		t.Fatalf("plan stats = %+v, want windowed 10/10 = 100%%", st)
	}
}

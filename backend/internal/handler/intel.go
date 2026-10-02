package handler

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"
	"simple-up-manage/internal/ops"

	"github.com/gin-gonic/gin"
)

type intelPlanStats struct {
	Samples      int64   `json:"samples"`
	Success      int64   `json:"success"`
	Accuracy     float64 `json:"accuracy"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

type intelPlanDTO struct {
	domain.IntelTestPlan
	GroupName string               `json:"group_name"`
	LastRun   *domain.IntelTestRun `json:"last_run"`
	Running   bool                 `json:"running"`
	Stats     intelPlanStats       `json:"stats"`
}

type intelPlanBody struct {
	Name            string `json:"name"`
	RouteGroupID    uint   `json:"route_group_id"`
	Model           string `json:"model"`
	QuestionKind    string `json:"question_kind"`
	Prompt          string `json:"prompt"`
	Protocol        string `json:"protocol"`
	IntervalMinutes int    `json:"interval_minutes"`
	Parallel        int    `json:"parallel"`
	Enabled         *bool  `json:"enabled"`
}

func (b *intelPlanBody) validate() (string, bool) {
	if b.RouteGroupID == 0 {
		return "route_group_id 不能为空", false
	}
	if strings.TrimSpace(b.Model) == "" || len(b.Model) > 128 {
		return "model 不能为空且不超过 128 字符", false
	}
	if !domain.ValidIntelQuestion(b.QuestionKind) {
		return "question_kind 必须是 candy 或 pelican", false
	}
	switch b.Protocol {
	case "", domain.ProtocolOpenAI, domain.ProtocolAnthropic:
	default:
		return "protocol 必须为空、openai 或 anthropic", false
	}
	if b.IntervalMinutes != 0 && b.IntervalMinutes < 15 {
		return "interval_minutes 为 0（仅手动）或至少 15", false
	}
	if b.Parallel == 0 {
		b.Parallel = 4
	}
	if b.Parallel < 1 || b.Parallel > 8 {
		return "parallel 取值 1-8", false
	}
	return "", true
}

func (b *intelPlanBody) apply(plan *domain.IntelTestPlan, now time.Time) {
	plan.Name = strings.TrimSpace(b.Name)
	plan.RouteGroupID = b.RouteGroupID
	plan.Model = strings.TrimSpace(b.Model)
	plan.QuestionKind = b.QuestionKind
	plan.Prompt = strings.TrimSpace(b.Prompt)
	plan.Protocol = b.Protocol
	plan.IntervalMinutes = b.IntervalMinutes
	plan.Parallel = b.Parallel
	plan.Enabled = b.Enabled == nil || *b.Enabled
	if plan.Enabled && plan.IntervalMinutes >= 15 {
		next := now.Add(time.Duration(plan.IntervalMinutes) * time.Minute)
		plan.NextRunAt = &next
	} else {
		plan.NextRunAt = nil
	}
}

func (h *Admin) ListIntelPlans(c *gin.Context) {
	var plans []domain.IntelTestPlan
	if err := h.DB.Order("id DESC").Find(&plans).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	planIDs := make([]uint, 0, len(plans))
	groupIDs := make([]uint, 0, len(plans))
	byKind := map[uint]string{}
	for _, p := range plans {
		planIDs = append(planIDs, p.ID)
		groupIDs = append(groupIDs, p.RouteGroupID)
		byKind[p.ID] = p.QuestionKind
	}
	groupNames := map[uint]string{}
	if len(groupIDs) > 0 {
		var groups []domain.RouteGroup
		if err := h.DB.Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		for _, g := range groups {
			groupNames[g.ID] = g.Name
		}
	}
	lastRuns := map[uint]*domain.IntelTestRun{}
	if len(planIDs) > 0 {
		var runs []domain.IntelTestRun
		if err := h.DB.Where("id IN (SELECT MAX(id) FROM intel_test_runs WHERE plan_id IN (?) GROUP BY plan_id)", planIDs).Find(&runs).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		for i := range runs {
			lastRuns[runs[i].PlanID] = &runs[i]
		}
	}
	stats := map[uint]intelPlanStats{}
	if len(planIDs) > 0 {
		var rows []struct {
			PlanID     uint
			Verdict    string
			Count      int64
			AvgLatency float64
		}
		if err := h.DB.Model(&domain.IntelTestResult{}).
			Select("plan_id, verdict, COUNT(*) AS count, AVG(latency_ms) AS avg_latency").
			Where("plan_id IN ?", planIDs).
			Group("plan_id, verdict").Scan(&rows).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		for _, row := range rows {
			st := stats[row.PlanID]
			st.Samples += row.Count
			if row.Verdict == domain.IntelSuccessVerdict(byKind[row.PlanID]) {
				st.Success += row.Count
				st.AvgLatencyMs += row.AvgLatency * float64(row.Count)
			}
			stats[row.PlanID] = st
		}
		for id, st := range stats {
			if st.Success > 0 {
				st.AvgLatencyMs /= float64(st.Success)
			}
			if st.Samples > 0 {
				st.Accuracy = float64(st.Success) / float64(st.Samples) * 100
			}
			stats[id] = st
		}
	}
	out := make([]intelPlanDTO, 0, len(plans))
	for _, p := range plans {
		dto := intelPlanDTO{IntelTestPlan: p, GroupName: groupNames[p.RouteGroupID], Stats: stats[p.ID]}
		if run, ok := lastRuns[p.ID]; ok {
			dto.LastRun = run
			dto.Running = run.Status == domain.IntelRunRunning
		}
		out = append(out, dto)
	}
	httpx.OK(c, gin.H{"items": out})
}

func (h *Admin) CreateIntelPlan(c *gin.Context) {
	var body intelPlanBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid plan request")
		return
	}
	if msg, ok := body.validate(); !ok {
		httpx.BadRequest(c, msg)
		return
	}
	var group domain.RouteGroup
	if err := h.DB.First(&group, body.RouteGroupID).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	plan := domain.IntelTestPlan{}
	body.apply(&plan, time.Now())
	if err := h.DB.Create(&plan).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.Created(c, plan)
}

func (h *Admin) UpdateIntelPlan(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var plan domain.IntelTestPlan
	if err := h.DB.First(&plan, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body intelPlanBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid plan request")
		return
	}
	if msg, ok := body.validate(); !ok {
		httpx.BadRequest(c, msg)
		return
	}
	var group domain.RouteGroup
	if err := h.DB.First(&group, body.RouteGroupID).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	body.apply(&plan, time.Now())
	if err := h.DB.Save(&plan).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, plan)
}

func (h *Admin) DeleteIntelPlan(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var running int64
	if err := h.DB.Model(&domain.IntelTestRun{}).Where("plan_id = ? AND status = ?", id, domain.IntelRunRunning).Count(&running).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if running > 0 {
		httpx.Fail(c, 409, "run_in_progress", "该任务正在测试中，稍后再删除")
		return
	}
	if err := h.Ops.DeleteIntelPlanData(id); err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if err := h.DB.Delete(&domain.IntelTestPlan{}, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, gin.H{"deleted": true})
}

func (h *Admin) RunIntelPlanNow(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	run, err := h.Ops.RunIntelPlan(id)
	if errors.Is(err, ops.ErrIntelRunInProgress) {
		httpx.Fail(c, 409, "run_in_progress", "该任务正在测试中")
		return
	}
	if err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, run)
}

func (h *Admin) ListIntelRuns(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	q := h.DB.Model(&domain.IntelTestRun{})
	if v := strings.TrimSpace(c.Query("plan_id")); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil || id == 0 {
			httpx.BadRequest(c, "invalid plan_id")
			return
		}
		q = q.Where("plan_id = ?", id)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.IntelTestRun
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.List(c, items, total, page, pageSize)
}

func (h *Admin) ListIntelResults(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	q := h.DB.Model(&domain.IntelTestResult{})
	if v := strings.TrimSpace(c.Query("plan_id")); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil || id == 0 {
			httpx.BadRequest(c, "invalid plan_id")
			return
		}
		q = q.Where("plan_id = ?", id)
	}
	if v := strings.TrimSpace(c.Query("run_id")); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil || id == 0 {
			httpx.BadRequest(c, "invalid run_id")
			return
		}
		q = q.Where("run_id = ?", id)
	}
	if v := strings.TrimSpace(c.Query("verdict")); v != "" {
		q = q.Where("verdict = ?", v)
	}
	if v := strings.TrimSpace(c.Query("has_output")); v == "true" {
		q = q.Where("has_output = ?", true)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.IntelTestResult
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.List(c, items, total, page, pageSize)
}

func (h *Admin) GetIntelResultOutput(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var out domain.IntelTestOutput
	if err := h.DB.Where("result_id = ?", id).First(&out).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, gin.H{"result_id": id, "output_text": out.OutputText})
}

type intelVerdictPoint struct {
	ID            uint      `json:"id"`
	RunID         uint      `json:"run_id"`
	Verdict       string    `json:"verdict"`
	LatencyMs     int       `json:"latency_ms"`
	AnswerPreview string    `json:"answer_preview"`
	ErrorMessage  string    `json:"error_message"`
	CreatedAt     time.Time `json:"created_at"`
}

type intelKeyStat struct {
	PlatformKeyID uint                `json:"platform_key_id"`
	UpstreamID    uint                `json:"upstream_id"`
	KeyName       string              `json:"key_name"`
	UpstreamName  string              `json:"upstream_name"`
	Samples       int64               `json:"samples"`
	Success       int64               `json:"success"`
	Accuracy      float64             `json:"accuracy"`
	AvgLatencyMs  float64             `json:"avg_latency_ms"`
	LastVerdict   string              `json:"last_verdict"`
	LastAnswer    string              `json:"last_answer"`
	LastAt        *time.Time          `json:"last_at"`
	History       []intelVerdictPoint `gorm:"-" json:"history"`
}

// IntelPlanSummary aggregates a plan's results per key: the candy scoreboard /
// pelican leaderboard, each row carrying a short verdict history strip.
func (h *Admin) IntelPlanSummary(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var plan domain.IntelTestPlan
	if err := h.DB.First(&plan, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	successVerdict := domain.IntelSuccessVerdict(plan.QuestionKind)

	var stats []intelKeyStat
	if err := h.DB.Model(&domain.IntelTestResult{}).
		Select("platform_key_id, MAX(upstream_id) AS upstream_id, MAX(key_name) AS key_name, MAX(upstream_name) AS upstream_name, "+
			"COUNT(*) AS samples, SUM(CASE WHEN verdict = ? THEN 1 ELSE 0 END) AS success, "+
			"AVG(CASE WHEN verdict = ? THEN latency_ms END) AS avg_latency_ms, MAX(id) AS last_result_id", successVerdict, successVerdict).
		Where("plan_id = ?", id).
		Group("platform_key_id").Scan(&stats).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}

	// Per-key verdict strips come from the newest window of results.
	const historyWindow = 1000
	const historyPerKey = 20
	var recent []domain.IntelTestResult
	if err := h.DB.Where("plan_id = ?", id).Order("id DESC").Limit(historyWindow).Find(&recent).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	byKey := map[uint][]intelVerdictPoint{}
	latest := map[uint]domain.IntelTestResult{}
	for _, r := range recent {
		if len(byKey[r.PlatformKeyID]) >= historyPerKey {
			continue
		}
		byKey[r.PlatformKeyID] = append(byKey[r.PlatformKeyID], intelVerdictPoint{
			ID: r.ID, RunID: r.RunID, Verdict: r.Verdict, LatencyMs: r.LatencyMs,
			AnswerPreview: r.AnswerPreview, ErrorMessage: r.ErrorMessage, CreatedAt: r.CreatedAt,
		})
		if _, ok := latest[r.PlatformKeyID]; !ok {
			latest[r.PlatformKeyID] = r
		}
	}
	for i := range stats {
		st := &stats[i]
		if st.Samples > 0 {
			st.Accuracy = float64(st.Success) / float64(st.Samples) * 100
		}
		if last, ok := latest[st.PlatformKeyID]; ok {
			v := last.CreatedAt
			st.LastVerdict = last.Verdict
			st.LastAnswer = last.AnswerPreview
			st.LastAt = &v
		}
		st.History = byKey[st.PlatformKeyID]
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Accuracy != stats[j].Accuracy {
			return stats[i].Accuracy > stats[j].Accuracy
		}
		return stats[i].Samples > stats[j].Samples
	})
	httpx.OK(c, gin.H{"items": stats, "success_verdict": successVerdict, "question_kind": plan.QuestionKind})
}

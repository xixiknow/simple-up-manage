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
	GroupName        string               `json:"group_name"`
	LastRun          *domain.IntelTestRun `json:"last_run"`
	Running          bool                 `json:"running"`
	Stats            intelPlanStats       `json:"stats"`
	QuarantinedCount int64                `json:"quarantined_count"`
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
	// QuarantineEnabled opts a candy plan into automatic quarantine: keys
	// below the accuracy threshold leave the group and retest on exponential
	// backoff until they answer correctly twice in a row.
	QuarantineEnabled bool `json:"quarantine_enabled"`
	// QuarantineMinSamples / QuarantineThreshold tune the trigger rule; 0
	// values fall back to the defaults (3 samples, 50%).
	QuarantineMinSamples int     `json:"quarantine_min_samples"`
	QuarantineThreshold  float64 `json:"quarantine_threshold"`
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
	if b.IntervalMinutes != 0 && b.IntervalMinutes < 5 {
		return "interval_minutes 为 0（仅手动）或至少 5", false
	}
	if b.Parallel == 0 {
		b.Parallel = 4
	}
	if b.Parallel < 1 || b.Parallel > 8 {
		return "parallel 取值 1-8", false
	}
	if b.QuarantineEnabled && b.QuestionKind != domain.IntelQuestionCandy {
		return "自动隔离仅支持糖果题任务", false
	}
	if b.QuarantineMinSamples != 0 && (b.QuarantineMinSamples < 1 || b.QuarantineMinSamples > domain.IntelQuarantineMaxWindow) {
		return "quarantine_min_samples 取值 1-10（0 为默认 3）", false
	}
	if b.QuarantineThreshold != 0 && (b.QuarantineThreshold <= 0 || b.QuarantineThreshold > 100) {
		return "quarantine_threshold 取值 1-100（0 为默认 50）", false
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
	plan.QuarantineEnabled = b.QuarantineEnabled && b.QuestionKind == domain.IntelQuestionCandy
	if plan.QuarantineEnabled {
		plan.QuarantineMinSamples, plan.QuarantineThreshold = b.QuarantineMinSamples, b.QuarantineThreshold
		if plan.QuarantineMinSamples == 0 {
			plan.QuarantineMinSamples = domain.IntelQuarantineDefaultMinSamples
		}
		if plan.QuarantineThreshold == 0 {
			plan.QuarantineThreshold = domain.IntelQuarantineDefaultThreshold
		}
	} else {
		plan.QuarantineMinSamples, plan.QuarantineThreshold = 0, 0
	}
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
	for _, p := range plans {
		planIDs = append(planIDs, p.ID)
		groupIDs = append(groupIDs, p.RouteGroupID)
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
	// Plan stats are a rolling view: the newest window of effective (judged)
	// results, so old failures do not haunt a key's accuracy forever.
	stats := map[uint]intelPlanStats{}
	for _, p := range plans {
		var rows []domain.IntelTestResult
		if err := h.DB.Where("plan_id = ? AND verdict <> ?", p.ID, domain.IntelVerdictError).
			Order("id DESC").Limit(intelStatsWindow).Find(&rows).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		st := intelPlanStats{Samples: int64(len(rows))}
		var latencySum int64
		successVerdict := domain.IntelSuccessVerdict(p.QuestionKind)
		for _, r := range rows {
			if r.Verdict == successVerdict {
				st.Success++
				latencySum += int64(r.LatencyMs)
			}
		}
		if st.Samples > 0 {
			st.Accuracy = float64(st.Success) / float64(st.Samples) * 100
		}
		if st.Success > 0 {
			st.AvgLatencyMs = float64(latencySum) / float64(st.Success)
		}
		stats[p.ID] = st
	}
	quarantined := map[uint]int64{}
	if len(planIDs) > 0 {
		var qrows []struct {
			PlanID uint
			Count  int64
		}
		if err := h.DB.Model(&domain.IntelQuarantineState{}).
			Select("plan_id, COUNT(*) AS count").
			Where("plan_id IN ? AND status = ?", planIDs, domain.IntelQuarantineQuarantined).
			Group("plan_id").Scan(&qrows).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		for _, row := range qrows {
			quarantined[row.PlanID] = row.Count
		}
	}
	out := make([]intelPlanDTO, 0, len(plans))
	for _, p := range plans {
		dto := intelPlanDTO{IntelTestPlan: p, GroupName: groupNames[p.RouteGroupID], Stats: stats[p.ID], QuarantinedCount: quarantined[p.ID]}
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

// intelStatsWindow is the rolling window for displayed accuracy / counts:
// the newest effective (judged) results per plan or key. Transport errors are
// excluded, matching the quarantine trigger's notion of evidence.
const intelStatsWindow = 10

type intelQuarantineInfo struct {
	Status          string     `json:"status"`
	PassStreak      int        `json:"pass_streak"`
	BackoffSec      int        `json:"backoff_sec"`
	NextTestAt      *time.Time `json:"next_test_at"`
	QuarantineCount int        `json:"quarantine_count"`
	Reason          string     `json:"reason"`
	QuarantinedAt   *time.Time `json:"quarantined_at"`
	RestoredAt      *time.Time `json:"restored_at"`
	LastTestedAt    *time.Time `json:"last_tested_at"`
}

type intelKeyStat struct {
	PlatformKeyID uint                 `json:"platform_key_id"`
	UpstreamID    uint                 `json:"upstream_id"`
	KeyName       string               `json:"key_name"`
	UpstreamName  string               `json:"upstream_name"`
	Samples       int64                `json:"samples"`
	Success       int64                `json:"success"`
	Accuracy      float64              `json:"accuracy"`
	AvgLatencyMs  float64              `json:"avg_latency_ms"`
	LastVerdict   string               `json:"last_verdict"`
	LastAnswer    string               `json:"last_answer"`
	LastAt        *time.Time           `json:"last_at"`
	History       []intelVerdictPoint  `gorm:"-" json:"history"`
	Quarantine    *intelQuarantineInfo `gorm:"-" json:"quarantine,omitempty"`
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

	// One pass over the newest results feeds both the per-key history strips
	// (newest 20, errors included) and the rolling accuracy stats (newest 10
	// effective results, transport errors excluded).
	const historyWindow = 2000
	const historyPerKey = 20
	const statsWindow = intelStatsWindow

	var recent []domain.IntelTestResult
	if err := h.DB.Where("plan_id = ?", id).Order("id DESC").Limit(historyWindow).Find(&recent).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	type keyAgg struct {
		UpstreamID   uint
		KeyName      string
		UpstreamName string
		Samples      int64
		Success      int64
		LatencySum   int64
	}
	aggs := map[uint]*keyAgg{}
	byKey := map[uint][]intelVerdictPoint{}
	for _, r := range recent {
		agg := aggs[r.PlatformKeyID]
		if agg == nil {
			agg = &keyAgg{UpstreamID: r.UpstreamID, KeyName: r.KeyName, UpstreamName: r.UpstreamName}
			aggs[r.PlatformKeyID] = agg
		}
		if len(byKey[r.PlatformKeyID]) < historyPerKey {
			byKey[r.PlatformKeyID] = append(byKey[r.PlatformKeyID], intelVerdictPoint{
				ID: r.ID, RunID: r.RunID, Verdict: r.Verdict, LatencyMs: r.LatencyMs,
				AnswerPreview: r.AnswerPreview, ErrorMessage: r.ErrorMessage, CreatedAt: r.CreatedAt,
			})
		}
		if r.Verdict != domain.IntelVerdictError && int(agg.Samples) < statsWindow {
			agg.Samples++
			if r.Verdict == successVerdict {
				agg.Success++
				agg.LatencySum += int64(r.LatencyMs)
			}
		}
	}
	var states []domain.IntelQuarantineState
	if err := h.DB.Where("plan_id = ?", id).Find(&states).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	quarantinedCount := int64(0)
	stateByKey := make(map[uint]*domain.IntelQuarantineState, len(states))
	for i := range states {
		stateByKey[states[i].PlatformKeyID] = &states[i]
		if states[i].Status == domain.IntelQuarantineQuarantined {
			quarantinedCount++
		}
	}
	stats := make([]intelKeyStat, 0, len(aggs))
	for keyID, agg := range aggs {
		st := intelKeyStat{
			PlatformKeyID: keyID,
			UpstreamID:    agg.UpstreamID,
			KeyName:       agg.KeyName,
			UpstreamName:  agg.UpstreamName,
			Samples:       agg.Samples,
			Success:       agg.Success,
			History:       byKey[keyID],
		}
		if st.Samples > 0 {
			st.Accuracy = float64(st.Success) / float64(st.Samples) * 100
		}
		if st.Success > 0 {
			st.AvgLatencyMs = float64(agg.LatencySum) / float64(st.Success)
		}
		if len(st.History) > 0 {
			st.LastVerdict = st.History[0].Verdict
			st.LastAnswer = st.History[0].AnswerPreview
			v := st.History[0].CreatedAt
			st.LastAt = &v
		}
		if qs, ok := stateByKey[keyID]; ok {
			st.Quarantine = &intelQuarantineInfo{
				Status:          qs.Status,
				PassStreak:      qs.PassStreak,
				BackoffSec:      qs.BackoffSec,
				NextTestAt:      qs.NextTestAt,
				QuarantineCount: qs.QuarantineCount,
				Reason:          qs.Reason,
				QuarantinedAt:   &qs.QuarantinedAt,
				RestoredAt:      qs.RestoredAt,
				LastTestedAt:    qs.LastTestedAt,
			}
		}
		stats = append(stats, st)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Accuracy != stats[j].Accuracy {
			return stats[i].Accuracy > stats[j].Accuracy
		}
		return stats[i].Samples > stats[j].Samples
	})
	httpx.OK(c, gin.H{"items": stats, "success_verdict": successVerdict, "question_kind": plan.QuestionKind, "quarantined_count": quarantinedCount})
}

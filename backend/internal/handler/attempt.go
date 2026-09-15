package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"gorm.io/gorm/clause"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/picker"
	"simple-up-manage/internal/upstream"
)

func (h *Gateway) completeAttempt(ctx context.Context, pk *domain.PlatformKey, a *domain.RequestAttempt, col *streamCollector, out forwardOutcome) {
	a.CompletedAt = time.Now()
	a.Result = "upstream_failure"
	switch {
	case out.neutral:
		a.Result = "request_rejected"
	case out.capacityBusy:
		a.Result = "capacity_rejected"
	case ctx.Err() != nil && !out.validSuccess:
		a.Result = "client_cancelled"
	case a.StartedAt.IsZero():
		a.Result = "local_failure"
	case out.validSuccess:
		a.Result = "success"
	}
	if a.StartedAt.IsZero() {
		a.StartedAt = a.CompletedAt
	}
	a.DurationMs = int(a.CompletedAt.Sub(a.StartedAt).Milliseconds())
	var usage upstream.TokenUsage
	if col != nil {
		usage = col.usage
		a.InputTokens, a.CacheReadTokens, a.CacheCreationTokens, a.OutputTokens = usage.InputTokens, usage.CacheReadTokens, usage.CacheCreationTokens, usage.OutputTokens
		// OpenAI input usage includes cached tokens; Anthropic input excludes them.
		if a.Protocol == domain.ProtocolOpenAI {
			a.InputTokens = max(0, a.InputTokens-a.CacheReadTokens-a.CacheCreationTokens)
		}
		if a.Result == "success" && !col.firstAt.IsZero() {
			a.TTFTMs = max(1, int(col.firstAt.Sub(a.StartedAt).Milliseconds()))
		}
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if h.DB != nil {
		res := h.DB.WithContext(writeCtx).Clauses(clause.OnConflict{DoNothing: true}).Create(a)
		if res.Error != nil {
			slog.Error("persist upstream attempt", "attempt_id", a.ID, "error", res.Error)
		} else if res.RowsAffected == 0 {
			return
		}
	}
	if a.Result == "success" || a.Result == "upstream_failure" {
		h.observeAttempt(pk, a.Model, a.Result == "success", usage, a.TTFTMs)
	}
}

func (lg *liveLog) recordDecision(h *Gateway, d picker.Decision) {
	if lg == nil || lg.id == 0 || d.SelectedKeyID == 0 {
		return
	}
	// Credentials/previews and balances are not part of a routing decision.
	for i := range d.Candidates {
		d.Candidates[i].KeyPreview = ""
		d.Candidates[i].LastBalance = nil
	}
	lg.mu.Lock()
	if len(lg.trace) > 0 {
		lg.trace[len(lg.trace)-1].Decision = &d
		lg.trace[len(lg.trace)-1].Reason = d.Reason
	}
	b, _ := json.Marshal(lg.trace)
	lg.mu.Unlock()
	h.writeLog(lg, map[string]any{"selection_trace": string(b)}, false)
}

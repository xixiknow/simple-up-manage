package handler

import (
	"github.com/gin-gonic/gin"
	"log/slog"
	"simple-up-manage/internal/picker"
)

func (h *Gateway) recordNoRoute(c *gin.Context, lg *liveLog, req picker.Request, decision picker.Decision) {
	if len(decision.Candidates) == 0 && h.Picker != nil {
		candidates, err := h.Picker.Explain(c.Request.Context(), req)
		if err != nil {
			slog.Warn("explain rejected route", "error", err)
		} else {
			decision.Candidates = candidates
		}
	}
	decision.Reason = "no_available_route"
	lg.traceEvent(h, nil, nil, "rejected", decision.Reason)
	lg.recordDecision(h, decision)
	// Preserve the actual last upstream failure when failover exhausts its pool.
	if len(req.Exclude)+len(req.ExcludeProviders)+len(req.ExcludeKeyModels) == 0 {
		lg.markFailure(h, "route", "no_available_route")
	}
	c.Header("Retry-After", "30")
}

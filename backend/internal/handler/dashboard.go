package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
)

func (h *Admin) DashboardLive(c *gin.Context) {
	if h.Dash == nil || h.Dash.Metrics == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache, no-transform")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	if f, ok := c.Writer.(http.Flusher); ok {
		f.Flush()
	}
	sub := h.Dash.Metrics.Subscribe()
	defer h.Dash.Metrics.Unsubscribe(sub)
	ctx := c.Request.Context()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	rc := http.NewResponseController(c.Writer)
	write := func(event string, v any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(dashboard.WriteTimeout))
		b := encodeLive(event, v)
		if _, err := c.Writer.Write(b); err != nil {
			return false
		}
		if f, ok := c.Writer.(http.Flusher); ok {
			f.Flush()
		}
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done:
			return
		case snap := <-sub.Ch:
			if !write("snapshot", snap) {
				return
			}
		case <-heartbeat.C:
			hb := dashboard.Heartbeat{InstanceID: h.Dash.Metrics.InstanceID(), ServerTime: time.Now().UTC()}
			if !write("heartbeat", hb) {
				return
			}
		}
	}
}

func encodeLive(event string, v any) []byte {
	b, _ := json.Marshal(v)
	out := make([]byte, 0, len(event)+len(b)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, "\ndata: "...)
	out = append(out, b...)
	out = append(out, "\n\n"...)
	return out
}

func (h *Admin) DashboardOverview(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	r, err := dashboardRange(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	out, err := h.Dash.Overview(c.Request.Context(), r, h.Dash.BalanceStale)
	if err != nil {
		if errors.Is(err, dashboard.ErrBadRange) {
			httpx.BadRequest(c, err.Error())
			return
		}
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, out)
}

func (h *Admin) DashboardTrends(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	r, err := dashboardRange(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	gran := c.DefaultQuery("granularity", "hour")
	out, err := h.Dash.Trends(c.Request.Context(), r, gran)
	if err != nil {
		if errors.Is(err, dashboard.ErrBadRange) {
			httpx.BadRequest(c, err.Error())
			return
		}
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, out)
}

func (h *Admin) DashboardRankings(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	r, err := dashboardRange(c)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	out, err := h.Dash.Rankings(c.Request.Context(), r, c.DefaultQuery("dimension", "group"), page, pageSize)
	if err != nil {
		if errors.Is(err, dashboard.ErrBadRange) {
			httpx.BadRequest(c, err.Error())
			return
		}
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, out)
}

func (h *Admin) DashboardRecommendations(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	out, err := h.Dash.Recommendations(c.Request.Context(), h.Dash.BalanceStale)
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, out)
}

func (h *Admin) DashboardGetSettings(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	httpx.OK(c, h.Dash.LoadSettings())
}

func (h *Admin) DashboardPutSettings(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "dashboard unavailable")
		return
	}
	var body dashboard.Settings
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	out, err := h.Dash.SaveSettings(body)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OK(c, out)
}

func dashboardRange(c *gin.Context) (dashboard.Range, error) {
	return dashboard.ParseRange(c.Query("from"), c.Query("to"))
}

package handler

import (
	"net/http"
	"regexp"
	"strings"

	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
)

// updateTargetRe accepts "latest", "previous", a semver release ("1.2.3",
// with or without the "v" prefix) or a git short/full sha — the tag shapes
// CI publishes to ghcr.
var updateTargetRe = regexp.MustCompile(`^(latest|previous|[0-9a-fA-F]{4,40}|v?[0-9]+\.[0-9]+\.[0-9]+)$`)

// SystemVersion reports the running version, the registry's latest and
// whether the environment can self-update.
func (h *Admin) SystemVersion(c *gin.Context) {
	httpx.OK(c, h.SelfUpdate.Info())
}

// SystemUpdate starts the blue-green rollout in the background.
func (h *Admin) SystemUpdate(c *gin.Context) {
	var body struct {
		Target string `json:"target"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "请求体格式错误")
		return
	}
	target := strings.ToLower(strings.TrimSpace(body.Target))
	if !updateTargetRe.MatchString(target) {
		httpx.BadRequest(c, "无效的目标版本（支持 latest / previous / git sha）")
		return
	}
	if err := h.SelfUpdate.StartUpdate(target); err != nil {
		httpx.Fail(c, http.StatusConflict, "update_rejected", err.Error())
		return
	}
	httpx.OK(c, gin.H{"started": true, "target": target})
}

// SystemUpdateStatus reports rollout progress (Redis-backed so the poller
// keeps seeing it after traffic switches to the successor instance).
func (h *Admin) SystemUpdateStatus(c *gin.Context) {
	httpx.OK(c, h.SelfUpdate.Status())
}

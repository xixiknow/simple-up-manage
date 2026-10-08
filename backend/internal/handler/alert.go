package handler

import (
	"net/http"
	"strings"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
)

type alertSettingsDTO struct {
	Enabled        bool    `json:"enabled"`
	HoursThreshold float64 `json:"hours_threshold"`
	SilenceHours   int     `json:"silence_hours"`
	SendKeyPreview string  `json:"send_key_preview"`
	HasKey         bool    `json:"has_key"`
}

func (h *Admin) alertPreview(cfg domain.AlertSettings) (preview string, hasKey bool) {
	if cfg.SendKey == "" || h.Enc == nil {
		return "", false
	}
	plain, err := h.Enc.Decrypt(cfg.SendKey)
	if err != nil {
		return "", true
	}
	return crypto.KeyPreview(plain), true
}

func (h *Admin) alertDTO(cfg domain.AlertSettings) alertSettingsDTO {
	preview, hasKey := h.alertPreview(cfg)
	return alertSettingsDTO{
		Enabled:        cfg.Enabled,
		HoursThreshold: cfg.HoursThreshold,
		SilenceHours:   cfg.SilenceHours,
		SendKeyPreview: preview,
		HasKey:         hasKey,
	}
}

// alertSettingsBody keeps the secret write-only: an empty send_key preserves
// the stored key, clear_send_key removes it.
type alertSettingsBody struct {
	Enabled        *bool    `json:"enabled"`
	HoursThreshold *float64 `json:"hours_threshold"`
	SilenceHours   *int     `json:"silence_hours"`
	SendKey        string   `json:"send_key"`
	ClearSendKey   bool     `json:"clear_send_key"`
}

func (h *Admin) GetAlertSettings(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "alert unavailable")
		return
	}
	httpx.OK(c, h.alertDTO(h.Dash.LoadAlertSettings()))
}

func (h *Admin) UpdateAlertSettings(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "alert unavailable")
		return
	}
	var body alertSettingsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	cfg := h.Dash.LoadAlertSettings()
	if body.Enabled != nil {
		cfg.Enabled = *body.Enabled
	}
	if body.HoursThreshold != nil {
		cfg.HoursThreshold = *body.HoursThreshold
	}
	if body.SilenceHours != nil {
		cfg.SilenceHours = *body.SilenceHours
	}
	switch {
	case body.ClearSendKey:
		cfg.SendKey = ""
	case strings.TrimSpace(body.SendKey) != "":
		if h.Enc == nil {
			httpx.Internal(c, "encryptor unavailable")
			return
		}
		enc, err := h.Enc.Encrypt(strings.TrimSpace(body.SendKey))
		if err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		cfg.SendKey = enc
	}
	saved, err := h.Dash.SaveAlertSettings(cfg)
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, h.alertDTO(saved))
}

// TestAlert pushes a fixed message through the stored key regardless of the
// enabled flag, so configuration can be verified before switching on.
func (h *Admin) TestAlert(c *gin.Context) {
	if h.Dash == nil {
		httpx.Internal(c, "alert unavailable")
		return
	}
	cfg := h.Dash.LoadAlertSettings()
	if cfg.SendKey == "" || h.Enc == nil {
		httpx.BadRequest(c, "尚未配置 SendKey")
		return
	}
	plain, err := h.Enc.Decrypt(cfg.SendKey)
	if err != nil {
		httpx.Internal(c, "decrypt send key: "+err.Error())
		return
	}
	if err := dashboard.SendServerChan(c.Request.Context(), plain, "供货商管理测试消息", "这是一条测试推送。余额不足预警配置成功后，当某提供商预计小时数低于阈值时会在此推送提醒。"); err != nil {
		httpx.Fail(c, http.StatusBadGateway, "serverchan_failed", err.Error())
		return
	}
	httpx.OK(c, gin.H{"message": "测试消息已发送"})
}

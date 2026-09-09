package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"
	"simple-up-manage/internal/ops"

	"github.com/gin-gonic/gin"
)

func (h *Admin) GetConsumerKeySecret(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var k domain.ConsumerKey
	if err := h.DB.First(&k, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, gin.H{"key": k.Key})
}

type consumerTestResult struct {
	Success    bool   `json:"success"`
	Status     int    `json:"status"`
	Model      string `json:"model"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func (h *Admin) TestConsumerKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var k domain.ConsumerKey
	if err := h.DB.First(&k, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	if k.Status != domain.StatusEnabled {
		httpx.Fail(c, http.StatusConflict, "disabled", "API key is paused")
		return
	}
	if h.Gateway == nil {
		httpx.Internal(c, "gateway unavailable")
		return
	}
	cfg := domain.DefaultSchedulerSettings()
	if h.Picker != nil {
		cfg = h.Picker.Settings()
	}
	groups, unbound := h.consumerBoundGroups(k.ID)
	started := time.Now()
	status, body, model := 0, []byte(nil), ""
	if groupAcceptsProtocol(groups, unbound, domain.ProtocolOpenAI) {
		model = pickTestModel(groups, unbound, domain.ProtocolOpenAI, cfg.ProbeOpenAIModel)
		status, body, model = h.runConsumerProbe(k.Key, domain.ProtocolOpenAI, model)
	}
	if (status == 0 || status == http.StatusServiceUnavailable) &&
		strings.TrimSpace(cfg.ProbeAnthropicModel) != "" &&
		groupAcceptsProtocol(groups, unbound, domain.ProtocolAnthropic) {
		status, body, model = h.runConsumerProbe(k.Key, domain.ProtocolAnthropic, cfg.ProbeAnthropicModel)
	}
	out := consumerTestResult{
		Success:    status >= 200 && status < 400,
		Status:     status,
		Model:      model,
		DurationMs: time.Since(started).Milliseconds(),
	}
	if !out.Success {
		if status == 0 {
			out.Status = http.StatusServiceUnavailable
			out.Error = "bound route groups do not match OpenAI Responses or Anthropic test"
		} else {
			out.Error = extractGatewayError(body, status)
		}
	}
	httpx.OK(c, out)
}

func (h *Admin) runConsumerProbe(apiKey, protocol, model string) (status int, body []byte, usedModel string) {
	model = strings.TrimSpace(model)
	path := "/v1/responses"
	payload := map[string]any{
		"model":             model,
		"input":             "hi",
		"max_output_tokens": 16,
		"store":             false,
	}
	if protocol == domain.ProtocolAnthropic {
		path = "/v1/messages"
		payload = map[string]any{
			"model":      model,
			"max_tokens": 1,
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
			"stream":     false,
		}
	}
	raw, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	if protocol == domain.ProtocolAnthropic {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	ctx.Request = req
	if protocol == domain.ProtocolAnthropic {
		h.Gateway.Messages(ctx)
	} else {
		h.Gateway.Responses(ctx)
	}
	return rec.Code, rec.Body.Bytes(), model
}

func (h *Admin) consumerBoundGroups(consumerID uint) (groups []domain.RouteGroup, unbound bool) {
	var links []domain.ConsumerRouteGroup
	if err := h.DB.Where("consumer_key_id = ?", consumerID).Find(&links).Error; err != nil || len(links) == 0 {
		return nil, true
	}
	ids := make([]uint, 0, len(links))
	for _, l := range links {
		ids = append(ids, l.RouteGroupID)
	}
	_ = h.DB.Where("id IN ?", ids).Find(&groups).Error
	return groups, false
}

func groupAcceptsProtocol(groups []domain.RouteGroup, unbound bool, protocol string) bool {
	if unbound {
		return true
	}
	for i := range groups {
		if groups[i].Status == domain.StatusEnabled && groups[i].MatchesProtocol(protocol) {
			return true
		}
	}
	return false
}

func pickTestModel(groups []domain.RouteGroup, unbound bool, protocol, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if unbound || fallback == "" {
		return fallback
	}
	for i := range groups {
		if ops.RouteGroupMatches(&groups[i], protocol, fallback) {
			return fallback
		}
	}
	for i := range groups {
		g := &groups[i]
		if g.Status != domain.StatusEnabled || !g.MatchesProtocol(protocol) {
			continue
		}
		for _, pattern := range g.Models {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" || strings.Contains(pattern, "*") {
				continue
			}
			return pattern
		}
	}
	return fallback
}

func extractGatewayError(body []byte, status int) string {
	var openai struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &openai); err == nil && strings.TrimSpace(openai.Error.Message) != "" {
		return openai.Error.Message
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return http.StatusText(status)
	}
	if len(msg) > 300 {
		return msg[:300]
	}
	return msg
}

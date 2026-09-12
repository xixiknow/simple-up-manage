package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"
	"simple-up-manage/internal/ops"
	"simple-up-manage/internal/picker"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Admin struct {
	DB      *gorm.DB
	Enc     *crypto.AESGCM
	Ops     *ops.Service
	Picker  picker.Picker
	Gateway *Gateway
}

func (h *Admin) ListUpstreams(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	var total int64
	q := h.DB.Model(&domain.Upstream{})
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.Upstream
	if err := q.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]upstreamDTO, 0, len(items))
	for _, u := range items {
		out = append(out, toUpstreamDTO(u))
	}
	if c.Query("include_summary") == "true" {
		if err := h.attachUpstreamSummaries(out); err != nil {
			httpx.Internal(c, err.Error())
			return
		}
	}
	httpx.List(c, out, total, page, pageSize)
}

func (h *Admin) attachUpstreamSummaries(items []upstreamDTO) error {
	ids := make([]uint, 0, len(items))
	byID := make(map[uint]*upstreamSummary, len(items))
	for i := range items {
		items[i].Summary = &upstreamSummary{HealthCounts: map[string]int{}}
		byID[items[i].ID] = items[i].Summary
		ids = append(ids, items[i].ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []struct {
		UpstreamID    uint
		Status        string
		HealthStatus  string
		Count         int
		LastRequestAt domain.LooseTime
	}
	if err := h.DB.Model(&domain.PlatformKey{}).Select("upstream_id, status, health_status, COUNT(*) AS count, MAX(last_request_at) AS last_request_at").
		Where("upstream_id IN ?", ids).Group("upstream_id, status, health_status").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		s := byID[row.UpstreamID]
		s.KeyCount += row.Count
		health := row.HealthStatus
		if row.Status == domain.StatusDisabled {
			health = domain.HealthDisabled
		}
		s.HealthCounts[health] += row.Count
		if row.Status == domain.StatusEnabled && health != domain.HealthHealthy {
			s.AbnormalCount += row.Count
		}
		t := row.LastRequestAt.Time()
		if !t.IsZero() && (s.LastRequestAt == nil || t.After(*s.LastRequestAt)) {
			s.LastRequestAt = &t
		}
	}
	return nil
}

type upstreamBody struct {
	Name        string   `json:"name"`
	BaseURL     string   `json:"base_url"`
	Kind        string   `json:"kind"`
	Protocols   []string `json:"protocols"`
	Note        string   `json:"note"`
	Status      string   `json:"status"`
	Concurrency *int     `json:"concurrency"`
}

func (h *Admin) CreateUpstream(c *gin.Context) {
	var body upstreamBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	u, err := h.buildUpstream(body, nil)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if err := h.DB.Create(u).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.Created(c, toUpstreamDTO(*u))
}

func (h *Admin) GetUpstream(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var u domain.Upstream
	if err := h.DB.First(&u, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, toUpstreamDTO(u))
}

func (h *Admin) UpdateUpstream(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var u domain.Upstream
	if err := h.DB.First(&u, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body upstreamBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	next, err := h.buildUpstream(body, &u)
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	next.ID = u.ID
	next.CreatedAt = u.CreatedAt
	if err := h.DB.Save(next).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if next.Name != u.Name {
		_ = h.recomposeKeyNames(c.Request.Context(), next.ID, next.Name)
	}
	_ = h.Ops.RefreshAllHealth(c.Request.Context())
	httpx.OK(c, toUpstreamDTO(*next))
}

func (h *Admin) DeleteUpstream(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("platform_key_id IN (?)",
			tx.Model(&domain.PlatformKey{}).Select("id").Where("upstream_id = ?", id),
		).Delete(&domain.RouteGroupKey{}).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.Upstream{}, id).Error
	})
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"id": id})
}

func (h *Admin) recomposeKeyNames(ctx context.Context, upstreamID uint, provider string) error {
	var keys []domain.PlatformKey
	if err := h.DB.WithContext(ctx).Where("upstream_id = ?", upstreamID).Find(&keys).Error; err != nil {
		return err
	}
	for i := range keys {
		tag := strings.TrimSpace(keys[i].NameTag)
		if tag == "" {
			continue
		}
		updates := map[string]any{
			"name": domain.ComposeKeyName(provider, tag, keys[i].RateMultiplier),
		}
		if err := h.DB.WithContext(ctx).Model(&keys[i]).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

func (h *Admin) buildUpstream(body upstreamBody, existing *domain.Upstream) (*domain.Upstream, error) {
	name := strings.TrimSpace(body.Name)
	base := strings.TrimSpace(body.BaseURL)
	kind := strings.TrimSpace(body.Kind)
	status := strings.TrimSpace(body.Status)
	if existing != nil {
		if name == "" {
			name = existing.Name
		}
		if base == "" {
			base = existing.BaseURL
		}
		if kind == "" {
			kind = existing.Kind
		}
		if status == "" {
			status = existing.Status
		}
		if len(body.Protocols) == 0 {
			body.Protocols = existing.ProtocolList()
		}
	}
	if name == "" {
		return nil, errors.New("name is required")
	}
	if base == "" {
		return nil, errors.New("base_url is required")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, errors.New("base_url must be http(s)")
	}
	if !domain.ValidKind(kind) {
		return nil, errors.New("kind must be sub2api, openai_compat, or anthropic_compat")
	}
	if status == "" {
		status = domain.StatusEnabled
	}
	if !domain.ValidStatus(status) {
		return nil, errors.New("status must be enabled or disabled")
	}
	protos := domain.NormalizeStrings(body.Protocols)
	if len(protos) == 0 {
		return nil, errors.New("protocols is required")
	}
	for _, p := range protos {
		if !domain.ValidProtocol(p) {
			return nil, errors.New("protocol must be openai or anthropic")
		}
	}
	conc := 0
	var bal *float64
	var balAt *time.Time
	if existing != nil {
		conc = existing.Concurrency
		bal = existing.LastBalance
		balAt = existing.LastBalanceAt
	}
	if body.Concurrency != nil {
		if *body.Concurrency < 0 {
			return nil, errors.New("concurrency must be >= 0")
		}
		conc = *body.Concurrency
	}
	u := &domain.Upstream{
		Name:          name,
		BaseURL:       strings.TrimRight(base, "/"),
		Kind:          kind,
		Protocols:     domain.JoinCSV(protos),
		Status:        status,
		Note:          strings.TrimSpace(body.Note),
		Concurrency:   conc,
		LastBalance:   bal,
		LastBalanceAt: balAt,
	}
	return u, nil
}

func (h *Admin) ListUpstreamKeys(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	if err := h.DB.First(&domain.Upstream{}, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	page, pageSize := httpx.PageParams(c)
	q := h.DB.Model(&domain.PlatformKey{}).Where("upstream_id = ?", id)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.PlatformKey
	if err := h.DB.Preload("Upstream").Where("upstream_id = ?", id).
		Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]keyDTO, 0, len(items))
	for _, k := range items {
		out = append(out, toKeyDTO(k))
	}
	h.enrichKeys(out)
	httpx.List(c, out, total, page, pageSize)
}

type keyBody struct {
	Name             string   `json:"name"`
	NameTag          string   `json:"name_tag"`
	APIKey           string   `json:"api_key"`
	Status           string   `json:"status"`
	Concurrency      *int     `json:"concurrency"`
	RateMultiplier   *float64 `json:"rate_multiplier"`
	BillingGroup     *string  `json:"billing_group"`
	ProbeIntervalSec *int     `json:"probe_interval_sec"`
	RPMLimit         *int     `json:"rpm_limit"`
	MaxConcurrency   *int     `json:"max_concurrency"`
}

func validRate(r float64) bool {
	return r >= 0 && r <= 1000
}

func (h *Admin) CreateUpstreamKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var up domain.Upstream
	if err := h.DB.First(&up, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body keyBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	tag := strings.TrimSpace(body.NameTag)
	if tag == "" {
		tag = strings.TrimSpace(body.Name)
	}
	if tag == "" {
		httpx.BadRequest(c, "name_tag is required")
		return
	}
	if len([]rune(tag)) > 64 {
		httpx.BadRequest(c, "name_tag is too long")
		return
	}
	raw := strings.TrimSpace(body.APIKey)
	if raw == "" {
		httpx.BadRequest(c, "api_key is required")
		return
	}
	enc, err := h.Enc.Encrypt(raw)
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	status := strings.TrimSpace(body.Status)
	if status == "" {
		status = domain.StatusEnabled
	}
	if !domain.ValidStatus(status) {
		httpx.BadRequest(c, "status must be enabled or disabled")
		return
	}
	rate := 1.0
	if body.RateMultiplier != nil {
		if !validRate(*body.RateMultiplier) {
			httpx.BadRequest(c, "rate_multiplier must be between 0 and 1000")
			return
		}
		rate = *body.RateMultiplier
	}
	billingGroup := ""
	if body.BillingGroup != nil {
		billingGroup = strings.TrimSpace(*body.BillingGroup)
	}
	probeSec := 0
	if body.ProbeIntervalSec != nil {
		if *body.ProbeIntervalSec < 0 {
			httpx.BadRequest(c, "probe_interval_sec must be >= 0")
			return
		}
		probeSec = *body.ProbeIntervalSec
	}
	rpmLimit := 0
	if body.RPMLimit != nil {
		if *body.RPMLimit < 0 {
			httpx.BadRequest(c, "rpm_limit must be >= 0")
			return
		}
		rpmLimit = *body.RPMLimit
	}
	maxConcurrency := 0
	if body.MaxConcurrency != nil {
		if *body.MaxConcurrency < 0 {
			httpx.BadRequest(c, "max_concurrency must be >= 0")
			return
		}
		maxConcurrency = *body.MaxConcurrency
	}
	k := domain.PlatformKey{
		UpstreamID:       id,
		Name:             domain.ComposeKeyName(up.Name, tag, rate),
		NameTag:          tag,
		EncryptedKey:     enc,
		KeyPreview:       crypto.KeyPreview(raw),
		RateMultiplier:   rate,
		BillingGroup:     billingGroup,
		Status:           status,
		HealthStatus:     domain.HealthHealthy,
		ProbeIntervalSec: probeSec,
		RPMLimit:         rpmLimit,
		MaxConcurrency:   maxConcurrency,
	}
	if err := h.DB.Create(&k).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	_ = h.Ops.RefreshKeyHealth(c.Request.Context(), k.ID)
	h.Ops.BootstrapKey(c.Request.Context(), k.ID)
	_ = h.DB.Preload("Upstream").First(&k, k.ID)
	httpx.Created(c, h.keyOut(k))
}

func (h *Admin) UpdateKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var k domain.PlatformKey
	if err := h.DB.First(&k, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	var body keyBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &fields)
	updates := map[string]any{}
	var up domain.Upstream
	if err := h.DB.First(&up, k.UpstreamID).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	tag := strings.TrimSpace(body.NameTag)
	if tag == "" {
		tag = strings.TrimSpace(body.Name)
	}
	if tag == "" {
		tag = k.EffectiveNameTag(up.Name)
	}
	if len([]rune(tag)) > 64 {
		httpx.BadRequest(c, "name_tag is too long")
		return
	}
	oldRate := k.RateMultiplier
	rate := k.RateMultiplier
	rateEdited := false
	if body.RateMultiplier != nil {
		if !validRate(*body.RateMultiplier) {
			httpx.BadRequest(c, "rate_multiplier must be between 0 and 1000")
			return
		}
		if *body.RateMultiplier != k.RateMultiplier {
			rate = *body.RateMultiplier
			updates["rate_multiplier"] = rate
			updates["rate_synced_at"] = nil
			rateEdited = true
		}
	}
	if tag != "" {
		updates["name_tag"] = tag
		updates["name"] = domain.ComposeKeyName(up.Name, tag, rate)
	}
	if raw := strings.TrimSpace(body.APIKey); raw != "" {
		enc, err := h.Enc.Encrypt(raw)
		if err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		updates["encrypted_key"] = enc
		updates["key_preview"] = crypto.KeyPreview(raw)
	}
	if body.Status != "" {
		if !domain.ValidStatus(body.Status) {
			httpx.BadRequest(c, "status must be enabled or disabled")
			return
		}
		updates["status"] = body.Status
	}
	if _, ok := fields["billing_group"]; ok {
		bg := ""
		if body.BillingGroup != nil {
			bg = strings.TrimSpace(*body.BillingGroup)
		}
		updates["billing_group"] = bg
	}
	if body.ProbeIntervalSec != nil {
		if *body.ProbeIntervalSec < 0 {
			httpx.BadRequest(c, "probe_interval_sec must be >= 0")
			return
		}
		updates["probe_interval_sec"] = *body.ProbeIntervalSec
	}
	if body.RPMLimit != nil {
		if *body.RPMLimit < 0 {
			httpx.BadRequest(c, "rpm_limit must be >= 0")
			return
		}
		updates["rpm_limit"] = *body.RPMLimit
	}
	if body.MaxConcurrency != nil {
		if *body.MaxConcurrency < 0 {
			httpx.BadRequest(c, "max_concurrency must be >= 0")
			return
		}
		updates["max_concurrency"] = *body.MaxConcurrency
	}
	if len(updates) > 0 {
		if err := h.DB.Model(&k).Updates(updates).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
	}
	if rateEdited && h.Ops != nil {
		k.Upstream = &up
		_ = h.Ops.RecordRateChange(c.Request.Context(), &k, oldRate, rate, domain.RateChangeManual)
	}
	_ = h.Ops.RefreshKeyHealth(c.Request.Context(), k.ID)
	if strings.TrimSpace(body.APIKey) != "" {
		h.Ops.BootstrapKey(c.Request.Context(), k.ID)
	}
	_ = h.DB.Preload("Upstream").First(&k, k.ID)
	httpx.OK(c, h.keyOut(k))
}

func (h *Admin) DeleteKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("platform_key_id = ?", id).Delete(&domain.RouteGroupKey{}).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.PlatformKey{}, id).Error
	})
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"id": id})
}

func (h *Admin) ListAllKeys(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	var upstreamIDs []uint
	if raw := strings.TrimSpace(c.Query("upstream_ids")); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
			if err != nil || id == 0 {
				httpx.BadRequest(c, "invalid upstream_ids")
				return
			}
			upstreamIDs = append(upstreamIDs, uint(id))
		}
		if len(upstreamIDs) > 100 {
			httpx.BadRequest(c, "at most 100 upstream_ids")
			return
		}
	}
	// routeFilter: "" = any, "0" = unassigned, "<id>" = member of that route group.
	routeFilter := strings.TrimSpace(c.Query("route_group_id"))
	applyFilters := func(q *gorm.DB) *gorm.DB {
		if len(upstreamIDs) > 0 {
			q = q.Where("upstream_id IN ?", upstreamIDs)
		}
		if uid := strings.TrimSpace(c.Query("upstream_id")); uid != "" {
			q = q.Where("upstream_id = ?", uid)
		}
		switch routeFilter {
		case "":
		case "0":
			q = q.Where("id NOT IN (?)", h.DB.Model(&domain.RouteGroupKey{}).Select("platform_key_id"))
		default:
			q = q.Where("id IN (?)", h.DB.Model(&domain.RouteGroupKey{}).Select("platform_key_id").Where("route_group_id = ?", routeFilter))
		}
		return q
	}
	q := applyFilters(h.DB.Model(&domain.PlatformKey{}))
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if c.Query("view") == "options" {
		var items []struct {
			ID         uint   `json:"id"`
			UpstreamID uint   `json:"upstream_id"`
			Name       string `json:"name"`
			KeyPreview string `json:"key_preview"`
		}
		if err := q.Select("id", "upstream_id", "name", "key_preview").Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&items).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		httpx.List(c, items, total, page, pageSize)
		return
	}
	if c.Query("view") == "rates" {
		items := make([]keyRateDTO, 0)
		if err := h.DB.Model(&domain.PlatformKey{}).
			Select("platform_keys.id, platform_keys.upstream_id, upstreams.name AS upstream_name, platform_keys.name, platform_keys.rate_multiplier").
			Joins("LEFT JOIN upstreams ON upstreams.id = platform_keys.upstream_id").
			Where("platform_keys.id IN (?)", q.Select("platform_keys.id")).
			Order("platform_keys.id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&items).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		httpx.List(c, items, total, page, pageSize)
		return
	}
	var items []domain.PlatformKey
	dbq := applyFilters(h.DB.Preload("Upstream"))
	if err := dbq.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]keyDTO, 0, len(items))
	for _, k := range items {
		out = append(out, toKeyDTO(k))
	}
	h.enrichKeys(out)
	httpx.List(c, out, total, page, pageSize)
}

type probeBody struct {
	Deep bool `json:"deep"`
}

func (h *Admin) ProbeKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var body probeBody
	_ = c.ShouldBindJSON(&body)
	out, err := h.Ops.ProbeKey(c.Request.Context(), id, body.Deep)
	if err != nil {
		writeGormErr(c, err)
		return
	}
	httpx.OK(c, out)
}

// FetchKeyModels pulls GET /v1/models from the key's upstream and stores the ids.
func (h *Admin) FetchKeyModels(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	out, err := h.Ops.FetchModels(c.Request.Context(), id)
	if err != nil {
		writeGormErr(c, err)
		return
	}
	if !out.Success {
		httpx.Fail(c, http.StatusBadGateway, "upstream_error", out.Message)
		return
	}
	httpx.OK(c, out)
}

// FetchUpstreamModels fetches models for every enabled key of one upstream.
func (h *Admin) FetchUpstreamModels(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	if err := h.DB.First(&domain.Upstream{}, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	okN, fail, union := h.Ops.FetchModelsForUpstream(c.Request.Context(), id)
	httpx.OK(c, gin.H{
		"ok":           okN,
		"failed":       fail,
		"models":       union,
		"models_count": len(union),
		"message":      fmt.Sprintf("成功 %d，失败 %d，共 %d 个模型", okN, fail, len(union)),
	})
}

func (h *Admin) RefreshUpstreamBalance(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	if err := h.DB.First(&domain.Upstream{}, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	if err := h.Ops.RefreshUpstreamBalance(c.Request.Context(), id, 0); err != nil {
		httpx.Fail(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	var u domain.Upstream
	_ = h.DB.First(&u, id)
	httpx.OK(c, toUpstreamDTO(u))
}

func (h *Admin) RefreshBalance(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	if err := h.Ops.RefreshBalance(c.Request.Context(), id); err != nil {
		httpx.Fail(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	var k domain.PlatformKey
	_ = h.DB.Preload("Upstream").First(&k, id)
	httpx.OK(c, h.keyOut(k))
}

func (h *Admin) RefreshBilling(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var k domain.PlatformKey
	if err := h.DB.Preload("Upstream").First(&k, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	if k.Upstream == nil || !domain.KindHasBilling(k.Upstream.Kind) {
		httpx.BadRequest(c, "该上游不是 sub2api / new-api，无法同步分组倍率")
		return
	}
	if err := h.Ops.RefreshBilling(c.Request.Context(), id); err != nil {
		httpx.Fail(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	_ = h.DB.Preload("Upstream").First(&k, id)
	httpx.OK(c, h.keyOut(k))
}

func (h *Admin) ListConsumerKeys(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	var total int64
	q := h.DB.Model(&domain.ConsumerKey{})
	_ = q.Count(&total).Error
	var items []domain.ConsumerKey
	if err := q.Order("id ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]consumerDTO, 0, len(items))
	for _, k := range items {
		out = append(out, toConsumerDTO(k, false))
	}
	h.attachConsumerRouteGroups(out)
	httpx.List(c, out, total, page, pageSize)
}

type consumerBody struct {
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	QuotaUSD      *float64 `json:"quota_usd"`
	RPM           *int     `json:"rpm"`
	RouteGroupIDs *[]uint  `json:"route_group_ids"`
}

func (h *Admin) consumerOut(k domain.ConsumerKey, includeRaw bool) consumerDTO {
	d := toConsumerDTO(k, includeRaw)
	sl := []consumerDTO{d}
	h.attachConsumerRouteGroups(sl)
	return sl[0]
}

func (h *Admin) CreateConsumerKey(c *gin.Context) {
	var body consumerBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		httpx.BadRequest(c, "name is required")
		return
	}
	raw, err := generateConsumerKey()
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	quota := 0.0
	if body.QuotaUSD != nil {
		quota = *body.QuotaUSD
	}
	rpm := 0
	if body.RPM != nil {
		rpm = *body.RPM
	}
	k := domain.ConsumerKey{
		Name:      name,
		Key:       raw,
		KeyPrefix: crypto.KeyPreview(raw),
		Status:    domain.StatusEnabled,
		QuotaUSD:  quota,
		RPM:       rpm,
	}
	if body.Status != "" {
		if !domain.ValidStatus(body.Status) {
			httpx.BadRequest(c, "status must be enabled or disabled")
			return
		}
		k.Status = body.Status
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&k).Error; err != nil {
			return err
		}
		if body.RouteGroupIDs != nil {
			return setConsumerRouteGroups(tx, k.ID, *body.RouteGroupIDs)
		}
		return nil
	})
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.Created(c, h.consumerOut(k, true))
}

func (h *Admin) UpdateConsumerKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var k domain.ConsumerKey
	if err := h.DB.First(&k, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body consumerBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	if name := strings.TrimSpace(body.Name); name != "" {
		k.Name = name
	}
	if body.Status != "" {
		if !domain.ValidStatus(body.Status) {
			httpx.BadRequest(c, "status must be enabled or disabled")
			return
		}
		k.Status = body.Status
	}
	if body.QuotaUSD != nil {
		k.QuotaUSD = *body.QuotaUSD
	}
	if body.RPM != nil {
		k.RPM = *body.RPM
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&k).Error; err != nil {
			return err
		}
		if body.RouteGroupIDs != nil {
			return setConsumerRouteGroups(tx, k.ID, *body.RouteGroupIDs)
		}
		return nil
	})
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OK(c, h.consumerOut(k, false))
}

func (h *Admin) DeleteConsumerKey(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("consumer_key_id = ?", id).Delete(&domain.ConsumerRouteGroup{}).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.ConsumerKey{}, id).Error
	})
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"id": id})
}

func (h *Admin) ListBalances(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	q := h.DB.Model(&domain.PlatformKey{})
	var total int64
	_ = q.Count(&total).Error
	var items []domain.PlatformKey
	if err := h.DB.Preload("Upstream").Order("id ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]keyDTO, 0, len(items))
	for _, k := range items {
		out = append(out, toKeyDTO(k))
	}
	h.enrichKeys(out)
	httpx.List(c, out, total, page, pageSize)
}

func (h *Admin) RefreshAllBalances(c *gin.Context) {
	ok, fail := h.Ops.RefreshAllBalances(c.Request.Context())
	httpx.OK(c, gin.H{
		"ok":      ok,
		"failed":  fail,
		"message": fmt.Sprintf("成功 %d 个提供商，失败 %d", ok, fail),
	})
}

func (h *Admin) Status(c *gin.Context) {
	var ups []domain.Upstream
	if err := h.DB.Order("id ASC").Find(&ups).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var keys []domain.PlatformKey
	if err := h.DB.Preload("Upstream").Order("id ASC").Find(&keys).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	flat := make([]keyDTO, 0, len(keys))
	counts := map[string]int{}
	for _, k := range keys {
		flat = append(flat, toKeyDTO(k))
		counts[k.HealthStatus]++
	}
	h.enrichKeys(flat)
	byUp := map[uint][]keyDTO{}
	for _, dto := range flat {
		byUp[dto.UpstreamID] = append(byUp[dto.UpstreamID], dto)
	}
	type upStatus struct {
		ID     uint     `json:"id"`
		Name   string   `json:"name"`
		Kind   string   `json:"kind"`
		Status string   `json:"status"`
		Keys   []keyDTO `json:"keys"`
	}
	rows := make([]upStatus, 0, len(ups))
	for _, u := range ups {
		ks := byUp[u.ID]
		if ks == nil {
			ks = []keyDTO{}
		}
		rows = append(rows, upStatus{
			ID:     u.ID,
			Name:   u.Name,
			Kind:   u.Kind,
			Status: u.Status,
			Keys:   ks,
		})
	}
	httpx.OK(c, gin.H{
		"upstreams": rows,
		"items":     flat,
		"counts":    counts,
	})
}

func (h *Admin) RunProbes(c *gin.Context) {
	var body struct {
		Deep       bool  `json:"deep"`
		UpstreamID *uint `json:"upstream_id"`
		KeyID      *uint `json:"key_id"`
	}
	_ = c.ShouldBindJSON(&body)
	if body.KeyID != nil && *body.KeyID > 0 {
		out, err := h.Ops.ProbeKey(c.Request.Context(), *body.KeyID, body.Deep)
		if err != nil {
			writeGormErr(c, err)
			return
		}
		httpx.OK(c, out)
		return
	}
	ok, fail, skipped := h.Ops.ProbeFiltered(c.Request.Context(), body.Deep, body.UpstreamID, 0)
	httpx.OK(c, gin.H{
		"ok":      ok,
		"failed":  fail,
		"skipped": skipped,
		"message": fmt.Sprintf("成功 %d，失败 %d", ok, fail),
	})
}

func (h *Admin) ListRequestLogs(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	snapshotAt := time.Now().UTC()
	if raw := c.Query("snapshot_at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.BadRequest(c, "invalid snapshot_at")
			return
		}
		snapshotAt = parsed.UTC()
	}
	q := h.DB.Model(&domain.RequestLog{})
	if v := strings.TrimSpace(c.Query("upstream_id")); v != "" {
		q = q.Where("upstream_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("key_id")); v != "" {
		q = q.Where("platform_key_id = ?", v)
	}
	if v := strings.TrimSpace(c.Query("success")); v != "" {
		q = q.Where("completed_at <= ?", snapshotAt)
		if v == "true" || v == "1" {
			q = q.Where("in_flight = ? AND success = ?", false, true)
		} else if v == "false" || v == "0" {
			q = q.Where("in_flight = ? AND success = ?", false, false)
		}
	}
	if v := strings.TrimSpace(c.Query("model")); v != "" {
		q = q.Where("model = ?", v)
	}
	if v := strings.TrimSpace(c.Query("from")); v != "" {
		if t, err := parseTime(v); err == nil {
			q = q.Where("created_at >= ?", t)
		}
	}
	if v := strings.TrimSpace(c.Query("to")); v != "" {
		if t, err := parseTime(v); err == nil {
			q = q.Where("created_at <= ?", t)
		}
	}
	var snapshotID uint64
	if raw, exists := c.GetQuery("snapshot_id"); exists {
		var err error
		snapshotID, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			httpx.BadRequest(c, "invalid snapshot_id")
			return
		}
	} else {
		if err := q.Session(&gorm.Session{}).Select("COALESCE(MAX(id), 0)").Scan(&snapshotID).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
	}
	q = q.Where("id <= ?", snapshotID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.RequestLog
	if err := q.Omit("RequestHeaders", "RequestBody", "ResponseHeaders", "ResponseBody").
		Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]logDTO, 0, len(items))
	upNames := map[uint]string{}
	ckNames := map[uint]string{}
	for _, l := range items {
		var un, cn string
		if l.UpstreamID != nil {
			if name, ok := upNames[*l.UpstreamID]; ok {
				un = name
			} else {
				var u domain.Upstream
				if err := h.DB.Select("name").First(&u, *l.UpstreamID).Error; err == nil {
					un = u.Name
					upNames[*l.UpstreamID] = un
				}
			}
		}
		if l.ConsumerKeyID != nil {
			if name, ok := ckNames[*l.ConsumerKeyID]; ok {
				cn = name
			} else {
				var k domain.ConsumerKey
				if err := h.DB.Select("name").First(&k, *l.ConsumerKeyID).Error; err == nil {
					cn = k.Name
					ckNames[*l.ConsumerKeyID] = cn
				}
			}
		}
		out = append(out, toLogDTO(l, un, cn))
	}
	h.attachLogCosts(out)
	httpx.OK(c, gin.H{"items": out, "total": total, "page": page, "page_size": pageSize, "snapshot_id": snapshotID, "snapshot_at": snapshotAt})
}

func (h *Admin) GetRequestLog(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var l domain.RequestLog
	if err := h.DB.First(&l, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	d := toLogDetailDTO(l, h.lookupUpstreamName(l.UpstreamID), h.lookupConsumerName(l.ConsumerKeyID))
	tmp := []logDTO{d.logDTO}
	h.attachLogCosts(tmp)
	d.logDTO = tmp[0]
	httpx.OK(c, d)
}

func (h *Admin) lookupUpstreamName(id *uint) string {
	if id == nil {
		return ""
	}
	var u domain.Upstream
	if err := h.DB.Select("name").First(&u, *id).Error; err != nil {
		return ""
	}
	return u.Name
}

func (h *Admin) lookupConsumerName(id *uint) string {
	if id == nil {
		return ""
	}
	var k domain.ConsumerKey
	if err := h.DB.Select("name").First(&k, *id).Error; err != nil {
		return ""
	}
	return k.Name
}

func (h *Admin) keyOut(k domain.PlatformKey) keyDTO {
	dto := toKeyDTO(k)
	sl := []keyDTO{dto}
	h.enrichKeys(sl)
	return sl[0]
}

func (h *Admin) enrichKeys(keys []keyDTO) {
	h.attachLastProbeAt(keys)
	h.attachHealthPulse(keys)
	h.attachCacheRate(keys)
	h.attachChannelScore(keys)
	h.attachRouteGroups(keys)
}

func (h *Admin) attachHealthPulse(keys []keyDTO) {
	if len(keys) == 0 || h.Ops == nil {
		return
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	pulses := h.Ops.HealthPulses(context.TODO(), ids)
	for i := range keys {
		cells := pulses[keys[i].ID]
		if len(cells) == 0 {
			continue
		}
		out := make([]pulseCell, len(cells))
		for j, c := range cells {
			out[j] = pulseCell{
				Start:         c.Start,
				State:         c.State,
				Ok:            c.Ok,
				Fail:          c.Fail,
				LastLatencyMs: c.LastLatencyMs,
				LatencyP50Ms:  c.LatencyP50Ms,
				Score:         c.Score,
			}
		}
		keys[i].HealthPulse = out
	}
}

func (h *Admin) attachCacheRate(keys []keyDTO) {
	if len(keys) == 0 || h.Ops == nil {
		return
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	rates := h.Ops.KeyCacheRates(context.TODO(), ids)
	for i := range keys {
		c, ok := rates[keys[i].ID]
		if !ok || c.Samples == 0 {
			continue
		}
		rate := c.Rate
		keys[i].CacheRate = &rate
		keys[i].CacheSamples = c.Samples
	}
}

func (h *Admin) attachChannelScore(keys []keyDTO) {
	if len(keys) == 0 || h.Ops == nil {
		return
	}
	cfg := domain.DefaultSchedulerSettings()
	if h.Picker != nil {
		cfg = h.Picker.Settings()
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	window := time.Duration(cfg.WindowMinutes) * time.Minute
	stats := h.Ops.KeyWindowStatsMap(context.TODO(), ids, window, cfg.WindowMaxSamples)
	for i := range keys {
		st, ok := stats[keys[i].ID]
		if !ok || st.Samples == 0 {
			continue
		}
		q := picker.Quality(picker.ScoreInputs{
			Success:    st.Success,
			Samples:    st.Samples,
			Cache:      st.Cache,
			HasCache:   st.HasCache,
			LatencyP50: st.LatencyP50,
			HasLatency: st.HasLatency,
		}, cfg)
		score := int(math.Round(100 * q))
		if score < 0 {
			score = 0
		}
		if score > 100 {
			score = 100
		}
		keys[i].ChannelScore = &score
		meta := scoreMeta{
			Samples:   st.Samples,
			Success:   st.Success,
			LowSample: st.Samples < cfg.MinSamples,
			Terms:     []string{"success"},
		}
		if st.HasLatency {
			meta.LatencyP50 = st.LatencyP50
			meta.Terms = append(meta.Terms, "latency")
		}
		if st.HasCache {
			c := st.Cache
			meta.Cache = &c
			meta.Terms = append(meta.Terms, "cache")
		}
		keys[i].ChannelScoreMeta = &meta
	}
}

func (h *Admin) attachLastProbeAt(keys []keyDTO) {
	if len(keys) == 0 {
		return
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	var rows []struct {
		PlatformKeyID uint             `gorm:"column:platform_key_id"`
		LastAt        domain.LooseTime `gorm:"column:last_at"`
	}
	_ = h.DB.Model(&domain.ProbeLog{}).
		Select("platform_key_id, MAX(created_at) as last_at").
		Where("platform_key_id IN ? AND kind IN ?", ids, []string{domain.ProbeLight, domain.ProbeDeep}).
		Group("platform_key_id").
		Scan(&rows).Error
	times := make(map[uint]time.Time, len(rows))
	for _, r := range rows {
		if t := r.LastAt.Time(); !t.IsZero() {
			times[r.PlatformKeyID] = t
		}
	}
	for i := range keys {
		if t, ok := times[keys[i].ID]; ok {
			tt := t
			keys[i].LastProbeAt = &tt
		}
	}
}

func writeGormErr(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpx.NotFound(c, "not found")
		return
	}
	httpx.Internal(c, err.Error())
}

func generateConsumerKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(b), nil
}

func (h *Admin) GetScheduler(c *gin.Context) {
	if h.Picker == nil {
		httpx.Internal(c, "scheduler unavailable")
		return
	}
	httpx.OK(c, h.Picker.Settings())
}

func (h *Admin) UpdateScheduler(c *gin.Context) {
	if h.Picker == nil {
		httpx.Internal(c, "scheduler unavailable")
		return
	}
	var body domain.SchedulerSettings
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	if err := h.Picker.UpdateSettings(c.Request.Context(), body); err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, h.Picker.Settings())
}

func (h *Admin) GetModelCatalog(c *gin.Context) {
	snap, err := h.Ops.ListModelCatalog(c.Request.Context())
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, snap)
}

func (h *Admin) SyncModelCatalog(c *gin.Context) {
	res, err := h.Ops.SyncModelCatalog(c.Request.Context())
	if err != nil {
		httpx.Fail(c, http.StatusBadGateway, "catalog_sync_failed", err.Error())
		return
	}
	if h.Picker != nil {
		h.Picker.Reload()
	}
	snap, _ := h.Ops.ListModelCatalog(c.Request.Context())
	httpx.OK(c, gin.H{
		"source":      res.Source,
		"model_count": res.ModelCount,
		"vendors":     snap.Vendors,
		"synced_at":   res.SyncedAt,
		"updated":     res.Updated,
		"message":     fmt.Sprintf("已同步 %d 个模型", res.ModelCount),
	})
}

func (h *Admin) ExplainScheduler(c *gin.Context) {
	if h.Picker == nil {
		httpx.Internal(c, "scheduler unavailable")
		return
	}
	protocol := strings.TrimSpace(c.Query("protocol"))
	if protocol == "" {
		protocol = domain.ProtocolAnthropic
	}
	if !domain.ValidProtocol(protocol) {
		httpx.BadRequest(c, "invalid protocol")
		return
	}
	model := strings.TrimSpace(c.Query("model"))
	req := picker.Request{
		Protocol: protocol,
		Model:    model,
		Session:  strings.TrimSpace(c.Query("session")),
	}
	var consumerID uint
	bound := false
	if raw := strings.TrimSpace(c.Query("consumer_key_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			httpx.BadRequest(c, "invalid consumer_key_id")
			return
		}
		consumerID = uint(id)
		allow, drift, b, err := ops.ResolveAllowKeys(c.Request.Context(), h.DB, consumerID, protocol, model)
		if err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		bound = b
		if bound {
			req.AllowKeys = allow
			req.DriftKeys = drift
		}
	}
	cands, err := h.Picker.Explain(c.Request.Context(), req)
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if cands == nil {
		cands = []picker.Candidate{}
	}
	httpx.OK(c, gin.H{
		"settings":        h.Picker.Settings(),
		"protocol":        protocol,
		"model":           model,
		"consumer_key_id": consumerID,
		"route_bound":     bound,
		"candidates":      cands,
		"items":           cands,
	})
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(unix, 0), nil
	}
	return time.Time{}, errors.New("invalid time")
}

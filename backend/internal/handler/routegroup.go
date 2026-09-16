package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"
	"simple-up-manage/internal/ops"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type routeGroupDTO struct {
	ID             uint      `json:"id"`
	Name           string    `json:"name"`
	Protocol       string    `json:"protocol"`
	Models         []string  `json:"models"`
	RateMin        *float64  `json:"rate_min"`
	RateMax        *float64  `json:"rate_max"`
	SaleMultiplier *float64  `json:"sale_multiplier"`
	Description    string    `json:"description"`
	Status         string    `json:"status"`
	MemberCount    int       `json:"member_count"`
	ConsumerCount  int       `json:"consumer_count"`
	DriftCount     int       `json:"drift_count"`
	KeyIDs         []uint    `json:"key_ids"`
	Consumers      []refDTO  `json:"consumers"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func toRouteGroupDTO(g domain.RouteGroup) routeGroupDTO {
	models := []string(g.Models)
	if models == nil {
		models = []string{}
	}
	return routeGroupDTO{
		ID:             g.ID,
		Name:           g.Name,
		Protocol:       g.Protocol,
		Models:         models,
		RateMin:        g.RateMin,
		RateMax:        g.RateMax,
		SaleMultiplier: g.SaleMultiplier,
		Description:    g.Description,
		Status:         g.Status,
		KeyIDs:         []uint{},
		Consumers:      []refDTO{},
		CreatedAt:      g.CreatedAt,
		UpdatedAt:      g.UpdatedAt,
	}
}

// decorateRouteGroups fills member/consumer/drift stats for a batch of groups.
func (h *Admin) decorateRouteGroups(ctx context.Context, groups []domain.RouteGroup) ([]routeGroupDTO, error) {
	out := make([]routeGroupDTO, 0, len(groups))
	if len(groups) == 0 {
		return out, nil
	}
	ids := make([]uint, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	var members []domain.RouteGroupKey
	if err := h.DB.WithContext(ctx).Where("route_group_id IN ?", ids).Order("platform_key_id ASC").Find(&members).Error; err != nil {
		return nil, err
	}
	var links []domain.ConsumerRouteGroup
	if err := h.DB.WithContext(ctx).Where("route_group_id IN ?", ids).Find(&links).Error; err != nil {
		return nil, err
	}

	// Rates for drift detection.
	keyIDs := map[uint]struct{}{}
	for _, m := range members {
		keyIDs[m.PlatformKeyID] = struct{}{}
	}
	keyRate := map[uint]float64{}
	if len(keyIDs) > 0 {
		list := make([]uint, 0, len(keyIDs))
		for id := range keyIDs {
			list = append(list, id)
		}
		var keys []domain.PlatformKey
		if err := h.DB.WithContext(ctx).Select("id", "rate_multiplier").Where("id IN ?", list).Find(&keys).Error; err != nil {
			return nil, err
		}
		for _, k := range keys {
			keyRate[k.ID] = k.RateMultiplier
		}
	}

	consumerIDs := map[uint]struct{}{}
	for _, l := range links {
		consumerIDs[l.ConsumerKeyID] = struct{}{}
	}
	consumerName := map[uint]string{}
	if len(consumerIDs) > 0 {
		list := make([]uint, 0, len(consumerIDs))
		for id := range consumerIDs {
			list = append(list, id)
		}
		var consumers []domain.ConsumerKey
		if err := h.DB.WithContext(ctx).Select("id", "name").Where("id IN ?", list).Find(&consumers).Error; err != nil {
			return nil, err
		}
		for _, ck := range consumers {
			consumerName[ck.ID] = ck.Name
		}
	}

	byGroup := map[uint]*routeGroupDTO{}
	for _, g := range groups {
		d := toRouteGroupDTO(g)
		out = append(out, d)
		byGroup[g.ID] = &out[len(out)-1]
	}
	groupByID := map[uint]*domain.RouteGroup{}
	for i := range groups {
		groupByID[groups[i].ID] = &groups[i]
	}
	for _, m := range members {
		d := byGroup[m.RouteGroupID]
		if d == nil {
			continue
		}
		d.KeyIDs = append(d.KeyIDs, m.PlatformKeyID)
		d.MemberCount++
		if rate, ok := keyRate[m.PlatformKeyID]; ok && !ops.RateInRange(groupByID[m.RouteGroupID], rate) {
			d.DriftCount++
		}
	}
	for _, l := range links {
		d := byGroup[l.RouteGroupID]
		if d == nil {
			continue
		}
		d.ConsumerCount++
		d.Consumers = append(d.Consumers, refDTO{ID: l.ConsumerKeyID, Name: consumerName[l.ConsumerKeyID]})
	}
	for i := range out {
		sort.Slice(out[i].Consumers, func(a, b int) bool { return out[i].Consumers[a].ID < out[i].Consumers[b].ID })
	}
	return out, nil
}

func (h *Admin) ListRouteGroups(c *gin.Context) {
	var groups []domain.RouteGroup
	if err := h.DB.Order("id ASC").Find(&groups).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out, err := h.decorateRouteGroups(c.Request.Context(), groups)
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"items": out})
}

type routeGroupBody struct {
	Name           *string   `json:"name"`
	Protocol       *string   `json:"protocol"`
	Models         *[]string `json:"models"`
	RateMin        *float64  `json:"rate_min"`
	RateMax        *float64  `json:"rate_max"`
	ClearRate      bool      `json:"clear_rate"`
	SaleMultiplier *float64  `json:"sale_multiplier"`
	Description    *string   `json:"description"`
	Status         *string   `json:"status"`
	KeyIDs         *[]uint   `json:"key_ids"`
}

func (h *Admin) applyRouteGroupBody(g *domain.RouteGroup, body routeGroupBody) error {
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			return errors.New("name is required")
		}
		g.Name = name
	}
	if body.Protocol != nil {
		p := strings.ToLower(strings.TrimSpace(*body.Protocol))
		if p != "" && !domain.ValidProtocol(p) {
			return errors.New("protocol must be openai, anthropic, or empty")
		}
		g.Protocol = p
	}
	if body.Models != nil {
		g.Models = domain.JSONStrings(domain.NormalizeStrings(*body.Models))
	}
	if body.ClearRate {
		g.RateMin, g.RateMax = nil, nil
	} else {
		if body.RateMin != nil {
			g.RateMin = body.RateMin
		}
		if body.RateMax != nil {
			g.RateMax = body.RateMax
		}
	}
	if g.RateMin != nil && g.RateMax != nil && *g.RateMin > *g.RateMax {
		return errors.New("rate_min must be <= rate_max")
	}
	if body.Description != nil {
		g.Description = strings.TrimSpace(*body.Description)
	}
	if body.Status != nil {
		s := strings.TrimSpace(*body.Status)
		if s == "" {
			s = domain.StatusEnabled
		}
		if !domain.ValidStatus(s) {
			return errors.New("status must be enabled or disabled")
		}
		g.Status = s
	}
	if g.Status == "" {
		g.Status = domain.StatusEnabled
	}
	return nil
}

func applySaleMultiplier(g *domain.RouteGroup, fields map[string]json.RawMessage, body routeGroupBody) error {
	raw, ok := fields["sale_multiplier"]
	if !ok {
		return nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		g.SaleMultiplier = nil
		return nil
	}
	if body.SaleMultiplier == nil {
		return errors.New("sale_multiplier must be a number or null")
	}
	if err := dashboard.ValidSaleMultiplier(*body.SaleMultiplier); err != nil {
		return err
	}
	v := *body.SaleMultiplier
	g.SaleMultiplier = &v
	return nil
}

func (h *Admin) CreateRouteGroup(c *gin.Context) {
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	var body routeGroupBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &fields)
	if body.Name == nil {
		httpx.BadRequest(c, "name is required")
		return
	}
	var g domain.RouteGroup
	if err := h.applyRouteGroupBody(&g, body); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if err := applySaleMultiplier(&g, fields, body); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&g).Error; err != nil {
			return err
		}
		if body.KeyIDs != nil {
			return replaceRouteGroupKeys(tx, g.ID, *body.KeyIDs)
		}
		return nil
	})
	if err != nil {
		if isUniqueErr(err) {
			httpx.BadRequest(c, "同名路由分组已存在")
			return
		}
		httpx.Internal(c, err.Error())
		return
	}
	h.respondRouteGroup(c, g.ID, true)
}

func (h *Admin) UpdateRouteGroup(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var g domain.RouteGroup
	if err := h.DB.First(&g, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	var body routeGroupBody
	if err := json.Unmarshal(rawBody, &body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &fields)
	if err := h.applyRouteGroupBody(&g, body); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	if err := applySaleMultiplier(&g, fields, body); err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	err = h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&g).Error; err != nil {
			return err
		}
		if body.KeyIDs != nil {
			return replaceRouteGroupKeys(tx, g.ID, *body.KeyIDs)
		}
		return nil
	})
	if err != nil {
		if isUniqueErr(err) {
			httpx.BadRequest(c, "同名路由分组已存在")
			return
		}
		httpx.Internal(c, err.Error())
		return
	}
	h.respondRouteGroup(c, g.ID, false)
}

func (h *Admin) DeleteRouteGroup(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var links []domain.ConsumerRouteGroup
	if err := h.DB.Where("route_group_id = ?", id).Find(&links).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if len(links) > 0 {
		ids := make([]uint, 0, len(links))
		for _, l := range links {
			ids = append(ids, l.ConsumerKeyID)
		}
		var consumers []domain.ConsumerKey
		_ = h.DB.Select("id", "name").Where("id IN ?", ids).Find(&consumers).Error
		names := make([]string, 0, len(consumers))
		for _, ck := range consumers {
			names = append(names, ck.Name)
		}
		httpx.Fail(c, 409, "conflict", "分组仍绑定 API 密钥（"+strings.Join(names, "、")+"）。请先解绑或改绑后再删除。")
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("route_group_id = ?", id).Delete(&domain.RouteGroupKey{}).Error; err != nil {
			return err
		}
		return tx.Delete(&domain.RouteGroup{}, id).Error
	})
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"id": id})
}

func (h *Admin) respondRouteGroup(c *gin.Context, id uint, created bool) {
	var g domain.RouteGroup
	if err := h.DB.First(&g, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	out, err := h.decorateRouteGroups(c.Request.Context(), []domain.RouteGroup{g})
	if err != nil || len(out) == 0 {
		httpx.Internal(c, "failed to load route group")
		return
	}
	if created {
		httpx.Created(c, out[0])
		return
	}
	httpx.OK(c, out[0])
}

// replaceRouteGroupKeys sets the full member list of a route group.
func replaceRouteGroupKeys(tx *gorm.DB, groupID uint, keyIDs []uint) error {
	if err := tx.Where("route_group_id = ?", groupID).Delete(&domain.RouteGroupKey{}).Error; err != nil {
		return err
	}
	return insertRouteGroupKeys(tx, groupID, keyIDs)
}

func insertRouteGroupKeys(tx *gorm.DB, groupID uint, keyIDs []uint) error {
	keyIDs = uniqueIDs(keyIDs)
	if len(keyIDs) == 0 {
		return nil
	}
	var count int64
	if err := tx.Model(&domain.PlatformKey{}).Where("id IN ?", keyIDs).Count(&count).Error; err != nil {
		return err
	}
	if int(count) != len(keyIDs) {
		return errors.New("some key_ids do not exist")
	}
	rows := make([]domain.RouteGroupKey, 0, len(keyIDs))
	now := time.Now()
	for _, id := range keyIDs {
		rows = append(rows, domain.RouteGroupKey{RouteGroupID: groupID, PlatformKeyID: id, CreatedAt: now})
	}
	// Ignore duplicates so batch add is idempotent.
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

func uniqueIDs(in []uint) []uint {
	seen := map[uint]struct{}{}
	out := make([]uint, 0, len(in))
	for _, id := range in {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

type routeGroupKeysBody struct {
	KeyIDs []uint `json:"key_ids"`
}

func (h *Admin) SetRouteGroupKeys(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var g domain.RouteGroup
	if err := h.DB.First(&g, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body routeGroupKeysBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	if body.KeyIDs == nil {
		body.KeyIDs = []uint{}
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		return replaceRouteGroupKeys(tx, g.ID, body.KeyIDs)
	})
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	h.respondRouteGroup(c, g.ID, false)
}

type routeGroupBatchBody struct {
	Add    []uint `json:"add"`
	Remove []uint `json:"remove"`
}

func (h *Admin) BatchRouteGroupKeys(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var g domain.RouteGroup
	if err := h.DB.First(&g, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body routeGroupBatchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if rm := uniqueIDs(body.Remove); len(rm) > 0 {
			if err := tx.Where("route_group_id = ? AND platform_key_id IN ?", g.ID, rm).Delete(&domain.RouteGroupKey{}).Error; err != nil {
				return err
			}
		}
		return insertRouteGroupKeys(tx, g.ID, body.Add)
	})
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	h.respondRouteGroup(c, g.ID, false)
}

type keyRouteGroupsBody struct {
	RouteGroupIDs []uint `json:"route_group_ids"`
}

// SetKeyRouteGroups replaces the set of route groups a single platform key belongs to.
func (h *Admin) SetKeyRouteGroups(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var k domain.PlatformKey
	if err := h.DB.Preload("Upstream").First(&k, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	var body keyRouteGroupsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	groupIDs := uniqueIDs(body.RouteGroupIDs)
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if len(groupIDs) > 0 {
			var count int64
			if err := tx.Model(&domain.RouteGroup{}).Where("id IN ?", groupIDs).Count(&count).Error; err != nil {
				return err
			}
			if int(count) != len(groupIDs) {
				return errors.New("some route_group_ids do not exist")
			}
		}
		if err := tx.Where("platform_key_id = ?", k.ID).Delete(&domain.RouteGroupKey{}).Error; err != nil {
			return err
		}
		if len(groupIDs) == 0 {
			return nil
		}
		rows := make([]domain.RouteGroupKey, 0, len(groupIDs))
		now := time.Now()
		for _, gid := range groupIDs {
			rows = append(rows, domain.RouteGroupKey{RouteGroupID: gid, PlatformKeyID: k.ID, CreatedAt: now})
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		httpx.BadRequest(c, err.Error())
		return
	}
	httpx.OK(c, h.keyOut(k))
}

type routeCandidateDTO struct {
	ID             uint     `json:"id"`
	Name           string   `json:"name"`
	KeyPreview     string   `json:"key_preview"`
	Status         string   `json:"status"`
	HealthStatus   string   `json:"health_status"`
	LastBalance    *float64 `json:"last_balance"`
	UpstreamID     uint     `json:"upstream_id"`
	UpstreamName   string   `json:"upstream_name"`
	UpstreamKind   string   `json:"upstream_kind"`
	Protocols      []string `json:"protocols"`
	RateMultiplier float64  `json:"rate_multiplier"`
	BillingGroup   string   `json:"billing_group,omitempty"`
	RouteGroupIDs  []uint   `json:"route_group_ids"`
}

// RouteGroupCandidates returns a compact list of every platform key with the
// fields the member picker needs, in a single request.
func (h *Admin) RouteGroupCandidates(c *gin.Context) {
	var keys []domain.PlatformKey
	if err := h.DB.Preload("Upstream").Order("upstream_id ASC, rate_multiplier ASC, id ASC").Find(&keys).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var members []domain.RouteGroupKey
	_ = h.DB.Find(&members).Error
	memberOf := map[uint][]uint{}
	for _, m := range members {
		memberOf[m.PlatformKeyID] = append(memberOf[m.PlatformKeyID], m.RouteGroupID)
	}
	out := make([]routeCandidateDTO, 0, len(keys))
	for _, k := range keys {
		d := routeCandidateDTO{
			ID:             k.ID,
			Name:           k.Name,
			KeyPreview:     k.KeyPreview,
			Status:         k.Status,
			HealthStatus:   k.HealthStatus,
			LastBalance:    k.LastBalance,
			UpstreamID:     k.UpstreamID,
			Protocols:      []string{},
			RateMultiplier: k.RateMultiplier,
			BillingGroup:   k.BillingGroup,
			RouteGroupIDs:  memberOf[k.ID],
		}
		if d.RouteGroupIDs == nil {
			d.RouteGroupIDs = []uint{}
		}
		if k.Upstream != nil {
			d.UpstreamName = k.Upstream.Name
			d.UpstreamKind = k.Upstream.Kind
			d.Protocols = k.EffectiveProtocols()
			d.LastBalance = k.Upstream.LastBalance
		}
		out = append(out, d)
	}
	httpx.OK(c, gin.H{"items": out})
}

// attachRouteGroups fills keyDTO.RouteGroups for a batch of keys.
func (h *Admin) attachRouteGroups(keys []keyDTO) {
	if len(keys) == 0 {
		return
	}
	ids := make([]uint, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	byKey, err := ops.RouteGroupsForKeys(context.TODO(), h.DB, ids)
	if err != nil {
		return
	}
	for i := range keys {
		keys[i].RouteGroups = toRouteGroupRefs(byKey[keys[i].ID])
	}
}

// attachConsumerRouteGroups fills consumerDTO.RouteGroups for a batch of consumers.
func (h *Admin) attachConsumerRouteGroups(items []consumerDTO) {
	if len(items) == 0 {
		return
	}
	ids := make([]uint, 0, len(items))
	for _, k := range items {
		ids = append(ids, k.ID)
	}
	byConsumer, err := ops.RouteGroupsForConsumers(context.TODO(), h.DB, ids)
	if err != nil {
		return
	}
	for i := range items {
		items[i].RouteGroups = toRouteGroupRefs(byConsumer[items[i].ID])
		if len(items[i].RouteGroups) == 1 {
			id := items[i].RouteGroups[0].ID
			items[i].RouteGroupID = &id
		}
	}
}

// setConsumerRouteGroups replaces a consumer key's bound route groups.
func setConsumerRouteGroups(tx *gorm.DB, consumerID uint, groupIDs []uint) error {
	groupIDs = uniqueIDs(groupIDs)
	if len(groupIDs) > 1 {
		return errors.New("API 密钥最多绑定一个分组")
	}
	if len(groupIDs) > 0 {
		var count int64
		if err := tx.Model(&domain.RouteGroup{}).Where("id IN ?", groupIDs).Count(&count).Error; err != nil {
			return err
		}
		if int(count) != len(groupIDs) {
			return errors.New("some route_group_ids do not exist")
		}
	}
	if err := tx.Where("consumer_key_id = ?", consumerID).Delete(&domain.ConsumerRouteGroup{}).Error; err != nil {
		return err
	}
	if len(groupIDs) == 0 {
		return nil
	}
	rows := make([]domain.ConsumerRouteGroup, 0, len(groupIDs))
	now := time.Now()
	for _, gid := range groupIDs {
		rows = append(rows, domain.ConsumerRouteGroup{ConsumerKeyID: consumerID, RouteGroupID: gid, CreatedAt: now})
	}
	return tx.Create(&rows).Error
}

func resolveConsumerGroupIDs(fields map[string]json.RawMessage, body consumerBody) (*[]uint, error) {
	_, hasID := fields["route_group_id"]
	_, hasIDs := fields["route_group_ids"]
	if !hasID && !hasIDs {
		return nil, nil
	}
	fromID := []uint{}
	if hasID {
		if strings.TrimSpace(string(fields["route_group_id"])) == "null" || body.RouteGroupID == nil {
			fromID = []uint{}
		} else {
			fromID = []uint{*body.RouteGroupID}
		}
	}
	fromIDs := []uint{}
	if hasIDs {
		if body.RouteGroupIDs == nil {
			fromIDs = []uint{}
		} else {
			fromIDs = uniqueIDs(*body.RouteGroupIDs)
		}
		if len(fromIDs) > 1 {
			return nil, errors.New("API 密钥最多绑定一个分组")
		}
	}
	if hasID && hasIDs {
		same := len(fromID) == len(fromIDs)
		if same && len(fromID) == 1 && fromID[0] != fromIDs[0] {
			same = false
		}
		if !same {
			return nil, errors.New("route_group_id 与 route_group_ids 表达的绑定不一致")
		}
	}
	if hasID {
		return &fromID, nil
	}
	return &fromIDs, nil
}

func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate")
}

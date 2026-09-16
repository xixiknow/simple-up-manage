package handler

import (
	"context"
	"math"
	"time"

	"github.com/gin-gonic/gin"

	"simple-up-manage/internal/dashboard"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/upstream"

	"gorm.io/gorm"
)

func logID(lg *liveLog) uint {
	if lg == nil {
		return 0
	}
	return lg.id
}

func snapshotConsumerBinding(ctx context.Context, db *gorm.DB, consumerID uint) (id *uint, name string, sale *float64, bound bool, err error) {
	if db == nil {
		return nil, "", nil, false, nil
	}
	var links []domain.ConsumerRouteGroup
	if err = db.WithContext(ctx).Where("consumer_key_id = ?", consumerID).Find(&links).Error; err != nil {
		return nil, "", nil, false, err
	}
	if len(links) == 0 {
		return nil, "", nil, false, nil
	}
	var g domain.RouteGroup
	if err = db.WithContext(ctx).First(&g, links[0].RouteGroupID).Error; err != nil {
		return nil, "", nil, true, err
	}
	gid := g.ID
	return &gid, g.Name, cloneFloat(g.SaleMultiplier), true, nil
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func requiresProbePermission(c *gin.Context) bool {
	return dashboard.SourceFrom(c.Request.Context()) == domain.SourceAdminTest || c.GetString("external_probe_rule") != ""
}

func (h *Gateway) restrictProbeEnabled(ctx context.Context, allow map[uint]struct{}, bound bool) (map[uint]struct{}, bool, error) {
	q := h.DB.WithContext(ctx).Model(&domain.PlatformKey{}).Where("status = ?", domain.StatusEnabled)
	if bound && allow != nil {
		ids := make([]uint, 0, len(allow))
		for id := range allow {
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return allow, true, nil
		}
		q = q.Where("id IN ?", ids)
	}
	var keys []domain.PlatformKey
	if err := q.Select("id", "probe_enabled").Find(&keys).Error; err != nil {
		return nil, false, err
	}
	out := map[uint]struct{}{}
	for _, k := range keys {
		if k.AllowsProbe() {
			out[k.ID] = struct{}{}
		}
	}
	// An empty routing pool is not evidence that a probe switch blocked it.
	return out, len(keys) > 0 && len(out) == 0, nil
}

func (h *Gateway) emitDashAttempt(pk *domain.PlatformKey, up *domain.Upstream, a *domain.RequestAttempt, usage upstream.TokenUsage, lg *liveLog, httpSent bool) {
	if h.Dash == nil || lg == nil || lg.dashUUID == "" || lg.source == "" {
		return
	}
	rate := 1.0
	if pk != nil && pk.RateMultiplier > 0 {
		rate = pk.RateMultiplier
	}
	providerID, providerName := uint(0), ""
	if up != nil {
		providerID, providerName = up.ID, up.Name
	} else if pk != nil {
		providerID = pk.UpstreamID
	}
	price := lg.price
	// BaseCostUSD accepts protocol-native usage, before log token normalization.
	base := dashboard.BaseCostUSD(price, a.Protocol, usage.InputTokens, usage.CacheReadTokens, usage.CacheCreationTokens, usage.OutputTokens, usage.UsageKnown)
	var estimated, reported, consumption *float64
	src := dashboard.CostSourceNone
	if usage.CostUSD != nil {
		reported = usage.CostUSD
		consumption = usage.CostUSD
		src = dashboard.CostSourceReported
	}
	if base != nil {
		v := round8(*base * rate)
		estimated = &v
		if consumption == nil {
			consumption = estimated
			src = dashboard.CostSourceEstimated
		}
	}
	if !httpSent {
		z := 0.0
		estimated, consumption = &z, &z
		src = dashboard.CostSourceNone
	}
	h.Dash.EnqueueAttempt(dashboard.AttemptFact{
		UUID: a.ID, RequestUUID: lg.dashUUID, ProviderID: providerID, ProviderName: providerName,
		KeyID: a.PlatformKeyID, CostMultiplier: rate, StartedAt: a.StartedAt, CompletedAt: a.CompletedAt,
		Result: a.Result, HTTPSent: httpSent, StatusCode: a.StatusCode, TTFTMs: a.TTFTMs, TTFTStatus: a.TTFTStatus,
		Protocol: a.Protocol, Model: a.Model, Path: a.Path, Stream: a.Stream, Source: lg.source,
		InputTokens: a.InputTokens, OutputTokens: a.OutputTokens, CacheReadTokens: a.CacheReadTokens,
		CacheWriteTokens: a.CacheCreationTokens, UsageKnown: usage.UsageKnown,
		EstimatedCostUSD: estimated, ReportedCostUSD: reported, ConsumptionUSD: consumption,
		CostSource: src, BaseCostUSD: base, CreatedAt: a.CompletedAt,
	})
}

func (h *Gateway) emitDashEnd(lg *liveLog, pk *domain.PlatformKey, up *domain.Upstream, protocol, model string, success bool, usage upstream.TokenUsage, ttft int, interrupted bool) {
	if h.Dash == nil || lg == nil || lg.dashUUID == "" {
		return
	}
	lg.mu.Lock()
	if lg.ended {
		lg.mu.Unlock()
		return
	}
	lg.ended = true
	httpN := lg.httpAttempts
	sale := lg.sale
	price := lg.price
	lg.mu.Unlock()
	var pid *uint
	var pname string
	if up != nil {
		id := up.ID
		pid, pname = &id, up.Name
	}
	ttftStatus := "interrupted"
	if ttft > 0 {
		ttftStatus = "measured"
	}
	h.Dash.EnqueueEnd(dashboard.RequestEnd{
		UUID: lg.dashUUID, Model: model, Protocol: protocol, Success: success, Interrupted: interrupted,
		CompletedAt: time.Now().UTC(), HTTPAttempts: httpN,
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheCreationTokens,
		UsageKnown: usage.UsageKnown, InputPrice: floatOrNil(price.OK, price.Input), OutputPrice: floatOrNil(price.OK, price.Output),
		CacheReadCoeff: price.CacheReadCoeff, CacheWriteCoeff: price.CacheWriteCoeff,
		TTFTMs: ttft, TTFTStatus: ttftStatus, FinalProviderID: pid, FinalProviderName: pname,
	})
	_ = sale
}

func floatOrNil(ok bool, v float64) *float64 {
	if !ok {
		return nil
	}
	return &v
}

func round8(v float64) float64 {
	return math.Round(v*1e8) / 1e8
}

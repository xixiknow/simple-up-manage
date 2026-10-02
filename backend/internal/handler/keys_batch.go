package handler

import (
	"errors"
	"fmt"
	"io"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type keyBatchBody struct {
	IDs []uint `json:"ids"`
}

// normalizeKeyIDs validates and de-duplicates a client-provided id list.
func normalizeKeyIDs(c *gin.Context, ids []uint, max int) ([]uint, bool) {
	if len(ids) == 0 {
		httpx.BadRequest(c, "ids is required")
		return nil, false
	}
	if len(ids) > max {
		httpx.BadRequest(c, fmt.Sprintf("at most %d ids", max))
		return nil, false
	}
	seen := make(map[uint]bool, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		httpx.BadRequest(c, "ids is required")
		return nil, false
	}
	return out, true
}

// parseKeyIDs reads and normalizes the shared {ids: []} batch body.
func parseKeyIDs(c *gin.Context, max int) ([]uint, bool) {
	var body keyBatchBody
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		httpx.BadRequest(c, "invalid json")
		return nil, false
	}
	return normalizeKeyIDs(c, body.IDs, max)
}

// BatchKeyStatus enables or disables many keys in one call.
func (h *Admin) BatchKeyStatus(c *gin.Context) {
	var body struct {
		IDs    []uint `json:"ids"`
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		httpx.BadRequest(c, "invalid json")
		return
	}
	if !domain.ValidStatus(body.Status) {
		httpx.BadRequest(c, "status must be enabled or disabled")
		return
	}
	ids, ok := normalizeKeyIDs(c, body.IDs, 500)
	if !ok {
		return
	}
	res := h.DB.Model(&domain.PlatformKey{}).Where("id IN ?", ids).Update("status", body.Status)
	if res.Error != nil {
		httpx.Internal(c, res.Error.Error())
		return
	}
	if h.Ops != nil {
		for _, id := range ids {
			_ = h.Ops.RefreshKeyHealth(c.Request.Context(), id)
		}
	}
	if h.Picker != nil {
		h.Picker.Reload()
	}
	httpx.OK(c, gin.H{"updated": int(res.RowsAffected)})
}

// BatchKeyDelete removes many keys and their route-group links in one transaction.
func (h *Admin) BatchKeyDelete(c *gin.Context) {
	ids, ok := parseKeyIDs(c, 500)
	if !ok {
		return
	}
	var deleted int64
	err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("platform_key_id IN ?", ids).Delete(&domain.RouteGroupKey{}).Error; err != nil {
			return err
		}
		res := tx.Where("id IN ?", ids).Delete(&domain.PlatformKey{})
		deleted = res.RowsAffected
		return res.Error
	})
	if err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	if h.Picker != nil {
		h.Picker.Reload()
	}
	httpx.OK(c, gin.H{"deleted": deleted})
}

// BatchKeyBilling syncs billing rates for many keys sequentially; per-key
// failures (unsupported upstream, upstream error, backoff) are reported in
// the errors list instead of failing the whole batch.
func (h *Admin) BatchKeyBilling(c *gin.Context) {
	ids, ok := parseKeyIDs(c, 200)
	if !ok {
		return
	}
	okCount, failed := 0, 0
	errs := make([]gin.H, 0)
	for _, id := range ids {
		var k domain.PlatformKey
		if err := h.DB.Preload("Upstream").First(&k, id).Error; err != nil {
			failed++
			errs = append(errs, gin.H{"id": id, "error": "key not found"})
			continue
		}
		if k.Upstream == nil || !domain.KindHasBilling(k.Upstream.Kind) {
			failed++
			errs = append(errs, gin.H{"id": id, "error": "该上游不是 sub2api / new-api，无法同步分组倍率"})
			continue
		}
		if h.Ops == nil {
			failed++
			errs = append(errs, gin.H{"id": id, "error": "billing service unavailable"})
			continue
		}
		if err := h.Ops.RefreshBilling(c.Request.Context(), id); err != nil {
			failed++
			errs = append(errs, gin.H{"id": id, "error": err.Error()})
			continue
		}
		okCount++
	}
	httpx.OK(c, gin.H{
		"ok":      okCount,
		"failed":  failed,
		"errors":  errs,
		"message": fmt.Sprintf("成功 %d，失败 %d", okCount, failed),
	})
}

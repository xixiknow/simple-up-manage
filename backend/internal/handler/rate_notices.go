package handler

import (
	"strings"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
)

func (h *Admin) ListRateNotices(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	q := h.DB.Model(&domain.RateChangeNotice{})
	if v := strings.TrimSpace(c.Query("unread")); v == "1" || strings.EqualFold(v, "true") {
		q = q.Where("read_at IS NULL")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.RateChangeNotice
	if err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]domain.RateChangeNotice, 0, len(items))
	out = append(out, items...)
	httpx.List(c, out, total, page, pageSize)
}

func (h *Admin) RateNoticeUnreadCount(c *gin.Context) {
	var n int64
	if err := h.DB.Model(&domain.RateChangeNotice{}).Where("read_at IS NULL").Count(&n).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"unread": n})
}

func (h *Admin) MarkRateNoticeRead(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var n domain.RateChangeNotice
	if err := h.DB.First(&n, id).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	if n.ReadAt == nil {
		now := time.Now()
		if err := h.DB.Model(&n).Update("read_at", now).Error; err != nil {
			httpx.Internal(c, err.Error())
			return
		}
		n.ReadAt = &now
	}
	httpx.OK(c, n)
}

func (h *Admin) MarkAllRateNoticesRead(c *gin.Context) {
	now := time.Now()
	if err := h.DB.Model(&domain.RateChangeNotice{}).Where("read_at IS NULL").Update("read_at", now).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"unread": 0})
}

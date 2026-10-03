package handler

import (
	"encoding/json"
	"strings"
	"time"

	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
)

type noticeDTO struct {
	ID        uint            `json:"id"`
	Kind      string          `json:"kind"`
	Source    string          `json:"source"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	ReadAt    *time.Time      `json:"read_at"`
	CreatedAt time.Time       `json:"created_at"`
}

func toNoticeDTO(n domain.Notice) noticeDTO {
	payload := json.RawMessage(n.Payload)
	if len(payload) == 0 || !json.Valid(payload) {
		payload = json.RawMessage(`{}`)
	}
	return noticeDTO{
		ID:        n.ID,
		Kind:      n.Kind,
		Source:    n.Source,
		Summary:   n.Summary,
		Payload:   payload,
		ReadAt:    n.ReadAt,
		CreatedAt: n.CreatedAt,
	}
}

func (h *Admin) ListNotices(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	q := h.DB.Model(&domain.Notice{})
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		q = q.Where("kind = ?", kind)
	}
	if v := strings.TrimSpace(c.Query("unread")); v == "1" || strings.EqualFold(v, "true") {
		q = q.Where("read_at IS NULL")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	var items []domain.Notice
	if err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	out := make([]noticeDTO, 0, len(items))
	for _, n := range items {
		out = append(out, toNoticeDTO(n))
	}
	httpx.List(c, out, total, page, pageSize)
}

func (h *Admin) NoticeUnreadCount(c *gin.Context) {
	var rows []struct {
		Kind string
		N    int64
	}
	if err := h.DB.Model(&domain.Notice{}).
		Select("kind, COUNT(1) AS n").
		Where("read_at IS NULL").
		Group("kind").
		Scan(&rows).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	byKind := map[string]int64{}
	var unread int64
	for _, r := range rows {
		byKind[r.Kind] = r.N
		unread += r.N
	}
	httpx.OK(c, gin.H{"unread": unread, "by_kind": byKind})
}

func (h *Admin) MarkNoticeRead(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var n domain.Notice
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
	httpx.OK(c, toNoticeDTO(n))
}

func (h *Admin) MarkAllNoticesRead(c *gin.Context) {
	q := h.DB.Model(&domain.Notice{}).Where("read_at IS NULL")
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		q = q.Where("kind = ?", kind)
	}
	now := time.Now()
	if err := q.Update("read_at", now).Error; err != nil {
		httpx.Internal(c, err.Error())
		return
	}
	httpx.OK(c, gin.H{"unread": 0})
}

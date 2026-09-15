package handler

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"simple-up-manage/internal/domain"
	"simple-up-manage/internal/httpx"
)

func (h *Admin) GetLogBody(c *gin.Context) {
	id, ok := httpx.ParseID(c, "id")
	if !ok {
		return
	}
	var meta domain.LogBody
	if err := h.DB.Where("id = ? AND request_log_id = ?", c.Param("body_id"), id).First(&meta).Error; err != nil {
		writeGormErr(c, err)
		return
	}
	if h.Ops == nil || h.Ops.Archives == nil || meta.Status == "saving" || meta.Status == "error" {
		c.AbortWithStatusJSON(http.StatusConflict, gin.H{"ok": false, "error": gin.H{"code": "body_unavailable", "message": "正文尚未完成或保存失败"}})
		return
	}
	r, err := h.Ops.Archives.Open(meta.ID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusGone, gin.H{"ok": false, "error": gin.H{"code": "body_expired", "message": "正文已过期或不可用"}})
		return
	}
	defer r.Close()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if c.Query("download") == "1" {
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="`+meta.ID+`.txt"`)
		_, _ = io.Copy(c.Writer, r)
		return
	}
	offset, err := strconv.ParseInt(c.DefaultQuery("offset", "0"), 10, 64)
	if err != nil || offset < 0 || offset > meta.SavedBytes {
		httpx.BadRequest(c, "invalid offset")
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "262144"))
	if err != nil || limit < 4 || limit > 262144 {
		httpx.BadRequest(c, "invalid limit")
		return
	}
	if _, err = io.CopyN(io.Discard, r, offset); err != nil {
		httpx.Internal(c, "正文读取失败")
		return
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(limit+utf8.UTFMax)))
	if err != nil {
		httpx.Internal(c, "正文读取失败")
		return
	}
	n := min(len(b), limit)
	if n < len(b) {
		for n > 0 && !utf8.RuneStart(b[n]) {
			n--
		}
	}
	if n == 0 && len(b) > 0 {
		n = min(len(b), limit)
	}
	httpx.OK(c, gin.H{"text": strings.ToValidUTF8(string(b[:n]), "\uFFFD"), "next_offset": offset + int64(n), "eof": offset+int64(n) >= meta.SavedBytes})
}

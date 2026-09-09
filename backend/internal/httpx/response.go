package httpx

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

type Envelope struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ListData struct {
	Items    any   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{OK: true, Data: data})
}

func Created(c *gin.Context, data any) {
	c.JSON(http.StatusCreated, Envelope{OK: true, Data: data})
}

func List(c *gin.Context, items any, total int64, page, pageSize int) {
	if items == nil {
		items = []any{}
	}
	OK(c, ListData{Items: items, Total: total, Page: page, PageSize: pageSize})
}

func Fail(c *gin.Context, status int, code, message string) {
	c.JSON(status, Envelope{
		OK: false,
		Error: &APIError{
			Code:    code,
			Message: message,
		},
	})
}

func BadRequest(c *gin.Context, message string) {
	Fail(c, http.StatusBadRequest, "validation_error", message)
}

func Unauthorized(c *gin.Context, message string) {
	Fail(c, http.StatusUnauthorized, "unauthorized", message)
}

func NotFound(c *gin.Context, message string) {
	Fail(c, http.StatusNotFound, "not_found", message)
}

func Internal(c *gin.Context, message string) {
	Fail(c, http.StatusInternalServerError, "internal", message)
}

func PageParams(c *gin.Context) (page, pageSize int) {
	page = atoiDefault(c.Query("page"), 1)
	pageSize = atoiDefault(c.Query("page_size"), 20)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func ParseID(c *gin.Context, name string) (uint, bool) {
	id64, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id64 == 0 {
		BadRequest(c, "invalid "+name)
		return 0, false
	}
	return uint(id64), true
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

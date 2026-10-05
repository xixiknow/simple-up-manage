package handler

import (
	"net/http"

	"simple-up-manage/internal/buildinfo"

	"github.com/gin-gonic/gin"
)

func Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "version": buildinfo.Version})
}

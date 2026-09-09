package middleware

import (
	"strings"

	"simple-up-manage/internal/httpx"

	"github.com/gin-gonic/gin"
)

func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Anthropic-Version, Anthropic-Beta, X-Api-Key, X-Request-Id")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Max-Age", "86400")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

func AdminAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		got := bearerToken(c)
		if got == "" || got != token {
			httpx.Unauthorized(c, "invalid admin token")
			c.Abort()
			return
		}
		c.Next()
	}
}

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if h == "" {
		return ""
	}
	const p = "Bearer "
	if strings.HasPrefix(h, p) || strings.HasPrefix(h, "bearer ") {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func ExtractAPIKey(c *gin.Context) string {
	if t := bearerToken(c); t != "" {
		return t
	}
	if k := strings.TrimSpace(c.GetHeader("x-api-key")); k != "" {
		return k
	}
	if k := strings.TrimSpace(c.GetHeader("X-Api-Key")); k != "" {
		return k
	}
	return ""
}

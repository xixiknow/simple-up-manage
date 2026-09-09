package router

import (
	"log"

	"simple-up-manage/internal/config"
	"simple-up-manage/internal/crypto"
	"simple-up-manage/internal/handler"
	"simple-up-manage/internal/middleware"
	"simple-up-manage/internal/ops"
	"simple-up-manage/internal/picker"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func New(cfg *config.Config, db *gorm.DB, enc *crypto.AESGCM, opsSvc *ops.Service, pick picker.Picker) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger(), middleware.CORS())

	r.GET("/health", handler.Health)

	g := handler.NewGateway(db, enc, opsSvc, pick)
	admin := &handler.Admin{DB: db, Enc: enc, Ops: opsSvc, Picker: pick, Gateway: g}

	a := r.Group("/api/v1/admin")
	a.Use(middleware.AdminAuth(cfg.AdminToken))
	{
		a.GET("/upstreams", admin.ListUpstreams)
		a.POST("/upstreams", admin.CreateUpstream)
		a.GET("/upstreams/:id", admin.GetUpstream)
		a.PUT("/upstreams/:id", admin.UpdateUpstream)
		a.DELETE("/upstreams/:id", admin.DeleteUpstream)
		a.POST("/upstreams/:id/refresh-balance", admin.RefreshUpstreamBalance)

		a.GET("/upstreams/:id/keys", admin.ListUpstreamKeys)
		a.POST("/upstreams/:id/keys", admin.CreateUpstreamKey)
		a.PUT("/keys/:id", admin.UpdateKey)
		a.DELETE("/keys/:id", admin.DeleteKey)
		a.POST("/keys/:id/probe", admin.ProbeKey)
		a.POST("/keys/:id/refresh-balance", admin.RefreshBalance)
		a.POST("/keys/:id/refresh-billing", admin.RefreshBilling)
		a.POST("/keys/:id/fetch-models", admin.FetchKeyModels)
		a.POST("/upstreams/:id/fetch-models", admin.FetchUpstreamModels)
		a.GET("/keys", admin.ListAllKeys)

		a.GET("/consumer-keys", admin.ListConsumerKeys)
		a.POST("/consumer-keys", admin.CreateConsumerKey)
		a.GET("/consumer-keys/:id/secret", admin.GetConsumerKeySecret)
		a.POST("/consumer-keys/:id/test", admin.TestConsumerKey)
		a.PUT("/consumer-keys/:id", admin.UpdateConsumerKey)
		a.DELETE("/consumer-keys/:id", admin.DeleteConsumerKey)

		a.GET("/route-groups", admin.ListRouteGroups)
		a.POST("/route-groups", admin.CreateRouteGroup)
		a.GET("/route-groups/candidates", admin.RouteGroupCandidates)
		a.PUT("/route-groups/:id", admin.UpdateRouteGroup)
		a.DELETE("/route-groups/:id", admin.DeleteRouteGroup)
		a.PUT("/route-groups/:id/keys", admin.SetRouteGroupKeys)
		a.POST("/route-groups/:id/keys/batch", admin.BatchRouteGroupKeys)
		a.PUT("/keys/:id/route-groups", admin.SetKeyRouteGroups)

		a.GET("/balances", admin.ListBalances)
		a.POST("/balances/refresh", admin.RefreshAllBalances)

		a.GET("/status", admin.Status)
		a.POST("/probes/run", admin.RunProbes)

		a.GET("/request-logs", admin.ListRequestLogs)
		a.GET("/request-logs/:id", admin.GetRequestLog)

		a.GET("/scheduler", admin.GetScheduler)
		a.PUT("/scheduler", admin.UpdateScheduler)
		a.GET("/scheduler/explain", admin.ExplainScheduler)

		a.GET("/model-catalog", admin.GetModelCatalog)
		a.POST("/model-catalog/sync", admin.SyncModelCatalog)
	}

	r.POST("/v1/messages", g.Messages)
	r.POST("/v1/chat/completions", g.ChatCompletions)
	r.POST("/v1/responses", g.Responses)
	r.POST("/v1/images/generations", g.Images)
	r.POST("/v1/images/edits", g.Images)
	r.POST("/v1/images/variations", g.Images)
	r.GET("/v1/models", g.Models)
	r.GET("/v1/usage", g.Usage)

	if err := MountSPA(r, cfg.StaticDir); err != nil {
		log.Printf("static dir %q: %v", cfg.StaticDir, err)
	} else if cfg.StaticDir != "" {
		log.Printf("serving spa from %s", cfg.StaticDir)
	}

	return r
}

package handlers

import (
	"net/http"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/server/middleware"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/server/router"
	"github.com/bestruirui/octopus/internal/update"
	"github.com/gin-gonic/gin"
)

func init() {
	router.NewGroupRouter("/api/v1/update").
		Use(middleware.Auth()).
		AddRoute(
			router.NewRoute("", http.MethodGet).
				Handle(latest),
		).
		AddRoute(
			router.NewRoute("/now-version", http.MethodGet).
				Handle(getNowVersion),
		).
		AddRoute(
			router.NewRoute("", http.MethodPost).
				Handle(updateFunc),
		)
}

// latest 在本构建里固定返回空版本号。
// 这是自行定制的版本, 一旦提示有新版本, 用户点下去就会用官方二进制覆盖当前程序, 定制改动全部丢失;
// 故不再查询上游发布, 界面据此也不显示任何更新入口。保留路由形状是为了让旧前端与脚本拿到正常响应。
func latest(c *gin.Context) {
	resp.Success(c, update.LatestInfo{})
}

func getNowVersion(c *gin.Context) {
	resp.Success(c, conf.Version)
}

// updateFunc 直接拒绝: 在线更新会用官方二进制替换当前可执行文件。
func updateFunc(c *gin.Context) {
	resp.Error(c, http.StatusForbidden, "online update is disabled in this custom build")
}

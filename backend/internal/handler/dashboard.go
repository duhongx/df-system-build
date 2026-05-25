package handler

import (
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type DashboardHandler struct{}

func NewDashboardHandler() *DashboardHandler { return &DashboardHandler{} }

func (h *DashboardHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/dashboard")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/stats", h.GetStats)
		g.GET("/recent-builds", h.RecentBuilds)
	}
}

func (h *DashboardHandler) GetStats(c *gin.Context) {
	db := repository.DB
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayEnd := todayStart.Add(24 * time.Hour)

	var totalApps int64
	db.Model(&model.Application{}).Count(&totalApps)

	var todayBuilds int64
	db.Model(&model.Pipeline{}).Where("created_at >= ? AND created_at < ?", todayStart, todayEnd).Count(&todayBuilds)

	var successCount int64
	db.Model(&model.Pipeline{}).
		Where("created_at >= ? AND created_at < ? AND status = ?", todayStart, todayEnd, "SUCCESS").
		Count(&successCount)

	var failedCount int64
	db.Model(&model.Pipeline{}).
		Where("created_at >= ? AND created_at < ? AND status = ?", todayStart, todayEnd, "FAILED").
		Count(&failedCount)

	response.OK(c, gin.H{
		"totalApps":    totalApps,
		"todayBuilds":  todayBuilds,
		"successCount": successCount,
		"failedCount":  failedCount,
	})
}

func (h *DashboardHandler) RecentBuilds(c *gin.Context) {
	var builds []model.Pipeline
	repository.DB.Order("id DESC").Limit(6).Find(&builds)
	response.OK(c, builds)
}

package handler

import (
	"context"
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"time"
)

func (h *OperationsHandler) Components(c *gin.Context) {
	rows, err := h.svc.Components(c.Request.Context())
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, rows)
}
func (h *OperationsHandler) SaveComponent(c *gin.Context) {
	var req service.ComponentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	req.ID = opsID(c)
	row, err := h.svc.SaveComponent(c.Request.Context(), req)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) DeleteComponent(c *gin.Context) {
	var row model.ComponentConnection
	if err := repository.DB.First(&row, opsID(c)).Error; err != nil {
		opsFail(c, err)
		return
	}
	if row.Source == "environment" {
		response.Fail(c, 16001, "环境集群请在环境配置维护")
		return
	}
	if err := repository.DB.Delete(&row).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}
func (h *OperationsHandler) LatestCollection(c *gin.Context) {
	row, err := h.svc.LatestCollection(c.Request.Context(), c.Param("kind"), opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) CollectComponent(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 100*time.Second)
	defer cancel()
	row, err := h.svc.CollectComponent(ctx, opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) CollectDatabaseInspection(c *gin.Context) {
	row, err := h.svc.CollectDatabaseInspection(c.Request.Context(), opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) CreateConfiguredRun(c *gin.Context) {
	var req struct {
		Name    string                            `json:"name"`
		Targets []service.InspectionTargetRequest `json:"targets"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	row, err := h.svc.CreateConfiguredInspection(c.Request.Context(), req.Name, middleware.GetCurrentUsername(c), req.Targets)
	if err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "开始现场采集并生成报告", "data": row})
}

func (h *OperationsHandler) SetPlanEnabled(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		response.Fail(c, 16001, "请提供启用状态")
		return
	}
	if err := h.svc.SetInspectionPlanEnabled(c.Request.Context(), opsID(c), *req.Enabled); err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}

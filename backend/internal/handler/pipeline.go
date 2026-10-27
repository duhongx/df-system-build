package handler

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type PipelineHandler struct {
	pipelineRepo *repository.PipelineRepo
	stageRepo    *repository.StageRepo
	logRepo      *repository.StageLogRepo
}

func NewPipelineHandler() *PipelineHandler {
	return &PipelineHandler{
		pipelineRepo: repository.NewPipelineRepo(),
		stageRepo:    repository.NewStageRepo(),
		logRepo:      repository.NewStageLogRepo(),
	}
}

func (h *PipelineHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/pipelines")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.GET("/:id", h.Get)
		g.POST("/:id/cancel", h.Cancel)
		g.POST("/:id/rollout", h.Rollout)
		g.GET("/:id/stages/:stageId/logs", h.GetStageLogs)
	}

	// Build queue
	q := r.Group("/build-queue")
	q.Use(middleware.AuthRequired())
	{
		q.GET("", h.GetQueue)
		q.DELETE("/:id", h.CancelQueued)
	}
}

func (h *PipelineHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	appName := c.Query("app")
	status := c.Query("status")

	params := repository.PipelineListParams{
		Page: page, PageSize: pageSize, AppName: appName, Status: status, Search: c.Query("search"), AppType: c.Query("appType"),
	}

	list, total, err := h.pipelineRepo.List(params)
	if err != nil {
		response.Fail(c, 10301, "获取构建历史失败")
		return
	}
	response.OKWithPage(c, list, total, page, pageSize)
}

func (h *PipelineHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	p, err := h.pipelineRepo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10302, "流水线不存在")
		return
	}
	if p.FrontendBaselineJSON != "" {
		var baseline map[string]json.RawMessage
		if json.Unmarshal([]byte(p.FrontendBaselineJSON), &baseline) == nil {
			public := map[string]json.RawMessage{}
			for _, key := range []string{"deploymentName", "namespace", "image", "newImage", "preparationImage", "assetsVerified", "contentSha256", "finalSha256", "regions"} {
				if value, ok := baseline[key]; ok {
					public[key] = value
				}
			}
			p.FrontendRelease, _ = json.Marshal(public)
		}
	}
	response.OK(c, p)
}

func (h *PipelineHandler) Cancel(c *gin.Context) {
	h.cancelActive(c, 10303)
}

func (h *PipelineHandler) cancelActive(c *gin.Context, errorCode int) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, errorCode, "无效的任务编号")
		return
	}
	changed, err := h.pipelineRepo.CancelActive(uint(id))
	if err != nil {
		response.Fail(c, errorCode, "取消失败")
		return
	}
	if !changed {
		response.Fail(c, errorCode, "任务已结束或状态已改变，请刷新后查看")
		return
	}
	if scheduler.DefaultScheduler != nil {
		scheduler.DefaultScheduler.Cancel(uint(id))
	}
	response.OKWithMessage(c, "已取消", nil)
}

func (h *PipelineHandler) GetStageLogs(c *gin.Context) {
	stageId, _ := strconv.ParseUint(c.Param("stageId"), 10, 64)
	logs, err := h.logRepo.GetByStageID(uint(stageId))
	if err != nil {
		response.Fail(c, 10304, "获取日志失败")
		return
	}
	response.OK(c, logs)
}

func (h *PipelineHandler) GetQueue(c *gin.Context) {
	if repository.DB == nil {
		response.FailWithStatus(c, 503, 10304, "数据库未初始化")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	var running, pending []model.Pipeline
	db := repository.DB.WithContext(ctx)
	if err := db.Preload("Stages").Where("status = ?", "RUNNING").Order("start_time ASC").Find(&running).Error; err != nil {
		response.FailWithStatus(c, 503, 10304, "读取运行任务失败: "+err.Error())
		return
	}
	if err := db.Where("status IN ?", []string{"PENDING", "DEPLOYING"}).Order("created_at ASC").Find(&pending).Error; err != nil {
		response.FailWithStatus(c, 503, 10304, "读取排队任务失败: "+err.Error())
		return
	}

	response.OK(c, gin.H{
		"running": running,
		"pending": pending,
	})
}

func (h *PipelineHandler) CancelQueued(c *gin.Context) {
	h.cancelActive(c, 10305)
}

// Rollout submits the second step without rebuilding or pushing an image.
func (h *PipelineHandler) Rollout(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 10306, "更新任务编号无效")
		return
	}
	p, err := scheduler.DefaultScheduler.StartRollout(uint(id))
	if err != nil {
		response.Fail(c, 10306, err.Error())
		return
	}
	response.OKWithMessage(c, "滚动更新已提交", p)
}

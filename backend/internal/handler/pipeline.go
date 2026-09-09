package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
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
		Page: page, PageSize: pageSize, AppName: appName, Status: status,
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
	response.OK(c, p)
}

func (h *PipelineHandler) Cancel(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	scheduler.DefaultScheduler.Cancel(uint(id))
	if err := h.pipelineRepo.UpdateStatus(uint(id), "CANCELED"); err != nil {
		response.Fail(c, 10303, "取消失败")
		return
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
	running, _ := h.pipelineRepo.GetRunning()
	pending, _ := h.pipelineRepo.GetPending()

	response.OK(c, gin.H{
		"running": running,
		"pending": pending,
	})
}

func (h *PipelineHandler) CancelQueued(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	scheduler.DefaultScheduler.Cancel(uint(id))
	if err := h.pipelineRepo.UpdateStatus(uint(id), "CANCELED"); err != nil {
		response.Fail(c, 10305, "取消失败")
		return
	}
	response.OKWithMessage(c, "已取消", nil)
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

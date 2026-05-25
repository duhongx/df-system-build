package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type TaskHandler struct {
	taskService *service.TaskService
}

func NewTaskHandler() *TaskHandler {
	return &TaskHandler{taskService: service.NewTaskService()}
}

func (h *TaskHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/tasks")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.GET("/:id", h.Get)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/execute", h.Execute)
		g.POST("/batch-execute", h.BatchExecute)
	}
}

func (h *TaskHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "50"))
	search := c.Query("search")
	appType := c.Query("appType")

	params := repository.TaskListParams{
		Page: page, PageSize: pageSize, Search: search, AppType: appType,
	}

	tasks, total, err := h.taskService.List(params)
	if err != nil {
		response.Fail(c, 10201, "获取任务列表失败")
		return
	}
	response.OKWithPage(c, tasks, total, page, pageSize)
}

func (h *TaskHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	task, err := h.taskService.GetByID(uint(id))
	if err != nil {
		response.Fail(c, 10202, "任务不存在")
		return
	}
	response.OK(c, task)
}

func (h *TaskHandler) Create(c *gin.Context) {
	var req service.CreateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10203, "参数错误: "+err.Error())
		return
	}

	task, err := h.taskService.Create(&req)
	if err != nil {
		response.Fail(c, 10203, err.Error())
		return
	}
	response.OKWithMessage(c, "创建成功", task)
}

func (h *TaskHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req service.UpdateTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10204, "参数错误")
		return
	}

	task, err := h.taskService.Update(uint(id), &req)
	if err != nil {
		response.Fail(c, 10204, err.Error())
		return
	}
	response.OKWithMessage(c, "更新成功", task)
}

func (h *TaskHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.taskService.Delete(uint(id)); err != nil {
		response.Fail(c, 10205, err.Error())
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

type executeReq struct {
	GitBranch  string `json:"gitBranch" binding:"required"`
	AutoDeploy *bool  `json:"autoDeploy"` // nil=使用全局默认, true=自动部署, false=手动确认
}

func (h *TaskHandler) Execute(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req executeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10206, "请输入分支")
		return
	}

	// Default to auto deploy (no approval) unless explicitly set to false
	autoDeploy := true
	if req.AutoDeploy != nil {
		autoDeploy = *req.AutoDeploy
	}

	username := middleware.GetCurrentUsername(c)
	p, err := scheduler.CreateAndEnqueue(uint(id), req.GitBranch, username, autoDeploy)
	if err != nil {
		response.Fail(c, 10206, err.Error())
		return
	}

	response.OKWithMessage(c, "构建任务已提交", p)
}

type batchExecuteReq struct {
	TaskIDs    []uint `json:"taskIds" binding:"required"`
	GitBranch  string `json:"gitBranch" binding:"required"`
	AutoDeploy *bool  `json:"autoDeploy"` // nil=使用全局默认, true=自动部署, false=手动确认
}

func (h *TaskHandler) BatchExecute(c *gin.Context) {
	var req batchExecuteReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10207, "参数错误")
		return
	}

	if len(req.TaskIDs) == 0 {
		response.Fail(c, 10207, "请选择至少一个任务")
		return
	}

	autoDeploy := true
	if req.AutoDeploy != nil {
		autoDeploy = *req.AutoDeploy
	}

	username := middleware.GetCurrentUsername(c)
	var pipelines []interface{}
	for _, taskID := range req.TaskIDs {
		p, err := scheduler.CreateAndEnqueue(taskID, req.GitBranch, username, autoDeploy)
		if err != nil {
			continue
		}
		pipelines = append(pipelines, p)
	}

	response.OKWithMessage(c, "批量构建已提交", gin.H{
		"count":     len(pipelines),
		"pipelines": pipelines,
	})
}

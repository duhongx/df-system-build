package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type ExecutorHandler struct {
	repo *repository.ExecutorRepo
}

func NewExecutorHandler() *ExecutorHandler {
	return &ExecutorHandler{repo: repository.NewExecutorRepo()}
}

func (h *ExecutorHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/executors")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
	}
}

func (h *ExecutorHandler) List(c *gin.Context) {
	list, err := h.repo.List()
	if err != nil {
		response.Fail(c, 10601, "获取执行器列表失败")
		return
	}
	response.OK(c, list)
}

type executorReq struct {
	Name        string `json:"name" binding:"required"`
	DockerImage string `json:"dockerImage" binding:"required"`
	Type        string `json:"type" binding:"required"`
	CPULimit    string `json:"cpuLimit"`
	MemoryLimit string `json:"memoryLimit"`
	CacheMounts string `json:"cacheMounts"`
}

func (h *ExecutorHandler) Create(c *gin.Context) {
	var req executorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10602, "参数错误")
		return
	}
	e := &model.Executor{
		Name: req.Name, DockerImage: req.DockerImage, Type: req.Type,
		CPULimit: req.CPULimit, MemoryLimit: req.MemoryLimit,
		CacheMounts: req.CacheMounts, Status: "online",
	}
	if err := h.repo.Create(e); err != nil {
		response.Fail(c, 10602, "创建失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "创建成功", e)
}

func (h *ExecutorHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req executorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10603, "参数错误")
		return
	}
	e, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10603, "执行器不存在")
		return
	}
	e.Name = req.Name
	e.DockerImage = req.DockerImage
	e.Type = req.Type
	e.CPULimit = req.CPULimit
	e.MemoryLimit = req.MemoryLimit
	e.CacheMounts = req.CacheMounts
	h.repo.Update(e)
	response.OKWithMessage(c, "更新成功", e)
}

func (h *ExecutorHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 10604, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

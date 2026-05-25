package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type ConfigItemHandler struct {
	repo *repository.ConfigItemRepo
}

func NewConfigItemHandler() *ConfigItemHandler {
	return &ConfigItemHandler{repo: repository.NewConfigItemRepo()}
}

func (h *ConfigItemHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/config-items")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.GET("/:id", h.Get)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
	}
}

func (h *ConfigItemHandler) List(c *gin.Context) {
	category := c.Query("category")
	items, err := h.repo.List(category)
	if err != nil {
		response.Fail(c, 11001, "获取配置项列表失败")
		return
	}
	response.OK(c, items)
}

func (h *ConfigItemHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	item, err := h.repo.GetByID(uint(id))
	if err != nil {
		response.Fail(c, 11002, "配置项不存在")
		return
	}
	response.OK(c, item)
}

func (h *ConfigItemHandler) Create(c *gin.Context) {
	var req model.ConfigItem
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 11003, "参数错误")
		return
	}
	if req.Name == "" || req.Code == "" || req.Category == "" || req.Content == "" {
		response.Fail(c, 11003, "名称、编码、分类和内容不能为空")
		return
	}
	if err := h.repo.Create(&req); err != nil {
		response.Fail(c, 11004, "创建失败: "+err.Error())
		return
	}
	response.OK(c, req)
}

func (h *ConfigItemHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	existing, err := h.repo.GetByID(uint(id))
	if err != nil {
		response.Fail(c, 11002, "配置项不存在")
		return
	}

	var req model.ConfigItem
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 11003, "参数错误")
		return
	}

	existing.Name = req.Name
	existing.Category = req.Category
	existing.ContentType = req.ContentType
	existing.Content = req.Content
	existing.Description = req.Description

	if err := h.repo.Update(existing); err != nil {
		response.Fail(c, 11005, "更新失败")
		return
	}
	response.OK(c, existing)
}

func (h *ConfigItemHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 11006, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

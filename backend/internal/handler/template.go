package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type TemplateHandler struct {
	repo *repository.TemplateRepo
}

func NewTemplateHandler() *TemplateHandler {
	return &TemplateHandler{repo: repository.NewTemplateRepo()}
}

func (h *TemplateHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/templates")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.GET("/:id", h.Get)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
	}
}

func (h *TemplateHandler) List(c *gin.Context) {
	list, err := h.repo.List()
	if err != nil {
		response.Fail(c, 10501, "获取模板列表失败")
		return
	}
	response.OK(c, list)
}

func (h *TemplateHandler) Get(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	t, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10502, "模板不存在")
		return
	}
	response.OK(c, t)
}

type createTemplateReq struct {
	Name        string                  `json:"name" binding:"required"`
	Code        string                  `json:"code" binding:"required"`
	Category    string                  `json:"category" binding:"required"`
	Description string                  `json:"description"`
	Defaults    []model.TemplateDefault `json:"defaults"`
}

func (h *TemplateHandler) Create(c *gin.Context) {
	var req createTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10503, "参数错误")
		return
	}
	t := &model.Template{Name: req.Name, Code: req.Code, Category: req.Category, Description: req.Description}
	if err := h.repo.Create(t); err != nil {
		response.Fail(c, 10503, "创建失败: "+err.Error())
		return
	}
	if len(req.Defaults) > 0 {
		h.repo.ReplaceDefaults(t.ID, req.Defaults)
	}
	t, _ = h.repo.FindByID(t.ID)
	response.OKWithMessage(c, "创建成功", t)
}

func (h *TemplateHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req createTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10504, "参数错误")
		return
	}
	t, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10504, "模板不存在")
		return
	}
	t.Name = req.Name
	t.Category = req.Category
	t.Description = req.Description
	h.repo.Update(t)
	if req.Defaults != nil {
		h.repo.ReplaceDefaults(t.ID, req.Defaults)
	}
	t, _ = h.repo.FindByID(t.ID)
	response.OKWithMessage(c, "更新成功", t)
}

func (h *TemplateHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 10505, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

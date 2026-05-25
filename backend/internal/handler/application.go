package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type ApplicationHandler struct {
	appService *service.ApplicationService
}

func NewApplicationHandler() *ApplicationHandler {
	return &ApplicationHandler{
		appService: service.NewApplicationService(),
	}
}

func (h *ApplicationHandler) RegisterRoutes(r *gin.RouterGroup) {
	apps := r.Group("/applications")
	apps.Use(middleware.AuthRequired())
	{
		apps.GET("", h.List)
		apps.GET("/all", h.ListAll)
		apps.GET("/:id", h.Get)
		apps.POST("", h.Create)
		apps.PUT("/:id", h.Update)
		apps.DELETE("/:id", h.Delete)
	}
}

func (h *ApplicationHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	search := c.Query("search")
	appType := c.Query("appType")

	params := repository.AppListParams{
		Page:     page,
		PageSize: pageSize,
		Search:   search,
		AppType:  appType,
	}

	apps, total, err := h.appService.List(params)
	if err != nil {
		response.Fail(c, 10101, "获取应用列表失败")
		return
	}

	response.OKWithPage(c, apps, total, page, pageSize)
}

func (h *ApplicationHandler) ListAll(c *gin.Context) {
	apps, err := h.appService.ListAll()
	if err != nil {
		response.Fail(c, 10101, "获取应用列表失败")
		return
	}
	response.OK(c, apps)
}

func (h *ApplicationHandler) Get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 10102, "参数错误")
		return
	}

	app, err := h.appService.GetByID(uint(id))
	if err != nil {
		response.Fail(c, 10102, "应用不存在")
		return
	}
	response.OK(c, app)
}

func (h *ApplicationHandler) Create(c *gin.Context) {
	var req service.CreateAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10103, "参数错误: "+err.Error())
		return
	}

	app, err := h.appService.Create(&req)
	if err != nil {
		response.Fail(c, 10103, err.Error())
		return
	}
	response.OKWithMessage(c, "创建成功", app)
}

func (h *ApplicationHandler) Update(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 10104, "参数错误")
		return
	}

	var req service.UpdateAppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10104, "参数错误")
		return
	}

	app, err := h.appService.Update(uint(id), &req)
	if err != nil {
		response.Fail(c, 10104, err.Error())
		return
	}
	response.OKWithMessage(c, "更新成功", app)
}

func (h *ApplicationHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 10105, "参数错误")
		return
	}

	if err := h.appService.Delete(uint(id)); err != nil {
		response.Fail(c, 10105, err.Error())
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

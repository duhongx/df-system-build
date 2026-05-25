package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/notification"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type NotificationHandler struct {
	repo *repository.NotificationRepo
}

func NewNotificationHandler() *NotificationHandler {
	return &NotificationHandler{repo: repository.NewNotificationRepo()}
}

func (h *NotificationHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/notifications")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.POST("", h.Create)
		g.PUT("/:id", h.Update)
		g.DELETE("/:id", h.Delete)
		g.POST("/:id/test", h.Test)
	}
}

func (h *NotificationHandler) List(c *gin.Context) {
	list, err := h.repo.List()
	if err != nil {
		response.Fail(c, 10801, "获取通知列表失败")
		return
	}
	response.OK(c, list)
}

type notifyReq struct {
	Name            string `json:"name" binding:"required"`
	Type            string `json:"type" binding:"required"`
	WebhookURL      string `json:"webhookUrl" binding:"required"`
	Secret          string `json:"secret"`
	NotifyOnSuccess bool   `json:"notifyOnSuccess"`
	NotifyOnFailure bool   `json:"notifyOnFailure"`
	Enabled         bool   `json:"enabled"`
}

func (h *NotificationHandler) Create(c *gin.Context) {
	var req notifyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10802, "参数错误")
		return
	}
	n := &model.NotificationWebhook{
		Name: req.Name, Type: req.Type, WebhookURL: req.WebhookURL,
		Secret: req.Secret, NotifyOnSuccess: req.NotifyOnSuccess,
		NotifyOnFailure: req.NotifyOnFailure, Enabled: req.Enabled,
	}
	if err := h.repo.Create(n); err != nil {
		response.Fail(c, 10802, "创建失败")
		return
	}
	response.OKWithMessage(c, "创建成功", n)
}

func (h *NotificationHandler) Update(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req notifyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10803, "参数错误")
		return
	}
	n, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10803, "通知配置不存在")
		return
	}
	n.Name = req.Name
	n.Type = req.Type
	n.WebhookURL = req.WebhookURL
	n.Secret = req.Secret
	n.NotifyOnSuccess = req.NotifyOnSuccess
	n.NotifyOnFailure = req.NotifyOnFailure
	n.Enabled = req.Enabled
	h.repo.Update(n)
	response.OKWithMessage(c, "更新成功", n)
}

func (h *NotificationHandler) Delete(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.repo.Delete(uint(id)); err != nil {
		response.Fail(c, 10804, "删除失败")
		return
	}
	response.OKWithMessage(c, "删除成功", nil)
}

func (h *NotificationHandler) Test(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	wh, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 10805, "通知配置不存在")
		return
	}

	if err := notification.SendTestMessage(wh); err != nil {
		response.Fail(c, 10805, "测试发送失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "测试消息已发送", nil)
}

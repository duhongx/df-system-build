package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type NotificationMsgHandler struct {
	repo *repository.NotificationMsgRepo
}

func NewNotificationMsgHandler() *NotificationMsgHandler {
	return &NotificationMsgHandler{repo: repository.NewNotificationMsgRepo()}
}

func (h *NotificationMsgHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/notification-msgs")
	g.Use(middleware.AuthRequired())
	{
		g.GET("", h.List)
		g.GET("/unread-count", h.UnreadCount)
		g.PUT("/:id/read", h.MarkRead)
		g.PUT("/read-all", h.MarkAllRead)
		g.POST("/announce", h.Announce)
	}
}

func (h *NotificationMsgHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	list, total, err := h.repo.List(page, pageSize)
	if err != nil {
		response.Fail(c, 14001, "获取通知失败")
		return
	}
	response.OKWithPage(c, list, total, page, pageSize)
}

func (h *NotificationMsgHandler) UnreadCount(c *gin.Context) {
	count := h.repo.UnreadCount()
	response.OK(c, gin.H{"count": count})
}

func (h *NotificationMsgHandler) MarkRead(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	h.repo.MarkRead(uint(id))
	response.OKWithMessage(c, "已读", nil)
}

func (h *NotificationMsgHandler) MarkAllRead(c *gin.Context) {
	h.repo.MarkAllRead()
	response.OKWithMessage(c, "全部已读", nil)
}

func (h *NotificationMsgHandler) Announce(c *gin.Context) {
	var req struct {
		Title   string `json:"title" binding:"required"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14002, "参数错误")
		return
	}
	msg := &model.NotificationMsg{
		Type:    "announcement",
		Title:   req.Title,
		Content: req.Content,
		Level:   "info",
	}
	h.repo.Create(msg)
	response.OKWithMessage(c, "公告已发布", msg)
}

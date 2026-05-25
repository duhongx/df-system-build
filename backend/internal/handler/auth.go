package handler

import (
	"df-build-server/internal/middleware"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	authService *service.AuthService
}

func NewAuthHandler() *AuthHandler {
	return &AuthHandler{
		authService: service.NewAuthService(),
	}
}

func (h *AuthHandler) RegisterRoutes(r *gin.RouterGroup) {
	auth := r.Group("/auth")
	{
		auth.POST("/login", h.Login)
		auth.POST("/logout", h.Logout)
		auth.GET("/profile", middleware.AuthRequired(), h.GetProfile)
		auth.PUT("/profile", middleware.AuthRequired(), h.UpdateProfile)
		auth.POST("/send-code", middleware.AuthRequired(), h.SendVerifyCode)
		auth.POST("/change-password", middleware.AuthRequired(), h.ChangePassword)
	}
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req service.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10001, "请输入用户名和密码")
		return
	}

	resp, err := h.authService.Login(&req)
	if err != nil {
		response.Fail(c, 10001, err.Error())
		return
	}

	response.OK(c, resp)
}

func (h *AuthHandler) Logout(c *gin.Context) {
	response.OKWithMessage(c, "退出成功", nil)
}

func (h *AuthHandler) GetProfile(c *gin.Context) {
	userID := middleware.GetCurrentUserID(c)
	user, err := h.authService.GetProfile(userID)
	if err != nil {
		response.Fail(c, 10003, "获取用户信息失败")
		return
	}
	response.OK(c, user)
}

func (h *AuthHandler) UpdateProfile(c *gin.Context) {
	userID := middleware.GetCurrentUserID(c)
	var req service.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10004, "参数错误")
		return
	}

	user, err := h.authService.UpdateProfile(userID, &req)
	if err != nil {
		response.Fail(c, 10004, err.Error())
		return
	}
	response.OK(c, user)
}

func (h *AuthHandler) SendVerifyCode(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10005, "请输入邮箱")
		return
	}

	if err := h.authService.SendVerifyCode(req.Email); err != nil {
		response.Fail(c, 10005, err.Error())
		return
	}
	response.OKWithMessage(c, "验证码已发送", nil)
}

func (h *AuthHandler) ChangePassword(c *gin.Context) {
	userID := middleware.GetCurrentUserID(c)
	var req service.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 10006, "参数错误")
		return
	}

	if err := h.authService.ChangePassword(userID, &req); err != nil {
		response.Fail(c, 10006, err.Error())
		return
	}
	response.OKWithMessage(c, "密码修改成功", nil)
}

package handler

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *OperationsHandler) DatabaseAccess(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	data, err := h.svc.DatabaseAccess(ctx, opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	response.OK(c, data)
}
func (h *OperationsHandler) DatabaseRolePrivileges(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	data, err := h.svc.DatabaseRolePrivileges(ctx, opsID(c), c.Query("role"))
	if err != nil {
		opsFail(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	response.OK(c, data)
}
func (h *OperationsHandler) PreviewDatabaseAccess(c *gin.Context) { h.databaseAccessChange(c, false) }
func (h *OperationsHandler) ApplyDatabaseAccess(c *gin.Context)   { h.databaseAccessChange(c, true) }
func (h *OperationsHandler) databaseAccessChange(c *gin.Context, apply bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var req service.AccessRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, fmt.Errorf("无效的用户权限参数"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	c.Header("Cache-Control", "private, no-store")
	if apply {
		data, err := h.svc.ApplyDatabaseAccess(ctx, opsID(c), req, middleware.GetCurrentUsername(c))
		if err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, data)
	} else {
		data, err := h.svc.PreviewDatabaseAccess(ctx, opsID(c), req)
		if err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, data)
	}
}

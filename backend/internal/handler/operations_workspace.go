package handler

import (
	"errors"
	"fmt"
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *OperationsHandler) Workspace(c *gin.Context) {
	owner := middleware.GetCurrentUsername(c)
	if c.Request.Method == "GET" {
		out, err := h.svc.DatabaseWorkspace(c.Request.Context(), owner)
		if err != nil {
			opsFail(c, err)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		response.OK(c, out)
		return
	}
	var req struct {
		State    service.DatabaseWorkspaceState `json:"state"`
		Revision uint                           `json:"revision"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, fmt.Errorf("无效的工作区草稿"))
		return
	}
	out, err := h.svc.SaveDatabaseWorkspace(c.Request.Context(), owner, req.State, req.Revision)
	if errors.Is(err, service.ErrWorkspaceConflict) {
		response.Fail(c, 16002, err.Error())
		return
	}
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, out)
}
func (h *OperationsHandler) SavedQueries(c *gin.Context) {
	owner := middleware.GetCurrentUsername(c)
	if c.Request.Method == "GET" {
		out, err := h.svc.SavedDatabaseQueries(c.Request.Context(), owner)
		if err != nil {
			opsFail(c, err)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		response.OK(c, out)
		return
	}
	if c.Request.Method == "DELETE" {
		revision, _ := strconv.ParseUint(c.Query("revision"), 10, 64)
		if err := h.svc.DeleteSavedDatabaseQuery(c.Request.Context(), owner, opsID(c), uint(revision)); err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, nil)
		return
	}
	var row model.DatabaseSavedQuery
	if err := c.ShouldBindJSON(&row); err != nil {
		opsFail(c, fmt.Errorf("无效的 SQL 收藏"))
		return
	}
	if c.Request.Method == "PUT" {
		row.ID = opsID(c)
	} else {
		row.ID = 0
	}
	out, err := h.svc.SaveDatabaseQuery(c.Request.Context(), owner, row)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, out)
}

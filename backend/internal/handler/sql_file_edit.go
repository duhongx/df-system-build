package handler

import (
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *PostgreSQLHandler) EditReleaseSQLFile(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 14000, "无效的文件 ID")
		return
	}
	var req service.EditReleaseSQLFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14006, "请输入完整 SQL 内容及原始文件内容")
		return
	}
	file, rows, edit, err := h.svc.EditReleaseSQLFile(uint(id), req, middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14006, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": rows, "edit": edit})
}

func (h *PostgreSQLHandler) ListSQLFileEdits(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 14000, "无效的文件 ID")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows, total, err := h.svc.ListSQLFileEdits(uint(id), page, size)
	if err != nil {
		response.Fail(c, 14006, err.Error())
		return
	}
	response.OKWithPage(c, rows, total, page, size)
}

func (h *PostgreSQLHandler) GetSQLFileEdit(c *gin.Context) {
	id, err1 := strconv.ParseUint(c.Param("id"), 10, 64)
	editID, err2 := strconv.ParseUint(c.Param("editId"), 10, 64)
	if err1 != nil || err2 != nil || id == 0 || editID == 0 {
		response.Fail(c, 14000, "无效的修改记录 ID")
		return
	}
	edit, before, err := h.svc.GetSQLFileEdit(uint(id), uint(editID))
	if err != nil {
		response.Fail(c, 14006, err.Error())
		return
	}
	response.OK(c, gin.H{"edit": edit, "beforeStatements": before})
}

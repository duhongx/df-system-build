package handler

import (
	"df-build-server/internal/middleware"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *PostgreSQLHandler) ListSQLStatementRecords(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "30"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	var filter service.SQLRecordFilter
	if err := c.ShouldBindQuery(&filter); err != nil {
		response.Fail(c, 14020, "查询参数错误")
		return
	}
	rows, total, err := h.svc.ListSQLStatementRecords(page, size, filter)
	if err != nil {
		response.Fail(c, 14020, err.Error())
		return
	}
	response.OKWithPage(c, rows, total, page, size)
}
func (h *PostgreSQLHandler) CancelSQLFileSequence(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 14000, "无效的批次 ID")
		return
	}
	batch, err := h.svc.CancelSQLFileSequence(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14012, err.Error())
		return
	}
	response.OK(c, batch)
}
func (h *PostgreSQLHandler) SetSQLFileExecutionConfig(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 14000, "无效的文件 ID")
		return
	}
	var req service.SQLFileExecutionConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14021, "请输入执行账号/schema 及原配置")
		return
	}
	file, rows, err := h.svc.SetSQLFileExecutionConfig(c.Request.Context(), uint(id), req, middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14021, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": rows})
}
func (h *PostgreSQLHandler) GetSQLBatchFile(c *gin.Context) {
	id, e1 := strconv.ParseUint(c.Param("id"), 10, 64)
	fileID, e2 := strconv.ParseUint(c.Param("fileId"), 10, 64)
	if e1 != nil || e2 != nil || id == 0 || fileID == 0 {
		response.Fail(c, 14000, "无效的批次或文件 ID")
		return
	}
	file, rows, err := h.svc.GetSQLBatchFile(uint(id), uint(fileID))
	if err != nil {
		response.Fail(c, 14022, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": rows})
}

package handler

import (
	"df-build-server/internal/middleware"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *PostgreSQLHandler) ListCurrentFailedSQL(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "30"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	var fileID uint64
	if value := c.Query("fileId"); value != "" {
		var err error
		fileID, err = strconv.ParseUint(value, 10, 64)
		if err != nil || fileID == 0 {
			response.Fail(c, 14000, "无效的文件 ID")
			return
		}
	}
	rows, total, err := h.svc.ListCurrentFailedSQL(page, size, service.SQLFailureFilter{Keyword: c.Query("keyword"), FileName: c.Query("fileName"), SchemaName: c.Query("schemaName"), Handling: c.Query("handling"), FileID: uint(fileID)})
	if err != nil {
		response.Fail(c, 14004, err.Error())
		return
	}
	response.OKWithPage(c, rows, total, page, size)
}
func (h *PostgreSQLHandler) TransferFailedSQLToViewTask(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 14000, "无效的语句 ID")
		return
	}
	task, err := h.svc.TransferFailedSQLToViewTask(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14018, err.Error())
		return
	}
	response.OK(c, task)
}

func (h *PostgreSQLHandler) MarkSQLFileFailed(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 14000, "无效的文件 ID")
		return
	}
	result, err := h.svc.MarkSQLFileFailed(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14006, err.Error())
		return
	}
	response.OK(c, result)
}

func bindSQLFileViewTransfer(c *gin.Context) (uint, service.SQLFileViewTransferRequest, bool) {
	var req service.SQLFileViewTransferRequest
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Fail(c, 14000, "无效的文件 ID")
		return 0, req, false
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14003, "转入参数错误")
		return 0, req, false
	}
	return uint(id), req, true
}

func (h *PostgreSQLHandler) PreviewSQLFileViewTransfer(c *gin.Context) {
	id, req, ok := bindSQLFileViewTransfer(c)
	if !ok {
		return
	}
	preview, err := h.svc.PreviewSQLFileViewTransfer(id, req)
	if err != nil {
		response.Fail(c, 14018, err.Error())
		return
	}
	response.OK(c, preview)
}

func (h *PostgreSQLHandler) TransferSQLFileToViewTask(c *gin.Context) {
	id, req, ok := bindSQLFileViewTransfer(c)
	if !ok {
		return
	}
	task, err := h.svc.TransferSQLFileToViewTask(id, req, middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14018, err.Error())
		return
	}
	response.OK(c, task)
}

func (h *PostgreSQLHandler) CancelSQLViewTaskTransfer(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Fail(c, 14000, "无效任务 ID")
		return
	}
	if err = h.svc.CancelSQLViewTaskTransfer(uint(id), middleware.GetCurrentUsername(c)); err != nil {
		response.Fail(c, 14018, err.Error())
		return
	}
	response.OK(c, nil)
}

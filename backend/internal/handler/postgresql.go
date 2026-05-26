package handler

import (
	"net/http"
	"strconv"

	"df-build-server/internal/middleware"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type PostgreSQLHandler struct {
	svc *service.PostgreSQLService
}

func NewPostgreSQLHandler() *PostgreSQLHandler {
	return &PostgreSQLHandler{svc: service.NewPostgreSQLService()}
}

func (h *PostgreSQLHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/postgresql")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/instance", h.Instance)
		g.GET("/sql-files", h.ListSQLFiles)
		g.GET("/sql-files/todo", h.ListTodoSQLFiles)
		g.GET("/sql-files/done", h.ListDoneSQLFiles)
		g.GET("/sql-files/:id", h.GetSQLFile)
		g.GET("/sql-files/:id/not-executable.sql", h.ExportNotExecutableSQL)
		g.GET("/sql-batches", h.ListSQLBatches)
		g.GET("/sql-batches/:id", h.GetSQLBatch)
		g.POST("/sql-batches/parse", h.ParseSQLBatch)
		g.POST("/sql-batches/:id/execute", h.ExecuteSQLBatch)
		g.POST("/sql-batches/:id/cancel", h.CancelSQLBatch)
		g.POST("/sql-files/parse", h.ParseSQL)
		g.POST("/sql-files/:id/execute", h.ExecuteSQLFile)
		g.POST("/sql-files/:id/cancel", h.CancelSQLFile)
		g.POST("/sql-files/save", h.ParseSQL)
		g.POST("/sql-files/import-server", h.ImportServerSQL)
		g.POST("/sql-files/execute-content", h.ExecuteSQLContent)
		g.POST("/sql-files/:id/skip", h.SkipSQLFile)
		g.DELETE("/sql-files/:id", h.DeleteSQLFile)
		g.POST("/sql-statements/:id/skip", h.SkipSQLStatement)
	}
}

func (h *PostgreSQLHandler) Instance(c *gin.Context) {
	info, err := h.svc.GetInstanceInfo(c.Request.Context())
	if err != nil {
		response.Fail(c, 14001, err.Error())
		return
	}
	response.OK(c, info)
}

func (h *PostgreSQLHandler) ParseSQL(c *gin.Context) {
	var req service.ParseSQLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14002, "参数错误")
		return
	}
	file, statements, err := h.svc.ParseSQL(req)
	if err != nil {
		response.Fail(c, 14002, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": statements})
}

func (h *PostgreSQLHandler) ExecuteSQLFile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Options service.SQLExecuteOptions `json:"options"`
	}
	_ = c.ShouldBindJSON(&req)
	file, statements, err := h.svc.ExecuteSQLFileWithOptions(c.Request.Context(), uint(id), middleware.GetCurrentUsername(c), req.Options)
	if err != nil {
		response.Fail(c, 14003, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": statements})
}

func (h *PostgreSQLHandler) CancelSQLFile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	file, err := h.svc.CancelSQLFile(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14012, err.Error())
		return
	}
	response.OK(c, file)
}

func (h *PostgreSQLHandler) ParseSQLBatch(c *gin.Context) {
	var req service.ParseSQLBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14013, "参数错误")
		return
	}
	batch, files, err := h.svc.ParseSQLBatch(req)
	if err != nil {
		response.Fail(c, 14013, err.Error())
		return
	}
	response.OK(c, gin.H{"batch": batch, "files": files})
}

func (h *PostgreSQLHandler) ExecuteSQLBatch(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var req struct {
		Options service.SQLExecuteOptions `json:"options"`
	}
	_ = c.ShouldBindJSON(&req)
	batch, files, err := h.svc.ExecuteSQLBatch(c.Request.Context(), uint(id), middleware.GetCurrentUsername(c), req.Options)
	if err != nil {
		response.Fail(c, 14014, err.Error())
		return
	}
	response.OK(c, gin.H{"batch": batch, "files": files})
}

func (h *PostgreSQLHandler) CancelSQLBatch(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	batch, err := h.svc.CancelSQLBatch(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14015, err.Error())
		return
	}
	response.OK(c, batch)
}

func (h *PostgreSQLHandler) ExecuteSQLContent(c *gin.Context) {
	var req service.ExecuteSQLContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14007, "参数错误")
		return
	}
	file, statements, err := h.svc.ExecuteSQLContent(c.Request.Context(), req, middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14007, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": statements})
}

func (h *PostgreSQLHandler) ListSQLFiles(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	files, total, err := h.svc.ListSQLFiles(page, pageSize)
	if err != nil {
		response.Fail(c, 14004, err.Error())
		return
	}
	response.OKWithPage(c, files, total, page, pageSize)
}

func (h *PostgreSQLHandler) ListSQLBatches(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	batches, total, err := h.svc.ListSQLBatches(page, pageSize)
	if err != nil {
		response.Fail(c, 14015, err.Error())
		return
	}
	response.OKWithPage(c, batches, total, page, pageSize)
}

func (h *PostgreSQLHandler) GetSQLBatch(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	batch, files, err := h.svc.GetSQLBatch(uint(id))
	if err != nil {
		response.Fail(c, 14016, err.Error())
		return
	}
	response.OK(c, gin.H{"batch": batch, "files": files})
}

func (h *PostgreSQLHandler) ListTodoSQLFiles(c *gin.Context) {
	h.listSQLFilesByGroup(c, "todo")
}

func (h *PostgreSQLHandler) ListDoneSQLFiles(c *gin.Context) {
	h.listSQLFilesByGroup(c, "done")
}

func (h *PostgreSQLHandler) listSQLFilesByGroup(c *gin.Context, group string) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	files, total, err := h.svc.ListSQLFilesByStatus(page, pageSize, group)
	if err != nil {
		response.Fail(c, 14004, err.Error())
		return
	}
	response.OKWithPage(c, files, total, page, pageSize)
}

func (h *PostgreSQLHandler) GetSQLFile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	file, statements, err := h.svc.GetSQLFile(uint(id))
	if err != nil {
		response.Fail(c, 14005, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": statements})
}

func (h *PostgreSQLHandler) ImportServerSQL(c *gin.Context) {
	var req service.ImportServerSQLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 14008, "参数错误")
		return
	}
	count, err := h.svc.ImportServerSQL(req)
	if err != nil {
		response.Fail(c, 14008, err.Error())
		return
	}
	response.OK(c, gin.H{"count": count})
}

func (h *PostgreSQLHandler) DeleteSQLFile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	if err := h.svc.DeleteSQLFile(uint(id)); err != nil {
		response.Fail(c, 14009, err.Error())
		return
	}
	response.OK(c, gin.H{"deleted": true})
}

func (h *PostgreSQLHandler) SkipSQLFile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	file, err := h.svc.SkipSQLFile(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14010, err.Error())
		return
	}
	response.OK(c, file)
}

func (h *PostgreSQLHandler) ExportNotExecutableSQL(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	content, err := h.svc.BuildNotExecutableSQLForFile(uint(id))
	if err != nil {
		response.Fail(c, 14011, err.Error())
		return
	}
	c.Header("Content-Type", "application/sql; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="not-executable.sql"`)
	c.String(http.StatusOK, content)
}

func (h *PostgreSQLHandler) SkipSQLStatement(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	stmt, err := h.svc.SkipSQLStatement(uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14006, err.Error())
		return
	}
	response.OK(c, stmt)
}

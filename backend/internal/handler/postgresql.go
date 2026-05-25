package handler

import (
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
		g.GET("/sql-files/:id", h.GetSQLFile)
		g.POST("/sql-files/parse", h.ParseSQL)
		g.POST("/sql-files/:id/execute", h.ExecuteSQLFile)
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
	file, statements, err := h.svc.ExecuteSQLFile(c.Request.Context(), uint(id), middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 14003, err.Error())
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

func (h *PostgreSQLHandler) GetSQLFile(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	file, statements, err := h.svc.GetSQLFile(uint(id))
	if err != nil {
		response.Fail(c, 14005, err.Error())
		return
	}
	response.OK(c, gin.H{"file": file, "statements": statements})
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

package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

type LogCollectionHandler struct{ service *service.LogCollectionService }

func NewLogCollectionHandler(s *service.LogCollectionService) *LogCollectionHandler {
	return &LogCollectionHandler{service: s}
}

func (h *LogCollectionHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/log-collection", middleware.AuthRequired())
	g.GET("/sources", h.Sources)
	g.POST("/sources", h.SaveSource)
	g.PUT("/sources/:id", h.SaveSource)
	g.DELETE("/sources/:id", h.DeleteSource)
	g.GET("/sources/:id/files", h.Files)
	g.GET("/jobs", h.Jobs)
	g.POST("/jobs", h.Start)
	g.GET("/jobs/:id", h.Get)
	g.POST("/jobs/:id/cancel", h.Cancel)
	g.DELETE("/jobs/:id", h.Delete)
	// A native browser download streams to disk, avoiding a full archive Blob
	// in JS memory. The credential is in the POST body, never the URL.
	r.POST("/log-collection/jobs/:id/download", logDownloadAuth, h.Download)
}

func logDownloadAuth(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	c.Request.Header.Set("Authorization", "Bearer "+c.PostForm("token"))
	middleware.AuthRequired()(c)
}

func logCollectionID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.Fail(c, 12401, "无效的记录编号")
		return 0, false
	}
	return uint(id), true
}

func (h *LogCollectionHandler) Sources(c *gin.Context) {
	items, err := h.service.Sources()
	if err != nil {
		response.Fail(c, 12401, "读取日志来源失败: "+err.Error())
		return
	}
	response.OK(c, items)
}
func (h *LogCollectionHandler) SaveSource(c *gin.Context) {
	var source model.LogSource
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	if err := c.ShouldBindJSON(&source); err != nil {
		response.Fail(c, 12401, "日志来源参数无效: "+err.Error())
		return
	}
	source.ID = 0
	source.CreatedAt = time.Time{}
	source.UpdatedAt = time.Time{}
	if c.Request.Method == http.MethodPut {
		id, ok := logCollectionID(c)
		if !ok {
			return
		}
		source.ID = id
	}
	result, err := h.service.SaveSource(source)
	if err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OK(c, result)
}
func (h *LogCollectionHandler) DeleteSource(c *gin.Context) {
	id, ok := logCollectionID(c)
	if !ok {
		return
	}
	if err := h.service.DeleteSource(id); err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OK(c, nil)
}
func (h *LogCollectionHandler) Files(c *gin.Context) {
	id, ok := logCollectionID(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 110*time.Second)
	defer cancel()
	files, err := h.service.Files(ctx, id)
	if err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OK(c, files)
}
func (h *LogCollectionHandler) Jobs(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	items, total, err := h.service.Jobs(page)
	if err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OKWithPage(c, items, total, page, 20)
}
func (h *LogCollectionHandler) Start(c *gin.Context) {
	var req struct {
		Selections []service.LogCollectionSelection `json:"selections"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12401, "收集参数无效: "+err.Error())
		return
	}
	job, err := h.service.Start(req.Selections, middleware.GetCurrentUsername(c))
	if err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OK(c, job)
}
func (h *LogCollectionHandler) Get(c *gin.Context) {
	id, ok := logCollectionID(c)
	if !ok {
		return
	}
	job, err := h.service.Get(id)
	if err != nil {
		response.Fail(c, 12401, "读取收集任务失败: "+err.Error())
		return
	}
	response.OK(c, job)
}
func (h *LogCollectionHandler) Cancel(c *gin.Context) {
	id, ok := logCollectionID(c)
	if !ok {
		return
	}
	if err := h.service.Cancel(id); err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OKWithMessage(c, "已请求停止收集", nil)
}
func (h *LogCollectionHandler) Delete(c *gin.Context) {
	id, ok := logCollectionID(c)
	if !ok {
		return
	}
	if err := h.service.Delete(id); err != nil {
		response.Fail(c, 12401, err.Error())
		return
	}
	response.OK(c, nil)
}
func (h *LogCollectionHandler) Download(c *gin.Context) {
	id, ok := logCollectionID(c)
	if !ok {
		return
	}
	job, err := h.service.Get(id)
	if err != nil {
		response.FailWithStatus(c, http.StatusNotFound, 12401, "收集任务不存在")
		return
	}
	if job.Status == "running" || job.ArchiveSize == 0 {
		response.FailWithStatus(c, http.StatusConflict, 12401, "任务尚未生成可下载的收集包，请查看任务详情")
		return
	}
	file, err := os.Open(h.service.ArchivePath(id))
	if err != nil {
		response.FailWithStatus(c, http.StatusNotFound, 12401, "收集包不存在或无法读取，请重新收集")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		response.FailWithStatus(c, http.StatusInternalServerError, 12401, "读取收集包信息失败")
		return
	}
	name := fmt.Sprintf("logs-%d-%s.zip", id, job.CreatedAt.Format("20060102-150405"))
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Header("Content-Type", "application/zip")
	c.Header("Cache-Control", "no-store")
	http.ServeContent(c.Writer, c.Request, name, stat.ModTime(), file)
}

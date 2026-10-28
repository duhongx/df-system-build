package handler

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *OperationsHandler) WorkbenchCatalog(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	cfg, err := h.svc.WorkbenchConfig(ctx, opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	if c.Query("schema") == "" {
		data, err := opsprobe.Schemas(ctx, cfg)
		if err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, data)
		return
	}
	if c.Query("kind") == "routines" {
		data, err := opsprobe.Routines(ctx, cfg, c.Query("schema"))
		if err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, data)
		return
	}
	data, err := opsprobe.Relations(ctx, cfg, c.Query("schema"))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, data)
}
func (h *OperationsHandler) WorkbenchObject(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	cfg, err := h.svc.WorkbenchConfig(ctx, opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	oid, err := strconv.ParseInt(c.Param("oid"), 10, 64)
	if err != nil || oid <= 0 || oid > 4294967295 {
		opsFail(c, fmt.Errorf("无效对象编号"))
		return
	}
	if c.Query("kind") == "routine" {
		data, err := opsprobe.DescribeRoutine(ctx, cfg, oid)
		if err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, data)
		return
	}
	data, err := opsprobe.DescribeRelation(ctx, cfg, oid)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, data)
}
func (h *OperationsHandler) WorkbenchQuery(c *gin.Context) {
	var req service.WorkbenchQuery
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, fmt.Errorf("无效查询参数"))
		return
	}
	result, file, err := h.svc.WorkbenchQuery(c.Request.Context(), opsID(c), req, middleware.GetCurrentUsername(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	if file != "" {
		defer os.Remove(file)
		c.Header("Cache-Control", "private, no-store")
		c.Header("X-Query-Record-ID", strconv.FormatUint(uint64(result.RecordID), 10))
		c.FileAttachment(file, fmt.Sprintf("query-%d.csv", result.RecordID))
		return
	}
	response.OK(c, result)
}
func (h *OperationsHandler) WorkbenchRecords(c *gin.Context) {
	page, size := opsPage(c)
	q := repository.DB.WithContext(c.Request.Context()).Model(&model.DatabaseQueryRecord{}).Where("database_id=?", opsID(c))
	var total int64
	if err := q.Count(&total).Error; err != nil {
		opsFail(c, err)
		return
	}
	var rows []model.DatabaseQueryRecord
	if err := q.Order("started_at DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OKWithPage(c, rows, total, page, size)
}
func (h *OperationsHandler) SlowAnalysis(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	minimum, _ := strconv.ParseFloat(c.Query("minDuration"), 64)
	data, err := h.svc.AnalyzeSlowLogs(ctx, opsID(c), service.OperationsEventFilter{From: c.Query("from"), To: c.Query("to"), Keyword: c.Query("keyword"), MinDuration: minimum}, c.Query("phase"))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, data)
}
func (h *OperationsHandler) StatementStatistics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	cfg, err := h.svc.WorkbenchConfig(ctx, opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	data, err := opsprobe.StatementStatistics(ctx, cfg, c.Query("order"))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, data)
}

package handler

import (
	"net/http"
	"strconv"

	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
)

func slowQueryFilter(c *gin.Context) service.SlowQueryFilter {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", c.DefaultQuery("pageSize", "20")))
	target, _ := strconv.ParseUint(c.Query("targetRunId"), 10, 64)
	asOf, _ := strconv.ParseUint(c.Query("asOfId"), 10, 64)
	return service.SlowQueryFilter{Page: page, Size: size, Keyword: c.Query("keyword"), Database: c.Query("database"), TargetRunID: uint(target), AsOfID: uint(asOf)}
}
func (h *OperationsHandler) MonitoringSlowQueries(c *gin.Context) {
	if c.Param("kind") != "database" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	page, err := h.svc.MonitoringSlowQueries(c.Request.Context(), opsID(c), slowQueryFilter(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, page)
}
func (h *OperationsHandler) InspectionSlowQueries(c *gin.Context) {
	page, err := h.svc.InspectionSlowQueries(c.Request.Context(), opsID(c), slowQueryFilter(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, page)
}

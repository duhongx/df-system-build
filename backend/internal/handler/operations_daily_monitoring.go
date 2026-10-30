package handler

import (
	"context"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *OperationsHandler) MonitoringOverview(c *gin.Context) {
	rows, err := h.svc.MonitoringOverview(c.Request.Context())
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, rows)
}
func (h *OperationsHandler) MonitoringDetail(c *gin.Context) {
	row, err := h.svc.MonitoringDetail(c.Request.Context(), c.Param("kind"), opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) MonitoringHistory(c *gin.Context) {
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "1"))
	rows, step, err := h.svc.MonitoringHistory(c.Request.Context(), c.Param("kind"), opsID(c), hours)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, gin.H{"samples": rows, "stepSeconds": step})
}
func (h *OperationsHandler) RefreshMonitoring(c *gin.Context) {
	if err := h.svc.RefreshMonitoring(context.Background(), c.Param("kind"), opsID(c)); err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "后台自动采集已受理", "data": nil})
}

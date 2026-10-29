package handler

import (
	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"strconv"
)

func serviceID(c *gin.Context) uint {
	v, _ := strconv.ParseUint(c.Param("serviceId"), 10, 64)
	return uint(v)
}
func (h *OperationsHandler) ManagedServices(c *gin.Context) {
	rows, err := h.svc.ManagedServices(c.Request.Context(), opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	actor := middleware.GetCurrentUsername(c)
	type entry struct {
		model.ManagedService
		CanConfigure bool `json:"canConfigure"`
		CanOperate   bool `json:"canOperate"`
	}
	entries := []entry{}
	for _, r := range rows {
		entries = append(entries, entry{r, service.ServiceCanConfigure(r, actor), service.ServiceCanOperate(r, actor)})
	}
	var host model.Server
	if err := repository.DB.First(&host, opsID(c)).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, gin.H{"services": entries, "canCreate": actor == "admin" || actor == host.CreatedBy})
}
func (h *OperationsHandler) SaveManagedService(c *gin.Context) {
	var req service.ManagedServiceInput
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	req.ID = serviceID(c)
	row, err := h.svc.SaveManagedService(c.Request.Context(), opsID(c), middleware.GetCurrentUsername(c), req)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) serviceInHost(c *gin.Context) bool {
	var row model.ManagedService
	if err := repository.DB.Where("server_id=?", opsID(c)).First(&row, serviceID(c)).Error; err != nil {
		opsFail(c, err)
		return false
	}
	return true
}
func (h *OperationsHandler) DeleteManagedService(c *gin.Context) {
	if !h.serviceInHost(c) {
		return
	}
	if err := h.svc.DeleteManagedService(c.Request.Context(), serviceID(c), middleware.GetCurrentUsername(c)); err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}
func (h *OperationsHandler) ManagedServiceStatus(c *gin.Context) {
	if !h.serviceInHost(c) {
		return
	}
	row, err := h.svc.ManagedServiceStatus(c.Request.Context(), serviceID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) ManagedServiceLogs(c *gin.Context) {
	if !h.serviceInHost(c) {
		return
	}
	data, err := h.svc.ServiceLogs(c.Request.Context(), serviceID(c), middleware.GetCurrentUsername(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, gin.H{"text": data})
}
func (h *OperationsHandler) ServiceOperation(c *gin.Context) {
	if !h.serviceInHost(c) {
		return
	}
	if c.Request.Method == "GET" {
		rows, err := h.svc.ServiceOperations(c.Request.Context(), serviceID(c))
		if err != nil {
			opsFail(c, err)
			return
		}
		response.OK(c, rows)
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	row, err := h.svc.StartServiceOperation(c.Request.Context(), serviceID(c), req.Action, middleware.GetCurrentUsername(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "已受理服务操作", "data": row})
}

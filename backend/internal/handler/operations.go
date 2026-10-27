package handler

import (
	"context"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/crypto"
	"df-build-server/pkg/response"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OperationsHandler struct{ svc *service.OperationsService }

func NewOperationsHandler(svc *service.OperationsService) *OperationsHandler {
	return &OperationsHandler{svc}
}
func (h *OperationsHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/operations")
	g.Use(middleware.AuthRequired())
	g.GET("/databases", h.Databases)
	g.POST("/databases", h.SaveDatabase)
	g.PUT("/databases/:id", h.SaveDatabase)
	g.DELETE("/databases/:id", h.DeleteDatabase)
	g.POST("/databases/import-current", h.ImportDatabase)
	g.GET("/databases/:id/diagnostics", h.Diagnostics)
	g.POST("/databases/:id/detect-logs", h.DetectLogs)
	g.POST("/databases/:id/sync-logs", h.SyncLogs)
	g.GET("/databases/:id/events", h.Events)
	g.GET("/databases/:id/events/:eventId", h.Event)
	g.GET("/databases/:id/log-cursors", h.Cursors)
	g.GET("/hosts/:id/monitor", h.HostMonitor)
	g.PUT("/hosts/:id/monitor", h.SaveMonitor)
	g.POST("/hosts/:id/collect", h.CollectHost)
	g.GET("/hosts/:id/history", h.History)
	g.GET("/inspection-runs", h.Runs)
	g.POST("/inspection-runs", h.CreateRun)
	g.GET("/inspection-runs/:id", h.Run)
	g.GET("/inspection-runs/:id/targets/:targetId", h.Target)
	g.POST("/inspection-runs/:id/cancel", h.CancelRun)
	g.POST("/inspection-runs/:id/reconcile", h.ReconcileRun)
	g.POST("/inspection-runs/:id/reports", h.RegenerateReports)
	g.GET("/inspection-runs/:id/report/:format", h.Report)
	g.DELETE("/inspection-runs/:id", h.DeleteRun)
	g.GET("/inspection-plans", h.Plans)
	g.POST("/inspection-plans", h.SavePlan)
	g.PUT("/inspection-plans/:id", h.SavePlan)
	g.DELETE("/inspection-plans/:id", h.DeletePlan)
}
func opsID(c *gin.Context) uint         { v, _ := strconv.ParseUint(c.Param("id"), 10, 64); return uint(v) }
func opsFail(c *gin.Context, err error) { response.Fail(c, 16001, err.Error()) }
func opsPage(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "30"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	return page, size
}
func (h *OperationsHandler) Databases(c *gin.Context) {
	var rows []model.OperationsDatabase
	if err := repository.DB.Order("id").Find(&rows).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, rows)
}

type databaseReq struct {
	Name, Host, Database, Username, Password, SSLMode string
	Port                                              int
	ServerID                                          uint
	LogEnabled                                        bool
	LogDirectory, LogFormat, LogTimezone, LogPrefix   string
	SlowThresholdMs, LookbackHours                    int
	Revision                                          uint
}

func (h *OperationsHandler) SaveDatabase(c *gin.Context) {
	var req databaseReq
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
	req.Database = strings.TrimSpace(req.Database)
	req.Username = strings.TrimSpace(req.Username)
	if req.Port == 0 {
		req.Port = 5432
	}
	if req.SSLMode == "" {
		req.SSLMode = "require"
	}
	if req.LogFormat == "" {
		req.LogFormat = "csv"
	}
	if req.LogTimezone == "" {
		req.LogTimezone = "Asia/Shanghai"
	}
	if req.SlowThresholdMs == 0 {
		req.SlowThresholdMs = 5000
	}
	if req.LookbackHours == 0 {
		req.LookbackHours = 24
	}
	if req.Name == "" || len([]rune(req.Name)) > 128 || req.Host == "" || len(req.Host) > 255 || req.Database == "" || len(req.Database) > 128 || req.Username == "" || len(req.Username) > 128 || req.Port < 1 || req.Port > 65535 {
		opsFail(c, fmt.Errorf("请填写合法的名称、数据库地址、端口、数据库和账号"))
		return
	}
	if strings.ContainsAny(req.Host, "/\x00\n\r ") || net.ParseIP(req.Host) == nil && strings.Contains(req.Host, ":") {
		opsFail(c, fmt.Errorf("数据库地址应为主机名或 IP，端口请单独填写"))
		return
	}
	switch req.SSLMode {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		opsFail(c, fmt.Errorf("SSL 模式无效"))
		return
	}
	if req.SlowThresholdMs < 1 || req.SlowThresholdMs > 3600000 || req.LookbackHours < 1 || req.LookbackHours > 720 {
		opsFail(c, fmt.Errorf("慢 SQL 阈值或首次导入范围超出限制"))
		return
	}
	if req.LogFormat != "csv" && req.LogFormat != "text" {
		opsFail(c, fmt.Errorf("日志格式无效"))
		return
	}
	if _, err := time.LoadLocation(req.LogTimezone); err != nil {
		opsFail(c, fmt.Errorf("日志时区无效"))
		return
	}
	if req.LogEnabled && (req.ServerID == 0 || !path.IsAbs(req.LogDirectory) || strings.ContainsRune(req.LogDirectory, 0)) {
		opsFail(c, fmt.Errorf("启用日志接入需要关联主机和绝对日志目录"))
		return
	}
	var row model.OperationsDatabase
	id := opsID(c)
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if id > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
				return err
			}
			if req.Revision != row.Revision {
				return fmt.Errorf("配置已被修改，请刷新后重试")
			}
		}
		if req.ServerID > 0 {
			var count int64
			tx.Model(&model.Server{}).Where("id=?", req.ServerID).Count(&count)
			if count == 0 {
				return fmt.Errorf("关联主机不存在")
			}
		}
		if req.Password != "" {
			encrypted, err := crypto.Encrypt(req.Password)
			if err != nil {
				return err
			}
			row.PasswordEncrypted = encrypted
		}
		if row.PasswordEncrypted == "" {
			return fmt.Errorf("请填写数据库密码")
		}
		row.Name, row.Host, row.Port, row.Database, row.Username, row.SSLMode = req.Name, req.Host, req.Port, req.Database, req.Username, req.SSLMode
		row.ServerID = req.ServerID
		row.LogEnabled = req.LogEnabled
		row.LogDirectory = req.LogDirectory
		row.LogFormat = req.LogFormat
		row.LogTimezone = req.LogTimezone
		row.LogPrefix = req.LogPrefix
		row.SlowThresholdMs = req.SlowThresholdMs
		row.LookbackHours = req.LookbackHours
		row.Revision++
		if row.LogStartAt == nil {
			start := time.Now().Add(-time.Duration(req.LookbackHours) * time.Hour)
			row.LogStartAt = &start
		}
		return tx.Save(&row).Error
	})
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) DeleteDatabase(c *gin.Context) {
	id := opsID(c)
	var busy int64
	repository.DB.Model(&model.InspectionTargetRun{}).Where("type='database' AND target_id=? AND status IN ?", id, []string{"QUEUED", "RUNNING", "UNKNOWN"}).Count(&busy)
	if busy > 0 {
		opsFail(c, fmt.Errorf("该实例存在进行中或待核实的巡检任务"))
		return
	}
	if err := repository.DB.Delete(&model.OperationsDatabase{}, id).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}
func (h *OperationsHandler) ImportDatabase(c *gin.Context) {
	row, err := h.svc.ImportCurrentDatabase("")
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) Diagnostics(c *gin.Context) {
	out, err := h.svc.Diagnose(c.Request.Context(), opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, out)
}
func (h *OperationsHandler) DetectLogs(c *gin.Context) {
	out, err := h.svc.DetectLogs(c.Request.Context(), opsID(c))
	if err != nil {
		c.JSON(200, gin.H{"code": 16001, "message": err.Error(), "data": out})
		return
	}
	response.OK(c, out)
}
func (h *OperationsHandler) SyncLogs(c *gin.Context) {
	if err := h.svc.SyncLogsAsync(context.Background(), opsID(c)); err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "日志同步已启动", "data": nil})
}
func (h *OperationsHandler) Events(c *gin.Context) {
	page, size := opsPage(c)
	minimum, _ := strconv.ParseFloat(c.Query("minDuration"), 64)
	filter := service.OperationsEventFilter{Type: c.Query("type"), Keyword: c.Query("keyword"), From: c.Query("from"), To: c.Query("to"), MinDuration: minimum}
	rows, total, err := h.svc.ListEvents(c.Request.Context(), opsID(c), page, size, filter)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OKWithPage(c, rows, total, page, size)
}
func (h *OperationsHandler) Event(c *gin.Context) {
	var row model.DatabaseLogEvent
	if err := repository.DB.Where("id=? AND database_id=?", c.Param("eventId"), opsID(c)).First(&row).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) Cursors(c *gin.Context) {
	var rows []model.DatabaseLogCursor
	if err := repository.DB.Where("database_id=?", opsID(c)).Order("updated_at DESC").Limit(100).Find(&rows).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, rows)
}
func (h *OperationsHandler) HostMonitor(c *gin.Context) {
	cfg, m, err := h.svc.HostMonitor(opsID(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, gin.H{"config": cfg, "metrics": m, "collecting": h.svc.HostCollecting(opsID(c))})
}
func (h *OperationsHandler) SaveMonitor(c *gin.Context) {
	var req struct {
		Enabled         bool    `json:"enabled"`
		IntervalSeconds int     `json:"intervalSeconds"`
		MetricsURL      *string `json:"metricsURL"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	var addresses []string
	if req.MetricsURL != nil {
		addresses = append(addresses, *req.MetricsURL)
	}
	cfg, err := h.svc.ConfigureMonitor(opsID(c), req.Enabled, req.IntervalSeconds, addresses...)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, cfg)
}
func (h *OperationsHandler) CollectHost(c *gin.Context) {
	if err := h.svc.CollectHostAsync(context.Background(), opsID(c)); err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "采集已接受", "data": nil})
}
func (h *OperationsHandler) History(c *gin.Context) {
	hours, _ := strconv.Atoi(c.DefaultQuery("hours", "1"))
	rows, step, err := h.svc.HostHistory(c.Request.Context(), opsID(c), hours)
	if err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, gin.H{"samples": rows, "stepSeconds": step, "aggregation": "每个时间桶最后一次样本"})
}
func (h *OperationsHandler) Runs(c *gin.Context) {
	page, size := opsPage(c)
	q := repository.DB.Model(&model.InspectionRun{})
	if c.Query("status") != "" {
		q = q.Where("status=?", c.Query("status"))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		opsFail(c, err)
		return
	}
	var rows []model.InspectionRun
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OKWithPage(c, rows, total, page, size)
}
func (h *OperationsHandler) CreateRun(c *gin.Context) {
	var req service.InspectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	run, err := h.svc.CreateInspection(req, middleware.GetCurrentUsername(c))
	if err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "巡检任务已接受", "data": run})
}
func (h *OperationsHandler) Run(c *gin.Context) {
	var run model.InspectionRun
	if err := repository.DB.First(&run, opsID(c)).Error; err != nil {
		opsFail(c, err)
		return
	}
	var targets []model.InspectionTargetRun
	if err := repository.DB.Omit("result_json", "progress_json").Where("run_id=?", run.ID).Order("id").Find(&targets).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, gin.H{"run": run, "targets": targets})
}
func (h *OperationsHandler) Target(c *gin.Context) {
	var row model.InspectionTargetRun
	if err := repository.DB.Where("run_id=? AND id=?", opsID(c), c.Param("targetId")).First(&row).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) CancelRun(c *gin.Context) {
	r := repository.DB.Model(&model.InspectionRun{}).Where("id=? AND status IN ?", opsID(c), []string{"QUEUED", "RUNNING"}).Updates(map[string]any{"cancel_requested": true, "message": "取消请求已接受，等待远端确认"})
	if r.Error != nil {
		opsFail(c, r.Error)
		return
	}
	if r.RowsAffected == 0 {
		opsFail(c, fmt.Errorf("任务已经结束或不存在"))
		return
	}
	response.OK(c, nil)
}
func (h *OperationsHandler) ReconcileRun(c *gin.Context) {
	if err := h.svc.ReconcileInspection(opsID(c)); err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}
func (h *OperationsHandler) RegenerateReports(c *gin.Context) {
	if err := h.svc.RegenerateReportsAsync(opsID(c)); err != nil {
		opsFail(c, err)
		return
	}
	c.JSON(202, gin.H{"code": 0, "message": "报告生成已接受", "data": nil})
}
func (h *OperationsHandler) Report(c *gin.Context) {
	var run model.InspectionRun
	if err := repository.DB.First(&run, opsID(c)).Error; err != nil {
		opsFail(c, err)
		return
	}
	format := c.Param("format")
	ready := run.HTMLStatus
	if format == "pdf" {
		ready = run.PDFStatus
	}
	if ready != "READY" {
		opsFail(c, fmt.Errorf("该格式报告尚未生成，请查看任务详情"))
		return
	}
	name, err := h.svc.ReportPath(run.ID, format)
	if err != nil {
		opsFail(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if format == "html" {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; frame-ancestors 'self'")
	}
	c.FileAttachment(name, fmt.Sprintf("inspection-%d.%s", run.ID, format))
}
func (h *OperationsHandler) DeleteRun(c *gin.Context) {
	if err := h.svc.DeleteInspection(opsID(c)); err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}
func (h *OperationsHandler) Plans(c *gin.Context) {
	var rows []model.InspectionPlan
	if err := repository.DB.Order("id DESC").Find(&rows).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, rows)
}
func (h *OperationsHandler) SavePlan(c *gin.Context) {
	var req struct {
		Name      string                            `json:"name"`
		Enabled   bool                              `json:"enabled"`
		DailyTime string                            `json:"dailyTime"`
		Timezone  string                            `json:"timezone"`
		Targets   []service.InspectionTargetRequest `json:"targets"`
		Services  []string                          `json:"services"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		opsFail(c, err)
		return
	}
	if req.Timezone == "" {
		req.Timezone = "Asia/Shanghai"
	}
	next, err := service.NextInspectionTime(time.Now(), req.DailyTime, req.Timezone)
	if err != nil {
		opsFail(c, err)
		return
	}
	if err := h.svc.ValidateInspection(service.InspectionRequest{Name: req.Name, Targets: req.Targets, Services: req.Services}); err != nil {
		opsFail(c, err)
		return
	}
	var row model.InspectionPlan
	if id := opsID(c); id > 0 {
		if err := repository.DB.First(&row, id).Error; err != nil {
			opsFail(c, err)
			return
		}
	}
	row.Name = req.Name
	row.Enabled = req.Enabled
	row.LastError = ""
	row.DailyTime = req.DailyTime
	row.Timezone = req.Timezone
	row.TargetsJSON = service.OperationsJSON(req.Targets)
	row.ServicesJSON = service.OperationsJSON(req.Services)
	row.NextRunAt = &next
	row.CreatedBy = middleware.GetCurrentUsername(c)
	if err := repository.DB.Save(&row).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OperationsHandler) DeletePlan(c *gin.Context) {
	if err := repository.DB.Delete(&model.InspectionPlan{}, opsID(c)).Error; err != nil {
		opsFail(c, err)
		return
	}
	response.OK(c, nil)
}

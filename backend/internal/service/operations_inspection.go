package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/opsremote"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type InspectionTargetRequest struct {
	Type string `json:"type"`
	ID   uint   `json:"id"`
}
type InspectionRequest struct {
	Commands map[string]opsprobe.ManualOptions `json:"commands"`
	Name     string                            `json:"name"`
	Targets  []InspectionTargetRequest         `json:"targets"`
	Services []string                          `json:"services"`
}

func (s *OperationsService) CreateInspection(req InspectionRequest, operator string) (*model.InspectionRun, error) {
	var out *model.InspectionRun
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = s.createInspectionTx(tx, req, operator, 0, "")
		return err
	})
	return out, err
}
func (s *OperationsService) createInspectionTx(tx *gorm.DB, req InspectionRequest, operator string, planID uint, key string, validateOnly ...bool) (*model.InspectionRun, error) {
	if err := validateInspectionCommands(req.Commands, req.Targets); err != nil {
		return nil, err
	}
	if len(req.Targets) == 0 || len(req.Targets) > 500 {
		return nil, fmt.Errorf("请选择 1～500 个巡检对象")
	}
	if len(req.Services) > 20 {
		return nil, fmt.Errorf("服务检查最多 20 项")
	}
	for _, name := range req.Services {
		if !regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,128}$`).MatchString(name) {
			return nil, fmt.Errorf("服务名称不合法")
		}
	}
	if req.Name == "" {
		req.Name = "系统巡检报告"
	}
	if len([]rune(req.Name)) > 128 {
		return nil, fmt.Errorf("任务名称过长")
	}
	var targets []model.InspectionTargetRun
	seen := map[string]bool{}
	for _, ref := range req.Targets {
		identity := fmt.Sprintf("%s:%d", ref.Type, ref.ID)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		t := model.InspectionTargetRun{Type: ref.Type, TargetID: ref.ID, Status: "QUEUED", Stage: "等待下发", ServicesJSON: mustJSON(req.Services)}
		switch ref.Type {
		case "host":
			var server model.Server
			if err := tx.First(&server, ref.ID).Error; err != nil {
				return nil, fmt.Errorf("主机 %d 不存在", ref.ID)
			}
			t.ServerID = server.ID
			t.Name = server.Remark
			if t.Name == "" {
				t.Name = server.Host
			}
			t.Endpoint = serverEndpoint(server)
			t.ServerEndpoint = t.Endpoint
		case "database":
			var db model.OperationsDatabase
			if err := tx.First(&db, ref.ID).Error; err != nil {
				return nil, fmt.Errorf("数据库实例 %d 不存在", ref.ID)
			}
			var server model.Server
			if db.ServerID == 0 {
				return nil, fmt.Errorf("数据库 %s 未关联巡检执行主机", db.Name)
			}
			if err := tx.First(&server, db.ServerID).Error; err != nil {
				return nil, fmt.Errorf("数据库 %s 关联主机不存在", db.Name)
			}
			t.ServerID = db.ServerID
			t.ServerEndpoint = serverEndpoint(server)
			t.Name = db.Name
			t.Endpoint = fmt.Sprintf("%s:%d/%s", db.Host, db.Port, db.Database)
			t.ConfigRevision = db.Revision
		default:
			return nil, fmt.Errorf("巡检类型仅支持 host 或 database")
		}
		if manual, ok := req.Commands[identity]; ok {
			t.ManualJSON = mustJSON(manual)
		}
		targets = append(targets, t)
	}
	trigger := "MANUAL"
	if planID > 0 {
		trigger = "SCHEDULED"
	}
	run := &model.InspectionRun{PlanID: planID, Name: req.Name, Status: "QUEUED", Trigger: trigger, Operator: operator, TotalTargets: len(targets), HTMLStatus: "PENDING", PDFStatus: "PENDING", Message: "已接受，等待下发"}
	if key != "" {
		run.ScheduleKey = key
	}
	if len(validateOnly) > 0 && validateOnly[0] {
		return run, nil
	}
	if err := tx.Create(run).Error; err != nil {
		return nil, err
	}
	for i := range targets {
		targets[i].RunID = run.ID
	}
	if err := tx.Create(&targets).Error; err != nil {
		return nil, err
	}
	return run, nil
}
func (s *OperationsService) scheduleInspections(ctx context.Context) {
	s.enqueueDuePlans(ctx)
	for i := 0; i < 2; i++ {
		select {
		case s.runSlots <- struct{}{}:
		default:
			return
		}
		var run model.InspectionRun
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='QUEUED' OR (status='RUNNING' AND (lease_until IS NULL OR lease_until<?))", time.Now()).Order("id").First(&run).Error; err != nil {
				return err
			}
			now, until := time.Now(), time.Now().Add(30*time.Second)
			updates := map[string]any{"status": "RUNNING", "lease_owner": s.owner, "lease_until": until, "message": "后台巡检中"}
			if run.StartedAt == nil {
				updates["started_at"] = now
			}
			return tx.Model(&run).Updates(updates).Error
		})
		if err != nil {
			<-s.runSlots
			return
		}
		go func(id uint) { defer func() { <-s.runSlots }(); s.processInspection(ctx, id) }(run.ID)
	}
}
func (s *OperationsService) processInspection(parent context.Context, id uint) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				r := s.db.Model(&model.InspectionRun{}).Where("id=? AND lease_owner=? AND status='RUNNING'", id, s.owner).Update("lease_until", time.Now().Add(30*time.Second))
				if r.Error != nil || r.RowsAffected != 1 {
					cancel()
					return
				}
			}
		}
	}()
	defer s.db.Model(&model.InspectionRun{}).Where("id=? AND lease_owner=?", id, s.owner).Update("lease_until", time.Now())
	var targets []model.InspectionTargetRun
	if err := s.db.Where("run_id=?", id).Order("id").Find(&targets).Error; err != nil {
		return
	}
	for i := range targets {
		if ctx.Err() != nil {
			return
		}
		t := &targets[i]
		if t.Status == "COMPLETED" || t.Status == "FAILED" || t.Status == "CANCELED" {
			continue
		}
		var run model.InspectionRun
		if err := s.db.First(&run, id).Error; err != nil {
			return
		}
		if run.CancelRequested && t.RemoteDirectory == "" {
			s.finishInspectionTarget(t, "CANCELED", "任务已取消，未下发", nil)
			continue
		}
		s.executeInspectionTarget(ctx, t)
		var completed int64
		s.db.Model(&model.InspectionTargetRun{}).Where("run_id=? AND status IN ?", id, []string{"COMPLETED", "FAILED", "CANCELED", "UNKNOWN"}).Count(&completed)
		s.db.Model(&model.InspectionRun{}).Where("id=? AND lease_owner=?", id, s.owner).Update("completed_targets", completed)
	}
	if ctx.Err() != nil {
		return
	}
	var run model.InspectionRun
	if err := s.db.First(&run, id).Error; err != nil {
		return
	}
	if err := s.db.Where("run_id=?", id).Order("id").Find(&targets).Error; err != nil {
		return
	}
	warnings, critical, unknown := 0, 0, 0
	for _, t := range targets {
		previousUnknown := unknown
		var result opsprobe.JobResult
		if t.ResultJSON != "" {
			if err := json.Unmarshal([]byte(t.ResultJSON), &result); err != nil {
				unknown++
				continue
			}
			for _, c := range result.Checks {
				switch c.Status {
				case "WARNING":
					warnings++
				case "CRITICAL":
					critical++
				case "UNKNOWN":
					unknown++
				}
			}
			for _, c := range result.ReportChecks {
				switch c.Status {
				case "WARNING":
					warnings++
				case "CRITICAL":
					critical++
				case "UNKNOWN":
					unknown++
				}
			}
		}
		if t.Status != "COMPLETED" && unknown == previousUnknown {
			unknown++
		}
	}
	status := "COMPLETED"
	if unknown > 0 {
		status = "PARTIAL"
	}
	if run.CancelRequested {
		status = "CANCELED"
	}
	r := s.db.Model(&run).Where("lease_owner=?", s.owner).Updates(map[string]any{"completed_targets": len(targets), "warning_count": warnings, "critical_count": critical, "error_count": unknown, "message": "采集已结束，正在生成报告"})
	if r.Error != nil || r.RowsAffected != 1 {
		return
	}
	reportErr := s.GenerateInspectionReports(ctx, id)
	// Cross-object requirements (for example, obtaining one current Leader
	// archive result) can be incomplete even if all SSH jobs finished.
	var reportUnknown int
	if err := s.db.Model(&model.InspectionRun{}).Where("id=?", id).Select("COALESCE((report_snapshot_json->'summary'->>'unknown')::integer,0)").Scan(&reportUnknown).Error; err != nil && reportErr == nil {
		reportErr = err
	}
	if reportUnknown > unknown {
		unknown = reportUnknown
		if status == "COMPLETED" {
			status = "PARTIAL"
		}
	}
	if reportErr != nil && status == "COMPLETED" {
		status = "PARTIAL"
	}
	final := map[string]any{"status": status, "finished_at": time.Now(), "error_count": unknown}
	if reportErr != nil {
		final["message"] = reportErr.Error()
	}
	s.db.Model(&run).Where("lease_owner=?", s.owner).Updates(final)
}
func (s *OperationsService) finishInspectionTarget(t *model.InspectionTargetRun, status, message string, result *opsprobe.JobResult) {
	now := time.Now()
	updates := map[string]any{"status": status, "message": message, "stage": "检查结束", "finished_at": now, "heartbeat_at": now}
	if result != nil {
		updates["result_json"] = mustJSON(result)
	}
	s.db.Model(t).Updates(updates)
}
func (s *OperationsService) executeInspectionTarget(parent context.Context, t *model.InspectionTargetRun) {
	timeout := 180 * time.Second
	if t.ManualJSON != "" {
		timeout = 360 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var server model.Server
	if err := s.db.First(&server, t.ServerID).Error; err != nil {
		s.finishInspectionTarget(t, "FAILED", "执行主机不存在", nil)
		return
	}
	if serverEndpoint(server) != t.ServerEndpoint {
		s.finishInspectionTarget(t, "UNKNOWN", "执行主机地址或账号已变化，请核对原任务；未向新地址下发", nil)
		return
	}
	now := time.Now()
	s.db.Model(t).Updates(map[string]any{"status": "RUNNING", "stage": "连接执行主机", "started_at": now, "message": "", "heartbeat_at": now})

	conn, err := s.connect(ctx, t.ServerID)
	if err != nil {
		state := "FAILED"
		if t.RemoteDirectory != "" {
			state = "UNKNOWN"
		}
		s.finishInspectionTarget(t, state, err.Error(), nil)
		return
	}
	defer conn.Close()
	if t.RemoteDirectory == "" {
		cfg := opsprobe.JobConfig{Type: t.Type, Name: t.Name, TimeoutSeconds: 120}
		if t.ManualJSON != "" {
			var manual opsprobe.ManualOptions
			if e := json.Unmarshal([]byte(t.ManualJSON), &manual); e != nil {
				s.finishInspectionTarget(t, "FAILED", "巡检命令配置无效", nil)
				return
			}
			cfg.Manual = &manual
			cfg.TimeoutSeconds = 300
		}
		_ = json.Unmarshal([]byte(t.ServicesJSON), &cfg.Services)
		if t.Type == "database" {
			var row model.OperationsDatabase
			if err := s.db.First(&row, t.TargetID).Error; err != nil {
				s.finishInspectionTarget(t, "FAILED", "数据库配置不存在", nil)
				return
			}
			if row.Revision != t.ConfigRevision {
				s.finishInspectionTarget(t, "FAILED", "数据库连接配置已变化，请新建任务；未使用新配置执行旧任务", nil)
				return
			}
			c, e := s.databaseConfig(row)
			if e != nil {
				s.finishInspectionTarget(t, "FAILED", e.Error(), nil)
				return
			}
			cfg.Database = &c
		}
		s.db.Model(t).Update("stage", "校验并下发执行包")
		exe, base, e := conn.EnsureRunner(ctx)
		if e != nil {
			s.finishInspectionTarget(t, "FAILED", e.Error(), nil)
			return
		}
		dir := path.Join(base, fmt.Sprintf("run-%d-target-%d-%d", t.RunID, t.ID, time.Now().UnixNano()))
		if err := conn.SFTP.MkdirAll(dir); err != nil {
			s.finishInspectionTarget(t, "FAILED", err.Error(), nil)
			return
		}
		if err := conn.SFTP.Chmod(dir, 0700); err != nil {
			s.finishInspectionTarget(t, "FAILED", err.Error(), nil)
			return
		}
		if err := s.db.Model(t).Update("remote_directory", dir).Error; err != nil {
			return
		}
		t.RemoteDirectory = dir
		if err := conn.JSONFile(path.Join(dir, "config.json"), cfg); err != nil {
			_ = conn.SFTP.Remove(path.Join(dir, "config.json"))
			s.finishInspectionTarget(t, "FAILED", err.Error(), nil)
			return
		}
		// No credentials appear in process arguments. The executor consumes and
		// removes the private configuration file immediately after claiming the job.
		_, err = conn.Run(ctx, "nohup "+opsremote.Quote(exe)+" --mode job --dir "+opsremote.Quote(dir)+" </dev/null >"+opsremote.Quote(path.Join(dir, "launcher.log"))+" 2>&1 &", 4096)
		if err != nil {
			s.finishInspectionTarget(t, "UNKNOWN", "下发结果待核实: "+err.Error(), nil)
			return
		}
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastProgress := ""
	waitStarted := time.Now()
	for {
		select {
		case <-ctx.Done():
			s.finishInspectionTarget(t, "UNKNOWN", "连接或等待已中断，远端任务可能仍在执行；可以核实远端结果，无需重复下发", nil)
			return
		default:
		}
		var run model.InspectionRun
		if err := s.db.First(&run, t.RunID).Error; err != nil {
			return
		}
		if run.CancelRequested {
			if err := conn.WriteFile(path.Join(t.RemoteDirectory, "cancel"), []byte("cancel"), 0600); err != nil {
				s.finishInspectionTarget(t, "UNKNOWN", "取消请求未获远端确认: "+err.Error(), nil)
				return
			}
		}
		if data, e := conn.ReadFile(path.Join(t.RemoteDirectory, "result.json"), 0); e == nil {
			var result opsprobe.JobResult
			if err := json.Unmarshal(data, &result); err != nil {
				s.finishInspectionTarget(t, "UNKNOWN", "远端结果格式错误: "+err.Error(), nil)
				return
			}
			if t.Type == "database" && t.ManualJSON == "" {
				s.appendLogInspectionChecks(t.TargetID, &result)
			}
			s.enrichInspectionHistory(t, &result)
			state, message := "COMPLETED", "远端检查完成，结果已回收"
			if result.Error != "" {
				state, message = "FAILED", result.Error
			}
			if run.CancelRequested {
				state = "CANCELED"
				message = "远端已结束，取消请求已核实"
			}
			s.finishInspectionTarget(t, state, message, &result)
			_ = conn.SFTP.Remove(path.Join(t.RemoteDirectory, "config.json"))
			return
		} else if !errors.Is(e, os.ErrNotExist) {
			s.finishInspectionTarget(t, "UNKNOWN", "回收远端结果失败: "+e.Error(), nil)
			return
		}
		if data, e := conn.ReadFile(path.Join(t.RemoteDirectory, "status.json"), 64<<10); e == nil {
			lastProgress = string(data)
			var p struct {
				Status, Stage, Message string
				At                     time.Time
			}
			if json.Unmarshal(data, &p) == nil {
				s.db.Model(t).Updates(map[string]any{"stage": p.Stage, "message": p.Message, "heartbeat_at": p.At, "progress_json": lastProgress})
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			s.finishInspectionTarget(t, "UNKNOWN", "读取远端进度失败: "+e.Error(), nil)
			return
		}
		if lastProgress == "" && time.Since(waitStarted) > 10*time.Second {
			if data, e := conn.ReadFile(path.Join(t.RemoteDirectory, "launcher.log"), 8192); e == nil && len(data) > 0 {
				s.finishInspectionTarget(t, "UNKNOWN", "执行器未报告启动状态，请核实远端任务: "+string(data), nil)
				return
			}
		}
		select {
		case <-ctx.Done():
			continue
		case <-ticker.C:
		}
	}
}
func (s *OperationsService) appendLogInspectionChecks(id uint, result *opsprobe.JobResult) {
	var row model.OperationsDatabase
	if err := s.db.First(&row, id).Error; err != nil {
		return
	}
	if !row.LogEnabled {
		result.Checks = append(result.Checks, opsprobe.Check{Code: "log_collection", Name: "历史慢 SQL 与死锁日志", Status: "UNKNOWN", Error: "尚未启用日志接入，不能根据空记录判断没有历史异常"})
		return
	}
	if row.LogError != "" || row.LastLogSyncAt == nil {
		result.Checks = append(result.Checks, opsprobe.Check{Code: "log_collection", Name: "日志采集状态", Status: "UNKNOWN", Error: row.LogError})
		return
	}
	if time.Since(*row.LastLogSyncAt) > 2*time.Minute {
		result.Checks = append(result.Checks, opsprobe.Check{Code: "log_freshness", Name: "日志采集时效", Status: "UNKNOWN", Error: "最近日志同步已超过 2 分钟，以下统计仅代表已入库样本"})
	}
	var capability map[string]string
	_ = json.Unmarshal([]byte(row.LogCapability), &capability)
	sourceMatches := capability["source_identity"] == fmt.Sprintf("%s:%d/%s@%d", row.Host, row.Port, row.Database, row.ServerID) && capability["resolved_directory"] == row.LogDirectory
	for _, kind := range []string{"slow_sql", "deadlock", "lock_wait"} {
		var count int64
		if err := s.db.Model(&model.DatabaseLogEvent{}).Where("database_id=? AND type=? AND event_time>=?", id, kind, time.Now().Add(-24*time.Hour)).Count(&count).Error; err != nil {
			result.Checks = append(result.Checks, opsprobe.Check{Code: "log:" + kind, Name: "历史日志统计", Status: "UNKNOWN", Error: err.Error()})
			continue
		}
		status := "NORMAL"
		if count > 0 {
			status = "WARNING"
		}
		if kind == "deadlock" && count > 0 {
			status = "CRITICAL"
		}
		message := ""
		if kind == "lock_wait" && (!sourceMatches || capability["log_lock_waits"] != "on") {
			status, message = "UNKNOWN", "最近配置检测未确认 log_lock_waits=on，无法据此判断历史上是否发生过锁等待；请结合当前锁快照核实"
		}
		result.Checks = append(result.Checks, opsprobe.Check{Code: "log:" + kind, Name: map[string]string{"slow_sql": "近 24 小时已采集慢 SQL 日志", "deadlock": "近 24 小时已采集死锁日志", "lock_wait": "近 24 小时已采集锁等待日志"}[kind], Status: status, Value: fmt.Sprintf("%d 条", count), Evidence: fmt.Sprintf("日志源 %s；最近同步 %s；仅统计已采集记录，包含不同协议阶段", row.LogDirectory, row.LastLogSyncAt.Format(time.RFC3339)), Error: message})
	}
}
func NextInspectionTime(now time.Time, daily, zone string) (time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("时区无效")
	}
	parsed, err := time.Parse("15:04", daily)
	if err != nil {
		return time.Time{}, fmt.Errorf("执行时间应为 HH:mm")
	}
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}
func (s *OperationsService) enqueueDuePlans(ctx context.Context) {
	// Each plan owns a transaction: one stale target must not block other plans.
	var ids []uint
	if err := s.db.WithContext(ctx).Model(&model.InspectionPlan{}).Where("enabled=true AND next_run_at<=?", time.Now()).Order("next_run_at").Limit(5).Pluck("id", &ids).Error; err != nil {
		return
	}
	for _, id := range ids {
		_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var p model.InspectionPlan
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("id=? AND enabled=true AND next_run_at<=?", id, time.Now()).First(&p).Error; err != nil {
				return err
			}
			next, err := NextInspectionTime(time.Now(), p.DailyTime, p.Timezone)
			if err != nil {
				return tx.Model(&p).Updates(map[string]any{"enabled": false, "last_error": err.Error()}).Error
			}
			var req InspectionRequest
			req.Name = p.Name
			parseErr := json.Unmarshal([]byte(p.TargetsJSON), &req.Targets)
			if parseErr == nil {
				parseErr = json.Unmarshal([]byte(p.ServicesJSON), &req.Services)
			}
			if parseErr == nil && p.CommandsJSON != "" {
				parseErr = json.Unmarshal([]byte(p.CommandsJSON), &req.Commands)
			}
			message := ""
			var busy int64
			if err := tx.Model(&model.InspectionRun{}).Where("plan_id=? AND status IN ?", p.ID, []string{"QUEUED", "RUNNING"}).Count(&busy).Error; err != nil {
				return err
			}
			if busy > 0 {
				message = "上一次巡检尚未结束，本次已跳过"
			} else if parseErr != nil {
				message = "巡检对象配置错误: " + parseErr.Error()
			} else {
				key := fmt.Sprintf("%d/%d", p.ID, p.NextRunAt.Unix())
				// Savepoint also recovers a PostgreSQL statement error before recording it.
				if err := tx.Transaction(func(jobTx *gorm.DB) error {
					_, err := s.createInspectionTx(jobTx, req, "定时计划", p.ID, key)
					return err
				}); err != nil {
					message = "任务未下发: " + err.Error()
				}
			}
			return tx.Model(&p).Updates(map[string]any{"next_run_at": next, "last_error": message}).Error
		})
	}
}

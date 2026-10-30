package service

import (
	"context"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"fmt"
	"gorm.io/gorm"
	"time"
)

// Resolve sources once; later connection edits cannot redirect queued tasks.
func (s *OperationsService) CreateConfiguredInspection(ctx context.Context, name, operator string, refs []InspectionTargetRequest) (*model.InspectionRun, error) {
	if err := s.EnsureInspectionSources(ctx); err != nil {
		return nil, err
	}
	cfg, err := NewPostgreSQLService().loadConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Host != "" && cfg.Database != "" && cfg.User != "" && cfg.Password != "" {
		if _, err := s.ImportCurrentDatabase(""); err != nil {
			return nil, err
		}
	}
	if len(refs) == 0 {
		var hosts []model.Server
		var databases []model.OperationsDatabase
		var components []model.ComponentConnection
		if err := s.db.Order("id").Find(&hosts).Error; err != nil {
			return nil, err
		}
		if err := s.db.Order("id").Find(&databases).Error; err != nil {
			return nil, err
		}
		if err := s.db.Where("enabled=true").Order("id").Find(&components).Error; err != nil {
			return nil, err
		}
		for _, h := range hosts {
			refs = append(refs, InspectionTargetRequest{Type: "host", ID: h.ID})
		}
		for _, d := range databases {
			refs = append(refs, InspectionTargetRequest{Type: "database", ID: d.ID})
		}
		for _, c := range components {
			refs = append(refs, InspectionTargetRequest{Type: "component", ID: c.ID})
		}
	}
	if name == "" {
		name = "系统巡检报告"
	}
	if len([]rune(name)) > 128 {
		return nil, fmt.Errorf("报告名称过长")
	}
	if len(refs) == 0 || len(refs) > 500 {
		return nil, fmt.Errorf("当前没有可巡检对象，或对象数超过 500")
	}
	var result *model.InspectionRun
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		run := model.InspectionRun{Name: name, CollectionMode: "configured", Status: "QUEUED", Trigger: "MANUAL", Operator: operator, HTMLStatus: "PENDING", PDFStatus: "PENDING", Message: "等待汇总监控数据"}
		seen := map[string]bool{}
		var targets []model.InspectionTargetRun
		var components []model.ComponentConnection
		if err := tx.Where("enabled=true").Find(&components).Error; err != nil {
			return err
		}
		selected := map[uint]bool{}
		for _, r := range refs {
			if r.Type == "component" {
				selected[r.ID] = true
			}
		}
		for _, ref := range refs {
			key := fmt.Sprintf("%s:%d", ref.Type, ref.ID)
			if seen[key] {
				continue
			}
			seen[key] = true
			t := model.InspectionTargetRun{Type: ref.Type, TargetID: ref.ID, Status: "QUEUED", Stage: "等待汇总监控数据", ServicesJSON: "[]"}
			switch ref.Type {
			case "host":
				var row model.Server
				if err := tx.First(&row, ref.ID).Error; err != nil {
					return err
				}
				t.Name = row.Remark
				if t.Name == "" {
					t.Name = row.Host
				}
				t.Endpoint = serverEndpoint(row)
				t.ServerEndpoint = t.Endpoint
				t.ServerID = row.ID
				opt := opsprobe.ManualOptions{Auto: true}
				for _, c := range components {
					if selected[c.ID] && c.ServerID == row.ID {
						opt.ConfiguredComponents = append(opt.ConfiguredComponents, c.Kind)
					}
				}
				t.ManualJSON = mustJSON(opt)
				t.ExpectedJSON = mustJSON(inspection.RequiredCodes("host"))
			case "database":
				var row model.OperationsDatabase
				if err := tx.First(&row, ref.ID).Error; err != nil {
					return err
				}
				t.Name = row.Name
				t.Endpoint = fmt.Sprintf("%s:%d/%s", row.Host, row.Port, row.Database)
				t.ServerID = row.ServerID
				t.ConfigRevision = row.Revision
				t.ServerEndpoint = "平台只读数据库连接"
				t.ExpectedJSON = mustJSON(inspection.RequiredCodes("database"))
			case "component":
				var row model.ComponentConnection
				if err := tx.First(&row, ref.ID).Error; err != nil {
					return err
				}
				if !row.Enabled {
					return fmt.Errorf("组件 %s 未启用巡检", row.Name)
				}
				t.Name = row.Name
				t.Endpoint = row.Endpoint
				if row.Source == "environment" {
					t.Endpoint = "复用环境配置"
				}
				t.ServerID = row.ServerID
				t.ConfigRevision = row.Revision
				t.ServerEndpoint = "平台只读组件接口"
				t.ExpectedJSON = mustJSON(inspection.RequiredCodes(row.Kind))
			default:
				return fmt.Errorf("不支持的巡检对象")
			}
			targets = append(targets, t)
		}
		run.TotalTargets = len(targets)
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		for i := range targets {
			targets[i].RunID = run.ID
		}
		if err := tx.Create(&targets).Error; err != nil {
			return err
		}
		result = &run
		return nil
	})
	return result, err
}

func (s *OperationsService) executeConfiguredTarget(ctx context.Context, t *model.InspectionTargetRun) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				var requested bool
				if err := s.db.Model(&model.InspectionRun{}).Where("id=?", t.RunID).Select("cancel_requested").Scan(&requested).Error; err == nil && requested {
					cancel()
					return
				}
			}
		}
	}()
	now := time.Now()
	if err := s.db.Model(t).Updates(map[string]any{"status": "RUNNING", "stage": "现场采集", "started_at": now, "heartbeat_at": now}).Error; err != nil {
		return
	}
	var result *opsprobe.JobResult
	if t.Type == "component" {
		var row model.ComponentConnection
		if err := s.db.First(&row, t.TargetID).Error; err != nil || row.Revision != t.ConfigRevision {
			s.finishInspectionTarget(t, "FAILED", "组件已删除或连接已修改，请重新发起巡检", nil)
			return
		}
		result = s.collectComponent(ctx, row)
	} else {
		var row model.OperationsDatabase
		if err := s.db.First(&row, t.TargetID).Error; err != nil || row.Revision != t.ConfigRevision {
			s.finishInspectionTarget(t, "FAILED", "数据库已删除或连接已修改，请重新发起巡检", nil)
			return
		}
		var err error
		result, err = s.collectDatabaseInspection(ctx, row)
		if err != nil {
			s.finishInspectionTarget(t, "FAILED", err.Error(), nil)
			return
		}
	}
	if err := s.rememberCollection(t.Type, t.TargetID, t.ConfigRevision, result); err != nil {
		result.Error = "保存临时采集结果失败"
	}
	var requested bool
	s.db.Model(&model.InspectionRun{}).Where("id=?", t.RunID).Select("cancel_requested").Scan(&requested)
	if requested {
		s.finishInspectionTarget(t, "CANCELED", "任务已取消，已取得的结果保留", result)
	} else {
		s.finishInspectionTarget(t, "COMPLETED", "现场采集已结束", result)
	}
}

func (s *OperationsService) collectDatabaseInspection(parent context.Context, row model.OperationsDatabase) (*opsprobe.JobResult, error) {
	cfg, err := s.databaseConfig(row)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	r := &opsprobe.JobResult{Version: opsprobe.Version, Type: "database", Name: row.Name, StartedAt: time.Now()}
	r.ReportChecks = opsprobe.InspectionDatabase(ctx, cfg)

	r.ReportChecks = append(r.ReportChecks, opsprobe.InspectionPostgreSQLDeployment(ctx, row.Name, cfg)...)
	d, err := opsprobe.Diagnose(ctx, cfg)
	if err != nil {
		r.Checks = append(r.Checks, opsprobe.Check{Code: "postgresql-diagnostics", Name: "PostgreSQL 当前会话诊断", Status: "UNKNOWN", Error: SQLExecutionErrorMessage(err)})
	} else {
		r.Database = d
		r.Checks = opsprobe.DatabaseChecks(d)
		for i := range r.ReportChecks {
			o := &r.ReportChecks[i]
			if o.Status == "UNKNOWN" || len(o.Gaps) > 0 {
				continue
			}
			switch o.Code {
			case "cloudhis-27":
				o.Value = fmt.Sprintf("实例连接 %d / 上限 %d", d.Connections, d.MaxConnections)
				if d.MaxConnections > 0 {
					o.Status = "NORMAL"
					if float64(d.Connections)/float64(d.MaxConnections) >= 0.8 {
						o.Status = "WARNING"
					}
				}
			case "cloudhis-28", "cloudhis-30", "cloudhis-33":
				if !d.StatsVisible || d.Truncated {
					continue
				}
				count := 0
				for _, a := range d.Activities {
					if (o.Code == "cloudhis-28" && a.WaitEventType == "Lock") || (o.Code == "cloudhis-30" && a.TransactionSeconds >= 300) || (o.Code == "cloudhis-33" && a.State == "active" && a.ElapsedSeconds >= 5) {
						count++
					}
				}
				o.Status = "NORMAL"
				if count > 0 {
					o.Status = "WARNING"
				}
				label := map[string]string{"cloudhis-28": "当前锁等待会话", "cloudhis-30": "运行超过 5 分钟的事务", "cloudhis-33": "当前执行超过 5 秒的 SQL"}[o.Code]
				o.Value = fmt.Sprintf("%s：%d 个", label, count)
			}
		}
	}
	s.collectDatabaseHosts(ctx, row, cfg, r)
	r.ExpectedChecks = inspection.RequiredCodes("database")
	r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, r.ReportChecks, r.Error)
	r.FinishedAt = time.Now()
	return r, nil
}
func (s *OperationsService) CollectDatabaseInspection(ctx context.Context, id uint) (*opsprobe.JobResult, error) {
	key := fmt.Sprintf("database:%d", id)
	if !s.claimCollection(key) {
		return nil, fmt.Errorf("该数据库正在采集，请稍后刷新")
	}
	defer s.releaseCollection(key)
	var row model.OperationsDatabase
	if err := s.db.First(&row, id).Error; err != nil {
		return nil, err
	}
	result, err := s.collectDatabaseInspection(ctx, row)
	if err != nil {
		return nil, err
	}
	return result, s.rememberCollection("database", id, row.Revision, result)
}

func configuredObject(o *inspection.Object, result opsprobe.JobResult) {
	seen := map[string]bool{}
	for _, obs := range o.Observations {
		seen[obs.Code] = true
	}
	for _, c := range result.Checks {
		if seen[c.Code] {
			continue
		}
		if c.Code == "database_identity" || c.Code == "database_activity" {
			continue
		}
		o.Observations = append(o.Observations, inspection.Observation{Code: c.Code, Name: c.Name, Status: c.Status, Value: c.Value, Reason: c.Error, Evidence: c.Evidence, Advice: c.Advice, CollectedAt: result.FinishedAt})
	}
	if len(o.Observations) == 0 && len(result.ExpectedChecks) == 0 {
		o.Observations = []inspection.Observation{{Code: o.Type + "-collection", Name: "现场采集", Status: "UNKNOWN", Reason: o.Message, Value: "未取得任何检查结果"}}
	}
	for i := range o.Observations {
		obs := &o.Observations[i]
		if obs.Name == "" {
			for _, d := range inspection.Catalogue() {
				if d.Code == obs.Code {
					obs.Name = d.Name
				}
			}
		}
	}
	o.CheckCodes = append([]string{}, result.ExpectedChecks...)
	o.Observations = inspection.EnsureResults(result.ExpectedChecks, o.Observations, result.Error)
	for _, obs := range o.Observations {
		o.CheckCodes = append(o.CheckCodes, obs.Code)
	}
}

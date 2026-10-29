package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
)

func validateInspectionCommands(commands map[string]opsprobe.ManualOptions, targets []InspectionTargetRequest) error {
	selected := map[string]string{}
	for _, t := range targets {
		selected[fmt.Sprintf("%s:%d", t.Type, t.ID)] = t.Type
	}
	for key, options := range commands {
		kind, ok := selected[key]
		if !ok {
			return fmt.Errorf("巡检命令只能配置给本次已选择的服务器或数据库")
		}
		if err := options.Validate(kind); err != nil {
			return err
		}
	}
	return nil
}

func (r inspectionReport) Document() *inspection.Report {
	if r.Snapshot != nil {
		return r.Snapshot
	}
	doc := inspection.Report{RunID: r.Run.ID, Name: r.Run.Name, Operator: r.Run.Operator, GeneratedAt: time.Now(), Objects: []inspection.Object{}}
	doc.Mode = r.Run.CollectionMode

	if r.Run.StartedAt != nil {
		doc.StartedAt = *r.Run.StartedAt
	}
	if r.Run.FinishedAt != nil {
		doc.FinishedAt = *r.Run.FinishedAt
	}
	pgHosts := map[uint]bool{}
	for _, t := range r.Targets {
		if t.Target.Type == "database" {
			pgHosts[t.Target.ServerID] = true
		}
	}
	for _, t := range r.Targets {
		o := inspection.Object{ID: t.Target.ID, Type: t.Target.Type, Name: t.Target.Name, Endpoint: t.Target.Endpoint, Status: t.Target.Status, Message: t.Target.Message, Observations: t.Result.ReportChecks, Supplement: []inspection.Observation{}, Tables: []inspection.Table{}}
		if t.Target.ManualJSON != "" {
			var manual opsprobe.ManualOptions
			if json.Unmarshal([]byte(t.Target.ManualJSON), &manual) == nil {
				o.CheckCodes = manual.Codes()
			}
		}
		o.ExecutorEndpoint = t.Target.ServerEndpoint
		if o.Roles == nil {
			o.Roles = []string{}
			if o.Type == "host" && pgHosts[t.Target.ServerID] {
				o.Roles = append(o.Roles, "postgresql")
			}
		}
		if t.Result.Error != "" {
			o.Message = t.Result.Error
		}
		for _, c := range t.Result.Checks {
			o.Supplement = append(o.Supplement, inspection.Observation{Code: c.Code, Name: c.Name, Status: c.Status, Value: c.Value, Evidence: c.Evidence, Reason: c.Error, Advice: c.Advice, CollectedAt: t.Result.FinishedAt})
		}
		if t.Result.Metrics != nil {
			m := t.Result.Metrics
			tables := hostReportTables(m)
			o.Tables = append(o.Tables, tables...)
			resourceCodes := []string{}
			for _, code := range o.CheckCodes {
				if code == "cloudhis-6" || code == "cloudhis-15" || code == "cloudhis-36" {
					resourceCodes = append(resourceCodes, code)
				}
			}
			if len(resourceCodes) == 0 && len(o.CheckCodes) == 0 {
				for _, role := range o.Roles {
					if n := map[string]int{"kubernetes": 6, "postgresql": 15, "minio": 36}[role]; n != 0 {
						resourceCodes = append(resourceCodes, fmt.Sprintf("cloudhis-%d", n))
					}
				}
			}
			for _, code := range resourceCodes {
				count := 0
				for _, disk := range m.Disks {
					if opsprobe.ReportableMountpoint(disk.Mountpoint) {
						count++
					}
				}
				obs := inspection.Observation{Code: code, Status: "NORMAL", Value: fmt.Sprintf("CPU %.1f%%，内存 %.1f%%，%d 个实际文件系统；资源明细在本机汇总中列出", m.CPUUsagePercent, m.MemUsagePercent, count), Source: m.SourceURL, CollectedAt: m.CollectedAt, Threshold: "CPU 80%/95%、内存 85%/95%、磁盘空间 80%/90% 分别提醒/严重；本次快照。", Advice: "结合业务负载和持续时间核对，本次为资源快照。"}
				if obs.Source == "" {
					obs.Source = "原远端执行器 /proc 与文件系统快照"
				}
				for _, c := range t.Result.Checks {
					if c.Status == "WARNING" || c.Status == "CRITICAL" {
						if obs.Reason != "" {
							obs.Reason += "；"
						}
						obs.Reason += c.Name + "：" + c.Value
					}
					if c.Status == "CRITICAL" {
						obs.Status = "CRITICAL"
					} else if c.Status == "WARNING" && obs.Status != "CRITICAL" {
						obs.Status = "WARNING"
					}
				}
				if m.MemTotalBytes == 0 || m.CPUCores == 0 || count == 0 {
					obs.Gaps = append(obs.Gaps, "CPU、内存或文件系统必需数据不完整。")
				}
				if len(m.Warnings) > 0 {
					obs.Gaps = append(obs.Gaps, m.Warnings...)
				}
				replaced := false
				for i, prior := range o.Observations {
					if prior.Code == code {
						obs.Executions = prior.Executions
						o.Observations[i] = obs
						replaced = true
						break
					}
				}
				if !replaced {
					o.Observations = append(o.Observations, obs)
				}
			}
		}
		// Legacy snapshots retain their actual scope; an identity result cannot
		// stand in for deployment topology or the entire database checklist.
		if t.Result.Database != nil && len(t.Result.ReportChecks) == 0 {
			d := t.Result.Database
			o.Observations = append(o.Observations, inspection.Observation{Code: "cloudhis-20", Status: "UNKNOWN", Value: d.Database + " / " + d.Role, Evidence: d.Version, Source: "旧版数据库诊断快照", CollectedAt: d.CheckedAt, Gaps: []string{"旧版结果缺少启动时间等安装信息；重新生成报告不会重新采集。"}})
		}
		if doc.StartedAt.IsZero() || (!t.Result.StartedAt.IsZero() && t.Result.StartedAt.Before(doc.StartedAt)) {
			if !t.Result.StartedAt.IsZero() {
				doc.StartedAt = t.Result.StartedAt
			}
		}
		if t.Result.FinishedAt.After(doc.FinishedAt) {
			doc.FinishedAt = t.Result.FinishedAt
		}
		if doc.Mode == "configured" {
			if t.Result.Type != "" {
				o.Type = t.Result.Type
			}
			var expected []string
			_ = json.Unmarshal([]byte(t.Target.ExpectedJSON), &expected)
			result := t.Result
			if len(expected) > 0 {
				result.ExpectedChecks = expected
			}
			configuredObject(&o, result)
			o.Supplement = nil
			if len(o.Tables) > 2 {
				o.Tables = o.Tables[:2]
			}
		}
		doc.Objects = append(doc.Objects, o)
	}
	if doc.FinishedAt.IsZero() {
		doc.FinishedAt = doc.GeneratedAt
	}
	if doc.Mode == "configured" {
		seen := map[uint]bool{}
		for _, t := range r.Targets {
			if t.Target.Type == "host" {
				seen[t.Target.ServerID] = true
			}
		}
		for _, t := range r.Targets {
			for _, h := range t.Result.RelatedHosts {
				if seen[h.ServerID] || h.Result == nil || h.Result.Metrics == nil {
					continue
				}
				seen[h.ServerID] = true
				doc.Objects = append(doc.Objects, inspection.Object{ID: 1000000000 + h.ServerID, Name: h.Name, Type: "host", Endpoint: h.Endpoint, ExecutorEndpoint: h.Endpoint, Status: "COMPLETED", Message: "组件关联主机资源", Tables: hostReportTables(h.Result.Metrics)})
			}
		}
	}
	for i := range doc.Objects {
		for j := range doc.Objects[i].Observations {
			for _, d := range inspection.Catalogue() {
				if d.Code == doc.Objects[i].Observations[j].Code {
					doc.Objects[i].Observations[j].Name = d.Name
				}
			}
		}
	}
	result := inspection.Build(doc)
	return &result
}

func (s *OperationsService) freezeInspectionReport(ctx context.Context, r *inspectionReport) error {
	if r.Run.ReportSnapshotJSON != "" {
		var d inspection.Report
		if err := json.Unmarshal([]byte(r.Run.ReportSnapshotJSON), &d); err != nil {
			return fmt.Errorf("已保存报告快照无效: %w", err)
		}
		r.Snapshot = &d
		return nil
	}
	doc := r.Document()

	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	u := s.db.Model(&model.InspectionRun{}).Where("id=? AND report_snapshot_json IS NULL", r.Run.ID).Update("report_snapshot_json", string(data))
	if u.Error != nil {
		return u.Error
	}
	if u.RowsAffected == 0 {
		var winner model.InspectionRun
		if err := s.db.First(&winner, r.Run.ID).Error; err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(winner.ReportSnapshotJSON), &doc); err != nil {
			return err
		}
	}
	r.Snapshot = doc
	return nil
}

func hostReportTables(m *opsprobe.HostMetrics) []inspection.Table {
	resources := inspection.Table{Title: "主机资源快照", Columns: []string{"指标", "实测值"}, Rows: [][]string{{"采集来源", m.SourceURL}, {"采集时间", m.CollectedAt.Format(time.RFC3339)}, {"CPU / 核数", fmt.Sprintf("%.1f%% / %d", m.CPUUsagePercent, m.CPUCores)}, {"I/O 等待", fmt.Sprintf("%.1f%%", m.CPUWaitPercent)}, {"内存使用 / 可用", fmt.Sprintf("%.1f%% / %.2f GiB", m.MemUsagePercent, float64(m.MemAvailableBytes)/(1<<30))}, {"负载 1 / 5 / 15 分钟", fmt.Sprintf("%.2f / %.2f / %.2f", m.Load1, m.Load5, m.Load15)}, {"Swap 使用 / 总量", fmt.Sprintf("%.2f / %.2f GiB", float64(m.SwapUsedBytes)/(1<<30), float64(m.SwapTotalBytes)/(1<<30))}}}
	if m.Source == "node_exporter" {
		resources.Rows = append(resources.Rows, []string{"CPU Steal", fmt.Sprintf("%.2f%%", m.CPUStealPercent)})
	}
	if m.RunnableTasks != nil {
		resources.Rows = append(resources.Rows, []string{"可运行任务", fmt.Sprint(*m.RunnableTasks)})
	}
	disks := inspection.Table{Title: "文件系统容量与 inode", Columns: []string{"设备 / 挂载点", "总量 GiB", "可用 GiB", "使用率", "inode 使用"}, Rows: [][]string{}}
	for _, d := range m.Disks {
		if !opsprobe.ReportableMountpoint(d.Mountpoint) {
			continue
		}
		inode := "未提供"
		if d.Inodes > 0 {
			inode = fmt.Sprintf("%.1f%%", d.InodeUsedPercent)
		}
		disks.Rows = append(disks.Rows, []string{d.Device + " / " + d.Mountpoint, fmt.Sprintf("%.2f", float64(d.TotalBytes)/(1<<30)), fmt.Sprintf("%.2f", float64(d.AvailableBytes)/(1<<30)), fmt.Sprintf("%.1f%%", d.UsedPercent), inode})
	}
	io := inspection.Table{Title: "磁盘 I/O", Columns: []string{"设备", "读 MiB/s", "写 MiB/s", "读/写 IOPS", "读/写时延 ms", "忙碌时间"}, Rows: [][]string{}, Note: "不同层级的设备不能相加；没有完成请求时，平均时延未取得。"}
	for _, d := range m.DiskIO {
		latency := "未取得"
		if d.ReadAwaitMs != nil || d.WriteAwaitMs != nil {
			latency = fmt.Sprintf("%s / %s", reportMS(d.ReadAwaitMs), reportMS(d.WriteAwaitMs))
		}
		io.Rows = append(io.Rows, []string{d.Device, fmt.Sprintf("%.2f", d.ReadPerSecond/(1<<20)), fmt.Sprintf("%.2f", d.WritePerSecond/(1<<20)), fmt.Sprintf("%.1f / %.1f", d.ReadIOPS, d.WriteIOPS), latency, fmt.Sprintf("%.1f%%", d.Utilization)})
	}
	return []inspection.Table{resources, disks, io}
}
func reportMS(v *float64) string {
	if v == nil {
		return "未取得"
	}
	return fmt.Sprintf("%.2f", *v)
}

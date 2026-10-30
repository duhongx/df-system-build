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

// Reports freeze the same snapshots as the monitoring pages. Only missing,
// stale or failed lanes are refreshed, with the same database lease as the
// background scheduler. Regenerating an existing report never reaches here.
func (s *OperationsService) monitoringReportResult(parent context.Context, t monitoringTarget) (*opsprobe.JobResult, error) {
	ctx, cancel := context.WithTimeout(parent, 200*time.Second)
	defer cancel()
	kind := t.Kind
	if t.Type == "database" {
		kind = "database"
	}
	if t.Type == "host" {
		kind = "host"
	}
	r := &opsprobe.JobResult{Type: kind, Name: t.Name, Version: opsprobe.Version}
	if !t.Enabled {
		r.Error = "该对象已停用后台监控"
		r.ReportChecks = inspection.EnsureResults(inspection.RequiredCodes(kind), nil, r.Error)
		r.ExpectedChecks = inspection.RequiredCodes(kind)
		r.FinishedAt = time.Now()
		return r, nil
	}
	lanes := targetLanes(t)
	// Low-frequency evidence first; current running metrics win overlapping
	// check codes, while original SQL executions retain their own timestamps.
	order := []string{"deep", "facts", "live"}
	for _, lane := range order {
		if lane == "live" && t.Type == "host" {
			live, err := s.monitoringHostForReport(ctx, t)
			if err != nil {
				r.Metrics = nil
				r.Checks = nil
				mergeMonitoringResult(r, &opsprobe.JobResult{ReportChecks: inspection.EnsureResults(monitoringLaneCodes(t, lane), nil, err.Error())})
				continue
			}
			mergeMonitoringResult(r, live)
			continue
		}
		wanted := false
		for _, l := range lanes {
			wanted = wanted || l == lane
		}
		if !wanted {
			continue
		}
		view, err := s.ensureMonitoringLane(ctx, t, lane)
		if err != nil {
			r.ReportChecks = append(r.ReportChecks, inspection.EnsureResults(monitoringLaneCodes(t, lane), nil, err.Error())...)
			continue
		}
		part := cloneMonitoringResult(view.Result)
		if part == nil {
			part = &opsprobe.JobResult{}
		}
		oldFailure := view.LastError != "" && (view.CollectedAt == nil || view.LastAttemptAt == nil || view.CollectedAt.Before(*view.LastAttemptAt))
		if oldFailure || view.Result == nil || view.Stale {
			reason := view.LastError
			if reason == "" {
				reason = "后台数据缺失或已过期"
			}
			for i := range part.ReportChecks {
				part.ReportChecks[i].Status = "UNKNOWN"
				part.ReportChecks[i].Reason = reason + "；保留上次采集时间供核对"
			}
			for i := range part.Checks {
				part.Checks[i].Status = "UNKNOWN"
				part.Checks[i].Error = reason + "；上次有效快照"
			}
			part.ReportChecks = inspection.EnsureResults(monitoringLaneCodes(t, lane), part.ReportChecks, reason)
		}
		s.overlayMonitoringHostMetrics(ctx, part)
		mergeMonitoringResult(r, part)
	}
	r.ExpectedChecks = inspection.RequiredCodes(kind)
	r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, r.ReportChecks, "本次后台监控未取得此项数据；请查看对应采集缺口和实际采集时间")
	if r.FinishedAt.IsZero() {
		r.FinishedAt = time.Now()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = r.FinishedAt
	}
	redactCollection(r)
	setCollectionCoverage(r)
	return r, nil
}
func mergeMonitoringResult(dst, src *opsprobe.JobResult) {
	start := src.StartedAt
	if start.IsZero() {
		start = src.FinishedAt
	}
	for _, o := range src.ReportChecks {
		if !o.CollectedAt.IsZero() && (start.IsZero() || o.CollectedAt.Before(start)) {
			start = o.CollectedAt
		}
	}
	if !start.IsZero() && (dst.StartedAt.IsZero() || start.Before(dst.StartedAt)) {
		dst.StartedAt = start
	}
	if src.Metrics != nil {
		dst.Metrics = src.Metrics
		dst.Hostname = src.Metrics.Hostname
	}
	if src.Database != nil {
		dst.Database = src.Database
	}
	if len(src.Checks) > 0 {
		dst.Checks = src.Checks
	}
	if src.FinishedAt.After(dst.FinishedAt) {
		dst.FinishedAt = src.FinishedAt
	}
	dst.RelatedHosts = append(dst.RelatedHosts, src.RelatedHosts...)
	for _, o := range src.ReportChecks {
		key := func(o inspection.Observation) string {
			if o.Origin != nil {
				return fmt.Sprintf("%s/%s/%d", o.Code, o.Origin.Type, o.Origin.ID)
			}
			return o.Code
		}
		found := false
		for i, old := range dst.ReportChecks {
			if key(old) == key(o) {
				o.Executions = append(old.Executions, o.Executions...)
				dst.ReportChecks[i] = o
				found = true
				break
			}
		}
		if !found {
			dst.ReportChecks = append(dst.ReportChecks, o)
		}
	}
}
func (s *OperationsService) ensureMonitoringLane(ctx context.Context, t monitoringTarget, lane string) (MonitoringLane, error) {
	state, err := s.ensureMonitoringState(ctx, t, lane)
	if err != nil {
		return MonitoringLane{}, err
	}
	view := monitoringLane(state, t)
	maxAge := time.Duration(laneInterval(t, lane)) * time.Second
	if lane == "live" {
		maxAge *= 3
	}
	needs := view.Result == nil || view.LastError != "" || view.CollectedAt == nil || time.Since(*view.CollectedAt) > maxAge
	if !needs {
		return view, nil
	}
	// A recent failed attempt already describes this gap. Do not hammer an
	// unavailable target when several users generate reports simultaneously.
	if state.LastAttemptAt != nil && state.LastError != "" && time.Since(*state.LastAttemptAt) < time.Duration(min(state.IntervalSeconds, 60))*time.Second {
		return view, nil
	}
	if !view.Collecting {
		if err := s.db.Model(&state).Where("lease_until IS NULL OR lease_until<?", time.Now()).Update("next_attempt_at", time.Now()).Error; err != nil {
			return view, err
		}
		if s.claimMonitoring(ctx, &state) {
			s.runMonitoring(ctx, state)
		}
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := s.db.WithContext(ctx).First(&state, state.ID).Error; err != nil {
			return view, err
		}
		view = monitoringLane(state, t)
		if !view.Collecting {
			return view, nil
		}
		select {
		case <-ctx.Done():
			return view, fmt.Errorf("等待后台采集超时：%w", ctx.Err())
		case <-tick.C:
		}
	}
}
func (s *OperationsService) monitoringHostForReport(ctx context.Context, t monitoringTarget) (*opsprobe.JobResult, error) {
	view, err := s.monitoringView(ctx, t, true)
	if err != nil {
		return nil, err
	}
	if view.Result != nil && !view.Stale && view.LastError == "" {
		return view.Result, nil
	}
	if err := s.CollectHostAsync(ctx, t.ID); err != nil {
		return nil, err
	}
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		view, err = s.monitoringView(ctx, t, false)
		if err != nil {
			return nil, err
		}
		if !view.Collecting {
			if view.LastError != "" {
				return nil, fmt.Errorf("主机指标采集失败：%s", view.LastError)
			}
			if view.Result != nil && !view.Stale {
				return view.Result, nil
			}
			return nil, fmt.Errorf("未取得当前 node_exporter 指标")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}
func (s *OperationsService) executeMonitoringReportTarget(ctx context.Context, ref *model.InspectionTargetRun) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				var requested bool
				if s.db.WithContext(ctx).Model(&model.InspectionRun{}).Where("id=?", ref.RunID).Select("cancel_requested").Scan(&requested).Error == nil && requested {
					cancel()
					return
				}
			}
		}
	}()
	t, err := s.findMonitoringTarget(ctx, ref.Type, ref.TargetID)
	if err != nil {
		s.finishInspectionTarget(ref, "FAILED", err.Error(), nil)
		return
	}
	if t.Revision != ref.ConfigRevision {
		s.finishInspectionTarget(ref, "FAILED", "连接已修改，请重新生成报告", nil)
		return
	}
	if t.Type == "host" {
		var h model.Server
		if s.db.First(&h, t.ID).Error != nil || serverEndpoint(h) != ref.ServerEndpoint {
			s.finishInspectionTarget(ref, "FAILED", "主机连接已修改，请重新生成报告", nil)
			return
		}
	}
	now := time.Now()
	s.db.Model(ref).Updates(map[string]any{"status": "RUNNING", "stage": "汇总后台监控数据", "started_at": now, "heartbeat_at": now})
	result, err := s.monitoringReportResult(ctx, t)
	if err != nil {
		s.finishInspectionTarget(ref, "FAILED", err.Error(), nil)
		return
	}
	current, err := s.findMonitoringTarget(ctx, ref.Type, ref.TargetID)
	if err != nil || current.Digest != t.Digest || current.FactsDigest != t.FactsDigest {
		s.finishInspectionTarget(ref, "FAILED", "汇总期间连接已变化，请重新生成报告", nil)
		return
	}
	var canceled bool
	s.db.Model(&model.InspectionRun{}).Where("id=?", ref.RunID).Select("cancel_requested").Scan(&canceled)
	if canceled {
		s.finishInspectionTarget(ref, "CANCELED", "报告任务已取消", result)
		return
	}
	if ref.Type != "host" {
		_ = s.rememberCollection(ref.Type, ref.TargetID, ref.ConfigRevision, result)
	}
	s.finishInspectionTarget(ref, "COMPLETED", "已冻结后台监控数据；各项目保留实际采集时间", result)
}

// Clone before merging: a report must not mutate a monitoring page's snapshot.
func cloneMonitoringResult(r *opsprobe.JobResult) *opsprobe.JobResult {
	if r == nil {
		return nil
	}
	var out opsprobe.JobResult
	_ = json.Unmarshal([]byte(mustJSON(r)), &out)
	return &out
}

func (s *OperationsService) overlayMonitoringHostMetrics(ctx context.Context, r *opsprobe.JobResult) {
	for i := range r.RelatedHosts {
		h := &r.RelatedHosts[i]
		var server model.Server
		cfg, m, err := s.HostMonitor(h.ServerID)
		usable := err == nil && cfg.Enabled && cfg.LastError == "" && m != nil && time.Since(m.CollectedAt) <= time.Duration(max(90, cfg.IntervalSeconds*3))*time.Second
		if s.db.WithContext(ctx).First(&server, h.ServerID).Error != nil || serverEndpoint(server) != h.Endpoint {
			usable = false
		}
		if h.Result != nil {
			if usable {
				h.Result.Metrics = m
				h.Result.Checks = opsprobe.HostChecks(m)
			} else {
				h.Result.Metrics = nil
			}
		}
		for j, o := range r.ReportChecks {
			if o.Origin == nil || o.Origin.Type != "host" || o.Origin.ID != h.ServerID {
				continue
			}
			if o.Code != "cloudhis-6" && o.Code != "cloudhis-15" && o.Code != "cloudhis-36" && o.Code != "cloudhis-37" {
				continue
			}
			if usable {
				fresh := &opsprobe.JobResult{}
				appendRelatedHost(fresh, server, &opsprobe.JobResult{Metrics: m, Checks: opsprobe.HostChecks(m)}, []string{o.Code})
				if len(fresh.ReportChecks) > 0 {
					r.ReportChecks[j] = fresh.ReportChecks[0]
				}
			} else {
				r.ReportChecks[j].Status = "UNKNOWN"
				r.ReportChecks[j].Value = "未取得当前主机监控数据"
				r.ReportChecks[j].Tables = nil
				r.ReportChecks[j].Reason = cfg.LastError
				if r.ReportChecks[j].Reason == "" {
					r.ReportChecks[j].Reason = "node_exporter 数据缺失、过期、已停用或主机连接发生变化"
				}
			}
		}
	}
}

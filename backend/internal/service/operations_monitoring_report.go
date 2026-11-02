package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
)

// Reports read existing platform snapshots only. Missing, delayed or failed
// collectors never cause report generation to run or wait for collection.
func (s *OperationsService) monitoringReportResult(ctx context.Context, t monitoringTarget) (*opsprobe.JobResult, error) {
	kind := t.Kind
	if t.Type == "database" {
		kind = "database"
	}
	if t.Type == "host" {
		kind = "host"
	}
	r := &opsprobe.JobResult{Type: kind, Name: t.Name, Version: opsprobe.Version}
	view, err := s.monitoringView(ctx, t, true)
	if err != nil {
		return nil, err
	}
	lanes := map[string]MonitoringLane{}
	for _, lane := range view.Lanes {
		lanes[lane.Lane] = lane
	}
	for _, name := range []string{"deep", "facts", "live"} {
		lane, ok := lanes[name]
		if !ok {
			continue
		}
		part := cloneMonitoringResult(lane.Result)
		if part == nil {
			reason := "尚无已保存的监控数据"
			if lane.LastError != "" {
				reason = reportDataReason(lane.LastError)
			}
			if !t.Enabled {
				reason = "后台监控已停用；" + reason
			}
			part = &opsprobe.JobResult{ReportChecks: inspection.EnsureResults(monitoringLaneCodes(t, name), nil, reason)}
		} else {
			for i := range part.ReportChecks {
				o := &part.ReportChecks[i]
				if o.Attributes == nil {
					o.Attributes = map[string]string{}
				}
				if lane.Stale {
					o.Attributes["delayed"] = "true"
				}
				if lane.LastError != "" {
					o.Attributes["lastCollectionError"] = reportDataReason(lane.LastError)
				}
			}
			// Existing values remain facts at their original collection time. A later
			// failure must not replace successful CPU/memory/filesystem results.
			s.overlayMonitoringHostMetrics(ctx, part)
		}
		part.ReportChecks = inspection.EnsureResults(monitoringLaneCodes(t, name), part.ReportChecks, "尚无已保存的本项数据")
		mergeMonitoringResult(r, part)
	}
	r.ExpectedChecks = inspection.RequiredCodes(kind)
	r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, r.ReportChecks, "尚无已保存的本项数据")
	applyMonitoringFacts(r)
	redactCollection(r)
	setCollectionCoverage(r)
	return r, nil
}

func reportDataReason(reason string) string {
	if strings.Contains(reason, "context deadline exceeded") || strings.Contains(reason, "等待后台采集超时") {
		return "后台采集未在时限内完成；此项尚无可用结果"
	}
	return inspection.Preview(reason, 500)
}
func mergeMonitoringResult(dst, src *opsprobe.JobResult) {
	start := src.StartedAt
	finish := src.FinishedAt
	if start.IsZero() {
		start = src.FinishedAt
	}
	for _, o := range src.ReportChecks {
		if o.CollectedAt.After(finish) {
			finish = o.CollectedAt
		}
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
	if finish.After(dst.FinishedAt) {
		dst.FinishedAt = finish
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
		var latest model.MonitoringState
		if err := s.db.WithContext(ctx).First(&latest, state.ID).Error; err != nil {
			return view, err
		}
		// GORM does not clear an existing *time.Time when a SQL NULL is read
		// into the same struct. A fresh value releases a completed lease.
		state = latest
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
		cfg, m, err := s.hostMonitor(ctx, h.ServerID, false)
		usable := err == nil && m != nil
		if s.db.WithContext(ctx).First(&server, h.ServerID).Error != nil || serverEndpoint(server) != h.Endpoint {
			usable = false
		}
		if h.Result != nil {
			if usable {
				h.Result.Metrics = m
				h.Result.Checks = opsprobe.HostChecks(m)
			} else if h.Result.Metrics != nil && time.Since(h.Result.Metrics.CollectedAt) >= 48*time.Hour {
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

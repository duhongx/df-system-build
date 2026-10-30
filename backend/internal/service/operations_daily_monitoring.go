package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type monitoringTarget struct {
	Type, Kind, Name, Endpoint, Digest, FactsDigest string
	ID, Revision                                    uint
	Enabled                                         bool
	Interval                                        int
}

type MonitoringLane struct {
	Lane            string              `json:"lane"`
	IntervalSeconds int                 `json:"intervalSeconds"`
	CollectedAt     *time.Time          `json:"collectedAt"`
	LastAttemptAt   *time.Time          `json:"lastAttemptAt"`
	LastError       string              `json:"lastError"`
	Stale           bool                `json:"stale"`
	Collecting      bool                `json:"collecting"`
	Result          *opsprobe.JobResult `json:"result,omitempty"`
}
type MonitoringView struct {
	TargetType string             `json:"targetType"`
	ID         uint               `json:"id"`
	Kind       string             `json:"kind"`
	Name       string             `json:"name"`
	Endpoint   string             `json:"endpoint"`
	Enabled    bool               `json:"enabled"`
	Status     string             `json:"status"`
	Summary    string             `json:"summary"`
	Indicators map[string]float64 `json:"indicators"`
	MonitoringLane
	Lanes []MonitoringLane `json:"lanes,omitempty"`
}
type MonitoringPoint struct {
	CollectedAt time.Time          `json:"collectedAt"`
	Indicators  map[string]float64 `json:"indicators"`
}

func monitorDigest(v any) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(mustJSON(v)))) }

func (s *OperationsService) monitoringTargets(ctx context.Context) ([]monitoringTarget, error) {
	var hosts []model.Server
	var databases []model.OperationsDatabase
	var components []model.ComponentConnection
	if err := s.db.WithContext(ctx).Order("id").Find(&hosts).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Order("id").Find(&databases).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Order("id").Find(&components).Error; err != nil {
		return nil, err
	}
	hostKeys := map[uint]string{}
	for _, h := range hosts {
		hostKeys[h.ID] = monitorDigest([]any{h.Host, h.Port, h.Username, h.AuthType, h.CredentialEncrypted, h.CertPassphrase, h.ConnTimeout})
	}
	// Changes to host bindings or another local instance invalidate host-wide
	// evidence as well, without invalidating unrelated live metric samples.
	bindings := []any{}
	for _, d := range databases {
		bindings = append(bindings, []any{d.ID, d.Revision, d.Host, d.Port, d.ServerID, d.BackupServerID, d.BackupDirectory, d.ArchiveDirectory})
	}
	for _, c := range components {
		bindings = append(bindings, []any{c.ID, c.Revision, c.Enabled, c.ServerID})
	}
	factsDigest := monitorDigest([]any{"host-bindings-v2", hostKeys, bindings})
	var configs []model.HostMonitorConfig
	if err := s.db.WithContext(ctx).Find(&configs).Error; err != nil {
		return nil, err
	}
	configMap := map[uint]model.HostMonitorConfig{}
	for _, cfg := range configs {
		configMap[cfg.ServerID] = cfg
	}
	var out []monitoringTarget
	for _, h := range hosts {
		cfg, exists := configMap[h.ID]
		if !exists {
			cfg = model.HostMonitorConfig{ServerID: h.ID, IntervalSeconds: 60, Enabled: true}
			if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&cfg).Error; err != nil {
				return nil, err
			}
			if err := s.db.WithContext(ctx).First(&cfg, h.ID).Error; err != nil {
				return nil, err
			}
		}
		name := h.Remark
		if name == "" {
			name = h.Host
		}
		digest := monitorDigest([]any{hostKeys[h.ID], cfg.MetricsURL})
		out = append(out, monitoringTarget{Type: "host", Kind: "hosts", ID: h.ID, Name: name, Endpoint: h.Host, Enabled: cfg.Enabled, Interval: cfg.IntervalSeconds, Digest: digest, FactsDigest: monitorDigest([]string{digest, factsDigest})})
	}
	for _, d := range databases {
		digest := monitorDigest([]any{d.Revision, d.Source, d.EnvironmentDigest, d.Host, d.Port, d.Database, d.Username, d.PasswordEncrypted, d.SSLMode})
		out = append(out, monitoringTarget{Type: "database", Kind: "postgresql", ID: d.ID, Name: d.Name, Endpoint: fmt.Sprintf("%s:%d/%s", d.Host, d.Port, d.Database), Revision: d.Revision, Enabled: true, Interval: 30, Digest: digest, FactsDigest: monitorDigest([]string{digest, factsDigest})})
	}
	for _, c := range components {
		digest := monitorDigest([]any{c.Revision, c.EnvironmentDigest, c.SecretEncrypted, c.Endpoint, c.Username, c.OptionsJSON})
		if c.Kind == "minio" {
			digest = monitorDigest([]string{digest, hostKeys[c.ServerID]})
		}
		interval := 30
		if c.Kind == "minio" || c.Kind == "elasticsearch" {
			interval = 60
		}
		endpoint := c.Endpoint
		if c.Source == "environment" {
			endpoint = "环境配置"
		}
		if endpoint == "" {
			endpoint = "主机 mc 别名"
		}
		out = append(out, monitoringTarget{Type: "component", Kind: c.Kind, ID: c.ID, Name: c.Name, Endpoint: endpoint, Revision: c.Revision, Enabled: c.Enabled, Interval: interval, Digest: digest, FactsDigest: monitorDigest([]string{digest, factsDigest})})
	}
	return out, nil
}

func targetLanes(t monitoringTarget) []string {
	if t.Type == "host" {
		return []string{"facts"}
	}
	if t.Type == "database" {
		return []string{"live", "facts", "deep"}
	}
	if t.Kind == "kubernetes" || t.Kind == "minio" {
		return []string{"live", "facts"}
	}
	return []string{"live"}
}
func laneInterval(t monitoringTarget, lane string) int {
	if lane == "facts" {
		return 900
	}
	if lane == "deep" {
		return 21600
	}
	return t.Interval
}
func laneDigest(t monitoringTarget, lane string) string {
	if lane == "facts" {
		return t.FactsDigest
	}
	return t.Digest
}

func monitoringLaneCodes(t monitoringTarget, lane string) []string {
	if t.Type == "host" {
		if lane == "live" {
			return []string{"host_identity", "host_cpu", "host_memory", "host_filesystems"}
		}
		return []string{"host-discovery"}
	}
	if t.Type == "database" {
		if lane == "live" {
			return []string{"cloudhis-2", "cloudhis-16", "postgresql-archive", "cloudhis-27", "cloudhis-28", "cloudhis-30", "cloudhis-33", "postgresql-slots", "postgresql-table-health", "postgresql-slow-history"}
		}
		if lane == "facts" {
			return []string{"cloudhis-4", "cloudhis-15", "cloudhis-17", "cloudhis-18", "cloudhis-19"}
		}
		out := []string{}
		for n := 20; n <= 35; n++ {
			out = append(out, fmt.Sprintf("cloudhis-%d", n))
		}
		return out
	}
	if t.Kind == "kubernetes" {
		if lane == "live" {
			return []string{"cloudhis-1", "cloudhis-7", "cloudhis-8", "cloudhis-9", "cloudhis-10"}
		}
		return []string{"cloudhis-6", "cloudhis-11", "cloudhis-12", "cloudhis-13", "cloudhis-14"}
	}
	if t.Kind == "minio" {
		if lane == "live" {
			return []string{"cloudhis-3", "cloudhis-38"}
		}
		return []string{"cloudhis-5", "cloudhis-36", "cloudhis-37", "cloudhis-39"}
	}
	return inspection.RequiredCodes(t.Kind)
}

func (s *OperationsService) syncMonitoringSources(ctx context.Context) error {
	if err := s.EnsureInspectionSources(ctx); err != nil {
		return err
	}
	if _, err := s.SyncEnvironmentDatabase(ctx, true, ""); err != nil {
		// Incomplete environment settings do not prevent independently
		// registered connections and hosts from being monitored.
		if _, e := s.SyncEnvironmentDatabase(ctx, false, ""); e != nil {
			return e
		}
	}
	targets, err := s.monitoringTargets(ctx)
	if err != nil {
		return err
	}
	ids := []uint{}
	for _, t := range targets {
		for _, lane := range targetLanes(t) {
			state, err := s.ensureMonitoringState(ctx, t, lane)
			if err != nil {
				return err
			}
			ids = append(ids, state.ID)
		}
	}
	q := s.db.WithContext(ctx).Model(&model.MonitoringState{})
	if len(ids) > 0 {
		q = q.Where("id NOT IN ?", ids)
	} else {
		q = q.Where("1=1")
	}
	return q.Update("enabled", false).Error
}
func (s *OperationsService) ensureMonitoringState(ctx context.Context, t monitoringTarget, lane string) (model.MonitoringState, error) {
	state := model.MonitoringState{TargetType: t.Type, TargetID: t.ID, Lane: lane, SourceDigest: laneDigest(t, lane), Revision: t.Revision, Enabled: t.Enabled, IntervalSeconds: laneInterval(t, lane), NextAttemptAt: time.Now()}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&state).Error; err != nil {
		return state, err
	}
	if err := s.db.WithContext(ctx).Where("target_type=? AND target_id=? AND lane=?", t.Type, t.ID, lane).First(&state).Error; err != nil {
		return state, err
	}
	updates := map[string]any{"enabled": t.Enabled, "interval_seconds": laneInterval(t, lane)}
	if state.SourceDigest != laneDigest(t, lane) {
		updates["source_digest"] = laneDigest(t, lane)
		updates["revision"] = t.Revision
		updates["result_json"] = nil
		updates["last_success_at"] = nil
		updates["last_attempt_at"] = nil
		updates["lease_until"] = nil
		updates["lease_token"] = ""
		updates["last_error"] = "连接已修改，等待自动采集"
		updates["next_attempt_at"] = time.Now()
		updates["failures"] = 0
	}
	err := s.db.WithContext(ctx).Model(&state).Updates(updates).Error
	if err == nil {
		err = s.db.WithContext(ctx).First(&state, state.ID).Error
	}
	return state, err
}
func (s *OperationsService) monitorSlotsFor(lane string) chan struct{} {
	if lane == "facts" {
		return s.factsSlots
	}
	if lane == "deep" {
		return s.deepSlots
	}
	return s.monitorSlots
}
func (s *OperationsService) claimMonitoring(ctx context.Context, state *model.MonitoringState) bool {
	now := time.Now()
	token := fmt.Sprintf("%s-%d", s.owner, now.UnixNano())
	r := s.db.WithContext(ctx).Model(state).Where("enabled=true AND source_digest=? AND (lease_until IS NULL OR lease_until<?) AND next_attempt_at<=?", state.SourceDigest, now, now).Updates(map[string]any{"lease_token": token, "lease_until": now.Add(4 * time.Minute), "last_attempt_at": now})
	if r.Error != nil || r.RowsAffected != 1 {
		return false
	}
	state.LeaseToken = token
	state.LastAttemptAt = &now
	return true
}
func (s *OperationsService) scheduleMonitoring(ctx context.Context) {
	for _, lane := range []string{"live", "facts", "deep"} {
		slots := s.monitorSlotsFor(lane)
		if len(slots) == cap(slots) {
			continue
		}
		var states []model.MonitoringState
		if s.db.WithContext(ctx).Where("lane=? AND enabled=true AND next_attempt_at<=? AND (lease_until IS NULL OR lease_until<?)", lane, time.Now(), time.Now()).Order("next_attempt_at,id").Limit(20).Find(&states).Error != nil {
			continue
		}
		for _, state := range states {
			select {
			case slots <- struct{}{}:
			default:
				continue
			}
			if !s.claimMonitoring(ctx, &state) {
				<-slots
				continue
			}
			go func(state model.MonitoringState) { defer func() { <-slots }(); s.runMonitoring(ctx, state) }(state)
		}
	}
}
func (s *OperationsService) findMonitoringTarget(ctx context.Context, kind string, id uint) (monitoringTarget, error) {
	targets, err := s.monitoringTargets(ctx)
	if err != nil {
		return monitoringTarget{}, err
	}
	for _, t := range targets {
		if t.Type == kind && t.ID == id {
			return t, nil
		}
	}
	return monitoringTarget{}, fmt.Errorf("监控对象不存在")
}
func (s *OperationsService) runMonitoring(parent context.Context, state model.MonitoringState) {
	timeout := 180 * time.Second
	if state.Lane == "live" {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	t, err := s.findMonitoringTarget(ctx, state.TargetType, state.TargetID)
	var result *opsprobe.JobResult
	if err == nil && t.Enabled && laneDigest(t, state.Lane) == state.SourceDigest {
		result, err = s.collectMonitoring(ctx, t, state.Lane)
	} else if err == nil {
		err = fmt.Errorf("连接已变化或监控已停用")
	}
	if err != nil {
		result = &opsprobe.JobResult{Type: t.Kind, Name: t.Name, FinishedAt: time.Now(), Error: err.Error()}
	}
	if result == nil {
		result = &opsprobe.JobResult{FinishedAt: time.Now(), Error: "采集未返回结果"}
	}
	redactCollection(result)
	valid := result.Error == "" && (result.Metrics != nil || result.Database != nil)
	errors := []string{}
	if result.Error != "" {
		errors = append(errors, result.Error)
	}
	for _, o := range result.ReportChecks {
		if o.Status != "UNKNOWN" && o.Status != "NOT_RUN" {
			valid = true
		} else {
			errors = append(errors, o.Name+"："+o.Reason)
		}
	}
	for _, c := range result.Checks {
		if c.Status == "UNKNOWN" {
			errors = append(errors, c.Name+"："+c.Error)
		}
	}
	message := strings.Join(errors, "；")
	if len(message) > 4096 {
		message = message[:4096]
	}
	now := time.Now()
	failures := 0
	delay := state.IntervalSeconds
	if !valid {
		failures = state.Failures + 1
		delay = min(300, state.IntervalSeconds*(1<<min(failures, 3)))
		if message == "" {
			message = "未取得有效监控数据"
		}
	}
	updates := map[string]any{"lease_until": nil, "lease_token": "", "last_error": message, "failures": failures, "next_attempt_at": now.Add(time.Duration(delay) * time.Second)}
	if valid {
		setCollectionCoverage(result)
		updates["result_json"] = mustJSON(result)
		updates["last_success_at"] = result.FinishedAt
	}
	// Recheck actual configuration, not only the periodically refreshed state.
	current, e := s.findMonitoringTarget(context.WithoutCancel(ctx), state.TargetType, state.TargetID)
	if e != nil || !current.Enabled || laneDigest(current, state.Lane) != state.SourceDigest {
		updates = map[string]any{"lease_until": nil, "lease_token": "", "next_attempt_at": now.Add(30 * time.Second)}
	}
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&model.MonitoringState{}).Where("id=? AND lease_token=? AND source_digest=?", state.ID, state.LeaseToken, state.SourceDigest).Updates(updates)
		if r.Error != nil || r.RowsAffected != 1 {
			return r.Error
		}
		if _, ok := updates["result_json"]; ok && state.Lane == "live" {
			compact := opsprobe.JobResult{Type: result.Type, FinishedAt: result.FinishedAt, Indicators: result.Indicators}
			return tx.Create(&model.CollectionSample{TargetType: state.TargetType, TargetID: state.TargetID, Revision: state.Revision, Lane: "live", SourceDigest: state.SourceDigest, CollectedAt: result.FinishedAt, ResultJSON: mustJSON(compact)}).Error
		}
		return nil
	})
}

func monitoringLane(state model.MonitoringState, t monitoringTarget) MonitoringLane {
	v := MonitoringLane{Lane: state.Lane, IntervalSeconds: laneInterval(t, state.Lane), LastAttemptAt: state.LastAttemptAt, LastError: state.LastError, Collecting: state.LeaseUntil != nil && state.LeaseUntil.After(time.Now()), Stale: true}
	if state.ID == 0 {
		return v
	}
	if state.SourceDigest != laneDigest(t, state.Lane) {
		v.LastError = "连接已修改，等待自动采集"
		v.Collecting = false
		return v
	}
	if state.LastSuccessAt != nil && time.Since(*state.LastSuccessAt) < 48*time.Hour && state.ResultJSON != "" {
		var result opsprobe.JobResult
		if json.Unmarshal([]byte(state.ResultJSON), &result) == nil {
			v.Result = &result
			v.CollectedAt = state.LastSuccessAt
			factor := 3
			if state.Lane != "live" {
				factor = 2
			}
			v.Stale = time.Since(*state.LastSuccessAt) > time.Duration(max(90, v.IntervalSeconds*factor))*time.Second
		}
	}
	return v
}

func (s *OperationsService) monitoringCurrentView(ctx context.Context, t monitoringTarget) (MonitoringView, error) {
	v := MonitoringView{TargetType: t.Type, ID: t.ID, Kind: t.Kind, Name: t.Name, Endpoint: t.Endpoint, Enabled: t.Enabled, Status: "PENDING", Indicators: map[string]float64{}, Lanes: []MonitoringLane{}}
	if t.Type == "host" {
		cfg, m, err := s.HostMonitor(t.ID)
		if err != nil {
			return v, err
		}
		v.MonitoringLane = MonitoringLane{Lane: "live", IntervalSeconds: cfg.IntervalSeconds, LastAttemptAt: cfg.LastAttemptAt, LastError: cfg.LastError, Stale: true}
		s.mu.Lock()
		v.Collecting = s.collecting[t.ID]
		s.mu.Unlock()
		if m != nil {
			r := &opsprobe.JobResult{Type: "host", Name: t.Name, Metrics: m, Checks: opsprobe.HostChecks(m), FinishedAt: m.CollectedAt}
			opsprobe.FinalizeInspection(r, "host")
			filtered := r.ReportChecks[:0]
			for _, o := range r.ReportChecks {
				if o.Code != "host-discovery" {
					filtered = append(filtered, o)
				}
			}
			r.ReportChecks = filtered
			r.ExpectedChecks = monitoringLaneCodes(t, "live")
			v.Result = r
			v.CollectedAt = &m.CollectedAt
			v.Stale = time.Since(m.CollectedAt) > time.Duration(max(90, cfg.IntervalSeconds*3))*time.Second
			v.Indicators = map[string]float64{"cpuPercent": m.CPUUsagePercent, "memoryPercent": m.MemUsagePercent, "load1": m.Load1}
			v.Summary = fmt.Sprintf("CPU %.1f%% · 内存 %.1f%% · 负载 %.2f", m.CPUUsagePercent, m.MemUsagePercent, m.Load1)
		}
	} else {
		var state model.MonitoringState
		err := s.db.WithContext(ctx).Where("target_type=? AND target_id=? AND lane='live'", t.Type, t.ID).First(&state).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return v, err
		}
		state.Lane = "live"
		v.MonitoringLane = monitoringLane(state, t)
		if v.Result != nil {
			v.Indicators = v.Result.Indicators
			var parts []string
			for _, o := range v.Result.ReportChecks {
				if o.Value != "" && len(parts) < 2 {
					parts = append(parts, o.Value)
				}
			}
			v.Summary = strings.Join(parts, "；")
		}
	}
	if v.Result != nil {
		v.Status = "NORMAL"
		for _, o := range v.Result.ReportChecks {
			if o.Status == "CRITICAL" {
				v.Status = "CRITICAL"
			} else if o.Status == "WARNING" && v.Status != "CRITICAL" {
				v.Status = "WARNING"
			}
		}
		for _, c := range v.Result.Checks {
			if c.Status == "CRITICAL" {
				v.Status = "CRITICAL"
			} else if c.Status == "WARNING" && v.Status != "CRITICAL" {
				v.Status = "WARNING"
			}
		}
		if t.Type == "host" {
			for _, c := range v.Result.Checks {
				if c.Status == "UNKNOWN" && v.Status == "NORMAL" {
					v.Status = "PARTIAL"
				}
			}
		}
	}
	if v.Stale && v.Result != nil {
		v.Status = "STALE"
	}
	if v.LastError != "" {
		v.Status = "ERROR"
		if v.CollectedAt != nil && v.LastAttemptAt != nil && !v.CollectedAt.Before(*v.LastAttemptAt) {
			v.Status = "PARTIAL"
		}
	}
	if !t.Enabled {
		v.Status = "DISABLED"
	}
	return v, nil
}

func (s *OperationsService) monitoringView(ctx context.Context, t monitoringTarget, detail bool) (MonitoringView, error) {
	v, err := s.monitoringCurrentView(ctx, t)
	if err != nil {
		return v, err
	}
	if detail {
		v.Lanes = append(v.Lanes, v.MonitoringLane)
		for _, lane := range targetLanes(t) {
			if lane == "live" {
				continue
			}
			var state model.MonitoringState
			err := s.db.WithContext(ctx).Where("target_type=? AND target_id=? AND lane=?", t.Type, t.ID, lane).First(&state).Error
			if err != nil && err != gorm.ErrRecordNotFound {
				return v, err
			}
			state.Lane = lane
			v.Lanes = append(v.Lanes, monitoringLane(state, t))
		}
		for i := range v.Lanes {
			if v.Lanes[i].Result != nil {
				s.overlayMonitoringHostMetrics(ctx, v.Lanes[i].Result)
			}
		}
	} else {
		v.Result = nil
	}
	return v, nil
}
func (s *OperationsService) MonitoringOverview(ctx context.Context) ([]MonitoringView, error) {
	targets, err := s.monitoringTargets(ctx)
	if err != nil {
		return nil, err
	}
	out := []MonitoringView{}
	for _, t := range targets {
		v, err := s.monitoringView(ctx, t, false)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *OperationsService) MonitoringDetail(ctx context.Context, kind string, id uint) (MonitoringView, error) {
	t, err := s.findMonitoringTarget(ctx, kind, id)
	if err != nil {
		return MonitoringView{}, err
	}
	return s.monitoringView(ctx, t, true)
}

// MonitoringDashboard returns current display data only. Report evidence remains
// in the original snapshots. Only bounded diagnostic tables and transformed
// bloat estimates reach the page; GET never starts collection or a deep scan.
func (s *OperationsService) MonitoringDashboard(ctx context.Context, kind string, id uint) (MonitoringView, error) {
	t, err := s.findMonitoringTarget(ctx, kind, id)
	if err != nil {
		return MonitoringView{}, err
	}
	v, err := s.monitoringCurrentView(ctx, t)
	if err != nil || v.Result == nil {
		return v, err
	}
	r := v.Result
	out := &opsprobe.JobResult{Type: r.Type, Name: r.Name, Version: r.Version, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Indicators: r.Indicators}
	for _, check := range r.ReportChecks {
		display := inspection.Observation{Code: check.Code, Name: check.Name, Status: check.Status, Value: check.Value, Reason: check.Reason, CollectedAt: check.CollectedAt, Attributes: check.Attributes, Measurements: check.Measurements}
		if monitoringOperationalTable(check.Code) {
			display.Tables = check.Tables
		} else if check.Status == "WARNING" || check.Status == "CRITICAL" {
			for _, table := range check.Tables {
				if table.Title == "当前异常 Pod" {
					display.Tables = append(display.Tables, table)
				}
			}
		}
		out.ReportChecks = append(out.ReportChecks, display)
	}
	if t.Type == "database" {
		out.ReportChecks = append(out.ReportChecks, s.monitoringBloatSections(ctx, t)...)
	}
	v.Result = out
	return v, nil
}

func monitoringOperationalTable(code string) bool {
	switch code {
	case "postgresql-slots", "postgresql-table-health", "postgresql-archive", "postgresql-slow-history", "cloudhis-33", "redis-slowlog", "rabbitmq-queues":
		return true
	}
	return false
}
func (s *OperationsService) MonitoringHistory(ctx context.Context, kind string, id uint, hours int) ([]MonitoringPoint, int, error) {
	if hours < 1 || hours > 48 {
		return nil, 0, fmt.Errorf("历史范围应为 1～48 小时")
	}
	t, err := s.findMonitoringTarget(ctx, kind, id)
	if err != nil {
		return nil, 0, err
	}
	step := max(t.Interval, (hours*3600+599)/600)
	var rows []model.CollectionSample
	err = s.db.WithContext(ctx).Raw(`SELECT * FROM (SELECT DISTINCT ON (bucket) id,target_type,target_id,collected_at,result_json FROM (SELECT *,floor(extract(epoch FROM collected_at)/?) AS bucket FROM collection_samples WHERE target_type=? AND target_id=? AND lane='live' AND source_digest=? AND collected_at>=?) r ORDER BY bucket,collected_at DESC,id DESC) s ORDER BY collected_at`, step, kind, id, t.Digest, time.Now().Add(-time.Duration(hours)*time.Hour)).Scan(&rows).Error
	out := []MonitoringPoint{}
	for _, row := range rows {
		var r opsprobe.JobResult
		if json.Unmarshal([]byte(row.ResultJSON), &r) == nil {
			out = append(out, MonitoringPoint{row.CollectedAt, r.Indicators})
		}
	}
	return out, step, err
}

func (s *OperationsService) RefreshMonitoring(ctx context.Context, kind string, id uint) error {
	t, err := s.findMonitoringTarget(ctx, kind, id)
	if err != nil {
		return err
	}
	if !t.Enabled {
		return fmt.Errorf("该对象已停用后台监控")
	}
	if kind == "host" {
		return s.CollectHostAsync(ctx, id)
	}
	state, err := s.ensureMonitoringState(ctx, t, "live")
	if err != nil {
		return err
	}
	// Concurrent viewers share the same scheduled sample; manual refresh is
	// rate limited to the normal collection interval.
	if state.LastAttemptAt == nil || time.Since(*state.LastAttemptAt) >= time.Duration(t.Interval)*time.Second {
		return s.db.Model(&state).Update("next_attempt_at", time.Now()).Error
	}
	return nil
}

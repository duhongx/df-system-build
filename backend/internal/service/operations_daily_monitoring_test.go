package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/repository"
	"df-build-server/pkg/crypto"
	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"
)

func dailyFixture(t *testing.T) (*OperationsService, *gorm.DB) {
	t.Helper()
	db := operationsTestDB(t)
	old := repository.DB
	repository.DB = db
	t.Cleanup(func() { repository.DB = old })
	t.Setenv("DF_ENCRYPT_KEY", "configured-test-key-32-characters")
	return NewOperationsService(db, t.TempDir()), db
}
func dailyElastic(t *testing.T) (string, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	calls := &atomic.Int32{}
	fail := &atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "fixture offline", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/_cluster/health" {
			fmt.Fprint(w, `{"cluster_name":"daily-fixture","status":"green","number_of_nodes":1,"number_of_data_nodes":1,"active_shards":10,"unassigned_shards":0}`)
		} else {
			fmt.Fprint(w, `{"nodes":{"one":{"name":"fixture-node","fs":{"total":{"total_in_bytes":100,"available_in_bytes":90}},"jvm":{"mem":{"heap_used_percent":30}}}}}`)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, calls, fail
}
func waitDaily(t *testing.T, db *gorm.DB, id uint, predicate func(model.MonitoringState) bool) model.MonitoringState {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var state model.MonitoringState
		if err := db.Where("target_type='component' AND target_id=? AND lane='live'", id).First(&state).Error; err == nil && predicate(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatal("background collector did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDailyMonitoringReusesHostSamplesWithoutAnInspection(t *testing.T) {
	s, db := dailyFixture(t)
	host := model.Server{Host: "127.0.0.1", Username: "fixture", Port: 22}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	m := opsprobe.HostMetrics{Source: "node_exporter", SourceURL: "http://127.0.0.1:9100/metrics", CollectedAt: time.Now(), CPUUsagePercent: 12, MemUsagePercent: 34, Hostname: "shared"}
	if err := db.Create(&model.HostMetricSample{ServerID: host.ID, CollectedAt: m.CollectedAt, MetricsJSON: mustJSON(m)}).Error; err != nil {
		t.Fatal(err)
	}
	view, err := s.MonitoringDetail(context.Background(), "host", host.ID)
	if err != nil || view.Result == nil || view.Result.Metrics.CPUUsagePercent != 12 || !view.Enabled || view.Indicators["memoryPercent"] != 34 {
		t.Fatalf("existing host samples not reused: %+v %v", view, err)
	}
	_, original, err := s.HostMonitor(host.ID)
	if err != nil || !original.CollectedAt.Equal(view.Result.Metrics.CollectedAt) {
		t.Fatal("host entrances use different samples")
	}
	var count int64
	db.Model(&model.InspectionRun{}).Count(&count)
	if count != 0 {
		t.Fatal("viewing a monitor created a report")
	}
	db.Model(&model.CollectionSample{}).Count(&count)
	if count != 0 {
		t.Fatal("monitoring required inspection snapshots")
	}
	if _, err := s.ConfigureMonitor(host.ID, false, 60); err != nil {
		t.Fatal(err)
	}
	if err := s.syncMonitoringSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	view, _ = s.MonitoringDetail(context.Background(), "host", host.ID)
	if view.Enabled || view.Status != "DISABLED" {
		t.Fatal("explicitly disabled monitor was re-enabled")
	}
}

func TestDailyMonitoringCollectsWithoutAViewerAndAutomaticallyRecovers(t *testing.T) {
	s, db := dailyFixture(t)
	endpoint, calls, fail := dailyElastic(t)
	row, err := s.SaveComponent(context.Background(), ComponentRequest{Kind: "elasticsearch", Name: "daily", Endpoint: endpoint, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.syncMonitoringSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.scheduleMonitoring(context.Background())
	state := waitDaily(t, db, row.ID, func(v model.MonitoringState) bool { return v.LastSuccessAt != nil && v.LeaseToken == "" })
	if calls.Load() != 2 {
		t.Fatal("unexpected requests", calls.Load())
	}
	view, err := s.MonitoringDetail(context.Background(), "component", row.ID)
	if err != nil || view.Result == nil || view.Indicators["activeShards"] != 10 || view.Status != "NORMAL" {
		t.Fatalf("automatic collection not visible: %+v %v", view, err)
	}
	target, err := s.findMonitoringTarget(context.Background(), "component", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.monitoringReportResult(context.Background(), target)
	if err != nil || calls.Load() != 2 || len(report.ReportChecks) != 2 || !report.FinishedAt.Equal(*state.LastSuccessAt) {
		t.Fatalf("report recollected valid monitoring snapshot: %v", err)
	}
	history, _, err := s.MonitoringHistory(context.Background(), "component", row.ID, 48)
	if err != nil || len(history) != 1 || history[0].Indicators["activeShards"] != 10 {
		t.Fatal("monitor history missing", err, history)
	}
	var sample model.CollectionSample
	db.Where("lane='live'").First(&sample)
	if strings.Contains(sample.ResultJSON, "fixture-node") || strings.Contains(sample.ResultJSON, "reportChecks") && strings.Contains(sample.ResultJSON, "elasticsearch-health") {
		t.Fatal("time series stored complete node inventories")
	}
	fail.Store(true)
	db.Model(&state).Updates(map[string]any{"next_attempt_at": time.Now(), "last_attempt_at": time.Now().Add(-time.Minute)})
	s.scheduleMonitoring(context.Background())
	failed := waitDaily(t, db, row.ID, func(v model.MonitoringState) bool { return v.LastError != "" && v.LeaseToken == "" })
	view, _ = s.MonitoringDetail(context.Background(), "component", row.ID)
	if view.Status != "ERROR" || view.Result == nil || view.Indicators["activeShards"] != 10 || !failed.NextAttemptAt.After(time.Now()) {
		t.Fatal("failure erased or misrepresented previous values", view)
	}
	fail.Store(false)
	db.Model(&failed).Update("next_attempt_at", time.Now())
	s.scheduleMonitoring(context.Background())
	_ = waitDaily(t, db, row.ID, func(v model.MonitoringState) bool {
		return v.LastError == "" && v.LeaseToken == "" && v.LastSuccessAt.After(*state.LastSuccessAt)
	})
	view, _ = s.MonitoringDetail(context.Background(), "component", row.ID)
	if view.Status != "NORMAL" {
		t.Fatal("recovery still requires a manual check")
	}
	row, err = s.SaveComponent(context.Background(), ComponentRequest{ID: row.ID, Kind: row.Kind, Name: "changed", Endpoint: endpoint, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	view, _ = s.MonitoringDetail(context.Background(), "component", row.ID)
	if view.Result != nil {
		t.Fatal("changed connection reused old monitoring data")
	}
}

func TestDailyMonitoringLeaseIsSharedAcrossWorkers(t *testing.T) {
	s, db := dailyFixture(t)
	endpoint, _, _ := dailyElastic(t)
	row, err := s.SaveComponent(context.Background(), ComponentRequest{Kind: "elasticsearch", Name: "lease", Endpoint: endpoint, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := s.findMonitoringTarget(context.Background(), "component", row.ID)
	state, err := s.ensureMonitoringState(context.Background(), target, "live")
	if err != nil {
		t.Fatal(err)
	}
	other := NewOperationsService(db, t.TempDir())
	var count atomic.Int32
	var wg sync.WaitGroup
	for _, worker := range []*OperationsService{s, other} {
		wg.Add(1)
		go func(worker *OperationsService) {
			defer wg.Done()
			copy := state
			if worker.claimMonitoring(context.Background(), &copy) {
				count.Add(1)
			}
		}(worker)
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("duplicate collectors claimed the same target", count.Load())
	}
}

func TestDailyMonitoringDiscardResultsIfConnectionChangesInFlight(t *testing.T) {
	s, db := dailyFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		http.Error(w, "old connection", 503)
	}))
	defer server.Close()
	row, err := s.SaveComponent(context.Background(), ComponentRequest{Kind: "elasticsearch", Name: "old", Endpoint: server.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.syncMonitoringSources(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.scheduleMonitoring(context.Background())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not start")
	}
	_, err = s.SaveComponent(context.Background(), ComponentRequest{ID: row.ID, Kind: row.Kind, Name: "new", Endpoint: "http://127.0.0.1:1", Enabled: true})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	state := waitDaily(t, db, row.ID, func(v model.MonitoringState) bool { return v.LeaseToken == "" })
	if state.LastSuccessAt != nil || state.ResultJSON != "" {
		t.Fatal("old target's response was saved")
	}
}

func TestDailyMonitoringRetentionKeepsFrozenReports(t *testing.T) {
	s, db := dailyFixture(t)
	old := time.Now().Add(-49 * time.Hour)
	state := model.MonitoringState{TargetType: "component", TargetID: 1, Lane: "live", LastSuccessAt: &old, ResultJSON: `{"name":"expired"}`}
	if err := db.Create(&state).Error; err != nil {
		t.Fatal(err)
	}
	db.Create(&model.CollectionSample{TargetType: "component", TargetID: 1, Lane: "live", CollectedAt: old, ResultJSON: `{}`})
	run := model.InspectionRun{Name: "frozen", ReportSnapshotJSON: `{"mode":"configured"}`}
	db.Create(&run)
	s.cleanupOperations(context.Background())
	db.First(&state, state.ID)
	if state.ResultJSON != "" { // SQL NULL scans as the field's zero value on a fresh struct.
		var fresh model.MonitoringState
		db.First(&fresh, state.ID)
		if fresh.ResultJSON != "" {
			t.Fatal("expired latest snapshot was retained")
		}
	}
	var count int64
	db.Model(&model.CollectionSample{}).Count(&count)
	if count != 0 {
		t.Fatal("expired metrics retained")
	}
	db.First(&run, run.ID)
	var frozen map[string]any
	if json.Unmarshal([]byte(run.ReportSnapshotJSON), &frozen) != nil || frozen["mode"] != "configured" {
		t.Fatal("cleanup deleted the report")
	}
}

func TestDailyMonitoringPostgreSQLLiveDoesNotRunGuideSQLOrSSH(t *testing.T) {
	s, db := dailyFixture(t)
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	password, err := crypto.Encrypt(cfg.Password)
	if err != nil {
		t.Fatal(err)
	}
	host := model.Server{Host: "127.0.0.1", Port: 1, Username: "unusable", AuthType: "invalid"}
	db.Create(&host)
	row := model.OperationsDatabase{Name: "live-pg", Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, PasswordEncrypted: password, SSLMode: "disable", Revision: 1, ServerID: host.ID}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	target, err := s.findMonitoringTarget(context.Background(), "database", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.collectMonitoring(context.Background(), target, "live")
	if err != nil || r.Database == nil || r.Indicators["maxConnections"] <= 0 || len(r.RelatedHosts) != 0 {
		t.Fatalf("live PostgreSQL requires heavy/SSH collection: %+v %v", r, err)
	}
	for _, o := range r.ReportChecks {
		if o.Code == "cloudhis-20" || o.Code == "cloudhis-34" || o.Code == "cloudhis-35" {
			t.Fatal("guide-wide SQL executed on the live interval")
		}
	}
}

func TestDailyMonitoringReportMergesTimesAndMarksOldFailedDataUnknown(t *testing.T) {
	s, db := dailyFixture(t)
	row := model.OperationsDatabase{Name: "cached-pg", Host: "127.0.0.1", Port: 1, Database: "fixture", Username: "fixture", Revision: 1}
	db.Create(&row)
	target, err := s.findMonitoringTarget(context.Background(), "database", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range targetLanes(target) {
		state, err := s.ensureMonitoringState(context.Background(), target, lane)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-10 * time.Second)
		if lane == "deep" {
			at = at.Add(-time.Hour)
		}
		r := opsprobe.JobResult{Type: "database", Name: row.Name, FinishedAt: at}
		for _, code := range monitoringLaneCodes(target, lane) {
			o := inspection.Observation{Code: code, Name: code, Status: "RECORDED", Value: lane, CollectedAt: at}
			if lane == "deep" {
				o.Executions = []inspection.Execution{{Kind: "sql", Command: "SELECT fixture", Stdout: "original result", StartedAt: at, FinishedAt: at}}
			}
			r.ReportChecks = append(r.ReportChecks, o)
		}
		if err := db.Model(&state).Updates(map[string]any{"result_json": mustJSON(r), "last_success_at": at, "last_attempt_at": at.Add(-time.Second)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.monitoringReportResult(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || time.Since(r.StartedAt) < time.Hour {
		t.Fatal("report reused task time instead of the source collection range", r.StartedAt, r.FinishedAt)
	}
	for _, o := range r.ReportChecks {
		if o.Code == "cloudhis-27" {
			if o.Value != "live" || len(o.Executions) != 1 || time.Since(o.CollectedAt) > time.Minute || time.Since(o.Executions[0].StartedAt) < time.Hour {
				t.Fatal("live value and deep SQL evidence times were conflated", o)
			}
		}
	}
	if err := db.Model(&model.MonitoringState{}).Where("target_type='database' AND target_id=? AND lane='live'", row.ID).Updates(map[string]any{"last_attempt_at": time.Now(), "last_error": "fixture connection failed"}).Error; err != nil {
		t.Fatal(err)
	}
	r, err = s.monitoringReportResult(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range r.ReportChecks {
		if o.Code == "cloudhis-27" {
			found = true
			if o.Status != "UNKNOWN" || !strings.Contains(o.Reason, "fixture connection failed") {
				t.Fatal("last good value was reported as current normal", o)
			}
		}
		if strings.HasPrefix(o.Code, "monitoring-") {
			t.Fatal("technical gap duplicated every missing check")
		}
	}
	if !found {
		t.Fatal("lost expected connection check")
	}
}

func TestDailyMonitoringHostSourceChangeDoesNotReuseOldEndpointHistory(t *testing.T) {
	s, db := dailyFixture(t)
	h := model.Server{Host: "127.0.0.1", Username: "fixture"}
	db.Create(&h)
	m := opsprobe.HostMetrics{Source: "node_exporter", SourceURL: "http://127.0.0.1:9100/metrics", CollectedAt: time.Now(), CPUUsagePercent: 12}
	db.Create(&model.HostMetricSample{ServerID: h.ID, CollectedAt: m.CollectedAt, MetricsJSON: mustJSON(m)})
	if _, err := s.ConfigureMonitor(h.ID, true, 60, "http://127.0.0.1:9999/metrics"); err != nil {
		t.Fatal(err)
	}
	_, current, err := s.HostMonitor(h.ID)
	if err != nil || current != nil {
		t.Fatal("old exporter data attached to a new endpoint")
	}
	history, _, err := s.HostHistory(context.Background(), h.ID, 1)
	if err != nil || len(history) != 0 {
		t.Fatal("changed endpoint retained the old curve", history, err)
	}
}

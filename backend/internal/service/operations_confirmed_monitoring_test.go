package service

import (
	"context"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfirmedReportFreezesWithoutCollectionAndArchivesEverySlowQuery(t *testing.T) {
	s, db := dailyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().Truncate(time.Microsecond)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "must not collect", 503) }))
	defer remote.Close()
	component, err := s.SaveComponent(ctx, ComponentRequest{Kind: "elasticsearch", Name: "snapshot-only", Endpoint: remote.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	row := model.OperationsDatabase{Name: "snapshot-pg", Host: "127.0.0.1", Port: 1, Database: "default", Username: "reader", Revision: 1, LogEnabled: true, LastLogSyncAt: &now}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	target, _ := s.findMonitoringTarget(ctx, "database", row.ID)
	state, err := s.ensureMonitoringState(ctx, target, "live")
	if err != nil {
		t.Fatal(err)
	}
	collected := now.Add(-time.Hour)
	lease := now.Add(time.Hour)
	result := opsprobe.JobResult{Type: "database", StartedAt: collected, FinishedAt: collected, ReportChecks: []inspection.Observation{{Code: "cloudhis-27", Name: "实例连接数", Status: "NORMAL", Value: "17 / 100", CollectedAt: collected}}}
	if err := db.Model(&state).Updates(map[string]any{"result_json": mustJSON(result), "last_success_at": collected, "last_error": "context deadline exceeded", "lease_until": lease}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 537; i++ {
		e := model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: fmt.Sprintf("frozen-%d", i), Type: "slow_sql", DBName: fmt.Sprintf("business_%d", i%3), EventTime: now.Add(-time.Duration(i/2) * time.Second), DurationMs: 5001, SQL: fmt.Sprintf("select %d /* %s */", i, strings.Repeat("long", 400)), CreatedAt: now}
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.MonitoringSlowQueries(ctx, row.ID, SlowQueryFilter{Page: 1, Size: 20})
	if err != nil || first.Total != 537 || len(first.List) != 20 {
		t.Fatalf("first page %d/%d: %v", first.Total, len(first.List), err)
	}
	if first.List[0].ID < first.List[1].ID {
		t.Fatal("timestamp ties are not ordered by id descending")
	}
	old := model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: "expired-slow", Type: "slow_sql", EventTime: now.Add(-49 * time.Hour), SQL: "expired"}
	db.Create(&old)
	second, err := s.MonitoringSlowQueries(ctx, row.ID, SlowQueryFilter{Page: 27, Size: 20, AsOfID: first.AsOfID})
	if err != nil || second.Total != 537 || len(second.List) != 17 {
		t.Fatalf("last page lost records: %+v %v", second, err)
	}
	run, err := s.CreateConfiguredInspection(ctx, "冻结验证", "fixture", []InspectionTargetRequest{{Type: "database", ID: row.ID}, {Type: "component", ID: component.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("report collected or waited for a collector")
	}
	var child model.InspectionTargetRun
	db.Where("run_id=? AND type='database'", run.ID).First(&child)
	var frozen opsprobe.JobResult
	if err := json.Unmarshal([]byte(child.ResultJSON), &frozen); err != nil {
		t.Fatal(err)
	}
	good := false
	for _, o := range frozen.ReportChecks {
		if o.Code == "cloudhis-27" {
			good = o.Value == "17 / 100" && o.Status == "RECORDED" && o.CollectedAt.Equal(collected)
		}
	}
	if !good || child.Status != "COMPLETED" || run.FrozenAt == nil {
		t.Fatal("snapshot discarded previous successful data")
	}
	// Live arrivals and retention cleanup must never change this report's records.
	db.Where("database_id=?", row.ID).Delete(&model.DatabaseLogEvent{})
	s.cleanupOperations(ctx)
	archive, err := s.InspectionSlowQueries(ctx, run.ID, SlowQueryFilter{Page: 27, Size: 20})
	if err != nil || archive.Total != 537 || len(archive.List) != 17 || len(archive.List[0].SQL) < 1600 {
		t.Fatalf("archive incomplete after source deletion: %d %v", archive.Total, err)
	}
	db.Model(&state).Update("result_json", `{"reportChecks":[]}`)
	var unchanged model.InspectionTargetRun
	db.First(&unchanged, child.ID)
	if unchanged.ResultJSON != child.ResultJSON {
		t.Fatal("frozen target changed with monitoring")
	}
	db.Model(run).Updates(map[string]any{"status": "RUNNING", "lease_owner": s.owner, "lease_until": time.Now().Add(time.Minute)})
	s.processInspection(ctx, run.ID)
	var finished model.InspectionRun
	if err := db.First(&finished, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if finished.Status != "COMPLETED" || finished.HTMLStatus != "READY" || finished.ErrorCount == 0 {
		t.Fatalf("report status was conflated with missing data: %s / %s / %s", finished.Status, finished.HTMLStatus, finished.ReportError)
	}
	var doc inspection.Report
	if err := json.Unmarshal([]byte(finished.ReportSnapshotJSON), &doc); err != nil {
		t.Fatal(err)
	}
	hasPreview := false
	for _, item := range doc.Items {
		if item.Code == "postgresql-slow-history" {
			for _, e := range item.Evidence {
				if len(e.Tables) > 0 && len(e.Tables[0].Rows) == 5 && e.Attributes["total"] == "537" {
					hasPreview = true
				}
			}
		}
	}
	if !hasPreview || doc.FrozenAt == nil || calls.Load() != 0 {
		t.Fatal("frozen report preview, timestamp or no-collection guarantee failed")
	}
	if err := s.GenerateInspectionReports(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var regenerated model.InspectionRun
	db.First(&regenerated, run.ID)
	if regenerated.ReportSnapshotJSON != finished.ReportSnapshotJSON {
		t.Fatal("regeneration changed the frozen report")
	}

	if dir := os.Getenv("DFOPS_CONFIGURED_REPORT_FIXTURE"); dir != "" {
		for _, ext := range []string{"html", "pdf"} {
			path, _ := s.ReportPath(run.ID, ext)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "confirmed-slow-queries."+ext), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.DeleteInspection(run.ID); err != nil {
		t.Fatal(err)
	}
	// Availability is also frozen, rather than read from today's connection.
	db.Model(&row).Update("log_enabled", false)
	unavailableRun, err := s.CreateConfiguredInspection(ctx, "未接入日志的报告", "fixture", []InspectionTargetRequest{{Type: "database", ID: row.ID}})
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&row).Update("log_enabled", true)
	unavailable, err := s.InspectionSlowQueries(ctx, unavailableRun.ID, SlowQueryFilter{})
	if err != nil || unavailable.Availability != "not_configured" || !strings.Contains(unavailable.Message, "日志采集未启用") {
		t.Fatalf("frozen availability lost: %+v %v", unavailable, err)
	}
	var count int64
	db.Model(&model.InspectionSlowQuery{}).Where("run_id=?", run.ID).Count(&count)
	if count != 0 {
		t.Fatal("report deletion left archived SQL")
	}
}

func TestConfirmedMonitoringAllDatabasesAndPermissionIsolation(t *testing.T) {
	s, _ := dailyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	names := []string{"monitor_a_" + suffix, "monitor_b_" + suffix}
	role := "monitor_reader_" + suffix
	for _, name := range names {
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		c := cfg.Copy()
		c.Database = name
		conn, err := pgx.ConnectConfig(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		_, err = conn.Exec(ctx, "CREATE TABLE public.monitor_marker(id int)")
		conn.Close(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	c := opsprobe.DatabaseConfig{Host: cfg.Host, Port: int(cfg.Port), Database: cfg.Database, Username: cfg.User, Password: cfg.Password, SSLMode: "disable"}
	result := &opsprobe.JobResult{Indicators: map[string]float64{}}
	s.collectAllPostgreSQLDatabases(ctx, c, "live", result)
	found := map[string]bool{}
	for _, o := range result.ReportChecks {
		if o.Code == "postgresql-table-health" {
			for _, table := range o.Tables {
				for _, row := range table.Rows {
					if len(row) > 1 && row[1] == "public.monitor_marker" {
						found[row[0]] = true
					}
				}
			}
		}
	}
	if !found[names[0]] || !found[names[1]] {
		t.Fatal("table statistics did not cover both databases", found)
	}
	deep := &opsprobe.JobResult{Indicators: map[string]float64{}}
	s.collectAllPostgreSQLDatabases(ctx, c, "deep", deep)
	scopes := map[string]bool{}
	for _, o := range deep.ReportChecks {
		if o.Code == "cloudhis-20" {
			for _, execution := range o.Executions {
				scopes[execution.Database] = true
			}
			for _, table := range o.Tables {
				if len(table.Columns) == 0 || table.Columns[0] != "数据库" {
					t.Fatal("deep SQL table lost database attribution")
				}
			}
		}
	}
	if !scopes[names[0]] || !scopes[names[1]] {
		t.Fatal("guide SQL did not run in every database")
	}
	if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD 'local-monitor-fixture'"); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{role}.Sanitize())
	if _, err := admin.Exec(ctx, "GRANT pg_read_all_stats TO "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "REVOKE CONNECT ON DATABASE "+pgx.Identifier{names[1]}.Sanitize()+" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	c.Username = role
	c.Password = "local-monitor-fixture"
	result = &opsprobe.JobResult{Indicators: map[string]float64{}}
	s.collectAllPostgreSQLDatabases(ctx, c, "live", result)
	good, denied := false, false
	for _, o := range result.ReportChecks {
		if o.Code == "postgresql-table-health" {
			if o.Status != "RECORDED" {
				t.Fatal("one denied database erased successful table statistics")
			}
			for _, gap := range o.Gaps {
				denied = denied || strings.Contains(gap, names[1])
			}
			for _, table := range o.Tables {
				for _, row := range table.Rows {
					good = good || (len(row) > 1 && row[0] == names[0] && row[1] == "public.monitor_marker")
				}
			}
		}
	}
	if !good || !denied {
		t.Fatal("permission isolation or database attribution lost")
	}
	if _, ok := result.Indicators["lockWaits"]; ok {
		t.Fatal("incomplete instance scope represented as a complete total")
	}
}

func TestConfirmedFactsOnlySpaceOver75(t *testing.T) {
	for _, n := range []float64{75, 75.1} {
		m := &opsprobe.HostMetrics{CPUUsagePercent: 99, MemUsagePercent: 99, Disks: []opsprobe.Disk{{Mountpoint: "/", UsedPercent: n, Inodes: 100, InodeUsedPercent: n}}}
		r := &opsprobe.JobResult{Metrics: m, ReportChecks: []inspection.Observation{{Code: "host_filesystems", Status: "NORMAL"}, {Code: "rabbitmq-queues", Status: "WARNING", Reason: "arbitrary backlog policy", Advice: "arbitrary"}}}
		applyMonitoringFacts(r)
		want := "RECORDED"
		if n > 75 {
			want = "CRITICAL"
		}
		if r.ReportChecks[0].Status != want || r.ReportChecks[1].Status != "RECORDED" || r.ReportChecks[1].Advice != "" {
			t.Fatal("incorrect confirmed policy", n, r.ReportChecks)
		}
		for _, c := range r.Checks {
			if (c.Code == "host_cpu" || c.Code == "host_memory") && c.Status != "RECORDED" {
				t.Fatal("space threshold applied to CPU or memory")
			}
		}
	}
}

func TestConfirmedSharedCollectorLeaseClearsSQLNull(t *testing.T) {
	s, db := dailyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	row, err := s.SaveComponent(ctx, ComponentRequest{Kind: "elasticsearch", Name: "shared-lease", Endpoint: "http://127.0.0.1:1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.findMonitoringTarget(ctx, "component", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.ensureMonitoringState(ctx, target, "live")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&state).Update("lease_until", time.Now().Add(time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		done <- db.Model(&state).Updates(map[string]any{"lease_until": nil, "last_success_at": time.Now(), "result_json": mustJSON(opsprobe.JobResult{Type: "elasticsearch"})}).Error
	}()
	lane, err := s.ensureMonitoringLane(ctx, target, "live")
	if updateErr := <-done; updateErr != nil {
		t.Fatal(updateErr)
	}
	if err != nil || lane.Collecting || lane.Result == nil {
		t.Fatalf("completed shared collector was still treated as leased: %v", err)
	}
}

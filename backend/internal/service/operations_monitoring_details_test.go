package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
)

func TestDailyMonitoringSlowHistoryIsScopedBoundedAndUsesSyncTime(t *testing.T) {
	s, db := dailyFixture(t)
	ctx := context.Background()
	now := time.Now()
	row := model.OperationsDatabase{Name: "slow-history", Host: "localhost", Port: 1, Database: "fixture", Username: "fixture", Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	o := s.monitoringPostgresSlowHistory(ctx, row)
	if o.Attributes["availability"] != "not_configured" || len(o.Measurements) != 0 {
		t.Fatalf("disabled logging became zero: %+v", o)
	}
	for i := 0; i < 55; i++ {
		e := model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: "monitoring-details-" + time.Now().Add(time.Duration(i)).Format(time.RFC3339Nano), Type: "slow_sql", EventTime: now.Add(-time.Duration(i) * time.Second), SQL: "SELECT " + strings.Repeat("x", 1200), DurationMs: float64(i + 1)}
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range []model.DatabaseLogEvent{{DatabaseID: row.ID, Type: "slow_sql", EventTime: now.Add(-49 * time.Hour), SQL: "old-private-query"}, {DatabaseID: row.ID + 999, Type: "slow_sql", EventTime: now, SQL: "other-db-private-query"}, {DatabaseID: row.ID, Type: "error", EventTime: now, SQL: "error-private-query"}} {
		e.SourceKey = "details-excluded-" + time.Now().Add(time.Duration(i)).Format(time.RFC3339Nano)
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	row.LogEnabled = true
	row.LastLogSyncAt = &now
	o = s.monitoringPostgresSlowHistory(ctx, row)
	if o.Status != "RECORDED" || o.Measurements["historicalSlowLogs"] != 55 || len(o.Tables[0].Rows) != 50 || !o.CollectedAt.Equal(now) {
		t.Fatalf("slow history scope: %+v", o)
	}
	data, _ := json.Marshal(o)
	if strings.Contains(string(data), "private-query") {
		t.Fatal("excluded log leaked")
	}
	if len([]rune(o.Tables[0].Rows[0][5])) > 1001 {
		t.Fatal("SQL preview is unbounded")
	}
	stale := now.Add(-4 * time.Minute)
	row.LastLogSyncAt = &stale
	o = s.monitoringPostgresSlowHistory(ctx, row)
	if o.Status != "UNKNOWN" || len(o.Tables[0].Rows) != 50 || !o.CollectedAt.Equal(stale) {
		t.Fatalf("stale log history presented as fresh: %+v", o)
	}
}

func TestDailyMonitoringDashboardTransformsBloatAndPreservesOriginalEvidence(t *testing.T) {
	s, db := dailyFixture(t)
	ctx := context.Background()
	row := model.OperationsDatabase{Name: "bloat-display", Host: "localhost", Port: 1, Database: "fixture", Username: "fixture", Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	target, err := s.findMonitoringTarget(ctx, "database", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	source := inspection.Observation{Code: "cloudhis-34", Status: "RECORDED", Executions: []inspection.Execution{{Command: "original-private-guide", Stdout: "original-private-evidence"}}, Tables: []inspection.Table{{Columns: []string{"schemaname", "tablename", "tbloat", "wastedbytes", "iname", "ibloat", "wastedibytes"}, Rows: [][]string{{"pg_catalog", "pg_class", "10", "5000000", "index", "5", "2000000"}, {"public", "orders", "2", "2097152", "orders_idx", "1.5", "1048576"}, {"public", "orders", "2", "2097152", "orders_idx", "1.5", "1048576"}}}}}
	for _, lane := range []string{"live", "deep"} {
		state, err := s.ensureMonitoringState(ctx, target, lane)
		if err != nil {
			t.Fatal(err)
		}
		r := opsprobe.JobResult{Type: "database", FinishedAt: stamp}
		if lane == "live" {
			r.ReportChecks = []inspection.Observation{{Code: "postgresql-slots", Status: "NORMAL", Tables: []inspection.Table{{Columns: []string{"Slot"}, Rows: [][]string{{"read_slot"}}}}}}
		} else {
			r.ReportChecks = []inspection.Observation{source}
			source.Code = "cloudhis-35"
			r.ReportChecks = append(r.ReportChecks, source)
		}
		if err := db.Model(&state).Updates(map[string]any{"result_json": mustJSON(r), "last_success_at": stamp}).Error; err != nil {
			t.Fatal(err)
		}
	}
	v, err := s.MonitoringDashboard(ctx, "database", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]inspection.Observation{}
	for _, c := range v.Result.ReportChecks {
		checks[c.Code] = c
	}
	if len(checks["postgresql-slots"].Tables) != 1 {
		t.Fatal("operational slot table was removed")
	}
	for _, code := range []string{"postgresql-table-bloat", "postgresql-index-bloat"} {
		o := checks[code]
		if o.Attributes["estimated"] != "true" || o.Status != "RECORDED" || !o.CollectedAt.Equal(stamp) || len(o.Tables[0].Rows) != 1 {
			t.Fatalf("bad bloat estimate: %+v", o)
		}
	}
	data, _ := json.Marshal(v)
	if strings.Contains(string(data), "original-private") || strings.Contains(string(data), "pg_catalog") {
		t.Fatal("raw deep evidence leaked to dashboard")
	}
	var saved model.MonitoringState
	db.Where("target_type='database' AND target_id=? AND lane='deep'", row.ID).First(&saved)
	if !strings.Contains(saved.ResultJSON, "original-private-evidence") {
		t.Fatal("original report evidence changed")
	}
	db.Model(&saved).Update("last_error", "deep update failed")
	v, err = s.MonitoringDashboard(ctx, "database", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Result.ReportChecks {
		if strings.HasPrefix(c.Code, "postgresql-table-bloat") && c.Attributes["stale"] != "true" {
			t.Fatal("last deep failure hidden")
		}
	}
	var samples int64
	db.Model(&model.CollectionSample{}).Count(&samples)
	if samples != 0 {
		t.Fatal("dashboard GET started collection")
	}
}

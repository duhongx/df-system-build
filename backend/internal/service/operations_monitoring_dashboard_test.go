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

func TestDailyMonitoringDashboardKeepsMetricsAndIssuesWithoutReportEvidence(t *testing.T) {
	s, db := dailyFixture(t)
	row := model.OperationsDatabase{Name: "dashboard", Host: "127.0.0.1", Port: 1, Database: "fixture", Username: "fixture", Revision: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	target, err := s.findMonitoringTarget(context.Background(), "database", row.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Second)
	for _, lane := range targetLanes(target) {
		state, err := s.ensureMonitoringState(context.Background(), target, lane)
		if err != nil {
			t.Fatal(err)
		}
		r := opsprobe.JobResult{Type: "database", Name: row.Name, FinishedAt: at, Indicators: map[string]float64{"connections": 12, "lockWaits": 1}}
		r.Database = &opsprobe.Diagnostic{Activities: []opsprobe.Activity{{Query: "private-session-query"}}}
		r.ReportChecks = []inspection.Observation{
			{Code: "cloudhis-2", Status: "RECORDED", Value: "单机", Attributes: map[string]string{"architecture": "单机", "version": "14.8"}, Executions: []inspection.Execution{{Command: "private-guide-sql", Stdout: "private-guide-output"}}},
			{Code: "cloudhis-28", Name: "锁等待", Status: "WARNING", Value: "1 个", Reason: "存在锁等待"},
		}
		if lane != "live" {
			r.ReportChecks[0].Value = "private-" + lane + "-evidence"
		}
		if err := db.Model(&state).Updates(map[string]any{"result_json": mustJSON(r), "last_success_at": at, "last_attempt_at": at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	dashboard, err := s.MonitoringDashboard(context.Background(), "database", row.ID)
	if err != nil || dashboard.Result == nil || dashboard.Status != "WARNING" || dashboard.Indicators["connections"] != 12 || len(dashboard.Lanes) != 0 {
		t.Fatalf("current monitoring data missing: %+v %v", dashboard, err)
	}
	if dashboard.Result.ReportChecks[0].Attributes["architecture"] != "单机" || dashboard.Result.ReportChecks[1].Reason != "存在锁等待" {
		t.Fatal("dashboard lost facts or actionable issues")
	}
	encoded, _ := json.Marshal(dashboard)
	if strings.Contains(string(encoded), "private-") || dashboard.Result.Database != nil {
		t.Fatal("dashboard transfers raw query or supplemental evidence")
	}
	full, err := s.MonitoringDetail(context.Background(), "database", row.ID)
	if err != nil || len(full.Lanes) != 3 || full.Result.Database == nil || full.Result.ReportChecks[0].Executions[0].Command != "private-guide-sql" {
		t.Fatal("dashboard changed report evidence", err)
	}
	var samples int64
	db.Model(&model.CollectionSample{}).Count(&samples)
	if samples != 0 {
		t.Fatal("viewing dashboard started a collection")
	}
}

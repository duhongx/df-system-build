package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/pkg/crypto"
)

func TestOperationsDailyRecordsUseOnlyConsecutiveActualDates(t *testing.T) {
	for _, c := range []struct{ name, data, status string }{{"complete", "2026-10-26 100\n2026-10-27 00:00:00 125\n2026-10-28 120\n", "RECORDED"}, {"gap", "2026-10-26 100\n2026-10-28 140\n", "UNKNOWN"}, {"ambiguous", "2026-10-26 100\n2026-10-26 110\n2026-10-27 120\n", "UNKNOWN"}, {"not midnight", "2026-10-26 11:00 100\n2026-10-27 120\n", "UNKNOWN"}} {
		t.Run(c.name, func(t *testing.T) {
			o := inspection.Observation{Code: "cloudhis-39", Status: "RECORDED", Executions: []inspection.Execution{{Stdout: "150"}, {Stdout: c.data}}}
			enrichDailyRecords(&o)
			if o.Status != c.status || o.Executions[1].Stdout != c.data {
				t.Fatalf("daily classification %+v", o)
			}
			if c.name == "complete" {
				rows := o.Tables[0].Rows
				if rows[1][2] != "25" || rows[2][2] != "-5" {
					t.Fatalf("wrong actual deltas %+v", rows)
				}
			}
		})
	}
}

func TestOperationsHistoryComparesSameServerArchivedTLAndKeepsIntervalDistinctFromDaily(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	before := time.Now().Add(-90 * 24 * time.Hour)
	run := model.InspectionRun{Name: "previous", Status: "COMPLETED"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	old := opsprobe.JobResult{ReportChecks: []inspection.Observation{{Code: "cloudhis-17", Status: "RECORDED", Measurements: map[string]float64{"192.0.2.1": 7}, CollectedAt: before}, {Code: "cloudhis-39", Status: "UNKNOWN", Measurements: map[string]float64{"inodeUsed": 100}, CollectedAt: before}}}
	prior := model.InspectionTargetRun{RunID: run.ID, Type: "host", TargetID: 1, ServerID: 1, ServerEndpoint: "same-identity", Status: "COMPLETED", FinishedAt: &before, ResultJSON: mustJSON(old)}
	if err := db.Create(&prior).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.GenerateInspectionReports(context.Background(), run.ID); err != nil && os.Getenv("DFOPS_REPORT_FONT") != "" {
		t.Fatal(err)
	}
	// Changes to mutable target data must not change the comparison against
	// the earlier frozen customer report.
	if err := db.Model(&prior).Update("result_json", mustJSON(opsprobe.JobResult{})).Error; err != nil {
		t.Fatal(err)
	}
	current := opsprobe.JobResult{StartedAt: time.Now(), ReportChecks: []inspection.Observation{{Code: "cloudhis-17", Status: "RECORDED", Measurements: map[string]float64{"192.0.2.1": 8}, Tables: []inspection.Table{{Title: "Patroni"}}}, {Code: "cloudhis-39", Status: "RECORDED", Measurements: map[string]float64{"inodeUsed": 140}, Executions: []inspection.Execution{{Stdout: "140"}}}}}
	target := model.InspectionTargetRun{RunID: run.ID + 1, Type: "host", TargetID: 1, ServerID: 1, ServerEndpoint: "same-identity", ManualJSON: "{}"}
	svc.enrichInspectionHistory(&target, &current)
	if current.ReportChecks[0].Status != "WARNING" || !strings.Contains(current.ReportChecks[0].Reason, "7 → 8") {
		t.Fatalf("TL growth missing %+v", current.ReportChecks[0])
	}
	counter := current.ReportChecks[1]
	if counter.Status != "UNKNOWN" || len(counter.Tables) != 1 || counter.Tables[0].Rows[0][4] != "40" || len(counter.Gaps) == 0 {
		t.Fatalf("quarterly difference became daily data %+v", counter)
	}
	target.ServerEndpoint = "changed-identity"
	other := opsprobe.JobResult{StartedAt: time.Now(), ReportChecks: []inspection.Observation{{Code: "cloudhis-17", Status: "RECORDED", Measurements: map[string]float64{"192.0.2.1": 9}, Tables: []inspection.Table{{Title: "Patroni"}}}}}
	svc.enrichInspectionHistory(&target, &other)
	if other.ReportChecks[0].Status != "RECORDED" {
		t.Fatal("compared unrelated SSH identity")
	}
}

func TestOperationsReplicaOnlyArchiveReportRemainsPartial(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	run := model.InspectionRun{Name: "replica-only", Status: "RUNNING", LeaseOwner: svc.owner, TotalTargets: 1}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	target := model.InspectionTargetRun{RunID: run.ID, Name: "replica", Type: "host", Status: "COMPLETED", ManualJSON: mustJSON(opsprobe.ManualOptions{Profiles: []string{"postgresql_archive"}, ArchiveDirectory: "/archive"}), ResultJSON: mustJSON(opsprobe.JobResult{ReportChecks: []inspection.Observation{{Code: "cloudhis-19", Status: "NOT_APPLICABLE", Reason: "当前主机是 Replica"}}})}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	svc.processInspection(context.Background(), run.ID)
	if err := db.First(&run, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if run.Status != "PARTIAL" || run.ErrorCount != 1 {
		t.Fatalf("missing Leader result reported as complete %+v", run)
	}
}

func TestOperationsFullResultsPassSSHAndSnapshotFormerSizeLimits(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	host, port := fakeOpsSSH(t)
	credential, err := crypto.Encrypt("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	server := model.Server{Host: host, Port: port, Username: "fixture", AuthType: "password", CredentialEncrypted: credential}
	if err := db.Create(&server).Error; err != nil {
		t.Fatal(err)
	}
	conn, err := svc.connect(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	content := strings.Repeat("a", 9<<20) + "-full-output-end"
	file := filepath.Join(t.TempDir(), "result.txt")
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := conn.ReadFile(file, 0)
	if err != nil || string(data) != content {
		t.Fatal("SSH result was rejected or shortened", err)
	}
	run := model.InspectionRun{Name: "full-results", Status: "COMPLETED"}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	r := inspectionReport{Run: run, Targets: []inspectionReportTarget{{Target: model.InspectionTargetRun{ID: 1, Type: "host", Name: "fixture", Status: "COMPLETED", ManualJSON: mustJSON(opsprobe.ManualOptions{Profiles: []string{"k8s_host"}})}, Result: opsprobe.JobResult{ReportChecks: []inspection.Observation{{Code: "cloudhis-6", Status: "RECORDED", Executions: []inspection.Execution{{Kind: "command", Stdout: string(data)}}}}}}}}
	if err := svc.freezeInspectionReport(context.Background(), &r); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&run, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(run.ReportSnapshotJSON) <= 16<<20 {
		t.Fatal("did not exercise former snapshot limit")
	}
	var saved inspection.Report
	if err := json.Unmarshal([]byte(run.ReportSnapshotJSON), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Objects[0].Observations[0].Executions[0].Stdout != content || saved.Items[5].Evidence[0].Executions[0].Stdout != content {
		t.Fatal("archived result was shortened")
	}
}

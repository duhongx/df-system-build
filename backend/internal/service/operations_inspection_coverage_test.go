package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
)

func TestInspectionCoverageExplainsMissingChecksWithoutLoadingRawOutput(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	doc := inspection.Report{Summary: inspection.Summary{Planned: 3, Completed: 1, Unknown: 2, NotRun: 1, Warning: 1}, Items: []inspection.Item{
		{Definition: inspection.Definition{Code: "cloudhis-27", Name: "连接数"}, Status: "WARNING", Complete: true},
		{Definition: inspection.Definition{Code: "cloudhis-16", Name: "集群运行"}, Status: "UNKNOWN", Evidence: []inspection.Evidence{{ObjectName: "db-host", Observation: inspection.Observation{Status: "UNKNOWN", Gaps: []string{"exit status 127"}, Executions: []inspection.Execution{{ExitCode: 127, Error: "exit status 127", Stderr: "patronictl: command not found", Stdout: strings.Repeat("large-raw-output", 100000)}}}}}},
		{Definition: inspection.Definition{Code: "cloudhis-38", Name: "MinIO 集群"}, Status: "UNKNOWN", Evidence: []inspection.Evidence{{ObjectName: "minio-host", Observation: inspection.Observation{Status: "UNKNOWN", Gaps: []string{"没有可用的管理接口凭据"}}}}},
		{Definition: inspection.Definition{Code: "cloudhis-5", Name: "MinIO 备份"}, Status: "NOT_RUN", Reason: "本次没有执行该项"},
	}}
	run := model.InspectionRun{Name: "coverage fixture", Status: "PARTIAL", ReportSnapshotJSON: mustJSON(doc), ErrorCount: 9, TotalTargets: 2, CompletedTargets: 2}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	targets := []model.InspectionTargetRun{{Status: "COMPLETED", Name: "db-host"}, {Status: "COMPLETED", Name: "minio-host"}}
	result, err := svc.InspectionCoverage(context.Background(), run, targets)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionLabel != "检查已结束" || result.Legacy || result.Summary.Completed != 1 || len(result.Issues) != 3 {
		t.Fatalf("unexpected coverage %+v", result)
	}
	if !strings.Contains(result.Issues[0].Reason, "command not found") || result.Issues[2].Status != "NOT_RUN" {
		t.Fatalf("missing actual failure cause %+v", result.Issues)
	}
	data, _ := json.Marshal(result)
	if len(data) > 3000 || strings.Contains(string(data), "large-raw-output") || strings.Contains(string(data), "连接数") {
		t.Fatal("coverage returned unrelated or raw evidence")
	}
	summaries, err := svc.InspectionSummaries(context.Background(), []uint{run.ID})
	if err != nil || summaries[run.ID].Summary.Unknown != 2 {
		t.Fatalf("summary differs from frozen report %+v %v", summaries, err)
	}
}

func TestInspectionCoverageDistinguishesLegacyFailuresUncertainExecutionAndReportFailure(t *testing.T) {
	db := operationsTestDB(t)
	svc := NewOperationsService(db, t.TempDir())
	run := model.InspectionRun{Status: "PARTIAL", PDFStatus: "FAILED", ErrorCount: 1}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	target := model.InspectionTargetRun{RunID: run.ID, Name: "legacy host", Status: "COMPLETED", ResultJSON: mustJSON(opsprobe.JobResult{Checks: []opsprobe.Check{{Code: "log", Name: "日志接入", Status: "UNKNOWN", Error: "日志读取权限不足 password=private-value"}}})}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.InspectionCoverage(context.Background(), run, []model.InspectionTargetRun{target})
	if err != nil || !result.Legacy || result.ExecutionLabel != "检查已结束" || len(result.Issues) != 1 {
		t.Fatalf("legacy %+v %v", result, err)
	}
	if strings.Contains(result.Issues[0].Reason, "private-value") {
		t.Fatal("credential leaked")
	}
	target.Status = "UNKNOWN"
	result, err = svc.InspectionCoverage(context.Background(), run, []model.InspectionTargetRun{target})
	if err != nil || result.UncertainTargets != 1 || result.ExecutionLabel != "远端执行状态待核实" {
		t.Fatalf("uncertain %+v %v", result, err)
	}
	// A PDF failure must not invent failed checks, and nil check arrays in old
	// snapshots must not cause jsonb_array_elements to fail.
	if err := db.Model(&target).Update("result_json", mustJSON(opsprobe.JobResult{})).Error; err != nil {
		t.Fatal(err)
	}
	target.Status = "COMPLETED"
	result, err = svc.InspectionCoverage(context.Background(), run, []model.InspectionTargetRun{target})
	if err != nil || len(result.Issues) != 0 || result.ExecutionLabel != "检查已结束" {
		t.Fatalf("report failure became check failure %+v %v", result, err)
	}
}

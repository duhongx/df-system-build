package scheduler

import (
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestRecoveryPreservesSubmittedDeploymentAsFailureWithDiagnostics(t *testing.T) {
	setupSchedulerTestDB(t)
	p := model.Pipeline{PipelineNo: "recover-submitted", AppName: "web-main", AppType: "vue", TriggerUser: "test", Status: "RUNNING", DeploymentProgress: "submitted"}
	repo := repository.NewPipelineRepo()
	if err := repo.Create(&p); err != nil {
		t.Fatal(err)
	}
	stage := model.PipelineStage{PipelineID: p.ID, StageCode: "K8S_DEPLOY", Status: "RUNNING"}
	if err := repository.DB.Create(&stage).Error; err != nil {
		t.Fatal(err)
	}
	record := model.ArtifactDeployRecord{DeployBatchNo: "recover-submitted", VersionNo: "v1", PipelineID: p.ID, AfterImage: "registry/web-main:new", BuildStatus: "success", DeployStatus: "deploying"}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	s := &BuildScheduler{pipelineRepo: repo}
	s.recoverPending() // No pending jobs exist; this must not invoke Docker/Kubernetes.
	got, err := repo.FindByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "FAILED" || got.ErrorStage != "K8S_DEPLOY" || !strings.Contains(got.ErrorMessage, "submitted") {
		t.Fatalf("lost interrupted progress: %+v", got)
	}
	if err := repository.DB.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.BuildStatus != "success" || record.DeployStatus != "failed" {
		t.Fatalf("incorrect recovered deployment state: %+v", record)
	}
	logs, err := repository.NewStageLogRepo().GetByStageID(stage.ID)
	if err != nil || len(logs) != 1 || logs[0].Stream != "stderr" {
		t.Fatalf("missing recovery diagnostics: %+v %v", logs, err)
	}
	if len(s.queue) != 0 {
		t.Fatal("interrupted deployment was replayed")
	}
}

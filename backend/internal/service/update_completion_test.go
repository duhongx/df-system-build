package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

func TestVersionReadFailureDoesNotFailUpdateOrRollback(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "metadata-only", VersionNo: "metadata-only"}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	record := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, PipelineID: 33, AppName: "service", DeploymentName: "service", BeforeImage: "registry/service:old", PackageVersionJSON: `{"version":"new"}`}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	reader := runtimeVersionReaderFunc(func(ctx context.Context, _ model.ArtifactDeployRecord) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Error("metadata read has no bounded timeout")
		}
		return "", errors.New("no Ready Pod")
	})
	if err := CaptureBeforeDeployVersions(context.Background(), 33, "service", record.BeforeImage, reader); err != nil {
		t.Fatal(err)
	}
	if err := CaptureAfterDeployVersions(context.Background(), 33, "service", "registry/service:new", reader); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Status != "deployed" || record.DeployStatus != "success" || record.VerifyStatus != "skipped" || record.ErrorMessage != "" || !strings.Contains(record.VersionWarning, "no Ready Pod") {
		t.Fatalf("metadata affected deployment: %+v", record)
	}
	rollbacker := &fakeDeploymentRollbacker{}
	if err := RollbackDeployBatch(context.Background(), batch.DeployBatchNo, rollbacker, reader); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.RollbackStatus != "success" || record.Status != "rolled_back" || len(rollbacker.calls) != 1 {
		t.Fatalf("metadata affected rollback: %+v", record)
	}
}

func TestBatchSummarySaveErrorsAreReturned(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "save-error", VersionNo: "save-error"}
	record := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, PipelineID: 44, AppName: "service", DeploymentName: "service", BeforeImage: "old"}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	cause := errors.New("injected batch save failure")
	cb := "test:batch_save_failure"
	if err := repository.DB.Callback().Update().Before("gorm:update").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "artifact_deploy_batches" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer repository.DB.Callback().Update().Remove(cb)
	if err := CaptureAfterDeployVersions(context.Background(), 44, "service", "new", nil); err == nil || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("summary error swallowed: %v", err)
	}
	if err := RollbackDeployBatch(context.Background(), batch.DeployBatchNo, &fakeDeploymentRollbacker{}, nil); err == nil || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("rollback save error swallowed: %v", err)
	}
}

func TestCompletedRolloutKeepsDeploymentSuccessAfterRecordFailure(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	record := model.ArtifactDeployRecord{PipelineID: 55, AppName: "service", DeploymentName: "service", AfterImage: "new", DeployStatus: "deploying", Status: "deploying"}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := MarkPipelineFailed(55, "滚动更新已完成，但保存部署结果失败: storage unavailable", "completed"); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.DeployStatus != "success" || record.VerifyStatus != "skipped" || !strings.Contains(record.ErrorMessage, "storage unavailable") {
		t.Fatalf("successful rollout mislabeled: %+v", record)
	}
}

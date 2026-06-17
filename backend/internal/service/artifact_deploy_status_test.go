package service

import (
	"context"
	"errors"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestCaptureDeployVersionsStoresBeforeAfterAndFailsOnBusinessMismatch(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "batch-verify", VersionNo: "batch-verify", Namespace: "prod", Status: "deploying", TotalCount: 2}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatalf("create batch: %v", err)
	}
	records := []model.ArtifactDeployRecord{
		{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, PipelineID: 10, AppName: "web-menzhenysz", AppType: "vue", FileName: "04.zip", DeploymentName: "web-main", Namespace: "prod", PackageVersionJSON: `{"xiTongId":"04","version":"2.0.1","date":"2026","branch":"r","commit":"abc"}`},
		{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, PipelineID: 10, AppName: "web-buliangsj", AppType: "vue", FileName: "88.zip", DeploymentName: "web-main", Namespace: "prod", PackageVersionJSON: `{"xiTongId":"88","version":"2.0.1","date":"2026","branch":"r","commit":"abc"}`},
	}
	if err := repository.DB.Create(&records).Error; err != nil {
		t.Fatalf("create records: %v", err)
	}
	reader := fakeRuntimeVersionReader{
		before: map[string]string{
			"web-menzhenysz": `{"xiTongId":"04","version":"2.0.0","date":"2025","branch":"r","commit":"old"}`,
			"web-buliangsj":  `{"xiTongId":"88","version":"2.0.0","date":"2025","branch":"r","commit":"old"}`,
		},
		after: map[string]string{
			"web-menzhenysz": `{"xiTongId":"04","version":"2.0.1","date":"2026","branch":"r","commit":"abc"}`,
			"web-buliangsj":  `{"xiTongId":"88","version":"2.0.9","date":"2026","branch":"r","commit":"abc"}`,
		},
	}

	if err := CaptureBeforeDeployVersions(context.Background(), 10, "web-main", "registry/web-main:old", reader.beforeReader()); err != nil {
		t.Fatalf("capture before: %v", err)
	}
	err := CaptureAfterDeployVersions(context.Background(), 10, "web-main", "registry/web-main:new", reader.afterReader())
	if err == nil {
		t.Fatalf("expected business version mismatch to fail")
	}

	var got []model.ArtifactDeployRecord
	if err := repository.DB.Where("pipeline_id = ?", 10).Order("app_name ASC").Find(&got).Error; err != nil {
		t.Fatalf("find records: %v", err)
	}
	byApp := map[string]model.ArtifactDeployRecord{}
	for _, record := range got {
		byApp[record.AppName] = record
	}
	okRecord := byApp["web-menzhenysz"]
	failedRecord := byApp["web-buliangsj"]
	if okRecord.BeforeImage != "registry/web-main:old" || okRecord.AfterImage != "registry/web-main:new" {
		t.Fatalf("images were not captured: %#v", okRecord)
	}
	if okRecord.VerifyStatus != "success" {
		t.Fatalf("first record should verify successfully: %#v", okRecord)
	}
	if failedRecord.VerifyStatus != "failed" || failedRecord.Status != "failed" {
		t.Fatalf("second record should fail verification: %#v", failedRecord)
	}
}

type fakeRuntimeVersionReader struct {
	before map[string]string
	after  map[string]string
}

func (r fakeRuntimeVersionReader) beforeReader() RuntimeVersionReader {
	return runtimeVersionReaderFunc(func(_ context.Context, record model.ArtifactDeployRecord) (string, error) {
		value, ok := r.before[record.AppName]
		if !ok {
			return "", errors.New("missing before")
		}
		return value, nil
	})
}

func (r fakeRuntimeVersionReader) afterReader() RuntimeVersionReader {
	return runtimeVersionReaderFunc(func(_ context.Context, record model.ArtifactDeployRecord) (string, error) {
		value, ok := r.after[record.AppName]
		if !ok {
			return "", errors.New("missing after")
		}
		return value, nil
	})
}

type runtimeVersionReaderFunc func(context.Context, model.ArtifactDeployRecord) (string, error)

func (fn runtimeVersionReaderFunc) ReadBusinessVersion(ctx context.Context, record model.ArtifactDeployRecord) (string, error) {
	return fn(ctx, record)
}

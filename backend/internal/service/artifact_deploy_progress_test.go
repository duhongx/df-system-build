package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestDeployFailureKeepsActualBuildAndDeploymentProgress(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	for _, phase := range []string{"preparing", "submitting", "submitted", "completed"} {
		t.Run(phase, func(t *testing.T) {
			p := model.Pipeline{PipelineNo: "progress-" + phase, AppName: "web-main", TriggerUser: "test"}
			if err := repository.DB.Create(&p).Error; err != nil {
				t.Fatal(err)
			}
			record := model.ArtifactDeployRecord{DeployBatchNo: "progress-" + phase, VersionNo: "v1", PipelineID: p.ID, DeploymentName: "web-main", BuildStatus: "pending", DeployStatus: "pending", VerifyStatus: "pending"}
			if err := repository.DB.Create(&record).Error; err != nil {
				t.Fatal(err)
			}
			if err := RecordDeploymentProgress(p.ID, phase, "registry/web-main:new"); err != nil {
				t.Fatal(err)
			}
			MarkPipelineFailed(p.ID, "deployment interrupted")
			if err := repository.DB.First(&record, record.ID).Error; err != nil {
				t.Fatal(err)
			}
			if record.BuildStatus != "success" || record.AfterImage != "registry/web-main:new" || record.DeployStatus == "skipped" {
				t.Fatalf("deployment error mislabeled as build failure: %+v", record)
			}
			if phase == "completed" && (record.DeployStatus != "success" || record.VerifyStatus != "skipped") {
				t.Fatalf("lost rollout success before validation failure: %+v", record)
			}
			if err := repository.DB.First(&p, p.ID).Error; err != nil || p.DeploymentProgress != phase {
				t.Fatalf("progress not persisted: %+v %v", p, err)
			}
		})
	}
}

func TestBeforeVersionReadFailureDoesNotBlockDeployment(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	r := model.ArtifactDeployRecord{DeployBatchNo: "before-error", VersionNo: "v1", PipelineID: 12345, AppName: "web-cdr", DeploymentName: "web-cdr", RuntimeVersionPath: "/usr/share/nginx/html/config.json"}
	if err := repository.DB.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	cause := errors.New("pod exec denied")
	err := CaptureBeforeDeployVersions(context.Background(), r.PipelineID, "web-cdr", "registry/web-cdr:old", runtimeVersionReaderFunc(func(context.Context, model.ArtifactDeployRecord) (string, error) { return "", cause }))
	if err != nil {
		t.Fatalf("optional version read blocked deployment: %v", err)
	}
	if err := repository.DB.First(&r, r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.VersionWarning, cause.Error()) || r.BeforeImage != "registry/web-cdr:old" {
		t.Fatalf("lost recovery evidence: %+v", r)
	}
}

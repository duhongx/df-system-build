package service

import (
	"context"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestRollbackDeployBatchRestoresImagesAndVerifiesPerRecordBusinessVersions(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "version-rollback", VersionNo: "version-rollback", Namespace: "prod", Status: "deployed", TotalCount: 3, SuccessCount: 3}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatalf("create batch: %v", err)
	}
	records := []model.ArtifactDeployRecord{
		{
			DeployBatchNo:             batch.DeployBatchNo,
			VersionNo:                 batch.VersionNo,
			AppName:                   "web-menzhenysz",
			AppCode:                   "04",
			AppType:                   "vue",
			VueRole:                   "sub",
			Namespace:                 "prod",
			DeploymentName:            "web-main",
			BeforeImage:               "registry/web-main:old",
			AfterImage:                "registry/web-main:new",
			BeforeBusinessVersionJSON: `{"xiTongId":"04","version":"2.0.0","date":"2025","branch":"r","commit":"old04"}`,
			AfterBusinessVersionJSON:  `{"xiTongId":"04","version":"2.0.1","date":"2026","branch":"r","commit":"new04"}`,
			PackageVersionJSON:        `{"xiTongId":"04","version":"2.0.1","date":"2026","branch":"r","commit":"new04"}`,
			RuntimeVersionPath:        "/usr/share/nginx/html/apps/04/config.json",
			Status:                    "deployed",
			DeployStatus:              "success",
			VerifyStatus:              "success",
			RollbackStatus:            "none",
		},
		{
			DeployBatchNo:             batch.DeployBatchNo,
			VersionNo:                 batch.VersionNo,
			AppName:                   "web-buliangsj",
			AppCode:                   "88",
			AppType:                   "vue",
			VueRole:                   "sub",
			Namespace:                 "prod",
			DeploymentName:            "web-main",
			BeforeImage:               "registry/web-main:old",
			AfterImage:                "registry/web-main:new",
			BeforeBusinessVersionJSON: `{"xiTongId":"88","version":"2.0.0","date":"2025","branch":"r","commit":"old88"}`,
			AfterBusinessVersionJSON:  `{"xiTongId":"88","version":"2.0.1","date":"2026","branch":"r","commit":"new88"}`,
			PackageVersionJSON:        `{"xiTongId":"88","version":"2.0.1","date":"2026","branch":"r","commit":"new88"}`,
			RuntimeVersionPath:        "/usr/share/nginx/html/apps/88/config.json",
			Status:                    "deployed",
			DeployStatus:              "success",
			VerifyStatus:              "success",
			RollbackStatus:            "none",
		},
		{
			DeployBatchNo:             batch.DeployBatchNo,
			VersionNo:                 batch.VersionNo,
			AppName:                   "his-gateway",
			AppType:                   "java",
			Namespace:                 "prod",
			DeploymentName:            "his-gateway",
			BeforeImage:               "registry/his-gateway:old",
			AfterImage:                "registry/his-gateway:new",
			BeforeBusinessVersionJSON: `{"git":{"branch":"release","commit":{"id":"abc","time":"2026-06-01 10:00:00"}}}`,
			AfterBusinessVersionJSON:  `{"git":{"branch":"release","commit":{"id":"def","time":"2026-06-02 10:00:00"}}}`,
			PackageVersionJSON:        `{"git":{"branch":"release","commit":{"id":"def","time":"2026-06-02 10:00:00"}}}`,
			RuntimeVersionPath:        "http://localhost:8080/actuator/info",
			Status:                    "deployed",
			DeployStatus:              "success",
			VerifyStatus:              "success",
			RollbackStatus:            "none",
		},
	}
	if err := repository.DB.Create(&records).Error; err != nil {
		t.Fatalf("create records: %v", err)
	}

	rollbacker := &fakeDeploymentRollbacker{}
	reader := runtimeVersionReaderFunc(func(_ context.Context, record model.ArtifactDeployRecord) (string, error) {
		return record.BeforeBusinessVersionJSON, nil
	})

	if err := RollbackDeployBatch(context.Background(), batch.DeployBatchNo, rollbacker, reader); err != nil {
		t.Fatalf("rollback batch: %v", err)
	}

	if len(rollbacker.calls) != 2 {
		t.Fatalf("expected 2 deployment rollback calls, got %#v", rollbacker.calls)
	}
	if rollbacker.calls[0] != "prod/his-gateway=registry/his-gateway:old" || rollbacker.calls[1] != "prod/web-main=registry/web-main:old" {
		t.Fatalf("unexpected rollback calls: %#v", rollbacker.calls)
	}

	var got []model.ArtifactDeployRecord
	if err := repository.DB.Where("deploy_batch_no = ?", batch.DeployBatchNo).Order("deployment_name ASC, app_name ASC").Find(&got).Error; err != nil {
		t.Fatalf("find records: %v", err)
	}
	for _, record := range got {
		if record.RollbackStatus != "success" || record.Status != "rolled_back" {
			t.Fatalf("record was not marked rolled back: %#v", record)
		}
		if record.RestoredBusinessVersionJSON == "" {
			t.Fatalf("record %s missing restored business version", record.AppName)
		}
	}

	var gotBatch model.ArtifactDeployBatch
	if err := repository.DB.Where("deploy_batch_no = ?", batch.DeployBatchNo).First(&gotBatch).Error; err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if gotBatch.Status != "rolled_back" || gotBatch.RolledBackCount != 3 {
		t.Fatalf("batch rollback counters wrong: %#v", gotBatch)
	}
}

type fakeDeploymentRollbacker struct {
	calls []string
}

func (r *fakeDeploymentRollbacker) RollbackDeployment(ctx context.Context, namespace, deploymentName, image string) error {
	r.calls = append(r.calls, namespace+"/"+deploymentName+"="+image)
	return nil
}

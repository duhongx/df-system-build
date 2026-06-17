package service

import (
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
)

func TestCreateArtifactDeployRecordsKeepsSubAppVersionsUnderWebMainDeployment(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	version := model.ArtifactVersion{VersionNo: "202606170001", SourceType: "download", Status: "available", DeployableCount: 3}
	if err := repository.DB.Create(&version).Error; err != nil {
		t.Fatalf("create version: %v", err)
	}
	apps := []model.Application{
		{AppName: "web-main", AppType: "vue", VueRole: "main", AppCode: "main"},
		{AppName: "web-menzhenysz", AppType: "vue", VueRole: "sub", AppCode: "04"},
		{AppName: "web-buliangsj", AppType: "vue", VueRole: "sub", AppCode: "88"},
	}
	if err := repository.DB.Create(&apps).Error; err != nil {
		t.Fatalf("create apps: %v", err)
	}
	items := []model.ArtifactVersionItem{
		{VersionNo: version.VersionNo, AppID: apps[0].ID, AppName: apps[0].AppName, AppType: "vue", FileName: "web-main.zip", Deployable: true, PackageVersionJSON: `{"version":"main-new"}`},
		{VersionNo: version.VersionNo, AppID: apps[1].ID, AppName: apps[1].AppName, AppType: "vue", FileName: "04.zip", Deployable: true, PackageVersionJSON: `{"xiTongId":"04","version":"04-new"}`},
		{VersionNo: version.VersionNo, AppID: apps[2].ID, AppName: apps[2].AppName, AppType: "vue", FileName: "88.zip", Deployable: true, PackageVersionJSON: `{"xiTongId":"88","version":"88-new"}`},
	}
	if err := repository.DB.Create(&items).Error; err != nil {
		t.Fatalf("create version items: %v", err)
	}
	pipeline := model.Pipeline{PipelineNo: "web-main-0001", ApplicationID: apps[0].ID, AppName: "web-main", AppType: "vue", BatchID: version.VersionNo}
	if err := repository.DB.Create(&pipeline).Error; err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	err := CreateArtifactDeployRecords(ArtifactDeployRecordInput{
		VersionNo:    version.VersionNo,
		Namespace:    "prod",
		DeployMode:   "immediate",
		TriggerUser:  "admin",
		Pipeline:     pipeline,
		VersionItems: items,
		Applications: apps,
	})
	if err != nil {
		t.Fatalf("create deploy records: %v", err)
	}

	var batch model.ArtifactDeployBatch
	if err := repository.DB.Where("version_no = ?", version.VersionNo).First(&batch).Error; err != nil {
		t.Fatalf("find deploy batch: %v", err)
	}
	if batch.TotalCount != 3 || batch.Namespace != "prod" || batch.DeployMode != "immediate" {
		t.Fatalf("unexpected deploy batch: %#v", batch)
	}

	var records []model.ArtifactDeployRecord
	if err := repository.DB.Where("deploy_batch_no = ?", batch.DeployBatchNo).Order("file_name ASC").Find(&records).Error; err != nil {
		t.Fatalf("find records: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	for _, record := range records {
		if record.DeploymentName != "web-main" {
			t.Fatalf("record %s deployment = %s, want web-main", record.FileName, record.DeploymentName)
		}
		if record.PackageVersionJSON == "" {
			t.Fatalf("record %s missing package business version", record.FileName)
		}
		if record.PipelineID != pipeline.ID {
			t.Fatalf("record %s pipeline id = %d, want %d", record.FileName, record.PipelineID, pipeline.ID)
		}
	}
}

func setupArtifactDeployRecordTestDB(t *testing.T) {
	t.Helper()
	if logger.Log == nil {
		logger.Init("error", "stdout", "")
	}
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.DB.AutoMigrate(
		&model.Application{},
		&model.Pipeline{},
		&model.ArtifactVersion{},
		&model.ArtifactVersionItem{},
		&model.ArtifactDeployBatch{},
		&model.ArtifactDeployRecord{},
	); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
}

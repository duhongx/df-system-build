package handler

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func TestBatchRolloutIncludesReadyTargetsAfterPreparationFailure(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}); err != nil {
		t.Fatal(err)
	}
	original := scheduler.DefaultScheduler
	scheduler.DefaultScheduler = &scheduler.BuildScheduler{}
	defer func() { scheduler.DefaultScheduler = original }()
	batch := model.ArtifactDeployBatch{DeployBatchNo: "two-step-batch", VersionNo: "two-step-batch", Status: "failed", DeployMode: "two_step", TotalCount: 5, FailedCount: 1}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	var pipelines []model.Pipeline
	for _, name := range []string{"bad-checkpoint", "web-main", "service-a", "failed-build"} {
		status := "IMAGE_READY"
		if name == "failed-build" {
			status = "FAILED"
		}
		p := model.Pipeline{PipelineNo: name, AppName: name, AppType: "java", BatchID: batch.DeployBatchNo, Status: status, DeployMode: "artifact_image", ImageName: "registry/" + name + ":prepared"}
		if err := repository.DB.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		pipelines = append(pipelines, p)
		stages := []model.PipelineStage{
			{PipelineID: p.ID, StageCode: "BUILD_IMAGE", StageOrder: 1, Status: "SUCCESS"},
			{PipelineID: p.ID, StageCode: "PUSH_IMAGE", StageOrder: 2, Status: "SUCCESS"},
			{PipelineID: p.ID, StageCode: "K8S_DEPLOY", StageOrder: 3, Status: "PENDING"},
			{PipelineID: p.ID, StageCode: "NOTIFY", StageOrder: 4, Status: "PENDING"},
		}
		if name == "bad-checkpoint" {
			stages[1].Status = "FAILED"
		}
		if err := repository.DB.Create(&stages).Error; err != nil {
			t.Fatal(err)
		}
		copies := 1
		if name == "web-main" {
			copies = 2
		}
		for i := 0; i < copies; i++ {
			r := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, PipelineID: p.ID, AppName: name, DeploymentName: name, Status: "image_ready", DeployStatus: "waiting", AfterImage: p.ImageName}
			if name == "failed-build" {
				r.Status = "failed"
				r.ErrorMessage = "build failed"
			}
			if err := repository.DB.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	h := NewBatchDeployHandler()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/deploy-batches?readyForRollout=true", nil)
	h.ListArtifactDeployBatches(c)
	var listed struct {
		Code int
		Data struct{ Batches []model.ArtifactDeployBatch }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Code != 0 || len(listed.Data.Batches) != 1 {
		t.Fatalf("partially failed batch hidden: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "batchNo", Value: batch.DeployBatchNo}}
	h.RolloutArtifactDeployBatch(c)
	var reply struct {
		Code int
		Data struct {
			Pipelines []model.Pipeline
			Errors    []string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Code != 0 || len(reply.Data.Pipelines) != 2 || len(reply.Data.Errors) != 1 || !strings.Contains(reply.Data.Errors[0], "构建和推送尚未完成") {
		t.Fatalf("batch submission did not continue: %s", w.Body.String())
	}
	for _, p := range reply.Data.Pipelines {
		if p.Status != "DEPLOYING" || p.ImageName == "" {
			t.Fatalf("lost image/status: %+v", p)
		}
	}
	if scheduler.DefaultScheduler.GetQueueSize() != 2 {
		t.Fatalf("shared target queued more than once: %d", scheduler.DefaultScheduler.GetQueueSize())
	}
	if _, err := scheduler.DefaultScheduler.StartRollout(reply.Data.Pipelines[0].ID); err == nil {
		t.Fatal("duplicate rollout accepted")
	}
	var readyRecords []model.ArtifactDeployRecord
	if err := repository.DB.Where("pipeline_id IN ?", []uint{pipelines[1].ID, pipelines[2].ID}).Find(&readyRecords).Error; err != nil {
		t.Fatal(err)
	}
	if len(readyRecords) != 3 {
		t.Fatalf("missing shared child records: %d", len(readyRecords))
	}
	for _, record := range readyRecords {
		if record.Status != "deploying" || record.DeployStatus != "queued" {
			t.Fatalf("missing queued progress: %+v", record)
		}
	}
}

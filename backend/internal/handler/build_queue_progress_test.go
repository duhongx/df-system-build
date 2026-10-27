package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/gin-gonic/gin"
)

func TestBuildQueueAndBatchedProgressRespectDatabaseErrors(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.Pipeline{
		{PipelineNo: "queue-a", AppName: "service-a", BatchID: "version-a", Status: "RUNNING", TriggerUser: "test"},
		{PipelineNo: "queue-b", AppName: "service-b", BatchID: "version-a", Status: "PENDING", TriggerUser: "test"},
		{PipelineNo: "queue-c", AppName: "service-c", BatchID: "version-b", Status: "IMAGE_READY", TriggerUser: "test"},
	}
	if err := repository.DB.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Create(&model.PipelineStage{PipelineID: rows[0].ID, StageCode: "BUILD_IMAGE", StageName: "构建镜像", StageOrder: 1, Status: "RUNNING"}).Error; err != nil {
		t.Fatal(err)
	}
	contextFor := func(ctx context.Context, path string) (*gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", path, nil).WithContext(ctx)
		return c, w
	}
	c, w := contextFor(context.Background(), "/build-queue")
	NewPipelineHandler().GetQueue(c)
	var queue struct {
		Code int
		Data struct{ Running, Pending []model.Pipeline }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &queue); err != nil {
		t.Fatal(err)
	}
	if queue.Code != 0 || len(queue.Data.Running) != 1 || len(queue.Data.Pending) != 1 || len(queue.Data.Running[0].Stages) != 1 {
		t.Fatalf("wrong queue: %s", w.Body.String())
	}
	c, w = contextFor(context.Background(), "/batch-deploy/artifact-versions/version-a/pipelines")
	c.Params = gin.Params{{Key: "versionNo", Value: "version-a"}}
	NewBatchDeployHandler().GetArtifactVersionPipelines(c)
	var progress struct {
		Code int
		Data struct{ Pipelines []model.Pipeline }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Code != 0 || len(progress.Data.Pipelines) != 2 || len(progress.Data.Pipelines[0].Stages) != 1 {
		t.Fatalf("batch progress missing or mixed: %s", w.Body.String())
	}
	for _, row := range progress.Data.Pipelines {
		if row.BatchID != "version-a" {
			t.Fatal("another batch leaked into current submission")
		}
	}
	lock := repository.DB.Begin()
	if lock.Error != nil {
		t.Fatal(lock.Error)
	}
	defer lock.Rollback()
	if err := lock.Exec("LOCK TABLE pipelines IN ACCESS EXCLUSIVE MODE").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c, w = contextFor(ctx, "/build-queue")
	started := time.Now()
	NewPipelineHandler().GetQueue(c)
	if time.Since(started) > time.Second || w.Code != 503 || !strings.Contains(w.Body.String(), "读取运行任务失败") {
		t.Fatalf("database timeout was hidden as an empty queue: HTTP=%d %s", w.Code, w.Body.String())
	}
	c, w = contextFor(ctx, "/batch-deploy/artifact-versions/version-a/pipelines")
	c.Params = gin.Params{{Key: "versionNo", Value: "version-a"}}
	NewBatchDeployHandler().GetArtifactVersionPipelines(c)
	if w.Code != 503 {
		t.Fatalf("failed progress read reported success: %s", w.Body.String())
	}
}

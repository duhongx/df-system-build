package handler

import (
	"bytes"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNestedFrontendArtifactsExecuteAsOneTarget(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}); err != nil {
		t.Fatal(err)
	}
	original := scheduler.DefaultScheduler
	scheduler.DefaultScheduler = &scheduler.BuildScheduler{}
	t.Cleanup(func() { scheduler.DefaultScheduler = original })
	t.Chdir(t.TempDir())
	apps := []model.Application{{AppName: "web-main", AppType: "vue", VueRole: "main", GitRepo: "test"}, {AppName: "web-alpha", AppType: "vue", VueRole: "sub", AppCode: "04", GitRepo: "test"}}
	if err := repository.DB.Create(&apps).Error; err != nil {
		t.Fatal(err)
	}
	root, err := batchUploadSourceRoot("download")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "nested-")
	if err != nil {
		t.Fatal(err)
	}
	mustCreateZipWithFiles(t, filepath.Join(dir, "web/web-main.zip"), map[string]string{"index.html": "main"})
	mustCreateZipWithFiles(t, filepath.Join(dir, "children/04.zip"), map[string]string{"index.html": "child"})
	version := filepath.Base(dir)
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{VersionNo: version, SourceType: "download", TargetPath: dir}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"sourceDir": dir, "batchId": version, "namespace": "default", "items": []map[string]any{{"fileName": "web-main.zip", "appId": apps[0].ID}, {"fileName": "04.zip", "appId": apps[1].ID}}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/execute", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	NewBatchDeployHandler().Execute(c)
	var reply struct {
		Code int
		Data struct {
			Pipelines     []struct{ ID uint }
			Errors        []string
			ConsumedFiles []string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Code != 0 || len(reply.Data.Errors) != 0 || len(reply.Data.Pipelines) != 1 || len(reply.Data.ConsumedFiles) != 2 {
		t.Fatalf("nested inputs rejected: %s", w.Body.String())
	}
	var pipeline model.Pipeline
	if err := repository.DB.First(&pipeline, reply.Data.Pipelines[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	var artifacts []model.FrontendArtifact
	if err := json.Unmarshal([]byte(pipeline.FrontendArtifactsJSON), &artifacts); err != nil {
		t.Fatal(err)
	}
	ws := types.Workspace{BaseDir: "workspaces", AppName: pipeline.AppName, PipelineID: pipeline.ID}
	if len(artifacts) != 2 {
		t.Fatalf("lost frontend artifacts: %+v", artifacts)
	}
	for _, artifact := range artifacts {
		_, hash, err := fileSHA256(filepath.Join(ws.SourceDir(), artifact.FileName))
		if err != nil || hash != artifact.SHA256 {
			t.Fatalf("copied wrong input: %+v hash=%s err=%v", artifact, hash, err)
		}
	}
	// Existing uploads receive actual application roles without a data migration.
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "versionNo", Value: version}}
	NewBatchDeployHandler().GetArtifactVersion(c)
	var detail struct {
		Code int
		Data struct{ Items []model.ArtifactVersionItem }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Code != 0 || len(detail.Data.Items) != 2 {
		t.Fatalf("missing item roles: %s", w.Body.String())
	}
	for _, item := range detail.Data.Items {
		if item.VueRole == "" {
			t.Fatalf("missing role: %+v", item)
		}
	}
}

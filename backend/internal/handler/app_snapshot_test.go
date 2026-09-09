package handler

import (
	"bytes"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteUsesValidatedApplicationSnapshot(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}); err != nil {
		t.Fatal(err)
	}
	original := scheduler.DefaultScheduler
	scheduler.DefaultScheduler = &scheduler.BuildScheduler{}
	defer func() { scheduler.DefaultScheduler = original }()
	t.Chdir(t.TempDir())
	apps := []model.Application{{AppName: "web-main", AppType: "vue", VueRole: "main", GitRepo: "test"}, {AppName: "service", AppType: "java", GitRepo: "test"}}
	if err := repository.DB.Create(&apps).Error; err != nil {
		t.Fatal(err)
	}
	root, err := batchUploadSourceRoot("upload")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, "reread-")
	if err != nil {
		t.Fatal(err)
	}
	mustCreateZipWithFiles(t, filepath.Join(dir, "web-main.zip"), map[string]string{"index.html": "main"})
	mustCreateZipWithFiles(t, filepath.Join(dir, "service.jar"), map[string]string{"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n"})
	version := filepath.Base(dir)
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{VersionNo: version, SourceType: "upload", TargetPath: dir}); err != nil {
		t.Fatal(err)
	}
	queries := 0
	cb := "review:app-reread"
	if err := repository.DB.Callback().Query().Before("gorm:query").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "applications" {
			queries++
			if queries == 3 {
				tx.AddError(errors.New("application query unavailable"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer repository.DB.Callback().Query().Remove(cb)
	body, _ := json.Marshal(map[string]any{"batchId": version, "namespace": "default", "items": []map[string]any{{"fileName": "web-main.zip", "appId": apps[0].ID}, {"fileName": "service.jar", "appId": apps[1].ID}}})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/execute", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	NewBatchDeployHandler().Execute(c)
	var reply struct {
		Code int
		Data struct {
			Pipelines []any
			Errors    []string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Code != 0 || len(reply.Data.Pipelines) != 2 || len(reply.Data.Errors) != 0 || queries != 2 {
		t.Fatalf("unexpected application queries=%d response=%s", queries, w.Body.String())
	}
	var records int64
	if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("version_no = ?", version).Count(&records).Error; err != nil {
		t.Fatal(err)
	}
	if records != 2 {
		t.Fatalf("missing deployment records: %d", records)
	}
}

package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func TestArtifactUpdateClaimsPersistAndAreAtomic(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	mustCreateZip(t, filepath.Join(dir, "his-gateway.jar"))
	mustCreateZip(t, filepath.Join(dir, "web-menzhenysz.zip"))
	input := artifactVersionInput{VersionNo: "once-claims", SourceType: "upload", TargetPath: dir}
	version, items, err := persistArtifactVersionFromDir(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := claimArtifactUpdates(version.VersionNo, dir, []string{"his-gateway.jar"}); err != nil {
		t.Fatal(err)
	}
	// An invalid mixed selection must not consume the previously unused input.
	if err := claimArtifactUpdates(version.VersionNo, dir, []string{"web-menzhenysz.zip", "his-gateway.jar"}); err == nil {
		t.Fatal("repeated input accepted")
	}
	used, err := usedArtifactFiles(repository.DB, version.VersionNo)
	if err != nil || !used["his-gateway.jar"] || used["web-menzhenysz.zip"] {
		t.Fatalf("unexpected claims: %v, %v", used, err)
	}
	// Workspace metadata refresh deletes/recreates item rows; usage must survive it.
	version, items, err = refreshArtifactVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	if err := markArtifactUsage(&version, items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.FileName == "his-gateway.jar" && !item.UpdateUsed {
			t.Fatal("refresh reset usage")
		}
	}
	if err := claimArtifactUpdates(version.VersionNo, t.TempDir(), []string{"web-menzhenysz.zip"}); err == nil {
		t.Fatal("accepted mismatched directory")
	}
	// Records predating this feature remain consumed even after failure or rollback.
	record := model.ArtifactDeployRecord{VersionNo: version.VersionNo, FileName: "web-menzhenysz.zip", Status: "failed", RollbackStatus: "success"}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := claimArtifactUpdates(version.VersionNo, dir, []string{record.FileName}); err == nil {
		t.Fatal("legacy failed/rolled-back input accepted")
	}
}

func TestArtifactUploadExecuteOnceAndReupload(t *testing.T) {
	setupArtifactVersionTestDB(t)
	// Offline customers can upload and queue image preparation without source capability.
	if err := repository.NewSettingsRepo().Set("customer_env", "浙江"); err != nil {
		t.Fatal(err)
	}
	if err := repository.RequireSourceBuild(); err == nil {
		t.Fatal("expected source builds to be disabled")
	}
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // Pipeline copies are isolated; no real build worker runs.
	original := scheduler.DefaultScheduler
	scheduler.DefaultScheduler = &scheduler.BuildScheduler{}
	t.Cleanup(func() { scheduler.DefaultScheduler = original })
	h := NewBatchDeployHandler()
	var app model.Application
	if err := repository.DB.Where("app_name = ?", "his-gateway").First(&app).Error; err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(t.TempDir(), "his-gateway.jar")
	mustCreateZip(t, jar)
	contents, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	upload := func() (model.ArtifactVersion, model.ArtifactVersionItem) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile("files", "his-gateway.jar")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(contents); err != nil {
			t.Fatal(err)
		}
		if err := mw.Close(); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/batch-deploy/upload", &body)
		c.Request.Header.Set("Content-Type", mw.FormDataContentType())
		h.Upload(c)
		var reply struct {
			Code int
			Data struct {
				BatchID string `json:"batchId"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Code != 0 || reply.Data.BatchID == "" {
			t.Fatalf("upload failed: %s", w.Body.String())
		}
		var version model.ArtifactVersion
		var item model.ArtifactVersionItem
		if err := repository.DB.Where("version_no = ?", reply.Data.BatchID).First(&version).Error; err != nil {
			t.Fatal(err)
		}
		if err := repository.DB.Where("version_no = ?", reply.Data.BatchID).First(&item).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(version.TargetPath) })
		return version, item
	}
	execute := func(version model.ArtifactVersion, wantSuccess bool) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"batchId": version.VersionNo, "namespace": "default", "items": []map[string]any{{"fileName": "his-gateway.jar", "appId": app.ID}}})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/batch-deploy/execute", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Execute(c)
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
		if wantSuccess {
			if reply.Code != 0 || len(reply.Data.Pipelines) != 1 || len(reply.Data.Errors) > 0 {
				t.Fatalf("execute failed: %s", w.Body.String())
			}
			var queued model.Pipeline
			if err := repository.DB.First(&queued, reply.Data.Pipelines[0].ID).Error; err != nil {
				t.Fatal(err)
			}
			if queued.DeployMode != "artifact_image" || queued.TaskID != 0 {
				t.Fatalf("upload entered source workflow: %+v", queued)
			}
		} else if reply.Code == 0 || !strings.Contains(w.Body.String(), "已提交过更新") {
			t.Fatalf("duplicate accepted: %s", w.Body.String())
		}
	}
	first, firstItem := upload()
	execute(first, true)
	execute(first, false)
	if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("version_no = ?", first.VersionNo).Updates(map[string]any{"status": "failed", "rollback_status": "success"}).Error; err != nil {
		t.Fatal(err)
	}
	execute(first, false)
	// The detail API drives the disabled state after a refresh.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/batch-deploy/artifact-versions/"+first.VersionNo, nil)
	c.Params = gin.Params{{Key: "versionNo", Value: first.VersionNo}}
	h.GetArtifactVersion(c)
	var detail struct {
		Code int
		Data struct {
			Version model.ArtifactVersion
			Items   []model.ArtifactVersionItem
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Code != 0 || detail.Data.Version.RemainingDeployableCount != 0 || len(detail.Data.Items) != 1 || !detail.Data.Items[0].UpdateUsed {
		t.Fatalf("usage missing: %s", w.Body.String())
	}
	second, secondItem := upload()
	if first.VersionNo == second.VersionNo || firstItem.SHA256 != secondItem.SHA256 {
		t.Fatal("expected new upload identity for identical bytes")
	}
	execute(second, true)
	var count int64
	if err := repository.DB.Model(&model.Pipeline{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected two pipelines for two uploads, got %d", count)
	}
}

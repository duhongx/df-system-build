package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/internal/service"
	"github.com/gin-gonic/gin"
)

func TestBatchContinuesOtherTargetsAfterFailure(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Pipeline{}, &model.PipelineStage{}); err != nil {
		t.Fatal(err)
	}
	original := scheduler.DefaultScheduler
	scheduler.DefaultScheduler = &scheduler.BuildScheduler{} // Queue only; no Docker/Kubernetes execution.
	t.Cleanup(func() { scheduler.DefaultScheduler = original })
	apps := []model.Application{}
	for _, name := range []string{"service-a", "service-b", "service-c", "service-d", "service-e", "service-f"} {
		apps = append(apps, model.Application{AppName: name, AppType: "java", GitRepo: "test"})
	}
	apps = append(apps, model.Application{AppName: "web-main", AppType: "vue", VueRole: "main", GitRepo: "test"}, model.Application{AppName: "web-alpha", AppType: "vue", VueRole: "sub", AppCode: "11", GitRepo: "test"}, model.Application{AppName: "web-beta", AppType: "vue", VueRole: "sub", AppCode: "12", GitRepo: "test"}, model.Application{AppName: "web-cdr", AppType: "vue", VueRole: "standalone", GitRepo: "test"})
	if err := repository.DB.Create(&apps).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		badIndex     int
		kind         string
		wantConsumed int
	}{
		{"invalid-java", 0, "invalid", 9},
		{"invalid-sub", 7, "invalid", 7},
		{"already-used", 0, "used", 9},
		{"copy-failure", 0, "copy", 10},
		{"unknown-app", 0, "unknown", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			root, err := batchUploadSourceRoot("upload")
			if err != nil {
				t.Fatal(err)
			}
			dir, err := os.MkdirTemp(root, "continue-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			versionNo := filepath.Base(dir)
			items := []map[string]any{}
			badFile := ""
			for i, app := range apps {
				file := app.AppName + ".zip"
				if app.VueRole == "sub" {
					file = app.AppCode + ".zip"
				}
				if app.AppType == "java" {
					file = app.AppName + ".jar"
				}
				mustCreateZipWithFiles(t, filepath.Join(dir, file), map[string]string{"index.html": "test", "META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n"})
				if i == tc.badIndex {
					badFile = file
				}
				items = append(items, map[string]any{"fileName": file, "appId": app.ID})
			}
			if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{VersionNo: versionNo, SourceType: "upload", TargetPath: dir}); err != nil {
				t.Fatal(err)
			}
			wantPipelines := 7
			if tc.kind == "unknown" {
				wantPipelines = 8
				badFile = "missing.jar"
				items = append(items, map[string]any{"fileName": badFile, "appId": uint(999999)})
			}
			switch tc.kind {
			case "invalid":
				if err := os.WriteFile(filepath.Join(dir, badFile), []byte("broken archive"), 0644); err != nil {
					t.Fatal(err)
				}
			case "used":
				if err := claimArtifactUpdates(versionNo, dir, []string{badFile}); err != nil {
					t.Fatal(err)
				}
			case "copy":
				if err := os.MkdirAll("workspaces", 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join("workspaces", apps[tc.badIndex].AppName), []byte("not a directory"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			body, _ := json.Marshal(map[string]any{"batchId": versionNo, "namespace": "default", "items": items})
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/execute", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			NewBatchDeployHandler().Execute(c)
			var reply struct {
				Code int
				Data struct {
					Pipelines []struct {
						ID  uint
						App string
					}
					Errors        []string
					ConsumedFiles []string
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Code != 0 || len(reply.Data.Pipelines) != wantPipelines || len(reply.Data.ConsumedFiles) != tc.wantConsumed || !strings.Contains(strings.Join(reply.Data.Errors, ";"), badFile) {
				t.Fatalf("other targets did not continue: %s", w.Body.String())
			}
			for _, p := range reply.Data.Pipelines {
				if (tc.kind != "unknown" && p.App == apps[tc.badIndex].AppName) || (tc.kind == "invalid" && tc.badIndex == 7 && p.App == "web-main") {
					t.Fatalf("failed group entered queue: %+v", p)
				}
			}
			var pipelines []model.Pipeline
			if err := repository.DB.Where("batch_id = ?", versionNo).Find(&pipelines).Error; err != nil {
				t.Fatal(err)
			}
			if tc.kind == "copy" {
				found := false
				for _, p := range pipelines {
					if p.AppName == apps[0].AppName {
						found = true
						if p.Status != "FAILED" || p.ErrorStage != "PREPARE_UPDATE" || !strings.Contains(p.ErrorMessage, "not a directory") {
							t.Fatalf("missing persistent cause: %+v", p)
						}
					}
				}
				if !found {
					t.Fatal("missing failed pipeline")
				}
				for _, queued := range reply.Data.Pipelines {
					if err := service.CaptureAfterDeployVersions(c.Request.Context(), queued.ID, "", "registry/test:new", nil); err != nil {
						t.Fatal(err)
					}
				}
				var batch model.ArtifactDeployBatch
				if err := repository.DB.Where("deploy_batch_no = ?", versionNo).First(&batch).Error; err != nil {
					t.Fatal(err)
				}
				if batch.TotalCount != 10 || batch.SuccessCount != 9 || batch.FailedCount != 1 || batch.Status != "failed" {
					t.Fatalf("incorrect batch summary after preparation failure: %+v", batch)
				}
				var failed model.ArtifactDeployRecord
				if err := repository.DB.Where("deploy_batch_no = ? AND status = ?", versionNo, "failed").First(&failed).Error; err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(failed.ErrorMessage, "not a directory") || failed.DeployStatus != "skipped" {
					t.Fatalf("missing preparation cause: %+v", failed)
				}

			} else if len(pipelines) != wantPipelines {
				t.Fatalf("unexpected pipeline count: %d", len(pipelines))
			}
		})
	}
}

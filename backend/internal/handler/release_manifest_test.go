package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"

	"github.com/gin-gonic/gin"
)

func TestArtifactChunkUploadReleaseManifestIsMetadataOnly(t *testing.T) {
	for _, raw := range []string{`{"appVersion":"2.15.3","readme":"业务修复","custom":{"a":1}}`, `{"appVersion":`} {
		t.Run(fmt.Sprintf("valid=%t", json.Valid([]byte(raw))), func(t *testing.T) {
			s, r := uploadTestStore(t)
			s.apps = func() ([]model.Application, error) {
				return []model.Application{{ID: 1, AppName: "service", AppType: "java", Enabled: true}}, nil
			}
			jar := filepath.Join(t.TempDir(), "service.jar")
			if err := createTestZip(jar); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(jar)
			if err != nil {
				t.Fatal(err)
			}
			captured := make(chan model.ReleaseMetadata, 1)
			s.persist = func(input artifactVersionInput, items []model.ArtifactVersionItem) error {
				if len(items) != 1 || items[0].FileName != "service.jar" || !items[0].Deployable {
					return fmt.Errorf("manifest changed artifact matching: %+v", items)
				}
				captured <- service.ReadReleaseManifest(input.TargetPath)
				return nil
			}
			createTestUpload(t, r, []artifactUploadFile{{Path: "release/service.jar", Size: int64(len(data))}, {Path: "release/version.json", Size: int64(len(raw))}})
			for i, body := range [][]byte{data, []byte(raw)} {
				if w := putTestChunk(r, i, 0, body); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			uploadSessionReply(t, uploadRequest(r, "POST", "/uploads/"+uploadTestID+"/complete", nil, nil))
			session := waitTestUpload(t, r, "completed")
			got := <-captured
			if got.ReleaseManifestJSON != raw || (got.ReleaseManifestError == "") != json.Valid([]byte(raw)) {
				t.Fatalf("release metadata lost after publishing: %+v", got)
			}
			if session.Result.Count != 1 || len(session.Result.Success) != 1 || len(session.Result.Failed) != 0 || session.CompletedFiles != 2 {
				t.Fatalf("manifest counted as deployment artifact: %+v", session)
			}
		})
	}
}

func TestArtifactUploadRejectsOversizedOrDuplicateManifest(t *testing.T) {
	for _, files := range [][]artifactUploadFile{
		{{Path: "version.json", Size: service.MaxReleaseManifestBytes + 1}},
		{{Path: "one/version.json", Size: 10}, {Path: "two/version.json", Size: 10}},
		{{Path: "other.json", Size: 10}},
	} {
		_, r := uploadTestStore(t)
		body, _ := json.Marshal(map[string]any{"id": uploadTestID, "files": files})
		if w := uploadRequest(r, "POST", "/uploads", body, nil); w.Code == 200 {
			t.Fatal("invalid metadata file accepted")
		}
	}
	if _, err := safeArtifactFileName("version.json"); err == nil {
		t.Fatal("business metadata became a deployment input")
	}
}

func TestDownloadedReleaseMetadataIgnoresCachedAndFailedManifests(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "version.json")
	if err := os.WriteFile(manifest, []byte(`{"appVersion":"downloaded","business":"业务信息"}`), 0600); err != nil {
		t.Fatal(err)
	}
	item := downloadJobItem{Name: "version.json", LocalPath: manifest, Status: "success"}
	if got := downloadedReleaseMetadata([]downloadJobItem{item}); got.AppVersion != "downloaded" || got.ReleaseManifestError != "" {
		t.Fatalf("downloaded business metadata missing: %+v", got)
	}
	item.Status, item.Error = "failed", "connection lost"
	if got := downloadedReleaseMetadata([]downloadJobItem{item}); got.AppVersion != "" || got.ReleaseManifestError == "" {
		t.Fatalf("failed download incorrectly reused cached metadata: %+v", got)
	}
	if got := downloadedReleaseMetadata(nil); got.AppVersion != "" || got.ReleaseManifestJSON != "" {
		t.Fatalf("unselected manifest reused from disk: %+v", got)
	}
	item.Status = "skipped"
	if got := downloadedReleaseMetadata([]downloadJobItem{item, item}); got.ReleaseManifestError == "" || got.AppVersion != "" {
		t.Fatalf("ambiguous release silently selected: %+v", got)
	}
	// The legacy import endpoint also copies business metadata, while returning
	// only JAR/ZIP files as deployment inputs.
	remote := fakeRemoteFS{dirs: map[string][]fakeRemoteEntry{"/release": {{name: "version.json"}, {name: "service.jar"}}}, files: map[string]string{"/release/version.json": `{"appVersion":"legacy-download"}`, "/release/service.jar": mustZipString(t)}}
	files, err := importArtifactsFromRemoteDir(remote, "/release", t.TempDir())
	if err != nil || len(files) != 1 || files[0] != "service.jar" {
		t.Fatalf("legacy download treated metadata as a deployable file: %v %v", files, err)
	}
}

func TestBusinessReleasePersistenceSnapshotsAndAPIs(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	mustCreateZipWithFiles(t, filepath.Join(dir, "04.zip"), map[string]string{"dist/config.json": `{"xiTongId":"04","version":"application-commit-version"}`})
	raw := []byte(`{"appVersion":"2.15.3","releaseTime":"2026-10-26 18:00:00","readme":"业务修复","featureList":["医保调整"],"hospital":"滨港医院"}`)
	writeManifest := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "version.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(raw)
	input := artifactVersionInput{VersionNo: "business-upload-1", SourceType: "upload", LocalDir: dir, TargetPath: dir}
	version, items, err := persistArtifactVersionFromDir(input)
	if err != nil || version.AppVersion != "2.15.3" || len(items) != 1 || !items[0].Deployable || !bytes.Contains([]byte(items[0].PackageVersionJSON), []byte("application-commit-version")) {
		t.Fatalf("business version changed matching/package versions: %+v %+v %v", version, items, err)
	}
	writeManifest([]byte(`{"appVersion":"mutated","readme":"different"}`))
	refreshed, _, err := refreshArtifactVersion(version)
	if err != nil || refreshed.ReleaseMetadata != version.ReleaseMetadata {
		t.Fatalf("matching refresh rewrote business audit snapshot: %+v %v", refreshed, err)
	}
	writeManifest(raw)
	input.VersionNo = "business-upload-2"
	if _, _, err := persistArtifactVersionFromDir(input); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := repository.DB.Model(&model.ArtifactVersion{}).Where("app_version = ?", "2.15.3").Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("re-upload overwrote the business version's history: %d %v", count, err)
	}
	router := gin.New()
	h := &BatchDeployHandler{}
	router.GET("/versions", h.ListArtifactVersions)
	router.GET("/versions/:versionNo", h.GetArtifactVersion)
	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest("GET", "/versions", nil))
	if list.Code != 200 || !bytes.Contains(list.Body.Bytes(), []byte(`"appVersion":"2.15.3"`)) || bytes.Contains(list.Body.Bytes(), []byte("releaseManifestJson")) {
		t.Fatalf("list omits business version or embeds full manifests: %s", list.Body.String())
	}
	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, httptest.NewRequest("GET", "/versions/business-upload-1", nil))
	if detail.Code != 200 || !bytes.Contains(detail.Body.Bytes(), []byte("releaseManifestJson")) || !bytes.Contains(detail.Body.Bytes(), []byte("滨港医院")) {
		t.Fatalf("detail lost business content: %s", detail.Body.String())
	}
	// A deliberate re-download of an unsubmitted record replaces metadata from
	// that download; subsequent matching refreshes still preserve its snapshot.
	reimport := service.ParseReleaseManifest([]byte(`{"appVersion":"redownloaded","readme":"重新下载的业务说明"}`))
	input.ReleaseMetadata = &reimport
	changed, _, err := persistArtifactVersionFromDir(input)
	if err != nil || changed.AppVersion != "redownloaded" {
		t.Fatalf("explicit re-import kept stale metadata: %+v %v", changed, err)
	}
	input.ReleaseMetadata = nil
	writeManifest([]byte(`{"appVersion":`))
	input.VersionNo = "business-upload-invalid"
	invalid, invalidItems, err := persistArtifactVersionFromDir(input)
	if err != nil || invalid.ReleaseManifestError == "" || invalid.Status != "available" || len(invalidItems) != 1 || !invalidItems[0].Deployable {
		t.Fatalf("metadata error blocked the existing artifact workflow: %+v %v", invalid, err)
	}
}

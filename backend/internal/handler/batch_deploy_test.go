package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/config"
	"df-build-server/pkg/logger"

	"github.com/gin-gonic/gin"
)

func TestSafeArtifactFileNameStripsPathAndRejectsUnsupportedExt(t *testing.T) {
	name, err := safeArtifactFileName("../../web-main.zip")
	if err != nil {
		t.Fatalf("expected zip filename to be accepted: %v", err)
	}
	if name != "web-main.zip" {
		t.Fatalf("expected basename web-main.zip, got %q", name)
	}

	if _, err := safeArtifactFileName("web-main.tar.gz"); err == nil {
		t.Fatalf("expected unsupported extension to be rejected")
	}
}

func TestResolveBatchSourceDirRejectsPathOutsideUploadRoot(t *testing.T) {
	outside := t.TempDir()
	if _, err := resolveBatchSourceDir(outside, ""); err == nil {
		t.Fatalf("expected source dir outside batch upload root to be rejected")
	}
}

func TestResolveBatchSourceDirRequiresBatchOrSourceDir(t *testing.T) {
	if _, err := resolveBatchSourceDir("", ""); err == nil {
		t.Fatalf("expected empty source dir and batch id to be rejected")
	}
}

func TestResolveBatchSourceDirAllowsBatchIDUnderUploadRoot(t *testing.T) {
	dir, err := resolveBatchSourceDir("", "batch-123")
	if err != nil {
		t.Fatalf("expected batch id to resolve: %v", err)
	}
	if filepath.Base(dir) != "batch-123" {
		t.Fatalf("expected resolved dir to end with batch id, got %s", dir)
	}
}

func TestValidateExecuteArtifactRejectsMismatchedApp(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "other-service.jar")
	if err := createTestZip(artifact); err != nil {
		t.Fatalf("create test jar: %v", err)
	}

	app := model.Application{AppName: "his-gateway", AppType: "java"}
	if err := validateExecuteArtifact(dir, "other-service.jar", app); err == nil {
		t.Fatalf("expected mismatched artifact/app pair to be rejected")
	}
}

func TestCopyFileSimpleReturnsErrorForMissingSource(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "target.jar")
	if err := copyFileSimple(filepath.Join(t.TempDir(), "missing.jar"), dst); err == nil {
		t.Fatalf("expected missing source copy to fail")
	}
}

func TestCollectArtifactFilesFromDirRecursivelyFindsJarAndZip(t *testing.T) {
	root := t.TempDir()
	mustCreateZip(t, filepath.Join(root, "his-gateway.jar"))
	mustCreateZip(t, filepath.Join(root, "nested", "web-main.zip"))
	if err := os.WriteFile(filepath.Join(root, "nested", "readme.txt"), []byte("ignore"), 0644); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	files, err := collectArtifactFilesFromDir(root)
	if err != nil {
		t.Fatalf("collect artifacts: %v", err)
	}
	sort.Strings(files)

	expected := []string{"his-gateway.jar", "web-main.zip"}
	if len(files) != len(expected) {
		t.Fatalf("expected %d files, got %d: %#v", len(expected), len(files), files)
	}
	for i := range expected {
		if files[i] != expected[i] {
			t.Fatalf("expected files %#v, got %#v", expected, files)
		}
	}
}

func TestImportArtifactsFromRemoteDirCopiesArtifactsIntoBatchDir(t *testing.T) {
	fs := fakeRemoteFS{
		dirs: map[string][]fakeRemoteEntry{
			"/release": {
				{name: "his-gateway.jar"},
				{name: "nested", dir: true},
				{name: "notes.txt"},
			},
			"/release/nested": {
				{name: "web-main.zip"},
			},
		},
		files: map[string]string{
			"/release/his-gateway.jar":     mustZipString(t),
			"/release/nested/web-main.zip": mustZipString(t),
			"/release/notes.txt":           "ignore",
		},
	}

	dest := t.TempDir()
	files, err := importArtifactsFromRemoteDir(fs, "/release", dest)
	if err != nil {
		t.Fatalf("import remote artifacts: %v", err)
	}
	sort.Strings(files)

	expected := []string{"his-gateway.jar", "web-main.zip"}
	if len(files) != len(expected) {
		t.Fatalf("expected %d imported files, got %d: %#v", len(expected), len(files), files)
	}
	for i := range expected {
		if files[i] != expected[i] {
			t.Fatalf("expected files %#v, got %#v", expected, files)
		}
		if _, err := os.Stat(filepath.Join(dest, expected[i])); err != nil {
			t.Fatalf("expected imported file %s: %v", expected[i], err)
		}
	}
}

func TestDownloadRemoteDirToLocalPreservesTreeAndReportsArtifacts(t *testing.T) {
	fs := fakeRemoteFS{
		dirs: map[string][]fakeRemoteEntry{
			"/release": {
				{name: "his-gateway.jar"},
				{name: "nested", dir: true},
				{name: "notes.txt"},
			},
			"/release/nested": {
				{name: "web-main.zip"},
				{name: "manual.txt"},
			},
		},
		files: map[string]string{
			"/release/his-gateway.jar":     mustZipString(t),
			"/release/nested/web-main.zip": mustZipString(t),
			"/release/notes.txt":           "keep notes",
			"/release/nested/manual.txt":   "keep manual",
		},
	}

	localParent := t.TempDir()
	targetDir, files, err := downloadRemoteDirToLocal(fs, "/release", localParent)
	if err != nil {
		t.Fatalf("download remote dir: %v", err)
	}
	sort.Strings(files)

	if filepath.Base(targetDir) != "release" {
		t.Fatalf("expected downloaded folder name release, got %s", targetDir)
	}
	for _, path := range []string{
		filepath.Join(targetDir, "his-gateway.jar"),
		filepath.Join(targetDir, "nested", "web-main.zip"),
		filepath.Join(targetDir, "notes.txt"),
		filepath.Join(targetDir, "nested", "manual.txt"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected downloaded file %s: %v", path, err)
		}
	}
	expected := []string{"his-gateway.jar", "web-main.zip"}
	for i := range expected {
		if files[i] != expected[i] {
			t.Fatalf("expected artifact list %#v, got %#v", expected, files)
		}
	}
}

func TestDownloadRemoteDirToLocalReportsProgress(t *testing.T) {
	fs := fakeRemoteFS{
		dirs: map[string][]fakeRemoteEntry{
			"/release": {
				{name: "his-gateway.jar"},
				{name: "nested", dir: true},
			},
			"/release/nested": {
				{name: "web-main.zip"},
				{name: "notes.txt"},
			},
		},
		files: map[string]string{
			"/release/his-gateway.jar":     mustZipString(t),
			"/release/nested/web-main.zip": mustZipString(t),
			"/release/nested/notes.txt":    "keep notes",
		},
	}

	var events []downloadProgress
	_, _, err := downloadRemoteDirToLocalWithProgress(fs, "/release", t.TempDir(), func(progress downloadProgress) {
		events = append(events, progress)
	})
	if err != nil {
		t.Fatalf("download remote dir with progress: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected progress events")
	}
	last := events[len(events)-1]
	if last.TotalFiles != 3 || last.CompletedFiles != 3 {
		t.Fatalf("expected final progress 3/3, got %d/%d", last.CompletedFiles, last.TotalFiles)
	}
}

func TestListArtifactSourceBatchesScansUploadDownloadAndArtifactWorkspaces(t *testing.T) {
	root, err := batchUploadRootDir()
	if err != nil {
		t.Fatalf("batch root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	_ = os.RemoveAll(root)

	mustCreateZip(t, filepath.Join(root, "upload", "batch-upload", "his-gateway.jar"))
	mustCreateZip(t, filepath.Join(root, "download", "batch-download", "远程目录", "web-main.zip"))
	mustCreateZip(t, filepath.Join(root, "artifact", "batch-artifact", "web-cdr.zip"))

	batches, err := listArtifactSourceBatches(root, nil)
	if err != nil {
		t.Fatalf("list source batches: %v", err)
	}

	byKey := map[string]artifactSourceBatch{}
	for _, batch := range batches {
		byKey[batch.SourceType+":"+batch.BatchID] = batch
	}

	for key, wantPathBase := range map[string]string{
		"upload:batch-upload":     "batch-upload",
		"download:batch-download": "远程目录",
		"artifact:batch-artifact": "batch-artifact",
	} {
		batch, ok := byKey[key]
		if !ok {
			t.Fatalf("missing batch %s in %#v", key, batches)
		}
		if batch.Count != 1 {
			t.Fatalf("%s count = %d, want 1", key, batch.Count)
		}
		if filepath.Base(batch.TargetPath) != wantPathBase {
			t.Fatalf("%s target path = %s, want base %s", key, batch.TargetPath, wantPathBase)
		}
	}
}

func TestCopyRemoteFileResumesPartialDownload(t *testing.T) {
	fs := &fakeRemoteFS{
		files: map[string]string{
			"/release/his-gateway.jar": "hello resumed artifact",
		},
	}
	target := filepath.Join(t.TempDir(), "his-gateway.jar")
	partPath := target + ".part"
	if err := os.WriteFile(partPath, []byte("hello "), 0644); err != nil {
		t.Fatalf("write partial file: %v", err)
	}

	err := copyRemoteFile(fs, "/release/his-gateway.jar", target, int64(len("hello resumed artifact")))
	if err != nil {
		t.Fatalf("copy remote file with resume: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read resumed target: %v", err)
	}
	if string(data) != "hello resumed artifact" {
		t.Fatalf("expected resumed content, got %q", string(data))
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Fatalf("expected partial file to be removed, got %v", err)
	}
	if len(fs.openOffsets) != 1 || fs.openOffsets[0] != int64(len("hello ")) {
		t.Fatalf("expected remote open offset %d, got %#v", len("hello "), fs.openOffsets)
	}
}

func TestDownloadJobPersistsAndRestoresActiveJob(t *testing.T) {
	if logger.Log == nil {
		logger.Init("error", "stdout", "")
	}
	if err := repository.InitDB(&config.DatabaseConfig{Driver: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "test.db")}); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.DB.AutoMigrate(&model.DownloadJob{}); err != nil {
		t.Fatalf("migrate download job: %v", err)
	}

	job := downloadJob{
		ID:             "batch-job-1",
		Status:         "running",
		RemotePath:     "/release",
		LocalDir:       "/tmp/batch-download",
		BatchID:        "batch-1",
		TotalFiles:     3,
		CompletedFiles: 1,
		CurrentPath:    "/release/his-gateway.jar",
		StartedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	persistDownloadJob(job)

	var dbJob model.DownloadJob
	if err := repository.DB.First(&dbJob, "id = ?", job.ID).Error; err != nil {
		t.Fatalf("find persisted job: %v", err)
	}
	restored := downloadJobFromModel(dbJob)
	if restored.Status != "running" || restored.CurrentPath != job.CurrentPath || restored.CompletedFiles != 1 {
		t.Fatalf("unexpected restored job: %#v", restored)
	}
}

func TestDownloadWorkspaceDirForStartReusesLatestFailedJobDir(t *testing.T) {
	if logger.Log == nil {
		logger.Init("error", "stdout", "")
	}
	if err := repository.InitDB(&config.DatabaseConfig{Driver: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "test.db")}); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.DB.AutoMigrate(&model.DownloadJob{}); err != nil {
		t.Fatalf("migrate download job: %v", err)
	}

	localDir, err := newBatchWorkspaceDir("download")
	if err != nil {
		t.Fatalf("new batch workspace: %v", err)
	}
	failed := model.DownloadJob{
		ID:         "failed-job",
		Status:     "failed",
		RemotePath: "/release",
		LocalDir:   localDir,
		BatchID:    filepath.Base(localDir),
		StartedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := repository.DB.Create(&failed).Error; err != nil {
		t.Fatalf("create failed job: %v", err)
	}

	reused, err := downloadWorkspaceDirForStart("/release")
	if err != nil {
		t.Fatalf("resolve download workspace: %v", err)
	}
	if reused != localDir {
		t.Fatalf("expected failed job dir %s to be reused, got %s", localDir, reused)
	}
}

func TestLatestDownloadJobFromWorkspaceRestoresNewestDownloadedDirectory(t *testing.T) {
	root := t.TempDir()
	oldTarget := filepath.Join(root, "download", "batch-old", "release-old")
	newTarget := filepath.Join(root, "download", "batch-new", "release-new")
	incompleteTarget := filepath.Join(root, "download", "batch-incomplete", "release-incomplete")
	if err := os.MkdirAll(oldTarget, 0755); err != nil {
		t.Fatalf("mkdir old target: %v", err)
	}
	if err := os.MkdirAll(newTarget, 0755); err != nil {
		t.Fatalf("mkdir new target: %v", err)
	}
	if err := os.MkdirAll(incompleteTarget, 0755); err != nil {
		t.Fatalf("mkdir incomplete target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(incompleteTarget, "his-gateway.jar.part"), []byte("partial"), 0644); err != nil {
		t.Fatalf("write partial file: %v", err)
	}
	oldTime := time.Unix(1000, 0)
	newTime := time.Unix(2000, 0)
	incompleteTime := time.Unix(3000, 0)
	if err := os.Chtimes(filepath.Join(root, "download", "batch-old"), oldTime, oldTime); err != nil {
		t.Fatalf("chtime old batch: %v", err)
	}
	if err := os.Chtimes(filepath.Join(root, "download", "batch-new"), newTime, newTime); err != nil {
		t.Fatalf("chtime new batch: %v", err)
	}
	if err := os.Chtimes(filepath.Join(root, "download", "batch-incomplete"), incompleteTime, incompleteTime); err != nil {
		t.Fatalf("chtime incomplete batch: %v", err)
	}

	job, ok := latestDownloadJobFromWorkspace(root)
	if !ok {
		t.Fatalf("expected latest download job from workspace")
	}
	if job.BatchID != "batch-new" || job.LocalDir != filepath.Join(root, "download", "batch-new") || job.TargetPath != newTarget {
		t.Fatalf("unexpected restored job: %#v", job)
	}
}

func TestListDownloadBatchesMergesFilesystemAndJobStatus(t *testing.T) {
	root := t.TempDir()
	successTarget := filepath.Join(root, "download", "batch-success", "release")
	failedTarget := filepath.Join(root, "download", "batch-failed", "release")
	mustCreateZip(t, filepath.Join(successTarget, "his-gateway.jar"))
	if err := os.MkdirAll(failedTarget, 0755); err != nil {
		t.Fatalf("mkdir failed target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(failedTarget, "his-gateway.jar.part"), []byte("partial"), 0644); err != nil {
		t.Fatalf("write part: %v", err)
	}

	jobs := []model.DownloadJob{{
		ID:             "job-failed",
		Status:         "failed",
		RemotePath:     "/remote/release",
		LocalDir:       filepath.Join(root, "download", "batch-failed"),
		BatchID:        "batch-failed",
		TotalFiles:     10,
		CompletedFiles: 4,
		CurrentPath:    "/remote/release/his-gateway.jar",
		Error:          "network interrupted",
		StartedAt:      time.Unix(100, 0),
		UpdatedAt:      time.Unix(200, 0),
	}}

	batches, err := listDownloadBatches(root, jobs)
	if err != nil {
		t.Fatalf("list download batches: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d: %#v", len(batches), batches)
	}

	byID := map[string]downloadJob{}
	for _, batch := range batches {
		byID[batch.BatchID] = batch
	}
	success := byID["batch-success"]
	if success.Status != "success" || success.Count != 1 || success.TargetPath != successTarget || success.HasPartial {
		t.Fatalf("unexpected success batch: %#v", success)
	}
	failed := byID["batch-failed"]
	if failed.Status != "failed" || !failed.HasPartial || failed.Error != "network interrupted" || failed.CompletedFiles != 4 {
		t.Fatalf("unexpected failed batch: %#v", failed)
	}
}

func TestResolveArtifactPathFindsArtifactInNestedSourceDir(t *testing.T) {
	root := t.TempDir()
	artifactPath := filepath.Join(root, "release", "nested", "his-gateway.jar")
	mustCreateZip(t, artifactPath)

	resolved, err := resolveArtifactPath(root, "his-gateway.jar")
	if err != nil {
		t.Fatalf("resolve artifact path: %v", err)
	}
	if resolved != artifactPath {
		t.Fatalf("expected %s, got %s", artifactPath, resolved)
	}
}

func TestParsePackageDownloadHostDefaultsToSSHPort(t *testing.T) {
	host, port, err := parsePackageDownloadHost("192.168.199.100")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}
	if host != "192.168.199.100" || port != 22 {
		t.Fatalf("expected 192.168.199.100:22, got %s:%d", host, port)
	}
}

func TestParsePackageDownloadHostAcceptsExplicitPort(t *testing.T) {
	host, port, err := parsePackageDownloadHost("192.168.199.100:2222")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}
	if host != "192.168.199.100" || port != 2222 {
		t.Fatalf("expected 192.168.199.100:2222, got %s:%d", host, port)
	}
}

func TestPackageDownloadRemoteServerPrefersSSHKey(t *testing.T) {
	server, err := packageDownloadRemoteServer("192.168.199.100", "root", "password", "private-key")
	if err != nil {
		t.Fatalf("build package download server: %v", err)
	}
	if server.AuthType != "ssh_key" {
		t.Fatalf("expected ssh_key auth, got %q", server.AuthType)
	}
	if server.CredentialEncrypted == "" {
		t.Fatalf("expected encrypted credential")
	}
}

func TestPackageDownloadRemoteServerAllowsKeyWithoutPassword(t *testing.T) {
	server, err := packageDownloadRemoteServer("192.168.199.100", "root", "", "private-key")
	if err != nil {
		t.Fatalf("expected key auth without password: %v", err)
	}
	if server.AuthType != "ssh_key" {
		t.Fatalf("expected ssh_key auth, got %q", server.AuthType)
	}
}

func TestPackageDownloadRemoteServerRequiresPasswordOrKey(t *testing.T) {
	if _, err := packageDownloadRemoteServer("192.168.199.100", "root", "", ""); err == nil {
		t.Fatalf("expected missing password and key to be rejected")
	}
}

func TestIsSQLArtifactFileOnlyMatchesSQLZip(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{name: "15.1_SP10.4_250610-sql.zip", want: true},
		{name: "HIS-SQL.ZIP", want: true},
		{name: "18.zip", want: false},
		{name: "his-gateway.jar", want: false},
	}
	for _, tc := range cases {
		if got := isSQLArtifactFile(tc.name); got != tc.want {
			t.Fatalf("isSQLArtifactFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestArtifactLibraryPathsUseAppVersionsAndLatest(t *testing.T) {
	versionPath, latestPath, err := artifactLibraryPaths("/opt/df-build-server/artifacts", "his-gateway", "batch-123", "his-gateway.jar")
	if err != nil {
		t.Fatalf("artifact library paths: %v", err)
	}
	expectedVersion := filepath.Join("/opt/df-build-server/artifacts", "apps", "his-gateway", "versions", "batch-123", "his-gateway.jar")
	expectedLatest := filepath.Join("/opt/df-build-server/artifacts", "apps", "his-gateway", "latest", "his-gateway.jar")
	if versionPath != expectedVersion || latestPath != expectedLatest {
		t.Fatalf("unexpected paths:\nversion=%s\nlatest=%s", versionPath, latestPath)
	}
}

func TestCopyArtifactToLibraryCopiesVersionAndLatest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "his-gateway.jar")
	mustCreateZip(t, source)

	meta, err := copyArtifactToLibrary(source, root, "batch-123", model.Application{AppName: "his-gateway", ArtifactName: "his-gateway.jar"})
	if err != nil {
		t.Fatalf("copy artifact to library: %v", err)
	}
	for _, path := range []string{meta.VersionPath, meta.LatestPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected copied artifact %s: %v", path, err)
		}
	}
	if meta.FileSizeBytes <= 0 {
		t.Fatalf("expected file size to be recorded")
	}
	if len(meta.SHA256) != 64 {
		t.Fatalf("expected sha256 hex, got %q", meta.SHA256)
	}
}

func TestCopyArtifactToWorkspaceCopiesMatchedArtifactOnly(t *testing.T) {
	source := filepath.Join(t.TempDir(), "his-gateway.jar")
	mustCreateZip(t, source)
	workspace := t.TempDir()

	target, err := copyArtifactToWorkspace(source, workspace, "his-gateway.jar")
	if err != nil {
		t.Fatalf("copy artifact to workspace: %v", err)
	}
	if target != filepath.Join(workspace, "his-gateway.jar") {
		t.Fatalf("expected artifact workspace target, got %s", target)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected workspace artifact: %v", err)
	}
}

func TestBatchIDFromSourceDirUsesSourceBatchSegment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspaces", "batch-upload")
	sourceDir := filepath.Join(root, "download", "batch-123", "release")

	if got := batchIDFromSourceDir(root, sourceDir); got != "batch-123" {
		t.Fatalf("expected batch-123, got %q", got)
	}
}

func TestPruneArtifactVersionsKeepsLatestThreeVersionsPerApp(t *testing.T) {
	root := t.TempDir()
	appName := "his-gateway"
	for i := 1; i <= 5; i++ {
		dir := filepath.Join(root, "apps", appName, "versions", fmt.Sprintf("batch-%d", i))
		mustCreateZip(t, filepath.Join(dir, "his-gateway.jar"))
		ts := time.Unix(int64(i), 0)
		if err := os.Chtimes(dir, ts, ts); err != nil {
			t.Fatalf("chtimes version dir: %v", err)
		}
	}

	if err := pruneArtifactVersions(root, appName, 3); err != nil {
		t.Fatalf("prune artifact versions: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "apps", appName, "versions"))
	if err != nil {
		t.Fatalf("read versions: %v", err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	expected := []string{"batch-3", "batch-4", "batch-5"}
	if strings.Join(names, ",") != strings.Join(expected, ",") {
		t.Fatalf("expected versions %v, got %v", expected, names)
	}
}

func TestPersistArtifactVersionFromDirStoresValidationAndMatchingItems(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	mustCreateZip(t, filepath.Join(dir, "his-gateway.jar"))
	mustCreateZip(t, filepath.Join(dir, "04.zip"))
	mustCreateZip(t, filepath.Join(dir, "15.1_SP10.4_250610-sql.zip"))
	mustCreateZip(t, filepath.Join(dir, "mystery.jar"))
	if err := os.WriteFile(filepath.Join(dir, "broken.zip"), []byte("not a zip"), 0644); err != nil {
		t.Fatalf("write broken zip: %v", err)
	}

	version, items, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   "batch-version-1",
		SourceType:  "upload",
		SourceLabel: "本地上传",
		Status:      "available",
		LocalDir:    dir,
		TargetPath:  dir,
	})
	if err != nil {
		t.Fatalf("persist artifact version: %v", err)
	}
	if version.VersionNo != "batch-version-1" || version.Count != 5 || version.DeployableCount != 2 {
		t.Fatalf("unexpected version summary: %#v", version)
	}
	if version.ValidCount != 4 || version.InvalidCount != 1 || version.SkippedCount != 1 || version.UnmatchedCount != 1 {
		t.Fatalf("unexpected version counters: %#v", version)
	}

	byName := map[string]model.ArtifactVersionItem{}
	for _, item := range items {
		byName[item.FileName] = item
	}
	assertVersionItem(t, byName["his-gateway.jar"], "matched", "valid", "his-gateway", true)
	assertVersionItem(t, byName["04.zip"], "matched", "valid", "web-menzhenysz", true)
	assertVersionItem(t, byName["15.1_SP10.4_250610-sql.zip"], "skipped", "valid", "", false)
	assertVersionItem(t, byName["mystery.jar"], "unmatched", "valid", "", false)
	assertVersionItem(t, byName["broken.zip"], "unmatched", "invalid", "", false)

	var dbItems int64
	if err := repository.DB.Model(&model.ArtifactVersionItem{}).Where("version_no = ?", "batch-version-1").Count(&dbItems).Error; err != nil {
		t.Fatalf("count version items: %v", err)
	}
	if dbItems != 5 {
		t.Fatalf("expected 5 version items in db, got %d", dbItems)
	}
}

func TestPersistArtifactVersionFromDirStoresPackageBusinessVersion(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	mustCreateZipWithFiles(t, filepath.Join(dir, "04.zip"), map[string]string{
		"dist/config.json": `{"xiTongId":"04","version":"2.0.1","branch":"release","commit":"abc","date":"2026.06.17"}`,
	})

	_, items, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   "batch-version-business",
		SourceType:  "upload",
		SourceLabel: "本地上传",
		Status:      "available",
		LocalDir:    dir,
		TargetPath:  dir,
	})
	if err != nil {
		t.Fatalf("persist artifact version: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	if !strings.Contains(items[0].PackageVersionJSON, `"xiTongId":"04"`) {
		t.Fatalf("expected package business version to be stored, got %q", items[0].PackageVersionJSON)
	}
}

func TestGetArtifactVersionReturnsVersionAndItems(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	mustCreateZip(t, filepath.Join(dir, "his-gateway.jar"))
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   "batch-version-2",
		SourceType:  "download",
		SourceLabel: "服务器下载",
		Status:      "available",
		LocalDir:    dir,
		TargetPath:  dir,
	}); err != nil {
		t.Fatalf("persist artifact version: %v", err)
	}

	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, "/batch-deploy/artifact-versions/batch-version-2", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "versionNo", Value: "batch-version-2"}}

	NewBatchDeployHandler().GetArtifactVersion(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Version model.ArtifactVersion       `json:"version"`
			Items   []model.ArtifactVersionItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 {
		t.Fatalf("expected success response, got %#v", payload)
	}
	if payload.Data.Version.VersionNo != "batch-version-2" || len(payload.Data.Items) != 1 {
		t.Fatalf("unexpected version detail: %#v", payload.Data)
	}
	if payload.Data.Items[0].AppName != "his-gateway" || !payload.Data.Items[0].Deployable {
		t.Fatalf("unexpected version item: %#v", payload.Data.Items[0])
	}
}

func TestDeleteArtifactVersionItemRemovesFileAndRecomputesCounters(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	mustCreateZip(t, filepath.Join(dir, "his-gateway.jar"))
	if err := os.WriteFile(filepath.Join(dir, "broken.zip"), []byte("not a zip"), 0644); err != nil {
		t.Fatalf("write broken zip: %v", err)
	}
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   "batch-version-delete",
		SourceType:  "upload",
		SourceLabel: "本地上传",
		Status:      "available",
		LocalDir:    dir,
		TargetPath:  dir,
	}); err != nil {
		t.Fatalf("persist artifact version: %v", err)
	}
	var item model.ArtifactVersionItem
	if err := repository.DB.Where("version_no = ? AND file_name = ?", "batch-version-delete", "broken.zip").First(&item).Error; err != nil {
		t.Fatalf("find item: %v", err)
	}

	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodDelete, "/batch-deploy/artifact-versions/batch-version-delete/items/1", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "versionNo", Value: "batch-version-delete"}, {Key: "itemID", Value: strconv.Itoa(int(item.ID))}}

	NewBatchDeployHandler().DeleteArtifactVersionItem(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "broken.zip")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected broken file removed, stat err=%v", err)
	}
	var version model.ArtifactVersion
	if err := repository.DB.Where("version_no = ?", "batch-version-delete").First(&version).Error; err != nil {
		t.Fatalf("find version: %v", err)
	}
	if version.Count != 1 || version.InvalidCount != 0 || version.DeployableCount != 1 {
		t.Fatalf("unexpected recomputed version: %#v", version)
	}
}

func TestReplaceArtifactVersionItemUploadsNewFileAndRecomputesCounters(t *testing.T) {
	setupArtifactVersionTestDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "his-gateway.jar"), []byte("not a zip"), 0644); err != nil {
		t.Fatalf("write broken jar: %v", err)
	}
	if _, _, err := persistArtifactVersionFromDir(artifactVersionInput{
		VersionNo:   "batch-version-replace",
		SourceType:  "upload",
		SourceLabel: "本地上传",
		Status:      "available",
		LocalDir:    dir,
		TargetPath:  dir,
	}); err != nil {
		t.Fatalf("persist artifact version: %v", err)
	}
	var item model.ArtifactVersionItem
	if err := repository.DB.Where("version_no = ? AND file_name = ?", "batch-version-replace", "his-gateway.jar").First(&item).Error; err != nil {
		t.Fatalf("find item: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "his-gateway.jar")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	zipBuf := bytes.Buffer{}
	zw := zip.NewWriter(&zipBuf)
	if _, err := zw.Create("META-INF/MANIFEST.MF"); err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if _, err := part.Write(zipBuf.Bytes()); err != nil {
		t.Fatalf("write multipart: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/batch-deploy/artifact-versions/batch-version-replace/items/1/replace", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "versionNo", Value: "batch-version-replace"}, {Key: "itemID", Value: strconv.Itoa(int(item.ID))}}

	NewBatchDeployHandler().ReplaceArtifactVersionItem(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", w.Code, w.Body.String())
	}
	var replaced model.ArtifactVersionItem
	if err := repository.DB.Where("version_no = ? AND file_name = ?", "batch-version-replace", "his-gateway.jar").First(&replaced).Error; err != nil {
		t.Fatalf("find replaced item: %v", err)
	}
	if replaced.ValidateStatus != "valid" || !replaced.Deployable || replaced.AppName != "his-gateway" {
		t.Fatalf("unexpected replaced item: %#v", replaced)
	}
}

func TestRedownloadArtifactItemFromRemoteReplacesBrokenLocalFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gy-jichufw.jar"), []byte("broken"), 0644); err != nil {
		t.Fatalf("write broken local file: %v", err)
	}
	remoteZip := bytes.Buffer{}
	zw := zip.NewWriter(&remoteZip)
	if _, err := zw.Create("META-INF/MANIFEST.MF"); err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	fs := memoryRemoteFS{files: map[string][]byte{
		"/packages/nested/gy-jichufw.jar": remoteZip.Bytes(),
	}}

	if err := redownloadArtifactItemFromRemote(fs, "/packages", dir, "gy-jichufw.jar"); err != nil {
		t.Fatalf("redownload artifact item: %v", err)
	}
	if err := validateArtifact(filepath.Join(dir, "gy-jichufw.jar")); err != nil {
		t.Fatalf("expected replaced file to be valid: %v", err)
	}
}

func TestGetArtifactDeployBatchReturnsRecordsForVersionRollback(t *testing.T) {
	setupArtifactVersionTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "deploy-batch-1", VersionNo: "deploy-batch-1", Namespace: "prod", Status: "deployed", TotalCount: 1, SuccessCount: 1}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatalf("create deploy batch: %v", err)
	}
	record := model.ArtifactDeployRecord{
		DeployBatchNo:             batch.DeployBatchNo,
		VersionNo:                 batch.VersionNo,
		AppName:                   "his-gateway",
		AppType:                   "java",
		FileName:                  "his-gateway.jar",
		Namespace:                 "prod",
		DeploymentName:            "his-gateway",
		BeforeImage:               "registry/his-gateway:old",
		AfterImage:                "registry/his-gateway:new",
		PackageVersionJSON:        `{"git":{"branch":"release","commit":{"id":"new"}}}`,
		BeforeBusinessVersionJSON: `{"git":{"branch":"release","commit":{"id":"old"}}}`,
		AfterBusinessVersionJSON:  `{"git":{"branch":"release","commit":{"id":"new"}}}`,
		Status:                    "deployed",
		DeployStatus:              "success",
		VerifyStatus:              "success",
		RollbackStatus:            "none",
	}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatalf("create deploy record: %v", err)
	}

	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, "/batch-deploy/deploy-batches/deploy-batch-1", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Params = gin.Params{{Key: "batchNo", Value: "deploy-batch-1"}}

	NewBatchDeployHandler().GetArtifactDeployBatch(c)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Batch   model.ArtifactDeployBatch    `json:"batch"`
			Records []model.ArtifactDeployRecord `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 || payload.Data.Batch.DeployBatchNo != batch.DeployBatchNo || len(payload.Data.Records) != 1 {
		t.Fatalf("unexpected deploy batch detail: %#v", payload)
	}
	if payload.Data.Records[0].BeforeImage == "" || payload.Data.Records[0].AfterBusinessVersionJSON == "" {
		t.Fatalf("deploy record should include images and business versions: %#v", payload.Data.Records[0])
	}
}

func TestMatchDoesNotRecordLatestArtifact(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Artifact{}); err != nil {
		t.Fatalf("migrate artifacts: %v", err)
	}
	sourceDir, err := newBatchWorkspaceDir("upload")
	if err != nil {
		t.Fatalf("new batch workspace: %v", err)
	}
	mustCreateZip(t, filepath.Join(sourceDir, "his-gateway.jar"))
	body := bytes.NewBufferString(fmt.Sprintf(`{"files":["his-gateway.jar"],"sourceDir":%q}`, sourceDir))

	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodPost, "/batch-deploy/match", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	NewBatchDeployHandler().Match(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	var artifactCount int64
	if err := repository.DB.Model(&model.Artifact{}).Count(&artifactCount).Error; err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if artifactCount != 0 {
		t.Fatalf("match should not update latest artifacts, got %d artifact rows", artifactCount)
	}
}

func TestListArtifactVersionsBackfillsExistingWorkspaceBatches(t *testing.T) {
	setupArtifactVersionTestDB(t)
	root, err := batchUploadRootDir()
	if err != nil {
		t.Fatalf("batch root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	_ = os.RemoveAll(root)
	mustCreateZip(t, filepath.Join(root, "upload", "batch-backfill", "his-gateway.jar"))

	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(http.MethodGet, "/batch-deploy/artifact-versions", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	NewBatchDeployHandler().ListArtifactVersions(c)

	var payload struct {
		Code int `json:"code"`
		Data struct {
			Versions []model.ArtifactVersion `json:"versions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != 0 || len(payload.Data.Versions) != 1 {
		t.Fatalf("expected one backfilled version, got %#v", payload)
	}
	version := payload.Data.Versions[0]
	if version.VersionNo != "batch-backfill" || version.DeployableCount != 1 || version.Status != "available" {
		t.Fatalf("unexpected backfilled version: %#v", version)
	}
}

func setupArtifactVersionTestDB(t *testing.T) {
	t.Helper()
	if logger.Log == nil {
		logger.Init("error", "stdout", "")
	}
	if err := repository.InitDB(&config.DatabaseConfig{Driver: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "test.db")}); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.DB.AutoMigrate(&model.Application{}, &model.ArtifactVersion{}, &model.ArtifactVersionItem{}, &model.ArtifactDeployBatch{}, &model.ArtifactDeployRecord{}); err != nil {
		t.Fatalf("migrate artifact version tables: %v", err)
	}
	apps := []model.Application{
		{AppName: "his-gateway", AppType: "java", GitRepo: "repo"},
		{AppName: "web-menzhenysz", AppType: "vue", VueRole: "sub", AppCode: "04", GitRepo: "repo"},
	}
	if err := repository.DB.Create(&apps).Error; err != nil {
		t.Fatalf("seed applications: %v", err)
	}
}

func assertVersionItem(t *testing.T, item model.ArtifactVersionItem, matchStatus, validateStatus, appName string, deployable bool) {
	t.Helper()
	if item.FileName == "" {
		t.Fatalf("missing version item")
	}
	if item.MatchStatus != matchStatus || item.ValidateStatus != validateStatus || item.AppName != appName || item.Deployable != deployable {
		t.Fatalf("unexpected item %s: match=%s validate=%s app=%s deployable=%v", item.FileName, item.MatchStatus, item.ValidateStatus, item.AppName, item.Deployable)
	}
}

type memoryRemoteFS struct {
	files map[string][]byte
}

func (fs memoryRemoteFS) ReadDir(dir string) ([]os.FileInfo, error) {
	cleanDir := pathpkg.Clean(dir)
	prefix := cleanDir
	if prefix != "/" {
		prefix += "/"
	}
	children := map[string]memoryFileInfo{}
	for filePath, data := range fs.files {
		cleanFile := pathpkg.Clean(filePath)
		if !strings.HasPrefix(cleanFile, prefix) {
			continue
		}
		rel := strings.TrimPrefix(cleanFile, prefix)
		if rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		name := parts[0]
		info := memoryFileInfo{name: name, size: int64(len(data))}
		if len(parts) > 1 {
			info.dir = true
			info.size = 0
		}
		children[name] = info
	}
	if len(children) == 0 {
		return nil, os.ErrNotExist
	}
	infos := make([]os.FileInfo, 0, len(children))
	for _, info := range children {
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name() < infos[j].Name() })
	return infos, nil
}

func (fs memoryRemoteFS) Open(filePath string) (io.ReadCloser, error) {
	data, ok := fs.files[pathpkg.Clean(filePath)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

type memoryFileInfo struct {
	name string
	size int64
	dir  bool
}

func (i memoryFileInfo) Name() string       { return i.name }
func (i memoryFileInfo) Size() int64        { return i.size }
func (i memoryFileInfo) Mode() os.FileMode  { if i.dir { return os.ModeDir | 0755 }; return 0644 }
func (i memoryFileInfo) ModTime() time.Time { return time.Time{} }
func (i memoryFileInfo) IsDir() bool        { return i.dir }
func (i memoryFileInfo) Sys() any           { return nil }

func createTestZip(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	w, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		zw.Close()
		return err
	}
	if _, err := w.Write([]byte("Manifest-Version: 1.0\n")); err != nil {
		zw.Close()
		return err
	}
	return zw.Close()
}

func mustCreateZip(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir test zip dir: %v", err)
	}
	if err := createTestZip(path); err != nil {
		t.Fatalf("create test zip %s: %v", path, err)
	}
}

func mustCreateZipWithFiles(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir test zip dir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test zip %s: %v", path, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
}

func mustZipString(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact.zip")
	mustCreateZip(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read test zip: %v", err)
	}
	return string(data)
}

type fakeRemoteEntry struct {
	name string
	dir  bool
	size int64
}

func (e fakeRemoteEntry) Name() string { return e.name }
func (e fakeRemoteEntry) Size() int64  { return e.size }
func (e fakeRemoteEntry) Mode() os.FileMode {
	if e.dir {
		return os.ModeDir | 0755
	}
	return 0644
}
func (e fakeRemoteEntry) ModTime() time.Time { return time.Time{} }
func (e fakeRemoteEntry) IsDir() bool        { return e.dir }
func (e fakeRemoteEntry) Sys() any           { return nil }

type fakeRemoteFS struct {
	dirs        map[string][]fakeRemoteEntry
	files       map[string]string
	openOffsets []int64
}

func (f fakeRemoteFS) ReadDir(path string) ([]os.FileInfo, error) {
	entries, ok := f.dirs[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	out := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.dir {
			filePath := path + "/" + entry.name
			if content, ok := f.files[filePath]; ok {
				entry.size = int64(len(content))
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func (f fakeRemoteFS) Open(path string) (io.ReadCloser, error) {
	content, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func (f *fakeRemoteFS) OpenAt(path string, offset int64) (io.ReadCloser, error) {
	content, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	f.openOffsets = append(f.openOffsets, offset)
	if offset > int64(len(content)) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return io.NopCloser(strings.NewReader(content[offset:])), nil
}

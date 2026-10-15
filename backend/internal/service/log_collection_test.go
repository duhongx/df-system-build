package service

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
)

type localLogRemote struct {
	open func(string) (logRemoteFile, error)
}

func (r localLogRemote) RealPath(p string) (string, error) { return filepath.EvalSymlinks(p) }
func (r localLogRemote) ReadDir(p string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	var result []os.FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	return result, nil
}
func (r localLogRemote) OpenFile(p string) (logRemoteFile, error) {
	if r.open != nil {
		return r.open(p)
	}
	return os.Open(p)
}
func (r localLogRemote) Close() error { return nil }

func logTestWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func logTestJob(root string, files ...string) *model.LogCollectionJob {
	job := &model.LogCollectionJob{ID: 1, Status: "running", TotalFiles: len(files)}
	for _, name := range files {
		job.Items = append(job.Items, model.LogCollectionItem{Source: model.LogSource{ID: 1, Name: "计费服务", Category: "microservice", ServerID: 1, Directory: root, Pattern: "*", Recursive: true}, Host: "host-1", Path: name, ArchivePath: "microservice/host-1/billing/" + name, Status: "pending"})
	}
	return job
}
func logTestZIP(t *testing.T, dir string) map[string]string {
	t.Helper()
	reader, err := zip.OpenReader(filepath.Join(dir, "logs.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	files := map[string]string{}
	for _, file := range reader.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = string(data)
	}
	return files
}

func TestLogCollectionListingRulesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	logTestWrite(t, filepath.Join(root, "current.log"), "a")
	logTestWrite(t, filepath.Join(root, "old", "archive.log.gz"), "b")
	logTestWrite(t, filepath.Join(root, "dump.rdb"), "not logs")
	if err := os.Symlink(filepath.Join(root, "old"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	source := model.LogSource{Directory: root, Pattern: "*.log,*.log.gz"}
	files, err := listLogFiles(context.Background(), localLogRemote{}, source)
	if err != nil || len(files) != 1 || files[0].Path != "current.log" {
		t.Fatalf("%+v %v", files, err)
	}
	source.Recursive = true
	files, err = listLogFiles(context.Background(), localLogRemote{}, source)
	if err != nil || len(files) != 2 {
		t.Fatalf("%+v %v", files, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listLogFiles(ctx, localLogRemote{}, source); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLogCollectionPartialFailureAndArchiveIntegrity(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	logTestWrite(t, filepath.Join(root, "current.log"), "line1\nline2\n")
	logTestWrite(t, filepath.Join(root, "other", "历史.log.gz"), "compressed bytes")
	job := logTestJob(root, "current.log", "missing.log", "other/历史.log.gz")
	err := collectLogArchive(context.Background(), job, dir, func(model.LogSource) (logRemote, error) { return localLogRemote{}, nil }, func() error { return nil })
	if err != nil || job.Status != "partial" || job.SuccessFiles != 2 || job.FailedFiles != 1 {
		t.Fatalf("%+v %v", job, err)
	}
	files := logTestZIP(t, dir)
	if len(files) != 3 || files[job.Items[0].ArchivePath] != "line1\nline2\n" || files[job.Items[2].ArchivePath] != "compressed bytes" {
		t.Fatalf("%+v", files)
	}
	if _, ok := files[job.Items[1].ArchivePath]; ok {
		t.Fatal("failed file added to ZIP")
	}
	var manifest struct {
		Job model.LogCollectionJob `json:"job"`
	}
	if err := json.Unmarshal([]byte(files["collection-manifest.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Job.Status != "partial" || !strings.Contains(manifest.Job.Items[1].Error, "missing.log") {
		t.Fatalf("%+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(dir, "current.tmp")); !os.IsNotExist(err) {
		t.Fatal("staging file not cleaned")
	}
}

func TestLogCollectionHostFailureContinuesOtherHosts(t *testing.T) {
	root := t.TempDir()
	logTestWrite(t, filepath.Join(root, "redis.log"), "ok")
	job := logTestJob(root, "a.log", "b.log", "redis.log")
	job.Items[2].Source.ServerID = 2
	attempts := 0
	err := collectLogArchive(context.Background(), job, t.TempDir(), func(source model.LogSource) (logRemote, error) {
		if source.ServerID == 1 {
			attempts++
			return nil, errors.New("SSH authentication failed")
		}
		return localLogRemote{}, nil
	}, func() error { return nil })
	if err != nil || job.Status != "partial" || job.SuccessFiles != 1 || job.FailedFiles != 2 || attempts != 1 {
		t.Fatalf("%+v attempts=%d err=%v", job, attempts, err)
	}
}

func TestLogCollectionCancelKeepsCompletedFiles(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	logTestWrite(t, filepath.Join(root, "first.log"), "first")
	logTestWrite(t, filepath.Join(root, "second.log"), "second")
	job := logTestJob(root, "first.log", "second.log")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := collectLogArchive(ctx, job, dir, func(model.LogSource) (logRemote, error) { return localLogRemote{}, nil }, func() error {
		if job.SuccessFiles == 1 {
			cancel()
		}
		return nil
	})
	if err != nil || job.Status != "canceled" || job.SuccessFiles != 1 || job.Items[1].Status != "canceled" {
		t.Fatalf("%+v %v", job, err)
	}
	if files := logTestZIP(t, dir); len(files) != 2 {
		t.Fatalf("%+v", files)
	}
}

type changedLogFile struct {
	*os.File
	afterStat func()
	info      os.FileInfo
}

func (f *changedLogFile) Stat() (os.FileInfo, error) {
	info, err := f.File.Stat()
	if f.afterStat != nil {
		f.afterStat()
	}
	return info, err
}

func TestLogCollectionBoundsGrowingFileAndRejectsTruncation(t *testing.T) {
	for _, truncate := range []bool{false, true} {
		t.Run(map[bool]string{false: "growing", true: "truncated"}[truncate], func(t *testing.T) {
			root := t.TempDir()
			name := filepath.Join(root, "app.log")
			logTestWrite(t, name, "original")
			remote := localLogRemote{open: func(p string) (logRemoteFile, error) {
				file, err := os.Open(p)
				if err != nil {
					return nil, err
				}
				return &changedLogFile{File: file, afterStat: func() {
					if truncate {
						if err := os.Truncate(p, 2); err != nil {
							t.Fatal(err)
						}
					} else {
						file, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
						if err != nil {
							t.Fatal(err)
						}
						_, _ = file.WriteString("appended")
						_ = file.Close()
					}
				}}, nil
			}}
			item := logTestJob(root, "app.log").Items[0]
			temp := filepath.Join(t.TempDir(), "temp")
			err := stageLogFile(context.Background(), remote, &item, temp, func() error { return nil })
			if truncate {
				if err == nil || !strings.Contains(err.Error(), "不完整") {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				data, _ := os.ReadFile(temp)
				if string(data) != "original" {
					t.Fatalf("%q", data)
				}
			}
		})
	}
}

func TestLogCollectionRejectsEscapeAndInvalidSource(t *testing.T) {
	for _, p := range []string{"../secret", "/etc/passwd", "a/../../secret", "a/../b", "a\\b", ".", ""} {
		if validLogRelative(p) {
			t.Fatalf("accepted %q", p)
		}
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.log")
	logTestWrite(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "link.log")); err != nil {
		t.Fatal(err)
	}
	item := logTestJob(root, "link.log").Items[0]
	if err := stageLogFile(context.Background(), localLogRemote{}, &item, filepath.Join(t.TempDir(), "tmp"), func() error { return nil }); err == nil || !strings.Contains(err.Error(), "超出") {
		t.Fatal(err)
	}
	source := model.LogSource{Name: "redis", ServerID: 1, Category: "redis", Directory: "relative"}
	if validateLogSource(&source) == nil {
		t.Fatal("relative source accepted")
	}
	source.Directory = root
	source.Pattern = "["
	if validateLogSource(&source) == nil {
		t.Fatal("invalid glob accepted")
	}
}

func TestLogCollectionProgressSaveFailureStops(t *testing.T) {
	root := t.TempDir()
	logTestWrite(t, filepath.Join(root, "a.log"), "a")
	dir := t.TempDir()
	job := logTestJob(root, "a.log")
	err := collectLogArchive(context.Background(), job, dir, func(model.LogSource) (logRemote, error) { return localLogRemote{}, nil }, func() error { return errors.New("database unavailable") })
	if err == nil || !strings.Contains(err.Error(), "保存收集进度失败") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs.zip")); !os.IsNotExist(err) {
		t.Fatal("published ZIP after persistence failure")
	}
}

func TestLogCollectionLocalStorageFailureStops(t *testing.T) {
	root := t.TempDir()
	logTestWrite(t, filepath.Join(root, "app.log"), "test")
	dir := t.TempDir()
	// Simulate an unavailable staging path without writing to a real full disk.
	if err := os.Mkdir(filepath.Join(dir, "current.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	job := logTestJob(root, "app.log")
	err := collectLogArchive(context.Background(), job, dir, func(model.LogSource) (logRemote, error) { return localLogRemote{}, nil }, func() error { return nil })
	var storageErr logStorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected fatal storage error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs.zip")); !os.IsNotExist(err) {
		t.Fatal("published incomplete ZIP")
	}
}

func TestLogCollectionPersistenceAndRecovery(t *testing.T) {
	db := testutil.OpenGormPostgres(t)
	if err := db.AutoMigrate(&model.Server{}, &model.LogSource{}, &model.LogCollectionJob{}); err != nil {
		t.Fatal(err)
	}
	s := NewLogCollectionService(db, t.TempDir())
	s.connect = func(context.Context, model.Server) (logRemote, error) { return localLogRemote{}, nil }
	host := model.Server{Host: "test-log-host", Username: "ops", AuthType: "password"}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	logTestWrite(t, filepath.Join(root, "rabbit.log"), "message")
	source, err := s.SaveSource(model.LogSource{Name: "RabbitMQ", Category: "rabbitmq", ServerID: host.ID, Directory: root, Pattern: "*.log"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start([]LogCollectionSelection{{SourceID: source.ID, Files: []string{"../outside.log"}}}, "tester"); err == nil {
		t.Fatal("accepted path traversal")
	}
	started, err := s.Start([]LogCollectionSelection{{SourceID: source.ID, Files: []string{"rabbit.log", "rabbit.log"}}}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	var job *model.LogCollectionJob
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err = s.Get(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != "completed" || len(job.Items) != 1 || job.ArchiveSize == 0 || job.Items[0].Host != host.Host {
		t.Fatalf("%+v", job)
	}
	if err := s.DeleteSource(source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(started.ID); err != nil {
		t.Fatal("source deletion removed history", err)
	}
	// A restarted process can still list and download completed archives.
	restarted := NewLogCollectionService(db, s.root+"/..")
	if err := restarted.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restarted.ArchivePath(job.ID)); err != nil {
		t.Fatal(err)
	}
	orphan := model.LogCollectionJob{Status: "running", TotalFiles: 1, Items: []model.LogCollectionItem{{Status: "running", Path: "running.log"}}}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	logTestWrite(t, filepath.Join(s.jobDir(orphan.ID), "logs.zip.part"), "unfinished")
	if err := restarted.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	recovered, err := restarted.Get(orphan.ID)
	if err != nil || recovered.Status != "failed" || recovered.Items[0].Status != "failed" || recovered.ArchiveSize != 0 {
		t.Fatalf("%+v %v", recovered, err)
	}
	if _, err := os.Stat(s.jobDir(orphan.ID)); !os.IsNotExist(err) {
		t.Fatal("orphan temporary files remain")
	}
	if err := restarted.Delete(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.ArchivePath(job.ID)); !os.IsNotExist(err) {
		t.Fatal("archive not deleted")
	}
}

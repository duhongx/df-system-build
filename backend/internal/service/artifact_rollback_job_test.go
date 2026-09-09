package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type backgroundRollbacker struct {
	entered     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	calls       []string
	badDeadline bool
	failName    string
}

func (r *backgroundRollbacker) RollbackDeployment(ctx context.Context, ns, name, image string) error {
	r.mu.Lock()
	r.calls = append(r.calls, name)
	first := len(r.calls) == 1
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 4*time.Minute {
		r.badDeadline = true
	}
	r.mu.Unlock()
	if first {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if name == r.failName {
		return errors.New("image pull denied")
	}
	return ctx.Err()
}

func TestBackgroundRollbackSurvivesRequestCompletionAndContinues(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "background-rollback", VersionNo: "background-rollback", Status: "deployed"}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"service-a", "service-b", "web-main", "web-main"} {
		record := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, Namespace: "test", DeploymentName: name, BeforeImage: "old", DeployStatus: "success", Status: "deployed"}
		if err := repository.DB.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Preparation failed before touching the cluster; do not turn it into a rollback error.
	record := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, Namespace: "test", DeploymentName: "unsubmitted", DeployStatus: "skipped", Status: "failed", ErrorMessage: "copy failed"}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	r := &backgroundRollbacker{entered: make(chan struct{}), release: make(chan struct{}), failName: "service-b"}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(r.release) })
	requestFinished := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer close(requestFinished)
		job, err := StartRollbackDeployBatch(batch.DeployBatchNo, r, nil)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if job.Status != "rolling_back" {
			http.Error(w, job.Status, 500)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submission status=%d", resp.StatusCode)
	}
	<-requestFinished // HTTP request context has ended before rollback is allowed to finish.
	select {
	case <-r.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker not started")
	}
	job, err := StartRollbackDeployBatch(batch.DeployBatchNo, r, nil)
	if err != nil || job.Status != "rolling_back" {
		t.Fatalf("duplicate submission: %+v %v", job, err)
	}
	releaseOnce.Do(func() { close(r.release) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := repository.DB.First(&batch, batch.ID).Error; err != nil {
			t.Fatal(err)
		}
		if batch.Status == "rollback_failed" && strings.HasPrefix(batch.ErrorMessage, "后台回滚未全部完成") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not finish: %+v", batch)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Join(r.calls, ",") != "service-a,service-b,web-main" || r.badDeadline {
		t.Fatalf("calls=%v badDeadline=%v", r.calls, r.badDeadline)
	}
	if batch.RolledBackCount != 3 || !strings.Contains(batch.ErrorMessage, "image pull denied") {
		t.Fatalf("missing progress/error: %+v", batch)
	}
	if err := repository.DB.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if record.RollbackStatus != "skipped" || record.ErrorMessage != "copy failed" {
		t.Fatalf("lost preparation failure: %+v", record)
	}
}

func TestInterruptedRollbackPreservesCompletedTargets(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "interrupted-rollback", VersionNo: "interrupted-rollback", Status: "rolling_back"}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	records := []model.ArtifactDeployRecord{
		{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, DeploymentName: "done", RollbackStatus: "success", Status: "rolled_back"},
		{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, DeploymentName: "running", RollbackStatus: "running", Status: "rolling_back"},
		{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, DeploymentName: "pending", RollbackStatus: "pending", Status: "deployed"},
	}
	if err := repository.DB.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterruptedRollbacks(); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Where("deploy_batch_no = ?", batch.DeployBatchNo).Order("id").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if records[0].RollbackStatus != "success" || records[1].RollbackStatus != "failed" || records[2].RollbackStatus != "failed" || !strings.Contains(records[1].ErrorMessage, "服务重启") {
		t.Fatalf("incorrect recovered progress: %+v", records)
	}
	if err := repository.DB.First(&batch, batch.ID).Error; err != nil {
		t.Fatal(err)
	}
	if batch.Status != "rollback_failed" {
		t.Fatalf("incorrect batch status: %+v", batch)
	}
}

func TestBackgroundRollbackCompletesAndDoesNotRepeat(t *testing.T) {
	setupArtifactDeployRecordTestDB(t)
	batch := model.ArtifactDeployBatch{DeployBatchNo: "successful-rollback", VersionNo: "successful-rollback", Status: "deployed"}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web-main", "web-main"} {
		record := model.ArtifactDeployRecord{DeployBatchNo: batch.DeployBatchNo, VersionNo: batch.VersionNo, Namespace: "test", DeploymentName: name, BeforeImage: "old", DeployStatus: "success", Status: "deployed"}
		if err := repository.DB.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := &backgroundRollbacker{entered: make(chan struct{}), release: make(chan struct{})}
	close(r.release)
	if _, err := StartRollbackDeployBatch(batch.DeployBatchNo, r, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := repository.DB.First(&batch, batch.ID).Error; err != nil {
			t.Fatal(err)
		}
		if batch.Status == "rolled_back" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollback stuck: %+v", batch)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if batch.RolledBackCount != 2 || batch.FailedCount != 0 || batch.FinishedAt == nil {
		t.Fatalf("incorrect completion: %+v", batch)
	}
	if job, err := StartRollbackDeployBatch(batch.DeployBatchNo, r, nil); err != nil || job.Status != "rolled_back" {
		t.Fatalf("completed submission: %+v %v", job, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) != 1 {
		t.Fatalf("repeated shared target: %v", r.calls)
	}
}

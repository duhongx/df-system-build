package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline"
	"df-build-server/internal/repository"
)

func TestSchedulerDispatchesNextBatchTaskAfterFailure(t *testing.T) {
	setupSchedulerTestDB(t)
	repo := repository.NewPipelineRepo()
	// Unsupported types fail inside the real engine without accessing Docker/Kubernetes.
	jobs := []model.Pipeline{
		{PipelineNo: "continue-first", BatchID: "same-batch", AppName: "first", AppType: "invalid-first", Status: "PENDING", TriggerUser: "test"},
		{PipelineNo: "continue-next", BatchID: "same-batch", AppName: "next", AppType: "invalid-next", Status: "PENDING", TriggerUser: "test"},
	}
	for i := range jobs {
		if err := repo.Create(&jobs[i]); err != nil {
			t.Fatal(err)
		}
	}
	s := &BuildScheduler{running: map[uint]context.CancelFunc{}, engine: pipeline.NewEngine(), pipelineRepo: repo, taskRepo: repository.NewTaskRepo(), appRepo: repository.NewApplicationRepo(), buildConfigRepo: repository.NewBuildConfigRepo(), settingsRepo: repository.NewSettingsRepo()}
	for _, job := range jobs {
		s.Enqueue(job.ID)
	}
	s.SetConcurrency(1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.GetRunningCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.GetRunningCount() != 0 {
		t.Fatal("scheduler did not finish")
	}
	for _, job := range jobs {
		got, err := repo.FindByID(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "FAILED" || !strings.Contains(got.ErrorMessage, "unsupported app type: "+job.AppType) {
			t.Fatalf("failure did not preserve cause or next task was not executed: %+v", got)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) != 0 {
		t.Fatalf("queue stalled: %v", s.queue)
	}
}

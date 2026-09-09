package scheduler

import (
	"errors"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/sse"
	"gorm.io/gorm"
)

func TestFinalStateSaveFailureIsReported(t *testing.T) {
	setupSchedulerTestDB(t)
	p := model.Pipeline{PipelineNo: "save-result", Status: "RUNNING", AppName: "service", TriggerUser: "test"}
	repo := repository.NewPipelineRepo()
	if err := repo.Create(&p); err != nil {
		t.Fatal(err)
	}
	cb := "test:pipeline_save_failure"
	if err := repository.DB.Callback().Update().Before("gorm:update").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "pipelines" {
			tx.AddError(errors.New("injected write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer repository.DB.Callback().Update().Remove(cb)
	ch := sse.DefaultHub.Subscribe(p.ID)
	p.Status = "SUCCESS"
	p.DeploymentProgress = "completed"
	s := &BuildScheduler{pipelineRepo: repo}
	if s.savePipelineResult(&p) {
		t.Fatal("failed save reported success")
	}
	event, ok := <-ch
	if !ok || event.Type != "state_error" || !strings.Contains(event.Data, "injected write failure") || !strings.Contains(event.Data, "SUCCESS") || !strings.Contains(event.Data, "部署已成功，结果记录保存失败") {
		t.Fatalf("missing visible failure: %+v", event)
	}
	stored, err := repo.FindByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "RUNNING" {
		t.Fatalf("unexpected persisted state: %s", stored.Status)
	}
}

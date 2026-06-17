package handler

import (
	"net/http/httptest"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/scheduler"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"

	"github.com/gin-gonic/gin"
)

func setupPipelineHandlerTestDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
}

func TestCancelQueuedPipelineRemovesItFromSchedulerQueue(t *testing.T) {
	setupPipelineHandlerTestDB(t)
	gin.SetMode(gin.TestMode)
	scheduler.Init()
	scheduler.DefaultScheduler.SetConcurrency(0)

	p := &model.Pipeline{
		PipelineNo:  "web-main-0001",
		AppName:     "web-main",
		AppType:     "vue",
		GitBranch:   "devops",
		Status:      "PENDING",
		TriggerUser: "tester",
	}
	if err := repository.NewPipelineRepo().Create(p); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	scheduler.DefaultScheduler.Enqueue(p.ID)
	if got := scheduler.DefaultScheduler.GetQueueSize(); got != 1 {
		t.Fatalf("expected queue size 1 before cancel, got %d", got)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	NewPipelineHandler().CancelQueued(c)

	if got := scheduler.DefaultScheduler.GetQueueSize(); got != 0 {
		t.Fatalf("expected queue size 0 after cancel, got %d", got)
	}
}

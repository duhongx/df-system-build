package service

import (
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
)

func setupTaskServiceTestDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
}

func TestUpdateTaskCanClearK8sNamespaceToUseGlobalDefault(t *testing.T) {
	setupTaskServiceTestDB(t)
	app := model.Application{AppName: "his-gateway", AppType: "java", GitRepo: "ssh://git/repo.git"}
	if err := repository.DB.Create(&app).Error; err != nil {
		t.Fatalf("create app: %v", err)
	}
	buildConfig := model.BuildConfig{Name: "java-docker", BuildMode: "docker", DockerImage: "gradle:8"}
	if err := repository.DB.Create(&buildConfig).Error; err != nil {
		t.Fatalf("create build config: %v", err)
	}
	task := model.Task{
		TaskName:      "his-gateway-build",
		ApplicationID: app.ID,
		GitBranch:     "main",
		BuildConfigID: buildConfig.ID,
		DeployMode:    "deploy",
		K8sNamespace:  "old-namespace",
		Enabled:       true,
	}
	if err := repository.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	emptyNamespace := ""
	updated, err := NewTaskService().Update(task.ID, &UpdateTaskRequest{
		TaskName:      task.TaskName,
		ApplicationID: app.ID,
		GitBranch:     task.GitBranch,
		BuildConfigID: buildConfig.ID,
		DeployMode:    "deploy",
		K8sNamespace:  &emptyNamespace,
	})
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	if updated.K8sNamespace != "" {
		t.Fatalf("expected namespace to be cleared, got %q", updated.K8sNamespace)
	}
}

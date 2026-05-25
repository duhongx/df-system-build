package scheduler

import (
	"path/filepath"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/config"
	"df-build-server/pkg/logger"
)

func setupSchedulerTestDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	dbPath := filepath.Join(t.TempDir(), "scheduler-test.db")
	if err := repository.InitDB(&config.DatabaseConfig{Driver: "sqlite", SQLitePath: dbPath}); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
}

func TestBuildContextSupportsArtifactPipelineWithoutTask(t *testing.T) {
	setupSchedulerTestDB(t)
	app := model.Application{
		AppName:      "web-main",
		AppType:      "vue",
		VueRole:      "main",
		GitRepo:      "ssh://git/repo.git",
		NodePort:     30080,
		IngressHost:  "web.example.test",
		ArtifactName: "web-main.zip",
	}
	if err := repository.DB.Create(&app).Error; err != nil {
		t.Fatalf("create app: %v", err)
	}

	s := &BuildScheduler{
		taskRepo:        repository.NewTaskRepo(),
		appRepo:         repository.NewApplicationRepo(),
		buildConfigRepo: repository.NewBuildConfigRepo(),
		settingsRepo:    repository.NewSettingsRepo(),
	}
	p := &model.Pipeline{
		ID:            100,
		ApplicationID: app.ID,
		AppName:       app.AppName,
		AppType:       app.AppType,
		GitBranch:     "manual-upload",
		ArtifactName:  "web-main.zip",
		DeployMode:    "artifact_deploy",
	}

	pCtx := s.buildContext(p)
	if pCtx == nil {
		t.Fatalf("artifact pipeline without task should still build a context")
	}
	if pCtx.VueRole != "main" || pCtx.NodePort != 30080 || pCtx.IngressHost != "web.example.test" {
		t.Fatalf("context did not load app deployment fields: %+v", pCtx)
	}
}

func TestBuildContextRestoresPersistedImageName(t *testing.T) {
	setupSchedulerTestDB(t)
	app := model.Application{AppName: "his-gateway", AppType: "java", GitRepo: "ssh://git/repo.git"}
	if err := repository.DB.Create(&app).Error; err != nil {
		t.Fatalf("create app: %v", err)
	}

	s := &BuildScheduler{
		taskRepo:        repository.NewTaskRepo(),
		appRepo:         repository.NewApplicationRepo(),
		buildConfigRepo: repository.NewBuildConfigRepo(),
		settingsRepo:    repository.NewSettingsRepo(),
	}
	p := &model.Pipeline{
		ID:            101,
		ApplicationID: app.ID,
		AppName:       app.AppName,
		AppType:       app.AppType,
		GitBranch:     "devops",
		ArtifactName:  "his-gateway.jar",
		DeployMode:    "deploy_with_approval",
		ImageName:     "registry.example/his-gateway:devops-20260525",
	}

	pCtx := s.buildContext(p)
	if pCtx == nil {
		t.Fatalf("context should not be nil")
	}
	if pCtx.ImageName != p.ImageName {
		t.Fatalf("expected image name %q, got %q", p.ImageName, pCtx.ImageName)
	}
}

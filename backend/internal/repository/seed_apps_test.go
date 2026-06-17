package repository

import (
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
)

func setupRepositoryTestDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := AutoMigrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
}

func TestSeedApplicationsUsesStandaloneRoleForIndependentVueApps(t *testing.T) {
	setupRepositoryTestDB(t)

	SeedApplications()

	for _, appName := range []string{"web-cdr", "web-opm"} {
		var app model.Application
		if err := DB.Where("app_name = ?", appName).First(&app).Error; err != nil {
			t.Fatalf("find %s: %v", appName, err)
		}
		if app.AppType != "vue" {
			t.Fatalf("%s app type = %q, want vue", appName, app.AppType)
		}
		if app.VueRole != "standalone" {
			t.Fatalf("%s vue role = %q, want standalone", appName, app.VueRole)
		}
		if app.AppCode != "" {
			t.Fatalf("%s app code = %q, want empty", appName, app.AppCode)
		}
		if app.ArtifactName != appName+".zip" {
			t.Fatalf("%s artifact name = %q, want %s.zip", appName, app.ArtifactName, appName)
		}
	}
}

func TestSeedApplicationsKeepsOnlyWebMainAsVueMainAndOmitsDeletedBiaodanEditor(t *testing.T) {
	setupRepositoryTestDB(t)

	SeedApplications()

	var webMain model.Application
	if err := DB.Where("app_name = ?", "web-main").First(&webMain).Error; err != nil {
		t.Fatalf("find web-main: %v", err)
	}
	if webMain.VueRole != "main" {
		t.Fatalf("web-main vue role = %q, want main", webMain.VueRole)
	}
	if webMain.ArtifactName != "web-main.zip" {
		t.Fatalf("web-main artifact name = %q, want web-main.zip", webMain.ArtifactName)
	}

	var deletedCount int64
	DB.Model(&model.Application{}).Where("app_name = ?", "web-biaodanbjq").Count(&deletedCount)
	if deletedCount != 0 {
		t.Fatalf("web-biaodanbjq seed count = %d, want 0", deletedCount)
	}
}

func TestRunMigrationsRepairsExistingIndependentVueApps(t *testing.T) {
	setupRepositoryTestDB(t)
	DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`)
	DB.Exec(`INSERT INTO schema_migrations (version, description) VALUES (4, 'test baseline')`)

	staleApps := []model.Application{
		{AppName: "web-cdr", AppType: "vue", VueRole: "main", ArtifactName: "web-main.zip", GitRepo: "git://web-cdr", Enabled: true},
		{AppName: "web-opm", AppType: "vue", VueRole: "main", ArtifactName: "web-main.zip", GitRepo: "git://web-opm", Enabled: true},
		{AppName: "web-main", AppType: "vue", VueRole: "standalone", AppCode: "wrong", ArtifactName: "web-main.zip", GitRepo: "git://web-main", Enabled: true},
	}
	for _, app := range staleApps {
		if err := DB.Create(&app).Error; err != nil {
			t.Fatalf("insert stale app %s: %v", app.AppName, err)
		}
	}

	RunMigrations()

	for _, appName := range []string{"web-cdr", "web-opm"} {
		var app model.Application
		if err := DB.Where("app_name = ?", appName).First(&app).Error; err != nil {
			t.Fatalf("find migrated %s: %v", appName, err)
		}
		if app.VueRole != "standalone" {
			t.Fatalf("%s migrated vue role = %q, want standalone", appName, app.VueRole)
		}
		if app.AppCode != "" {
			t.Fatalf("%s migrated app code = %q, want empty", appName, app.AppCode)
		}
		if app.ArtifactName != appName+".zip" {
			t.Fatalf("%s migrated artifact name = %q, want %s.zip", appName, app.ArtifactName, appName)
		}
	}

	var webMain model.Application
	if err := DB.Where("app_name = ?", "web-main").First(&webMain).Error; err != nil {
		t.Fatalf("find migrated web-main: %v", err)
	}
	if webMain.VueRole != "main" {
		t.Fatalf("web-main migrated vue role = %q, want main", webMain.VueRole)
	}
	if webMain.AppCode != "" {
		t.Fatalf("web-main migrated app code = %q, want empty", webMain.AppCode)
	}
	if webMain.ArtifactName != "web-main.zip" {
		t.Fatalf("web-main migrated artifact name = %q, want web-main.zip", webMain.ArtifactName)
	}
}

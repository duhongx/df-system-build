package repository

import (
	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"testing"
)

func TestSourceBuildRequiresLocalEnvironment(t *testing.T) {
	originalDB := DB
	t.Cleanup(func() { DB = originalDB })
	DB = testutil.OpenGormPostgres(t)
	if err := DB.AutoMigrate(&model.Settings{}); err != nil {
		t.Fatal(err)
	}
	settings := NewSettingsRepo()
	if err := DB.Where("key = ?", "customer_env").Delete(&model.Settings{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := RequireSourceBuild(); err == nil {
		t.Fatal("missing environment enabled source builds")
	}
	for _, tc := range []struct {
		environment string
		legacyMode  string
		enabled     bool
	}{
		{"本地", "artifact", true}, {" 本地 ", "invalid", true}, {"本地", "", true},
		{"浙江", "both", false}, {"新疆", "source", false}, {"local", "both", false},
		{"", "both", false}, {"  ", "both", false},
	} {
		if err := settings.Set("customer_env", tc.environment); err != nil {
			t.Fatal(err)
		}
		if err := settings.Set("deploy_source", tc.legacyMode); err != nil {
			t.Fatal(err)
		}
		if err := RequireSourceBuild(); (err == nil) != tc.enabled {
			t.Fatalf("environment=%q legacy=%q: %v", tc.environment, tc.legacyMode, err)
		}
	}
	DB = nil
	if err := RequireSourceBuild(); err == nil {
		t.Fatal("unavailable storage enabled source builds")
	}
}

func TestLegacyContainerConfigsRetiredWithoutBreakingNewCreates(t *testing.T) {
	originalDB := DB
	t.Cleanup(func() { DB = originalDB })
	DB = testutil.OpenGormPostgres(t)
	if err := DB.Exec(`CREATE TABLE build_configs (id bigserial PRIMARY KEY, name varchar(64), category varchar(20), build_mode varchar(20) NOT NULL, build_command varchar(512) NOT NULL, status varchar(20), description text);
INSERT INTO build_configs (name, category, build_mode, build_command, status) VALUES ('old-container', 'java', 'docker', 'gradle build', 'online'), ('existing-host', 'java', 'local', 'gradle build', 'online');`).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.AutoMigrate(&model.BuildConfig{}, &model.Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := migrateLocalBuildConfigs(); err != nil {
		t.Fatal(err)
	}
	if err := migrateLocalBuildConfigs(); err != nil {
		t.Fatal(err)
	}
	var rows []model.BuildConfig
	if err := DB.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Status != "offline" || !rows[0].Retired || rows[1].Status != "online" || rows[1].Retired {
		t.Fatalf("incorrect migration: %+v", rows)
	}
	if err := DB.Create(&model.BuildConfig{Name: "new-host", Category: "java", BuildCommand: "gradle build"}).Error; err != nil {
		t.Fatalf("old non-null column rejected new create: %v", err)
	}
}

func TestArchiveLegacyDockerBuildConfigsPreservesNativeConfigsAndHistory(t *testing.T) {
	setupRepositoryTestDB(t)
	if err := DB.Exec(`ALTER TABLE build_configs ADD COLUMN docker_image text;
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, description TEXT, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP);
INSERT INTO schema_migrations (version, description) VALUES (7, 'existing installation');`).Error; err != nil {
		t.Fatal(err)
	}
	configs := []model.BuildConfig{
		{Name: "legacy-container", Category: "java", Status: "offline", BuildCommand: "gradle build"},
		{Name: "Docker maintenance native", Category: "java", Status: "offline", BuildCommand: "gradle build"},
		{Name: "reviewed-native", Category: "java", Status: "online", BuildCommand: "gradle build"},
	}
	for i := range configs {
		if err := DB.Create(&configs[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := DB.Exec("UPDATE build_configs SET docker_image = 'old-image' WHERE id IN (?, ?)", configs[0].ID, configs[2].ID).Error; err != nil {
		t.Fatal(err)
	}
	RunMigrations()
	repo := NewBuildConfigRepo()
	archived, err := repo.FindByID(configs[0].ID)
	if err != nil || !archived.Retired {
		t.Fatalf("historical config lost or not archived: %+v %v", archived, err)
	}
	visible, err := repo.List()
	if err != nil || len(visible) != 2 || visible[0].ID != configs[1].ID || visible[1].ID != configs[2].ID {
		t.Fatalf("native configuration visibility changed: %+v %v", visible, err)
	}
	// Even if an old external writer re-enables it, it cannot become the default.
	if err := DB.Model(archived).UpdateColumn("status", "online").Error; err != nil {
		t.Fatal(err)
	}
	selected, err := DefaultBuildConfig("java")
	if err != nil || selected.ID != configs[2].ID {
		t.Fatalf("archived configuration selected: %+v %v", selected, err)
	}
	// This migration runs once: later disabling a reviewed native configuration
	// must not archive it merely because it retains a historical image column.
	if err := DB.Model(&configs[2]).UpdateColumn("status", "offline").Error; err != nil {
		t.Fatal(err)
	}
	RunMigrations()
	visible, err = repo.List()
	if err != nil || len(visible) != 2 {
		t.Fatalf("repeated startup hid a native configuration: %+v %v", visible, err)
	}
}

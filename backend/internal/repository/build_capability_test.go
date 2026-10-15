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
	if len(rows) != 2 || rows[0].Status != "offline" || rows[1].Status != "online" {
		t.Fatalf("incorrect migration: %+v", rows)
	}
	if err := DB.Create(&model.BuildConfig{Name: "new-host", Category: "java", BuildCommand: "gradle build"}).Error; err != nil {
		t.Fatalf("old non-null column rejected new create: %v", err)
	}
}

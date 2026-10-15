package repository

import (
	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"testing"
)

func TestSourceBuildProfiles(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		enabled bool
		valid   bool
	}{
		{"both", true, true}, {"source", true, true}, {"", true, true}, {"artifact", false, true}, {"invalid", false, false},
	} {
		got, err := sourceBuildEnabled(tc.mode)
		if got != tc.enabled || (err == nil) != tc.valid {
			t.Fatalf("%q: %t %v", tc.mode, got, err)
		}
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

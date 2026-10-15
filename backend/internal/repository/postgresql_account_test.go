package repository

import (
	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
	"strings"
	"testing"
)

func TestPostgreSQLPasswordMigrationAndReads(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := DB.AutoMigrate(&model.Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"postgresql_password", "postgresql_admin_password"} {
		if err := DB.Create(&model.Settings{Key: key, Value: "legacy-password"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := migratePostgreSQLPasswords(); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"postgresql_password", "postgresql_admin_password"} {
			var raw model.Settings
			DB.First(&raw, "key = ?", key)
			if !strings.HasPrefix(raw.Value, postgresSettingCipherPrefix) {
				t.Fatal("legacy plaintext not encrypted")
			}
			value, err := NewSettingsRepo().GetByKey(key)
			if err != nil || value != "legacy-password" {
				t.Fatal("legacy password changed", err)
			}
		}
	}
	all, err := NewSettingsRepo().GetAll()
	if err != nil || len(all) != 2 || all[0].Value != "legacy-password" {
		t.Fatal("settings response cannot read encrypted passwords", err)
	}
	if err := NewSettingsRepo().Set("postgresql_password", "updated-password"); err != nil {
		t.Fatal(err)
	}
	if value, err := NewSettingsRepo().GetByKey("postgresql_password"); err != nil || value != "updated-password" {
		t.Fatal("save roundtrip failed", err)
	}
	DB.Model(&model.Settings{}).Where("key = ?", "postgresql_password").Update("value", postgresSettingCipherPrefix+"corrupt")
	if _, err := NewSettingsRepo().Read("postgresql_password", true); err == nil {
		t.Fatal("corrupt encrypted setting accepted")
	}
}

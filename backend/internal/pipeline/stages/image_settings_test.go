package stages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

type imageSettingsFunc func(string) (string, error)

func (f imageSettingsFunc) GetByKey(key string) (string, error) { return f(key) }

func TestImageSettingsDistinguishMissingAndReadFailures(t *testing.T) {
	cause := errors.New("storage unavailable")
	for _, key := range []string{"docker_registry_url", "docker_registry_user", "docker_registry_password", "skywalking_oap_url", "nacos_user", "nacos_password"} {
		_, err := readImageSetting(imageSettingsFunc(func(string) (string, error) { return "", cause }), key, key == "docker_registry_url")
		if !errors.Is(err, cause) || !strings.Contains(err.Error(), key) {
			t.Fatalf("lost setting/cause for %s: %v", key, err)
		}
	}
	missing := imageSettingsFunc(func(string) (string, error) { return "", gorm.ErrRecordNotFound })
	if value, err := readImageSetting(missing, "nacos_user", false); err != nil || value != "" {
		t.Fatalf("optional missing setting rejected: %q %v", value, err)
	}
	if _, err := readImageSetting(missing, "docker_registry_url", true); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("required setting accepted: %v", err)
	}
	for _, failing := range []string{"docker_registry_user", "docker_registry_password"} {
		_, err := readImageRegistry(imageSettingsFunc(func(key string) (string, error) {
			if key == failing {
				return "", cause
			}
			return "configured-secret", nil
		}))
		if !errors.Is(err, cause) || strings.Contains(err.Error(), "configured-secret") {
			t.Fatalf("credential read failed silently or disclosed value: %v", err)
		}
	}
}

func TestImageStagesStopOnSettingsStorageFailure(t *testing.T) {
	setupImageFlowDB(t)
	cause := errors.New("settings storage unavailable")
	cb := "test:image_settings_read_failure"
	if err := repository.DB.Callback().Query().Before("gorm:query").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			tx.AddError(cause)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.DB.Callback().Query().Remove(cb) })
	pc := &types.PipelineContext{AppType: "java", ImageName: "registry/service:unique", OnLog: func(uint, uint, string, string) {}}
	if _, err := (&BuildImageStage{}).Run(context.Background(), pc); !errors.Is(err, cause) {
		t.Fatalf("build lost database error: %v", err)
	}
	if _, err := (&PushImageStage{}).Run(context.Background(), pc); !errors.Is(err, cause) {
		t.Fatalf("push lost database error: %v", err)
	}
	dir := t.TempDir()
	if err := (&BuildImageStage{}).copyScripts(pc, dir, "registry"); !errors.Is(err, cause) {
		t.Fatalf("script generation lost database error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "app.sh")); !os.IsNotExist(err) {
		t.Fatal("generated script after settings read failure")
	}
}

func TestDockerfileReadFailurePreservesCause(t *testing.T) {
	setupImageFlowDB(t)
	cb := "test:dockerfile_read_failure"
	if err := repository.DB.Callback().Query().Before("gorm:query").Register(cb, func(tx *gorm.DB) {
		if tx.Statement.Table == "config_items" {
			tx.AddError(errors.New("template storage unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.DB.Callback().Query().Remove(cb) })
	pc := &types.PipelineContext{AppType: "java", OnLog: func(uint, uint, string, string) {}}
	_, err := (&BuildImageStage{}).buildStandardImage(context.Background(), pc, repository.NewSettingsRepo(), "registry")
	if err == nil || !strings.Contains(err.Error(), "template storage unavailable") || strings.Contains(err.Error(), "不存在") {
		t.Fatalf("misleading template error: %v", err)
	}
}

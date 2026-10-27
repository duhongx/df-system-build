package repository

import (
	"df-build-server/internal/model"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Source builds are enabled only by the deployment environment. Legacy
// deploy_source values no longer override this setting.
func RequireSourceBuild() error {
	if DB == nil {
		return fmt.Errorf("无法读取部署环境：数据库尚未初始化")
	}
	environment, err := NewSettingsRepo().Read("customer_env", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(environment) != "本地" {
		return fmt.Errorf("非本地环境仅支持制品更新，源码编译已关闭")
	}
	return nil
}

// Retain old columns for database compatibility while retiring container
// execution. Former container configurations require review before enabling.
func migrateLocalBuildConfigs() error {
	if err := DB.Where("key = ?", "build_mode").Delete(&model.Settings{}).Error; err != nil {
		return err
	}
	if !DB.Migrator().HasColumn("build_configs", "build_mode") {
		return nil
	}
	return DB.Exec(`
UPDATE build_configs SET status = 'offline', build_mode = 'local', retired = true WHERE build_mode = 'docker';
ALTER TABLE build_configs ALTER COLUMN build_mode SET DEFAULT 'local';
`).Error
}

// Earlier releases already converted build_mode to local. The retained image
// column identifies those historical container configs without matching names
// or hiding ordinary disabled native configurations. Applied once via v8.
func archiveLegacyDockerBuildConfigs(tx *gorm.DB) error {
	if !tx.Migrator().HasColumn("build_configs", "docker_image") {
		return nil
	}
	return tx.Model(&model.BuildConfig{}).
		Where("status = ? AND COALESCE(docker_image, '') <> ''", "offline").
		UpdateColumn("retired", true).Error
}

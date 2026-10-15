package repository

import (
	"df-build-server/internal/model"
	"fmt"
	"strings"
)

func sourceBuildEnabled(mode string) (bool, error) {
	switch mode {
	case "", "source", "both": // Existing installations without the key retain their behavior.
		return true, nil
	case "artifact":
		return false, nil
	default:
		return false, fmt.Errorf("使用场景配置无效，请在全局参数中重新设置")
	}
}

func RequireSourceBuild() error {
	if DB == nil {
		return fmt.Errorf("无法读取使用场景：数据库尚未初始化")
	}
	mode, err := NewSettingsRepo().Read("deploy_source", false)
	if err != nil {
		return err
	}
	enabled, err := sourceBuildEnabled(mode)
	if err != nil {
		return err
	}
	if !enabled {
		return fmt.Errorf("客户现场仅支持制品更新，源码编译已关闭")
	}
	environment, err := NewSettingsRepo().Read("customer_env", false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(environment) != "" && strings.TrimSpace(environment) != "本地" {
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
UPDATE build_configs SET status = 'offline', build_mode = 'local' WHERE build_mode = 'docker';
ALTER TABLE build_configs ALTER COLUMN build_mode SET DEFAULT 'local';
`).Error
}

package repository

import (
	"fmt"
	"strings"

	"df-build-server/internal/model"
)

// DefaultBuildConfig resolves the shared local toolchain, rather than requiring
// every task to choose it. Existing installations reuse their active toolchain.
func DefaultBuildConfig(appType string) (*model.BuildConfig, error) {
	if appType != "java" && appType != "vue" {
		return nil, fmt.Errorf("不支持的应用类型: %s", appType)
	}
	var configs []model.BuildConfig
	err := DB.Model(&model.BuildConfig{}).Where("build_configs.category = ? AND build_configs.status = ? AND build_configs.retired = ?", appType, "online", false).Order("(SELECT COUNT(*) FROM tasks WHERE tasks.build_config_id = build_configs.id) DESC").Order("build_configs.id ASC").Find(&configs).Error
	if err != nil {
		return nil, err
	}
	for _, bc := range configs {
		command := strings.ToLower(bc.BuildCommand + " " + bc.InstallCommand)
		if (appType == "java" && strings.Contains(command, "gradle")) || (appType == "vue" && (strings.Contains(command, "npm") || strings.Contains(command, "yarn") || strings.Contains(command, "node") || strings.Contains(command, "pnpm"))) {
			return &bc, nil
		}
	}
	tool := "Gradle"
	if appType == "vue" {
		tool = "Node"
	}
	return nil, fmt.Errorf("没有可用的 %s 默认编译配置，请检查本机编译配置是否启用", tool)
}

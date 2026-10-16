package k8s

import (
	"fmt"
	"strings"

	"df-build-server/internal/repository"
)

// Management pages cannot select a different namespace than the environment.
// Discovery for editing environment settings uses its separate settings API.
func ManagementNamespace(requested string) (string, error) {
	configured, err := repository.NewSettingsRepo().Read("k8s_namespace", true)
	if err != nil {
		return "", err
	}
	configured = strings.TrimSpace(configured)
	if strings.TrimSpace(requested) != "" && requested != configured {
		return "", fmt.Errorf("命名空间已由环境配置固定为 %s，请刷新页面", configured)
	}
	return configured, nil
}

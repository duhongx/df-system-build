package model

import (
	"fmt"
	"regexp"
)

var frontendCode = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,19}$`)
var frontendName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func ValidSubAppCode(code string) bool { return frontendCode.MatchString(code) && code != "cdn" }

func (a Application) SharedFrontend() bool {
	return a.AppType == "vue" && (a.VueRole == "main" || a.VueRole == "sub" || a.VueRole == "cdn")
}

func (a Application) DeploymentTarget() string {
	if a.SharedFrontend() {
		return "web-main"
	}
	return a.AppName
}

// ValidateIdentity is shared by registration and execution. An old invalid
// registration must not become executable by bypassing the application form.
func (a Application) ValidateIdentity() error {
	if a.AppType != "java" && a.AppType != "vue" {
		return fmt.Errorf("项目类型必须是 java 或 vue")
	}
	if a.AppType != "vue" {
		return nil
	}
	if len(a.AppName) > 63 || !frontendName.MatchString(a.AppName) {
		return fmt.Errorf("前端应用名称须为小写字母、数字和短横线，最长63位")
	}
	if (a.AppName == "web-main" && a.VueRole != "main") || (a.AppName == "web-cdn" && a.VueRole != "cdn") {
		return fmt.Errorf("web-main 和 web-cdn 为主应用及公共资源保留名称")
	}
	switch a.VueRole {
	case "main":
		if a.AppName != "web-main" {
			return fmt.Errorf("主应用名称必须为 web-main")
		}
	case "sub":
		if !ValidSubAppCode(a.AppCode) {
			return fmt.Errorf("子应用编号须为小写字母、数字、下划线或短横线，最长20位，且不能使用 cdn")
		}
	case "standalone":
		if a.AppName == "web-main" || a.AppName == "web-cdn" {
			return fmt.Errorf("独立应用不能占用共享发布目标名称")
		}
	case "cdn":
		if a.AppName != "web-cdn" {
			return fmt.Errorf("公共资源应用名称必须为 web-cdn")
		}
	default:
		return fmt.Errorf("Vue 应用角色必须是 main/sub/standalone/cdn")
	}
	if a.VueRole != "sub" && a.AppCode != "" {
		return fmt.Errorf("仅子应用可以设置应用编号")
	}
	return nil
}

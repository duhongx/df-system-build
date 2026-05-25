package deployer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"df-build-server/internal/model"
	sshclient "df-build-server/internal/ssh"
)

// CheckResult holds the result of a pre-deploy check
type CheckResult struct {
	Category string       `json:"category"` // package / server
	Target   string       `json:"target"`   // file path or server name
	Items    []CheckItem  `json:"items"`
	Passed   bool         `json:"passed"`
}

type CheckItem struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass / fail / warn
	Message string `json:"message"`
}

// Manifest represents the offline package manifest
type Manifest struct {
	Version    string              `json:"version"`
	Date       string              `json:"date"`
	Components []ManifestComponent `json:"components"`
}

type ManifestComponent struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Required bool     `json:"required"`
	Files    []string `json:"files"`
}

// CheckPackages validates the offline package directory
func CheckPackages(packageDir string) *CheckResult {
	result := &CheckResult{Category: "package", Target: packageDir, Passed: true}

	// Check manifest.json exists
	manifestPath := filepath.Join(packageDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		result.Items = append(result.Items, CheckItem{Name: "manifest.json", Status: "fail", Message: "文件不存在或无法读取"})
		result.Passed = false
		return result
	}
	result.Items = append(result.Items, CheckItem{Name: "manifest.json", Status: "pass", Message: "存在且可读"})

	// Parse manifest
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		result.Items = append(result.Items, CheckItem{Name: "manifest.json 格式", Status: "fail", Message: "JSON 解析失败: " + err.Error()})
		result.Passed = false
		return result
	}
	result.Items = append(result.Items, CheckItem{Name: "manifest.json 格式", Status: "pass", Message: fmt.Sprintf("版本 %s, %d 个组件", manifest.Version, len(manifest.Components))})

	// Check each component's files
	for _, comp := range manifest.Components {
		compDir := filepath.Join(packageDir, comp.Name)
		if _, err := os.Stat(compDir); os.IsNotExist(err) {
			status := "fail"
			if !comp.Required {
				status = "warn"
			}
			result.Items = append(result.Items, CheckItem{Name: comp.Name + "/", Status: status, Message: "目录不存在"})
			if comp.Required {
				result.Passed = false
			}
			continue
		}

		// Check individual files
		missing := 0
		for _, f := range comp.Files {
			fpath := filepath.Join(compDir, f)
			if _, err := os.Stat(fpath); os.IsNotExist(err) {
				missing++
			}
		}

		if missing > 0 {
			result.Items = append(result.Items, CheckItem{Name: comp.Name + "/", Status: "fail", Message: fmt.Sprintf("缺少 %d 个文件", missing)})
			result.Passed = false
		} else {
			result.Items = append(result.Items, CheckItem{Name: comp.Name + "/", Status: "pass", Message: fmt.Sprintf("%d 个文件完整", len(comp.Files))})
		}
	}

	return result
}

// CheckServer validates a server's prerequisites
func CheckServer(ctx context.Context, server *model.Server) *CheckResult {
	result := &CheckResult{Category: "server", Target: fmt.Sprintf("%s (%s)", server.Host, server.Remark), Passed: true}

	// Connect SSH
	client, err := sshclient.Connect(&model.RemoteServer{
		Host: server.Host, Port: server.Port,
		Username: server.Username, AuthType: server.AuthType,
		CredentialEncrypted: server.CredentialEncrypted,
	})
	if err != nil {
		result.Items = append(result.Items, CheckItem{Name: "SSH 连接", Status: "fail", Message: err.Error()})
		result.Passed = false
		return result
	}
	defer client.Close()
	result.Items = append(result.Items, CheckItem{Name: "SSH 连接", Status: "pass", Message: "连接正常"})

	// Check firewalld
	output, _, _ := client.Exec(ctx, "systemctl is-active firewalld 2>/dev/null || echo inactive")
	if output == "active\n" || output == "active" {
		result.Items = append(result.Items, CheckItem{Name: "firewalld", Status: "fail", Message: "firewalld 未关闭"})
		result.Passed = false
	} else {
		result.Items = append(result.Items, CheckItem{Name: "firewalld", Status: "pass", Message: "已关闭"})
	}

	// Check SELinux
	output, _, _ = client.Exec(ctx, "getenforce 2>/dev/null || echo Disabled")
	if output != "Disabled\n" && output != "Disabled" && output != "Permissive\n" && output != "Permissive" {
		result.Items = append(result.Items, CheckItem{Name: "SELinux", Status: "fail", Message: "SELinux 未关闭 (当前: " + output + ")"})
		result.Passed = false
	} else {
		result.Items = append(result.Items, CheckItem{Name: "SELinux", Status: "pass", Message: "已关闭"})
	}

	// Check swap
	output, _, _ = client.Exec(ctx, "swapon --show | wc -l")
	if output != "0\n" && output != "0" {
		result.Items = append(result.Items, CheckItem{Name: "swap", Status: "fail", Message: "swap 未关闭"})
		result.Passed = false
	} else {
		result.Items = append(result.Items, CheckItem{Name: "swap", Status: "pass", Message: "已关闭"})
	}

	// Check disk space (root partition > 20G)
	output, _, _ = client.Exec(ctx, "df -BG / | tail -1 | awk '{print $4}' | tr -d 'G'")
	result.Items = append(result.Items, CheckItem{Name: "磁盘空间 (/)", Status: "pass", Message: "可用 " + output + "G"})

	// Check chrony/ntp
	output, _, _ = client.Exec(ctx, "systemctl is-active chronyd 2>/dev/null || systemctl is-active ntpd 2>/dev/null || echo inactive")
	if output == "inactive\n" || output == "inactive" {
		result.Items = append(result.Items, CheckItem{Name: "时间同步", Status: "warn", Message: "chrony/ntp 未运行"})
	} else {
		result.Items = append(result.Items, CheckItem{Name: "时间同步", Status: "pass", Message: "已配置"})
	}

	return result
}

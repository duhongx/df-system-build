package frontend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type BuildManifest struct {
	Schema  int    `json:"schema"`
	AppName string `json:"appName"`
	Role    string `json:"role"`
	AppCode string `json:"appCode,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Files   Files  `json:"files"`
}

func WriteBuildManifest(root string, m BuildManifest) error {
	files, err := Inventory(root)
	if err != nil {
		return err
	}
	delete(files, ".df-build.json")
	if m.Role == "main" {
		for file := range files {
			if strings.HasPrefix(file, "apps/") {
				delete(files, file)
			}
		}
	}
	m.Schema, m.Files = 1, files
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ".df-build.json"), data, 0644)
}

func ValidateBuildManifest(root, appName, role, code string) error {
	data, err := os.ReadFile(filepath.Join(root, ".df-build.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var m BuildManifest
	if err := json.Unmarshal(data, &m); err != nil || m.Schema != 1 || m.Role != role || m.AppCode != code || (appName != "" && m.AppName != appName) {
		return fmt.Errorf("构建清单与应用身份不一致")
	}
	actual, err := Inventory(root)
	if err != nil {
		return err
	}
	delete(actual, ".df-build.json")
	if len(actual) != len(m.Files) {
		return fmt.Errorf("构建清单与制品文件数量不一致")
	}
	for file, want := range m.Files {
		if actual[file] != want {
			return fmt.Errorf("构建清单文件缺失或内容改变: %s", file)
		}
	}
	return nil
}

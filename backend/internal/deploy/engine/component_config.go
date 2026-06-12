package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func ComponentConfigDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "components")
}

func LoadDeploymentCustomContent(configPath string, customPath string) ([]byte, error) {
	if content, ok, err := loadSplitComponentConfigContent(configPath); err != nil {
		return nil, err
	} else if ok {
		return content, nil
	}
	if customPath != "" {
		if content, err := os.ReadFile(customPath); err == nil {
			return content, nil
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("读取自定义配置失败: %w", err)
		}
	}
	return []byte("components: {}\n"), nil
}

func ListComponentConfigNames(configPath string) ([]string, error) {
	dir := ComponentConfigDir(configPath)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isYAMLFile(entry.Name()) {
			continue
		}
		names = append(names, strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".yaml"), ".yml"))
	}
	sort.Strings(names)
	return names, nil
}

func ReadComponentConfig(configPath string, name string) (map[string]any, []byte, error) {
	path := componentConfigPath(configPath, name)
	content, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	components, err := parseComponentConfigFile(name, content)
	if err != nil {
		return nil, nil, err
	}
	if values, ok := components[name]; ok {
		return values, content, nil
	}
	return map[string]any{}, content, nil
}

func WriteComponentConfig(configPath string, name string, values map[string]any) error {
	if name == "" {
		return fmt.Errorf("组件名不能为空")
	}
	dir := ComponentConfigDir(configPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	target := componentConfigPath(configPath, name)
	content, err := yaml.Marshal(map[string]any{name: values})
	if err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func loadSplitComponentConfigContent(configPath string) ([]byte, bool, error) {
	dir := ComponentConfigDir(configPath)
	names, err := ListComponentConfigNames(configPath)
	if err != nil {
		return nil, false, fmt.Errorf("读取组件配置目录失败: %w", err)
	}
	if len(names) == 0 {
		return nil, false, nil
	}
	merged := deploymentCustomFile{Components: map[string]map[string]any{}}
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name+".yml"))
		if os.IsNotExist(err) {
			content, err = os.ReadFile(filepath.Join(dir, name+".yaml"))
		}
		if err != nil {
			return nil, false, err
		}
		components, err := parseComponentConfigFile(name, content)
		if err != nil {
			return nil, false, err
		}
		for component, values := range components {
			merged.Components[component] = values
		}
	}
	content, err := yaml.Marshal(merged)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func parseComponentConfigFile(defaultName string, content []byte) (map[string]map[string]any, error) {
	var raw map[string]any
	if err := yaml.NewDecoder(bytes.NewReader(content)).Decode(&raw); err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	if componentsRaw, ok := raw["components"].(map[string]any); ok {
		for component, valuesRaw := range componentsRaw {
			values, ok := valuesRaw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("组件 %s 配置必须是映射", component)
			}
			out[component] = values
		}
		return out, nil
	}
	if valuesRaw, ok := raw[defaultName]; ok {
		values, ok := valuesRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("组件 %s 配置必须是映射", defaultName)
		}
		out[defaultName] = values
		return out, nil
	}
	if len(raw) > 0 {
		out[defaultName] = raw
	}
	return out, nil
}

func componentConfigPath(configPath string, name string) string {
	return filepath.Join(ComponentConfigDir(configPath), name+".yml")
}

func isYAMLFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

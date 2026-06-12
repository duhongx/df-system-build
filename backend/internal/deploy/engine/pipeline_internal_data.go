package engine

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed pipeline/*.yml
var pipelineFS embed.FS

// defaultPipelineYAML is assembled from the embedded YAML files for backward
// compatibility with code that needs the full pipeline as a single YAML string.
var defaultPipelineYAML = func() string {
	defaultsData, err := pipelineFS.ReadFile("pipeline/defaults.yml")
	if err != nil {
		panic("embedded pipeline: " + err.Error())
	}

	entries, err := pipelineFS.ReadDir("pipeline")
	if err != nil {
		panic("embedded pipeline: " + err.Error())
	}

	var buf bytes.Buffer
	buf.Write(defaultsData)
	buf.WriteString("\ncomponents:\n")

	for _, entry := range entries {
		name := entry.Name()
		if name == "defaults.yml" || !strings.HasSuffix(name, ".yml") {
			continue
		}
		componentName := strings.TrimSuffix(name, ".yml")
		data, err := pipelineFS.ReadFile(filepath.Join("pipeline", name))
		if err != nil {
			panic("embedded pipeline: " + err.Error())
		}
		buf.WriteString("  " + componentName + ":\n")
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" {
				buf.WriteString("\n")
			} else {
				buf.WriteString("    " + line + "\n")
			}
		}
	}
	return buf.String()
}()

// loadEmbeddedPipeline loads the default pipeline configuration from embedded
// YAML files. It reads pipeline/defaults.yml for cluster and hosts, then reads
// all other pipeline/*.yml files as individual component definitions.
func loadEmbeddedPipeline() (*Config, error) {
	cfg := &Config{
		Components: make(map[string]Component),
	}

	// Load defaults (cluster + hosts)
	defaultsData, err := pipelineFS.ReadFile("pipeline/defaults.yml")
	if err != nil {
		return nil, fmt.Errorf("读取默认配置失败: %w", err)
	}
	if err := yaml.Unmarshal(defaultsData, cfg); err != nil {
		return nil, fmt.Errorf("解析默认配置失败: %w", err)
	}

	// Load all component files
	entries, err := pipelineFS.ReadDir("pipeline")
	if err != nil {
		return nil, fmt.Errorf("读取pipeline目录失败: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "defaults.yml" {
			continue
		}
		if !strings.HasSuffix(name, ".yml") {
			continue
		}
		componentName := strings.TrimSuffix(name, ".yml")

		data, err := pipelineFS.ReadFile(filepath.Join("pipeline", name))
		if err != nil {
			return nil, fmt.Errorf("读取组件文件 %s 失败: %w", name, err)
		}
		var comp Component
		if err := yaml.Unmarshal(data, &comp); err != nil {
			return nil, fmt.Errorf("解析组件 %s 失败: %w", componentName, err)
		}
		cfg.Components[componentName] = comp
	}

	return cfg, nil
}

// LoadDefaultPipelineConfig loads the built-in default pipeline configuration
// from embedded YAML files.
func LoadDefaultPipelineConfig() (*Config, error) {
	cfg, err := loadEmbeddedPipeline()
	if err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

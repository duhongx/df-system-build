package engine

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadDeploymentConfigFiles(configPath string, customPath string) (*Config, error) {
	configContent, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if isUnifiedInternalConfig(configContent) {
		return LoadConfig(bytes.NewReader(configContent))
	}
	customContent, err := LoadDeploymentCustomContent(configPath, customPath)
	if err != nil {
		return nil, err
	}
	pipelineCfg, err := loadEmbeddedPipeline()
	if err != nil {
		return nil, fmt.Errorf("加载内置pipeline失败: %w", err)
	}
	return CompileDeploymentConfigFromPipeline(bytes.NewReader(configContent), bytes.NewReader(customContent), pipelineCfg)
}

func isUnifiedInternalConfig(content []byte) bool {
	var raw struct {
		Components map[string]Component `yaml:"components"`
		Cluster    ClusterConfig        `yaml:"cluster"`
	}
	if err := yaml.Unmarshal(content, &raw); err != nil {
		return false
	}
	return len(raw.Components) > 0 && raw.Cluster.ResourceDir != ""
}

// CompileDeploymentConfigFromPipeline is like CompileDeploymentConfig but accepts
// a pre-loaded pipeline *Config instead of an io.Reader.
func CompileDeploymentConfigFromPipeline(envReader io.Reader, customReader io.Reader, pipeline *Config) (*Config, error) {
	var envFile deploymentEnvFile
	if err := yaml.NewDecoder(envReader).Decode(&envFile); err != nil {
		return nil, fmt.Errorf("解析环境配置失败: %w", err)
	}
	var customFile deploymentCustomFile
	if customReader != nil {
		if err := yaml.NewDecoder(customReader).Decode(&customFile); err != nil {
			return nil, fmt.Errorf("解析自定义配置失败: %w", err)
		}
	}
	if envFile.Env.Name != "" {
		pipeline.Cluster.Name = envFile.Env.Name
	}
	if envFile.Env.ResourceDir != "" {
		pipeline.Cluster.ResourceDir = envFile.Env.ResourceDir
	}
	if envFile.Env.RemoteRoot != "" {
		pipeline.Cluster.RemoteRoot = envFile.Env.RemoteRoot
	}
	if envFile.Env.StateDir != "" {
		pipeline.Cluster.StateDir = envFile.Env.StateDir
	}
	hosts, err := compileHosts(envFile.Hosts)
	if err != nil {
		return nil, err
	}
	// Same rationale as compileDeploymentConfig: an explicit hosts: []
	// in the env file overrides the embedded pipeline's dev-time
	// defaults, which is what dfctl-web needs when the database is
	// fresh.
	if envFile.Hosts != nil {
		pipeline.Hosts = hosts
	}
	if envFile.DeployComponents != nil {
		enabled := map[string]bool{}
		for _, component := range *envFile.DeployComponents {
			if _, ok := pipeline.Components[component]; !ok {
				return nil, fmt.Errorf("配置错误: deploy_components 包含不存在的组件 %s", component)
			}
			enabled[component] = true
		}
		for name, component := range pipeline.Components {
			component.Enabled = enabled[name]
			pipeline.Components[name] = component
		}
	}
	if err := applyCustomTargetRolesOverride(pipeline, customFile); err != nil {
		return nil, err
	}
	vars := deploymentVariables(envFile, customFile, pipeline.Cluster)
	slbCfg := extractSLBConfig(envFile.Hosts)
	addDerivedDeploymentVariables(vars, hosts, slbCfg)
	applyVariablesToConfig(pipeline, vars)
	if err := pipeline.validate(); err != nil {
		return nil, err
	}
	return pipeline, nil
}

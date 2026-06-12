package engine

import (
	"fmt"
	"io"
	"sort"

	"gopkg.in/yaml.v3"
)

func LoadConfig(r io.Reader) (*Config, error) {
	var cfg Config
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

type deploymentEnvFile struct {
	Env              deploymentEnvSettings `yaml:"env"`
	DeployComponents *[]string             `yaml:"deploy_components"`
	Hosts            any                   `yaml:"hosts"`
	Network          map[string]any        `yaml:"network"`
	Vars             map[string]any        `yaml:"vars"`
}

type deploymentEnvSettings struct {
	Name        string `yaml:"name"`
	ResourceDir string `yaml:"resource_dir"`
	RemoteRoot  string `yaml:"remote_root"`
	StateDir    string `yaml:"state_dir"`
}

type deploymentCustomFile struct {
	Global     map[string]any            `yaml:"global"`
	Components map[string]map[string]any `yaml:"components"`
}

func CompileDeploymentConfig(envReader io.Reader, customReader io.Reader, pipelineReader io.Reader) (*Config, error) {
	pipeline, err := LoadConfig(pipelineReader)
	if err != nil {
		return nil, err
	}
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
	// When the env file explicitly sets `hosts:` (even to an empty
	// list) we honour it as-is. Treating "key absent" and "empty list"
	// identically would let dfctl-web's empty database silently fall
	// back to dev-time hosts baked into the embedded pipeline.
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
	// custom.yml may carry per-component `target_roles` overrides.
	// Pull them out *before* deploymentVariables runs so they don't
	// leak into the variable table as ${component.target_roles}.
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

// applyCustomTargetRolesOverride lets dfctl-web (or any custom.yml
// author) replace a component's target_roles without touching the
// embedded pipeline. We mutate pipeline.Components in place and remove
// the key from customFile.Components so it doesn't get flattened into
// the variable map afterwards.
//
// The key may be either a []string (typical) or a []interface{} when
// yaml decoded into map[string]any. Both shapes are accepted.
func applyCustomTargetRolesOverride(pipeline *Config, customFile deploymentCustomFile) error {
	for name, params := range customFile.Components {
		raw, ok := params["target_roles"]
		if !ok {
			continue
		}
		delete(params, "target_roles")
		roles, err := coerceStringSlice(raw)
		if err != nil {
			return fmt.Errorf("custom.components.%s.target_roles: %w", name, err)
		}
		comp, exists := pipeline.Components[name]
		if !exists {
			// Silently ignore: the engine will surface the unknown
			// component on its own when it's actually referenced.
			continue
		}
		if len(roles) > 0 {
			comp.TargetRoles = roles
			pipeline.Components[name] = comp
		}
	}
	return nil
}

// coerceStringSlice accepts either []string or []interface{} (the
// shape yaml.v3 produces for `map[string]any`) and returns a []string.
func coerceStringSlice(v any) ([]string, error) {
	switch val := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return val, nil
	case []any:
		out := make([]string, 0, len(val))
		for i, item := range val {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, expected string", i, item)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected list of strings, got %T", v)
	}
}

func sortedAnyMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func stringSliceValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := stringValue(item); text != "" {
				values = append(values, text)
			}
		}
		return values
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func (cfg *Config) ComponentOrder() ([]string, error) {
	names := make([]string, 0, len(cfg.Components))
	for name := range cfg.Components {
		names = append(names, name)
	}
	cfg.sortComponents(names)

	visited := map[string]bool{}
	visiting := map[string]bool{}
	var order []string
	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("组件依赖存在循环: %s", name)
		}
		component, ok := cfg.Components[name]
		if !ok {
			return fmt.Errorf("依赖组件不存在: %s", name)
		}
		visiting[name] = true
		deps := append([]string{}, component.DependsOn...)
		cfg.sortComponents(deps)
		for _, dep := range deps {
			if dep == "" {
				continue
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		order = append(order, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func (cfg *Config) sortComponents(names []string) {
	sort.Slice(names, func(i, j int) bool {
		left := cfg.Components[names[i]]
		right := cfg.Components[names[j]]
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return names[i] < names[j]
	})
}

func ComponentOrderForPhase(cfg *Config, phase string) ([]string, error) {
	order, err := cfg.ComponentOrder()
	if err != nil {
		return nil, err
	}
	if phase == "rollback" || phase == "residue" {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	enabled := make([]string, 0, len(order))
	for _, componentName := range order {
		if cfg.Components[componentName].Enabled {
			enabled = append(enabled, componentName)
		}
	}
	return enabled, nil
}

func (cfg *Config) validate() error {
	if cfg.Cluster.ResourceDir == "" {
		return fmt.Errorf("配置错误: cluster.resource_dir 不能为空")
	}
	if len(cfg.Hosts) == 0 {
		return fmt.Errorf("配置错误: hosts 不能为空")
	}
	for i, host := range cfg.Hosts {
		if host.Name == "" {
			return fmt.Errorf("配置错误: hosts[%d].name 不能为空", i)
		}
		if host.Address == "" {
			return fmt.Errorf("配置错误: hosts[%d].address 不能为空", i)
		}
		if len(host.Roles) == 0 {
			return fmt.Errorf("配置错误: hosts[%d].roles 不能为空", i)
		}
	}
	if len(cfg.Components) == 0 {
		return fmt.Errorf("配置错误: components 不能为空")
	}
	for name, component := range cfg.Components {
		if len(component.TargetRoles) == 0 {
			return fmt.Errorf("配置错误: components.%s.target_roles 不能为空", name)
		}
		for i, task := range component.Tasks {
			if task.ID == "" {
				return fmt.Errorf("配置错误: components.%s.tasks[%d].id 不能为空", name, i)
			}
			if task.Name == "" {
				return fmt.Errorf("配置错误: components.%s.tasks[%d].name 不能为空", name, i)
			}
			if task.Phase == "" {
				return fmt.Errorf("配置错误: components.%s.tasks[%d].phase 不能为空", name, i)
			}
		}
	}
	if component, ok := cfg.Components["slb"]; ok && component.Enabled {
		targetRoles := map[string]bool{}
		for _, role := range component.TargetRoles {
			targetRoles[role] = true
		}
		targets := 0
		for _, host := range cfg.Hosts {
			for _, role := range host.Roles {
				if targetRoles[role] {
					targets++
					break
				}
			}
		}
		if targets != 1 {
			return fmt.Errorf("配置错误: slb 必须且只能匹配 1 台目标主机,当前 %d 台", targets)
		}
	}
	return nil
}

func (cfg *Config) TargetsForComponent(componentName string) ([]Host, error) {
	component, ok := cfg.Components[componentName]
	if !ok {
		return nil, fmt.Errorf("组件不存在: %s", componentName)
	}
	if !component.Enabled {
		return nil, fmt.Errorf("组件未启用: %s", componentName)
	}

	targetRoles := map[string]bool{}
	for _, role := range component.TargetRoles {
		targetRoles[role] = true
	}

	var targets []Host
	for _, host := range cfg.Hosts {
		for _, role := range host.Roles {
			if targetRoles[role] {
				targets = append(targets, host)
				break
			}
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("组件 %s 没有匹配目标主机", componentName)
	}
	return targets, nil
}

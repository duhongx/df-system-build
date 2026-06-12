package engine

type PlannedTask struct {
	Context TaskContext
	Host    Host
	Task    Task
}

func DeployPhases() []string {
	return []string{"preflight", "render", "deploy", "test"}
}

func BuildPlan(cfg *Config, componentName string, phase string) ([]PlannedTask, error) {
	return BuildPlanForHost(cfg, componentName, phase, "")
}

func BuildPlanForPhases(cfg *Config, componentName string, phases []string, hostFilter string) ([]PlannedTask, error) {
	var plan []PlannedTask
	for _, phase := range phases {
		items, err := BuildPlanForHost(cfg, componentName, phase, hostFilter)
		if err != nil {
			var deployErr *DeployError
			if phaseHasNoTasks(err, &deployErr) {
				continue
			}
			return nil, err
		}
		plan = append(plan, items...)
	}
	if len(plan) == 0 {
		return nil, &DeployError{
			Context:    TaskContext{Component: componentName, HostName: hostFilter},
			Action:     "生成部署计划",
			Position:   "阶段列表",
			Reason:     "没有匹配任务",
			Detail:     componentName + " 没有匹配的阶段任务",
			Suggestion: "检查组件 tasks 的 phase 是否配置正确。",
		}
	}
	return plan, nil
}

func BuildPlanForHost(cfg *Config, componentName string, phase string, hostFilter string) ([]PlannedTask, error) {
	component, ok := cfg.Components[componentName]
	if !ok {
		return nil, &DeployError{
			Context:    TaskContext{Component: componentName},
			Action:     "生成部署计划",
			Position:   "配置 components." + componentName,
			Reason:     "组件不存在",
			Detail:     "配置文件中没有找到组件 " + componentName,
			Suggestion: "检查 dfctl 配置文件 components 是否包含该组件。",
		}
	}
	targets, err := cfg.TargetsForComponent(componentName)
	if err != nil {
		return nil, err
	}

	var plan []PlannedTask
	matchedHost := hostFilter == ""
	if hostFilter != "" {
		for _, host := range targets {
			if host.Name == hostFilter || host.Address == hostFilter {
				matchedHost = true
				break
			}
		}
	}
	for _, task := range component.Tasks {
		if phase != "" && phase != "all" && task.Phase != phase {
			continue
		}
		for _, host := range targets {
			if hostFilter != "" && host.Name != hostFilter && host.Address != hostFilter {
				continue
			}
			plan = append(plan, PlannedTask{
				Context: TaskContext{
					Component: componentName,
					HostName:  host.Name,
					HostAddr:  host.Address,
					TaskID:    task.ID,
					TaskName:  task.Name,
					Phase:     task.Phase,
				},
				Host: host,
				Task: task,
			})
		}
	}
	if len(plan) == 0 && hostFilter != "" && !matchedHost {
		return nil, &DeployError{
			Context:    TaskContext{Component: componentName, HostName: hostFilter},
			Action:     "生成部署计划",
			Position:   "目标主机 " + hostFilter,
			Reason:     "目标主机没有匹配任务",
			Detail:     "组件 " + componentName + " 的目标主机中没有找到 " + hostFilter,
			Suggestion: "检查配置 hosts、roles、component.target_roles 和 --host 参数是否一致。",
		}
	}
	if len(plan) == 0 {
		return nil, &DeployError{
			Context:    TaskContext{Component: componentName},
			Action:     "生成部署计划",
			Position:   "阶段 " + phase,
			Reason:     "没有匹配任务",
			Detail:     "组件 " + componentName + " 没有 phase=" + phase + " 的任务",
			Suggestion: "检查组件 tasks 的 phase 是否配置正确。",
		}
	}
	return plan, nil
}

func phaseHasNoTasks(err error, deployErr **DeployError) bool {
	if !AsDeployError(err, deployErr) {
		return false
	}
	return (*deployErr).Reason == "没有匹配任务"
}

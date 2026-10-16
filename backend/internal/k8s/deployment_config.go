package k8s

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Pointers distinguish an unchanged field from an explicit empty resource value.
// No environment, volume, image, or arbitrary Deployment fields are accepted.
type ContainerConfigEdit struct {
	Name                    string  `json:"name"`
	CPURequest              *string `json:"cpuRequest,omitempty"`
	CPULimit                *string `json:"cpuLimit,omitempty"`
	MemoryRequest           *string `json:"memoryRequest,omitempty"`
	MemoryLimit             *string `json:"memoryLimit,omitempty"`
	StartupInitialDelay     *int32  `json:"startupInitialDelay,omitempty"`
	ReadinessInitialDelay   *int32  `json:"readinessInitialDelay,omitempty"`
	LivenessInitialDelay    *int32  `json:"livenessInitialDelay,omitempty"`
	StartupFailureThreshold *int32  `json:"startupFailureThreshold,omitempty"`
}

type DeploymentConfigEdit struct {
	UID             string                `json:"uid"`
	ResourceVersion string                `json:"resourceVersion"`
	Replicas        *int32                `json:"replicas,omitempty"`
	Containers      []ContainerConfigEdit `json:"containers"`
}

type ConfigDifference struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func ExpectedDeploymentStrategy(name string, managedMicroservice bool) appsv1.DeploymentStrategyType {
	if name == "df-cdc" {
		return appsv1.RecreateDeploymentStrategyType
	}
	if managedMicroservice {
		return appsv1.RollingUpdateDeploymentStrategyType
	}
	return ""
}

// Used both by configuration changes and first/subsequent image publications.
func ApplyDeploymentStrategy(dep *appsv1.Deployment, managedMicroservice bool) {
	expected := ExpectedDeploymentStrategy(dep.Name, managedMicroservice)
	if expected == "" {
		return
	}
	dep.Spec.Strategy.Type = expected
	if expected == appsv1.RecreateDeploymentStrategyType {
		dep.Spec.Strategy.RollingUpdate = nil
	} else if dep.Spec.Strategy.RollingUpdate == nil {
		dep.Spec.Strategy.RollingUpdate = &appsv1.RollingUpdateDeployment{MaxSurge: ptrIntString("25%"), MaxUnavailable: ptrIntString("25%")}
	}
}

func ptrIntString(value string) *intstr.IntOrString {
	x := intstr.Parse(value)
	return &x
}

func PrepareDeploymentConfig(before *appsv1.Deployment, edit DeploymentConfigEdit, managed bool) (*appsv1.Deployment, []ConfigDifference, error) {
	if edit.UID == "" || edit.ResourceVersion == "" || edit.UID != string(before.UID) || edit.ResourceVersion != before.ResourceVersion {
		return nil, nil, fmt.Errorf("Deployment 已变化，请重新加载配置")
	}
	next := before.DeepCopy()
	if edit.Replicas != nil {
		if *edit.Replicas < 0 {
			return nil, nil, fmt.Errorf("副本数不能为负数")
		}
		value := *edit.Replicas
		next.Spec.Replicas = &value
	}
	seen := map[string]bool{}
	for _, change := range edit.Containers {
		if change.Name == "" || seen[change.Name] {
			return nil, nil, fmt.Errorf("容器名称为空或重复")
		}
		seen[change.Name] = true
		index := -1
		for i := range next.Spec.Template.Spec.Containers {
			if next.Spec.Template.Spec.Containers[i].Name == change.Name {
				index = i
				break
			}
		}
		if index < 0 {
			return nil, nil, fmt.Errorf("容器 %s 不存在", change.Name)
		}
		c := &next.Spec.Template.Spec.Containers[index]
		for _, field := range []struct {
			value *string
			name  corev1.ResourceName
			limit bool
		}{
			{change.CPURequest, corev1.ResourceCPU, false}, {change.CPULimit, corev1.ResourceCPU, true},
			{change.MemoryRequest, corev1.ResourceMemory, false}, {change.MemoryLimit, corev1.ResourceMemory, true},
		} {
			if field.value == nil {
				continue
			}
			values := c.Resources.Requests
			if field.limit {
				values = c.Resources.Limits
			}
			if values == nil {
				values = corev1.ResourceList{}
			}
			value := strings.TrimSpace(*field.value)
			if value == "" {
				delete(values, field.name)
			} else {
				quantity, err := resource.ParseQuantity(value)
				if err != nil || quantity.Sign() < 0 {
					return nil, nil, fmt.Errorf("容器 %s 的 %s 资源量无效: %s", c.Name, field.name, value)
				}
				values[field.name] = quantity
			}
			if field.limit {
				c.Resources.Limits = values
			} else {
				c.Resources.Requests = values
			}
		}
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			request, hasRequest := c.Resources.Requests[name]
			limit, hasLimit := c.Resources.Limits[name]
			if hasRequest && hasLimit && request.Cmp(limit) > 0 {
				return nil, nil, fmt.Errorf("容器 %s 的 %s 请求量不能超过限制量", c.Name, name)
			}
		}
		for _, field := range []struct {
			probe *corev1.Probe
			value *int32
			name  string
		}{
			{c.StartupProbe, change.StartupInitialDelay, "startupProbe"},
			{c.ReadinessProbe, change.ReadinessInitialDelay, "readinessProbe"},
			{c.LivenessProbe, change.LivenessInitialDelay, "livenessProbe"},
		} {
			if field.value == nil {
				continue
			}
			if field.probe == nil {
				return nil, nil, fmt.Errorf("容器 %s 未配置 %s，不能修改其延迟", c.Name, field.name)
			}
			if *field.value < 0 || *field.value > 86400 {
				return nil, nil, fmt.Errorf("探针初始延迟必须为 0～86400 秒")
			}
			field.probe.InitialDelaySeconds = *field.value
		}
		if change.StartupFailureThreshold != nil {
			if c.StartupProbe == nil || *change.StartupFailureThreshold < 1 || *change.StartupFailureThreshold > 86400 {
				return nil, nil, fmt.Errorf("startupProbe 失败阈值必须为 1～86400，且探针必须已配置")
			}
			c.StartupProbe.FailureThreshold = *change.StartupFailureThreshold
		}
	}
	ApplyDeploymentStrategy(next, managed)
	// A longer startup allowance must not outlive the Deployment progress limit.
	if !apiequality.Semantic.DeepEqual(before.Spec.Template, next.Spec.Template) || DesiredReplicas(before) != DesiredReplicas(next) {
		EnsureDeploymentProgress(next)
	}
	return next, DeploymentConfigDifferences(before, next), nil
}

func DesiredReplicas(dep *appsv1.Deployment) int32 {
	if dep.Spec.Replicas == nil {
		return 1
	}
	return *dep.Spec.Replicas
}

func EnsureDeploymentProgress(dep *appsv1.Deployment) {
	required := RequiredProgressSeconds(dep)
	if dep.Spec.ProgressDeadlineSeconds == nil || *dep.Spec.ProgressDeadlineSeconds < required {
		dep.Spec.ProgressDeadlineSeconds = &required
	}
}

func ProbeStartSeconds(c corev1.Container) int64 {
	var seconds int64
	if p := c.StartupProbe; p != nil {
		period, failures := p.PeriodSeconds, p.FailureThreshold
		if period < 1 {
			period = 10
		}
		if failures < 1 {
			failures = 3
		}
		seconds = int64(p.InitialDelaySeconds) + int64(period)*int64(failures) + int64(p.TimeoutSeconds)
	}
	if p := c.ReadinessProbe; p != nil {
		period, successes := p.PeriodSeconds, p.SuccessThreshold
		if period < 1 {
			period = 10
		}
		if successes < 1 {
			successes = 1
		}
		seconds += int64(p.InitialDelaySeconds) + int64(period)*int64(successes) + int64(p.TimeoutSeconds)
	}
	return seconds
}

func RequiredProgressSeconds(dep *appsv1.Deployment) int32 {
	seconds := int64(600)
	for _, c := range dep.Spec.Template.Spec.Containers {
		if candidate := ProbeStartSeconds(c) + int64(dep.Spec.MinReadySeconds) + 120; candidate > seconds {
			seconds = candidate
		}
	}
	if seconds > 2147483000 {
		seconds = 2147483000
	}
	return int32(seconds)
}

func DeploymentWaitTimeout(dep *appsv1.Deployment) time.Duration {
	seconds := RequiredProgressSeconds(dep)
	if dep.Spec.ProgressDeadlineSeconds != nil && *dep.Spec.ProgressDeadlineSeconds > seconds {
		seconds = *dep.Spec.ProgressDeadlineSeconds
	}
	grace := int64(30)
	if dep.Spec.Template.Spec.TerminationGracePeriodSeconds != nil {
		grace = *dep.Spec.Template.Spec.TerminationGracePeriodSeconds
	}
	return time.Duration(int64(seconds)+grace+120) * time.Second
}

func DeploymentConfigDifferences(before, after *appsv1.Deployment) []ConfigDifference {
	var diffs []ConfigDifference
	add := func(field string, a, b any) {
		x, y := fmt.Sprint(a), fmt.Sprint(b)
		if x != y {
			diffs = append(diffs, ConfigDifference{field, x, y})
		}
	}
	add("副本数", DesiredReplicas(before), DesiredReplicas(after))
	add("更新策略", before.Spec.Strategy.Type, after.Spec.Strategy.Type)
	deadline := func(d *appsv1.Deployment) int32 {
		if d.Spec.ProgressDeadlineSeconds == nil {
			return 600
		}
		return *d.Spec.ProgressDeadlineSeconds
	}
	add("部署进度期限（秒）", deadline(before), deadline(after))
	for i, c := range before.Spec.Template.Spec.Containers {
		n := after.Spec.Template.Spec.Containers[i]
		for _, field := range []struct {
			name string
			a, b corev1.ResourceList
			key  corev1.ResourceName
		}{
			{"CPU 请求", c.Resources.Requests, n.Resources.Requests, corev1.ResourceCPU}, {"CPU 限制", c.Resources.Limits, n.Resources.Limits, corev1.ResourceCPU},
			{"内存请求", c.Resources.Requests, n.Resources.Requests, corev1.ResourceMemory}, {"内存限制", c.Resources.Limits, n.Resources.Limits, corev1.ResourceMemory},
		} {
			value := func(m corev1.ResourceList) string {
				if q, ok := m[field.key]; ok {
					return q.String()
				}
				return "未设置"
			}
			add(c.Name+" / "+field.name, value(field.a), value(field.b))
		}
		for _, field := range []struct {
			name string
			a, b *corev1.Probe
		}{{"启动探针", c.StartupProbe, n.StartupProbe}, {"就绪探针", c.ReadinessProbe, n.ReadinessProbe}, {"存活探针", c.LivenessProbe, n.LivenessProbe}} {
			if field.a != nil && field.b != nil {
				add(c.Name+" / "+field.name+"延迟（秒）", field.a.InitialDelaySeconds, field.b.InitialDelaySeconds)
			}
		}
		if c.StartupProbe != nil && n.StartupProbe != nil {
			add(c.Name+" / 启动探针失败阈值", c.StartupProbe.FailureThreshold, n.StartupProbe.FailureThreshold)
		}
	}
	return diffs
}

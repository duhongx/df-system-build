package k8s

import (
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func configDeployment() *appsv1.Deployment {
	deadline := int32(600)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "agg-service", Namespace: "prod", UID: "uid", ResourceVersion: "12"}, Spec: appsv1.DeploymentSpec{
		ProgressDeadlineSeconds: &deadline, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "time", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}, Containers: []corev1.Container{{
			Name: "agg-service", Image: "registry/service:old", Env: []corev1.EnvVar{{Name: "CONFIG", Value: "preserve"}}, VolumeMounts: []corev1.VolumeMount{{Name: "time", MountPath: "/etc/time", ReadOnly: true}},
			Resources:    corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("2G")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2500M")}},
			StartupProbe: &corev1.Probe{InitialDelaySeconds: 60, PeriodSeconds: 10, FailureThreshold: 30, TimeoutSeconds: 5}, ReadinessProbe: &corev1.Probe{PeriodSeconds: 10, TimeoutSeconds: 5}, LivenessProbe: &corev1.Probe{PeriodSeconds: 15, FailureThreshold: 4, TimeoutSeconds: 5},
		}, {Name: "sidecar", Image: "registry/sidecar:old"}}}},
	}}
}

func TestDeploymentConfigWhitelistAndSlowStartup(t *testing.T) {
	before := configDeployment()
	snapshot := before.DeepCopy()
	cpu, empty := "250m", ""
	failure, delay := int32(60), int32(0)
	next, diffs, err := PrepareDeploymentConfig(before, DeploymentConfigEdit{UID: "uid", ResourceVersion: "12", Containers: []ContainerConfigEdit{{Name: "agg-service", CPURequest: &cpu, MemoryLimit: &empty, StartupFailureThreshold: &failure, StartupInitialDelay: &delay}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	c := next.Spec.Template.Spec.Containers[0]
	if _, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
		t.Fatal("explicit clear ignored")
	}
	if !reflect.DeepEqual(c.Env, before.Spec.Template.Spec.Containers[0].Env) || !reflect.DeepEqual(c.VolumeMounts, before.Spec.Template.Spec.Containers[0].VolumeMounts) || !reflect.DeepEqual(next.Spec.Template.Spec.Volumes, before.Spec.Template.Spec.Volumes) || c.Image != before.Spec.Template.Spec.Containers[0].Image || !reflect.DeepEqual(next.Spec.Template.Spec.Containers[1], before.Spec.Template.Spec.Containers[1]) {
		t.Fatal("read-only fields changed")
	}
	if c.StartupProbe.FailureThreshold != 60 || c.StartupProbe.InitialDelaySeconds != 0 || c.StartupProbe.PeriodSeconds != 10 || c.LivenessProbe.FailureThreshold != 4 {
		t.Fatal("probe whitelist violated")
	}
	if *next.Spec.ProgressDeadlineSeconds <= 600 || DeploymentWaitTimeout(next) <= 10*time.Minute || len(diffs) < 3 {
		t.Fatalf("slow startup deadlines not coordinated: %v %v", next.Spec.ProgressDeadlineSeconds, diffs)
	}
	if !reflect.DeepEqual(before, snapshot) {
		t.Fatal("original object changed")
	}
}

func TestDeploymentConfigRejectsInvalidOrStaleChanges(t *testing.T) {
	negative, bad, large := "-1", "garbage", "9G"
	minus, zero := int32(-1), int32(0)
	for name, edit := range map[string]ContainerConfigEdit{
		"negative-resource": {Name: "agg-service", CPURequest: &negative}, "invalid-quantity": {Name: "agg-service", CPULimit: &bad}, "request-over-limit": {Name: "agg-service", MemoryRequest: &large},
		"missing-container": {Name: "missing", CPURequest: &large}, "negative-delay": {Name: "agg-service", StartupInitialDelay: &minus}, "zero-failures": {Name: "agg-service", StartupFailureThreshold: &zero}, "absent-probe": {Name: "sidecar", StartupInitialDelay: &zero},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := PrepareDeploymentConfig(configDeployment(), DeploymentConfigEdit{UID: "uid", ResourceVersion: "12", Containers: []ContainerConfigEdit{edit}}, true); err == nil {
				t.Fatal("invalid change accepted")
			}
		})
	}
	for _, req := range []DeploymentConfigEdit{{UID: "other", ResourceVersion: "12"}, {UID: "uid", ResourceVersion: "11"}, {UID: "uid", ResourceVersion: "12", Containers: []ContainerConfigEdit{{Name: "agg-service"}, {Name: "agg-service"}}}} {
		if _, _, err := PrepareDeploymentConfig(configDeployment(), req, true); err == nil {
			t.Fatal("stale or duplicate edit accepted")
		}
	}
}

func TestDeploymentStrategyIsApplicationSpecific(t *testing.T) {
	d := configDeployment()
	d.Name = "df-cdc"
	d.Spec.Strategy.RollingUpdate = &appsv1.RollingUpdateDeployment{}
	ApplyDeploymentStrategy(d, false)
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || d.Spec.Strategy.RollingUpdate != nil {
		t.Fatal("df-cdc must Recreate without rollingUpdate")
	}
	d.Name = "another-service"
	ApplyDeploymentStrategy(d, true)
	if d.Spec.Strategy.Type != appsv1.RollingUpdateDeploymentStrategyType || d.Spec.Strategy.RollingUpdate == nil {
		t.Fatal("managed microservice must roll")
	}
	d.Name = "postgres"
	d.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	ApplyDeploymentStrategy(d, false)
	if d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatal("unmanaged workload modified")
	}
}

func TestDeploymentConfigScalingPreservesPodTemplate(t *testing.T) {
	before := configDeployment()
	for _, count := range []int32{0, 2} {
		next, diffs, err := PrepareDeploymentConfig(before, DeploymentConfigEdit{UID: "uid", ResourceVersion: "12", Replicas: &count}, true)
		if err != nil {
			t.Fatal(err)
		}
		if DesiredReplicas(next) != count || !reflect.DeepEqual(before.Spec.Template, next.Spec.Template) {
			t.Fatal("scaling changed the Pod template")
		}
		found := false
		for _, d := range diffs {
			if d.Field == "副本数" {
				found = true
			}
		}
		if !found {
			t.Fatal("scaling omitted from preview/audit")
		}
	}
	negative := int32(-1)
	if _, _, err := PrepareDeploymentConfig(before, DeploymentConfigEdit{UID: "uid", ResourceVersion: "12", Replicas: &negative}, true); err == nil {
		t.Fatal("negative replicas accepted")
	}
}

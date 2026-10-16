package repository

import (
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func javaDeploymentDefault(t *testing.T) model.ConfigItem {
	t.Helper()
	for _, item := range coreConfigItems() {
		if item.Code == "deployment-java" {
			return item
		}
	}
	t.Fatal("default Java Deployment missing")
	return model.ConfigItem{}
}

func TestDefaultJavaDeploymentUsesTCPProbes(t *testing.T) {
	item := javaDeploymentDefault(t)
	rendered := strings.NewReplacer("${appName}", "example-service", "${namespace}", "test", "${imageName}", "registry/service:test").Replace(item.Content)
	var dep appsv1.Deployment
	if err := yaml.UnmarshalStrict([]byte(rendered), &dep); err != nil {
		t.Fatal(err)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	for name, probe := range map[string]*corev1.Probe{"startup": c.StartupProbe, "readiness": c.ReadinessProbe, "liveness": c.LivenessProbe} {
		if probe == nil || probe.TCPSocket == nil || probe.TCPSocket.Port.IntValue() != 8080 || probe.HTTPGet != nil || probe.Exec != nil || probe.GRPC != nil {
			t.Fatalf("%s must exclusively check TCP 8080", name)
		}
	}
	if strings.Contains(item.Content, "/actuator") {
		t.Fatal("Java Deployment still depends on Actuator")
	}
	if c.StartupProbe.InitialDelaySeconds != 60 || c.StartupProbe.PeriodSeconds != 10 || c.StartupProbe.FailureThreshold != 30 || c.StartupProbe.TimeoutSeconds != 5 {
		t.Fatal("startup timing changed unexpectedly")
	}
	if c.ReadinessProbe.PeriodSeconds != 10 || c.LivenessProbe.PeriodSeconds != 15 || len(c.Env) != 5 || len(c.VolumeMounts) != 1 {
		t.Fatal("unrelated configuration changed")
	}
}

func TestSeedCoreConfigItemsUpgradesOnlyUnmodifiedLegacyJavaDefault(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := DB.AutoMigrate(&model.ConfigItem{}); err != nil {
		t.Fatal(err)
	}
	fresh := javaDeploymentDefault(t)
	legacy := strings.ReplaceAll(fresh.Content, "          tcpSocket:\n            port: 8080", "          httpGet:\n            path: /actuator/health\n            port: 8080\n            scheme: HTTP")
	if !isLegacyJavaHTTPDeploymentTemplate(legacy) {
		t.Fatal("migration fixture does not match the previous shipped template")
	}
	old := fresh
	old.Content = legacy
	if err := DB.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	SeedCoreConfigItems()
	var saved model.ConfigItem
	if err := DB.First(&saved, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Content != fresh.Content {
		t.Fatal("stored legacy default was not upgraded")
	}
	updated := saved.UpdatedAt
	SeedCoreConfigItems()
	if err := DB.First(&saved, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !saved.UpdatedAt.Equal(updated) {
		t.Fatal("idempotent seeding rewrote current default")
	}
	custom := strings.Replace(legacy, "memory: 2.0G", "memory: 1G", 1)
	if err := DB.Model(&saved).Update("content", custom).Error; err != nil {
		t.Fatal(err)
	}
	SeedCoreConfigItems()
	if err := DB.First(&saved, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Content != custom {
		t.Fatal("customer-edited template was overwritten")
	}
	if err := DB.Delete(&saved).Error; err != nil {
		t.Fatal(err)
	}
	SeedCoreConfigItems()
	var created model.ConfigItem
	if err := DB.Where("code = ?", "deployment-java").First(&created).Error; err != nil {
		t.Fatal(err)
	}
	if created.Content != fresh.Content {
		t.Fatal("fresh install did not receive TCP template")
	}
}

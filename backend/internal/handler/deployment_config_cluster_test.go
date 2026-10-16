package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/gin-gonic/gin"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Explicit opt-in; cluster writes are confined to a newly created namespace.
func TestDeploymentConfigurationCluster(t *testing.T) {
	if os.Getenv("DF_DEPLOYMENT_CONFIG_CLUSTER_TEST") != "1" {
		t.Skip("opt-in isolated Deployment configuration test")
	}
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Settings{}, &model.DeploymentConfigChange{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Getenv("DF_DEPLOYMENT_CONFIG_CLUSTER_SETTINGS"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	ns := fmt.Sprintf("df-config-test-%d", time.Now().UnixNano())
	settings["k8s_namespace"] = ns
	if err := repository.NewSettingsRepo().BatchUpdate(settings); err != nil {
		t.Fatal(err)
	}
	k8s.ResetClient()
	defer k8s.ResetClient()
	cs, err := k8s.GetClient()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cs.CoreV1().Namespaces().Delete(cleanup, ns, metav1.DeleteOptions{}); err != nil {
			t.Errorf("cleanup namespace: %v", err)
		}
	})
	probe := func() *corev1.Probe {
		return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt(80)}}, PeriodSeconds: 1, TimeoutSeconds: 1, FailureThreshold: 30}
	}
	one := int32(1)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "df-cdc", Namespace: ns}, Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "config-test"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "config-test"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "df-cdc", Image: "192.168.1.154:8888/base/nginx:v2", Command: []string{"sh", "-c"}, Args: []string{`printf 'server { listen 80; location / { return 200 "ok"; } }' > /etc/nginx/conf.d/config-test.conf; exec nginx -g 'daemon off;'`}, Env: []corev1.EnvVar{{Name: "UNCHANGED", Value: "keep"}}, VolumeMounts: []corev1.VolumeMount{{Name: "scratch", MountPath: "/scratch"}}, StartupProbe: probe(), ReadinessProbe: probe(), LivenessProbe: probe()}}, Volumes: []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}}}
	dep, err = cs.AppsV1().Deployments(ns).Create(ctx, dep, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitReady := func(generation int64, replicas int32) *appsv1.Deployment {
		for ctx.Err() == nil {
			d, err := cs.AppsV1().Deployments(ns).Get(ctx, "df-cdc", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if d.Status.ObservedGeneration >= generation && d.Status.Replicas == replicas && d.Status.UpdatedReplicas == replicas && d.Status.ReadyReplicas == replicas && d.Status.AvailableReplicas == replicas {
				return d
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		t.Fatal("isolated test Deployment did not become ready")
		return nil
	}
	dep = waitReady(dep.Generation, 1)
	h := &KubernetesHandler{configClient: cs}
	call := func(preview bool) {
		body, _ := json.Marshal(map[string]any{"uid": dep.UID, "resourceVersion": dep.ResourceVersion, "replicas": 2, "containers": []any{map[string]any{"name": "df-cdc", "cpuRequest": "10m", "cpuLimit": "200m", "memoryRequest": "32Mi", "memoryLimit": "128Mi", "startupInitialDelay": 1, "startupFailureThreshold": 60}}})
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/config?namespace="+ns, bytes.NewReader(body))
		c.Params = gin.Params{{Key: "name", Value: "df-cdc"}}
		c.Set("username", "cluster-test")
		h.editDeploymentConfig(c, preview)
		var reply struct {
			Code    int
			Message string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Code != 0 {
			t.Fatalf("configuration API preview=%v: %s", preview, w.Body.String())
		}
	}
	call(true)
	unchanged, err := cs.AppsV1().Deployments(ns).Get(ctx, "df-cdc", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Generation != dep.Generation || unchanged.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().Sign() != 0 {
		t.Fatal("preview persisted a template change")
	}
	// Status updates can change resourceVersion even though the preview is read-only.
	dep = unchanged
	call(false)
	after := waitReady(dep.Generation+1, 2)
	c := after.Spec.Template.Spec.Containers[0]
	if after.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || after.Spec.Strategy.RollingUpdate != nil || c.Resources.Requests.Cpu().String() != "10m" || c.StartupProbe.FailureThreshold != 60 || c.StartupProbe.InitialDelaySeconds != 1 || c.Env[0].Value != "keep" || c.VolumeMounts[0].MountPath != "/scratch" || after.Spec.Template.Spec.Volumes[0].EmptyDir == nil {
		t.Fatal("applied policy, editable or read-only configuration mismatch")
	}
	var record model.DeploymentConfigChange
	if err := repository.DB.First(&record).Error; err != nil || record.Status != "applied" {
		t.Fatalf("configuration audit: %+v %v", record, err)
	}
	t.Log("cluster dry-run preserved template; Recreate/resource/probe update reached Ready; env and volumes preserved; audit applied")
}

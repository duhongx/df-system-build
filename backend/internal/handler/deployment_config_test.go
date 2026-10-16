package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/gin-gonic/gin"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedapps "k8s.io/client-go/kubernetes/typed/apps/v1"
)

// The pinned client-go fake drops UpdateOptions. Intercept the typed method so
// dry-run behavior is exercised faithfully instead of persisting a fake update.
type configTestClient struct {
	kubernetes.Interface
	dryRuns, commits int
}
type configTestApps struct {
	typedapps.AppsV1Interface
	owner *configTestClient
}
type configTestDeployments struct {
	typedapps.DeploymentInterface
	owner *configTestClient
}

func (c *configTestClient) AppsV1() typedapps.AppsV1Interface {
	return configTestApps{c.Interface.AppsV1(), c}
}
func (c configTestApps) Deployments(namespace string) typedapps.DeploymentInterface {
	return configTestDeployments{c.AppsV1Interface.Deployments(namespace), c.owner}
}
func (c configTestDeployments) Update(ctx context.Context, dep *appsv1.Deployment, options metav1.UpdateOptions) (*appsv1.Deployment, error) {
	candidate := dep.DeepCopy()
	if len(options.DryRun) > 0 {
		c.owner.dryRuns++
		return candidate, nil
	}
	c.owner.commits++
	candidate.Generation = 2
	candidate.ResourceVersion = "2"
	return c.DeploymentInterface.Update(ctx, candidate, options)
}

func TestDeploymentConfigurationAPIWhitelistPreviewAndApply(t *testing.T) {
	setupArtifactVersionTestDB(t)
	if err := repository.DB.AutoMigrate(&model.Settings{}, &model.DeploymentConfigChange{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSettingsRepo().Set("k8s_namespace", "prod"); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Create(&model.Application{AppName: "df-cdc", AppType: "java", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "df-cdc", Namespace: "prod", UID: "uid", ResourceVersion: "1"}, Spec: appsv1.DeploymentSpec{Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "df-cdc", Image: "old", Env: []corev1.EnvVar{{Name: "CONFIG", Value: "unchanged"}}, StartupProbe: &corev1.Probe{InitialDelaySeconds: 60, PeriodSeconds: 10, FailureThreshold: 30}}}}}}}
	client := fake.NewSimpleClientset(dep)
	wrapped := &configTestClient{Interface: client}
	h := &KubernetesHandler{configClient: wrapped}

	call := func(body, ns string, preview bool) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/config?namespace="+ns, bytes.NewBufferString(body))
		c.Params = gin.Params{{Key: "name", Value: "df-cdc"}}
		c.Set("username", "test")
		h.editDeploymentConfig(c, preview)
		return w.Body.String()
	}
	for _, body := range []string{
		`{"uid":"uid","resourceVersion":"1","env":[]}`,
		`{"uid":"uid","resourceVersion":"1","containers":[{"name":"df-cdc","volumeMounts":[]}]}`,
		`{"uid":"uid","resourceVersion":"0","containers":[]}`,
	} {
		var reply struct{ Code int }
		_ = json.Unmarshal([]byte(call(body, "prod", true)), &reply)
		if reply.Code == 0 {
			t.Fatalf("forbidden/stale input accepted: %s", body)
		}
	}
	if wrapped.dryRuns != 0 || wrapped.commits != 0 {
		t.Fatal("rejected requests touched cluster")
	}
	good := `{"uid":"uid","resourceVersion":"1","replicas":2,"containers":[{"name":"df-cdc","cpuRequest":"100m","startupFailureThreshold":60}]}`
	var badNS struct{ Code int }
	_ = json.Unmarshal([]byte(call(good, "other", true)), &badNS)
	if badNS.Code == 0 {
		t.Fatal("namespace override accepted")
	}
	var preview struct {
		Code int
		Data struct{ Changes []any }
	}
	_ = json.Unmarshal([]byte(call(good, "prod", true)), &preview)
	if preview.Code != 0 || len(preview.Data.Changes) < 3 || wrapped.dryRuns != 1 || wrapped.commits != 0 {
		t.Fatalf("preview mutated or failed: %+v runs=%d commits=%d", preview, wrapped.dryRuns, wrapped.commits)
	}
	var applied struct{ Code int }
	response := call(good, "prod", false)
	_ = json.Unmarshal([]byte(response), &applied)
	if applied.Code != 0 || wrapped.commits != 1 || wrapped.dryRuns != 2 {
		t.Fatalf("apply failed: %s", response)
	}
	obj, err := client.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), "prod", "df-cdc")
	if err != nil {
		t.Fatal(err)
	}
	after := obj.(*appsv1.Deployment)
	if after.Spec.Replicas == nil || *after.Spec.Replicas != 2 || after.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || !reflect.DeepEqual(after.Spec.Template.Spec.Containers[0].Env, dep.Spec.Template.Spec.Containers[0].Env) || after.Spec.Template.Spec.Containers[0].Image != "old" {
		t.Fatal("policy or readonly preservation failed")
	}
	var record model.DeploymentConfigChange
	if err := repository.DB.First(&record).Error; err != nil || record.Status != "applied" || record.Operator != "test" {
		t.Fatalf("audit record missing: %+v %v", record, err)
	}
	var scaled struct {
		Code int
		Data struct {
			ReplicasChanged bool `json:"replicasChanged"`
			RolloutRequired bool `json:"rolloutRequired"`
		}
	}
	reply := call(`{"uid":"uid","resourceVersion":"2","replicas":0,"containers":[]}`, "prod", false)
	if err := json.Unmarshal([]byte(reply), &scaled); err != nil || scaled.Code != 0 || !scaled.Data.ReplicasChanged || scaled.Data.RolloutRequired {
		t.Fatalf("scale-only effect classified incorrectly: %s", reply)
	}
	obj, err = client.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), "prod", "df-cdc")
	if err != nil {
		t.Fatal(err)
	}
	zero := obj.(*appsv1.Deployment)
	if zero.Spec.Replicas == nil || *zero.Spec.Replicas != 0 || !reflect.DeepEqual(zero.Spec.Template, after.Spec.Template) {
		t.Fatal("scale-to-zero changed the template or was ignored")
	}

}

package stages

import (
	"context"
	"testing"

	"df-build-server/internal/pipeline/types"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolvePlanDeploysVueSubAppsThroughWebMain(t *testing.T) {
	plan := resolvePlan(&types.PipelineContext{
		AppType: "vue",
		VueRole: "sub",
	})

	if plan.skipAll {
		t.Fatalf("vue sub app builds web-main image and must update web-main deployment")
	}
	if plan.deploymentCode != "deployment-web" {
		t.Fatalf("deploymentCode = %q, want deployment-web", plan.deploymentCode)
	}
	if plan.serviceCode != "" {
		t.Fatalf("serviceCode = %q, want empty because sub apps should not rewrite web-main service", plan.serviceCode)
	}
	if plan.wantIngress {
		t.Fatalf("vue sub app should not rewrite web-main ingress")
	}
}

func TestApplyExistingWebResourcesSkipsWithoutUpdating(t *testing.T) {
	ctx := context.Background()
	namespace := "prod"
	pCtx := &types.PipelineContext{
		AppName:    "web-main",
		AppType:    "vue",
		VueRole:    "main",
		NodePort:   0,
		OnLog:      func(uint, uint, string, string) {},
		PipelineID: 1,
	}
	cs := fake.NewSimpleClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "web-main", Namespace: namespace},
			Data:       map[string]string{"web-main.conf": "existing"},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "web-main", Namespace: namespace},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{Port: 80}},
			},
		},
		&netv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "web-main", Namespace: namespace},
		},
	)

	stage := &K8sDeployStage{}
	if err := stage.applyConfigMap(ctx, cs, pCtx, namespace, "configmap-web-main"); err != nil {
		t.Fatalf("apply existing configmap should skip without rendering: %v", err)
	}
	if err := stage.applyService(ctx, cs, pCtx, namespace, "web-main", "service-web"); err != nil {
		t.Fatalf("apply existing service should skip without rendering: %v", err)
	}
	if err := stage.applyIngress(ctx, cs, pCtx, namespace, "web-main", "web-main", "his.example.test"); err != nil {
		t.Fatalf("apply existing ingress should skip without rendering: %v", err)
	}

	cm, _ := cs.CoreV1().ConfigMaps(namespace).Get(ctx, "web-main", metav1.GetOptions{})
	if cm.Data["web-main.conf"] != "existing" {
		t.Fatalf("existing configmap was updated: %q", cm.Data["web-main.conf"])
	}
}

package service

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestSetDeploymentImagePrefersNamedContainerAndFallsBackToFirst(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "sidecar", Image: "sidecar:v1"},
						{Name: "his-gateway", Image: "gateway:v1"},
					},
				},
			},
		},
	}

	if !SetDeploymentImage(dep, "his-gateway", "gateway:v2") {
		t.Fatalf("expected named container image to be updated")
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "sidecar:v1" {
		t.Fatalf("sidecar image should not change")
	}
	if dep.Spec.Template.Spec.Containers[1].Image != "gateway:v2" {
		t.Fatalf("named container image was not updated")
	}

	if !SetDeploymentImage(dep, "missing", "fallback:v2") {
		t.Fatalf("expected first container fallback to be updated")
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "fallback:v2" {
		t.Fatalf("first container fallback was not updated")
	}
}

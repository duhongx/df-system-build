package stages

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestContainerStatusMatchesCanonicalImageNames(t *testing.T) {
	for _, tc := range []struct {
		expected, actual string
		matches          bool
	}{
		{"registry.his.com/prod/web-main", "registry.his.com/prod/web-main:latest", true},
		{"registry.his.com/prod/web-opm", "registry.his.com/prod/web-opm:latest", true},
		{"registry.his.com:5000/prod/web-opm", "registry.his.com:5000/prod/web-opm:latest", true},
		{"nginx", "docker.io/library/nginx:latest", true},
		{"registry.his.com/prod/web-opm:v1", "registry.his.com/prod/web-opm:v2", false},
		{"registry.his.com/prod/web-opm", "registry.his.com/prod/web-main:latest", false},
		{"registry.his.com/prod/web-opm", "other.his.com/prod/web-opm:latest", false},
		{"registry.his.com/prod/web-opm", "registry.his.com/prod/web-opm:old", false},
		{"", "", false},
	} {
		t.Run(tc.expected+"_"+tc.actual, func(t *testing.T) {
			if got := containerStatusMatches(corev1.ContainerStatus{Image: tc.actual}, tc.expected); got != tc.matches {
				t.Fatalf("matches=%t, want=%t", got, tc.matches)
			}
		})
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if !containerStatusMatches(corev1.ContainerStatus{ImageID: "docker-pullable://registry.his.com/prod/web-opm@" + digest}, "registry.his.com/prod/web-opm@"+digest) {
		t.Fatal("pinned digest rejected")
	}
	if containerStatusMatches(corev1.ContainerStatus{ImageID: "containerd://sha256:" + strings.Repeat("b", 64)}, "registry.his.com/prod/web-opm@"+digest) {
		t.Fatal("different digest accepted")
	}
}

func TestReadyWebPodAcceptsImplicitLatestAndStillRequiresReady(t *testing.T) {
	for _, target := range []string{"web-main", "web-opm"} {
		t.Run(target, func(t *testing.T) {
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: target, Namespace: "prod"}, Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": target}},
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: target, Image: "registry.his.com/prod/" + target}}}},
			}}
			pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: target + "-pod", Labels: map[string]string{"app": target}}, Spec: *dep.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{
				Phase: corev1.PodRunning, Conditions: readyConditions(true), ContainerStatuses: []corev1.ContainerStatus{{Name: target, Image: "registry.his.com/prod/" + target + ":latest", Ready: true}},
			}}
			if got, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); !ok || got.Name != pod.Name {
				t.Fatal("Running and Ready pod with implicit latest was rejected")
			}
			pod.Status.ContainerStatuses[0].Ready = false
			if _, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); ok {
				t.Fatal("unready container accepted")
			}
			message := webReadyPodSelectionError(dep, []corev1.Pod{pod}).Error()
			if !strings.Contains(message, "prod/"+target) || !strings.Contains(message, "ready=false") {
				t.Fatal(message)
			}
			if target == "web-opm" && strings.Contains(message, "web-main") {
				t.Fatal("standalone diagnostic mislabeled as web-main")
			}
			pod.Status.ContainerStatuses[0].Ready = true
			pod.Status.Conditions = readyConditions(false)
			if _, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); ok {
				t.Fatal("Running pod without PodReady accepted")
			}
		})
	}
}

func TestStandaloneWebPodLookupCannotSelectWebMainWithOverlappingLabels(t *testing.T) {
	controller := true
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web-opm", Namespace: "prod", UID: "opm-deployment"}, Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "frontend"}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "frontend"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web-opm", Image: "registry.his.com/prod/web-opm"}}}},
	}}
	sets := func(name string, owner types.UID) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", UID: types.UID(name), Labels: map[string]string{"app": "frontend"}, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: owner, Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: *dep.Spec.Template.DeepCopy()}}
	}
	pods := func(name, owner string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"app": "frontend"}, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: types.UID(owner), Controller: &controller}}}, Spec: *dep.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions(true), ContainerStatuses: []corev1.ContainerStatus{{Name: "web-opm", Image: "registry.his.com/prod/web-opm:latest", Ready: true}}}}
	}
	client := fake.NewSimpleClientset(dep, sets("opm-rs", dep.UID), sets("main-rs", "main-deployment"), pods("web-opm-pod", "opm-rs"), pods("web-main-pod", "main-rs"))
	actual, err := readWebDeployment(context.Background(), client, "prod", "web-opm")
	if err != nil || actual.Name != "web-opm" {
		t.Fatalf("wrong deployment: %v", err)
	}
	owned, err := currentDeploymentPods(context.Background(), client, "prod", actual)
	if err != nil || len(owned) != 1 || owned[0].Name != "web-opm-pod" {
		t.Fatalf("wrong pod ownership: %+v %v", owned, err)
	}
	if pod, ok := selectReadyPodForDeployment(actual, owned); !ok || pod.Name != "web-opm-pod" {
		t.Fatal("standalone target not preserved")
	}
}

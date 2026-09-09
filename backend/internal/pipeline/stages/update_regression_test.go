package stages

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"df-build-server/internal/pipeline/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSourceZipCanBeInstalledAtStandaloneWebRoot(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "dist/index.html"), "new-page")
	mustWriteFile(t, filepath.Join(root, "dist/assets/new.js"), "new-script")
	zip := filepath.Join(root, "web-cdr.zip")
	if err := ZipDirectory(root, "dist", zip); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "docker-build/dist")
	mustWriteFile(t, filepath.Join(target, "old.js"), "old-script")
	if err := extractWebZip(nil, zip, target); err != nil {
		t.Fatal(err)
	}
	assertWebContent(t, filepath.Join(target, "index.html"), "new-page")
	assertWebContent(t, filepath.Join(target, "assets/new.js"), "new-script")
	assertMissing(t, filepath.Join(target, "dist"))
	assertMissing(t, filepath.Join(target, "old.js"))
}

func TestPodErrorsOnlyInspectCurrentOwnedReplicaSet(t *testing.T) {
	controller := true
	owner := func(kind, uid string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{Kind: kind, UID: ktypes.UID(uid), Controller: &controller}}
	}
	labels := map[string]string{"component": "portal"} // Deliberately no app=web-main label.
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web-main", Namespace: "test", UID: "dep"}, Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web-main", Image: "new"}}}},
	}}
	newSet := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "test", UID: "new", Labels: labels, OwnerReferences: owner("Deployment", "dep")}, Spec: appsv1.ReplicaSetSpec{Template: *dep.Spec.Template.DeepCopy()}}
	newSet.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey] = "new-hash"
	oldSet := newSet.DeepCopy()
	oldSet.Name, oldSet.UID = "old", "old"
	oldSet.Spec.Template.Spec.Containers[0].Image = "old"
	foreignSet := newSet.DeepCopy()
	foreignSet.Name, foreignSet.UID = "foreign", "foreign"
	foreignSet.OwnerReferences = owner("Deployment", "other")
	badPod := func(name, set string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", Labels: labels, OwnerReferences: owner("ReplicaSet", set)}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "web-main", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "failed startup"}}}}}}
	}
	deleted := badPod("deleted", "new")
	now := metav1.Now()
	deleted.DeletionTimestamp = &now
	deleted.Finalizers = []string{"test"}
	cs := fake.NewSimpleClientset(dep, newSet, oldSet, foreignSet, badPod("old-bad", "old"), badPod("foreign-bad", "foreign"), deleted)
	pc := &types.PipelineContext{OnLog: func(uint, uint, string, string) {}}
	s := &K8sDeployStage{}
	if msg := s.checkPodErrors(context.Background(), cs, "test", dep, pc); msg != "" {
		t.Fatalf("old/unrelated/deleting Pod blocked update: %s", msg)
	}
	if _, err := cs.CoreV1().Pods("test").Create(context.Background(), badPod("current-bad", "new"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if msg := s.checkPodErrors(context.Background(), cs, "test", dep, pc); !strings.Contains(msg, "current-bad") {
		t.Fatalf("missed current Pod failure: %s", msg)
	}
	cs.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("access denied") })
	if msg := s.checkPodErrors(context.Background(), cs, "test", dep, pc); !strings.Contains(msg, "access denied") {
		t.Fatalf("Pod API error swallowed: %s", msg)
	}
}

func TestConfigMapReadFailureIsReturnedBeforeMutation(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("get", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("permission denied") })
	pc := &types.PipelineContext{AppName: "web-cdr", VueRole: "standalone", OnLog: func(uint, uint, string, string) {}}
	err := (&K8sDeployStage{}).applyConfigMap(context.Background(), cs, pc, "test", "configmap-web-cdr")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("lost original error: %v", err)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("unexpected mutation: %v", action)
		}
	}
}

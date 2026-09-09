package stages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"df-build-server/internal/pipeline/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestRolloutFailureIncludesDiagnosticsWithoutLosingCause(t *testing.T) {
	for _, denied := range []bool{false, true} {
		name := "with-logs"
		if denied {
			name = "logs-denied"
		}
		t.Run(name, func(t *testing.T) {
			controller := true
			owner := func(kind, uid string) []metav1.OwnerReference {
				return []metav1.OwnerReference{{Kind: kind, UID: ktypes.UID(uid), Controller: &controller}}
			}
			labels := map[string]string{"app": "service"}
			dep := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "service", Namespace: "test", UID: "dep", Generation: 2}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "service", Image: "new"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2}}
			set := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "new", UID: "new", OwnerReferences: owner("Deployment", "dep")}, Spec: appsv1.ReplicaSetSpec{Template: *dep.Spec.Template.DeepCopy()}}
			old := *set.DeepCopy()
			old.Name, old.UID = "old", "old"
			old.Spec.Template.Spec.Containers[0].Image = "old"
			pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "new-pod", UID: "pod-new", OwnerReferences: owner("ReplicaSet", "new")}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "service", Image: "new", RestartCount: 2, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "original startup failure"}}, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", Message: "application exited"}}}}}}
			oldPod := *pod.DeepCopy()
			oldPod.Name = "old-pod"
			oldPod.OwnerReferences = owner("ReplicaSet", "old")
			seenPrevious, seenCurrent, seenEvents := false, false, false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("diagnostics mutated cluster: %s", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				var result any
				switch {
				case strings.HasSuffix(r.URL.Path, "/deployments/service"):
					result = &dep
				case strings.HasSuffix(r.URL.Path, "/replicasets"):
					result = &appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{set, old}}
				case strings.HasSuffix(r.URL.Path, "/pods"):
					result = &corev1.PodList{Items: []corev1.Pod{oldPod, pod}}
				case strings.HasSuffix(r.URL.Path, "/pods/new-pod/log"):
					previous := r.URL.Query().Get("previous") == "true"
					if previous {
						seenPrevious = true
					} else {
						seenCurrent = true
					}
					if r.URL.Query().Get("container") != "service" || r.URL.Query().Get("tailLines") != "50" || r.URL.Query().Get("limitBytes") != "4096" {
						t.Errorf("unbounded/wrong log request: %s", r.URL)
					}
					if denied {
						http.Error(w, "logs access denied", http.StatusForbidden)
						return
					}
					w.Header().Set("Content-Type", "text/plain")
					text := "current startup output"
					if previous {
						text = "previous startup exception: invalid argument"
					}
					w.Write([]byte(text + strings.Repeat("x", 5000)))
					return
				case strings.HasSuffix(r.URL.Path, "/events"):
					seenEvents = true
					if r.URL.Query().Get("fieldSelector") != "involvedObject.uid=pod-new" {
						t.Errorf("events not scoped to Pod UID: %s", r.URL)
					}
					result = &corev1.EventList{Items: []corev1.Event{{Reason: "BackOff", Message: "container restart event", Count: 2}}}
				default:
					t.Errorf("unexpected request (possibly old Pod logs): %s", r.URL)
					http.NotFound(w, r)
					return
				}
				if err := json.NewEncoder(w).Encode(result); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
			if err != nil {
				t.Fatal(err)
			}
			var logs []string
			pc := &types.PipelineContext{OnLog: func(_, _ uint, line, stream string) { logs = append(logs, line) }}
			err = (&K8sDeployStage{}).waitForRollout(context.Background(), cs, "test", "service", pc)
			if err == nil {
				t.Fatal("startup failure reported success")
			}
			for _, want := range []string{"original startup failure", "退出码=1", "application exited", "container restart event"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in error: %s", want, err)
				}
			}
			if !seenPrevious || !seenCurrent || !seenEvents {
				t.Fatalf("diagnostics incomplete: previous=%t current=%t events=%t", seenPrevious, seenCurrent, seenEvents)
			}
			if denied {
				if !strings.Contains(err.Error(), "采集失败") {
					t.Fatalf("diagnostic access error missing: %s", err)
				}
			} else if !strings.Contains(err.Error(), "previous startup exception") || !strings.Contains(err.Error(), "current startup output") {
				t.Fatalf("logs absent: %s", err)
			}
			if len(err.Error()) > 33*1024 || len(strings.Join(logs, "\n")) > 34*1024 {
				t.Fatal("diagnostics exceeded output budget")
			}
		})
	}
}

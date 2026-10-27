package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func rolloutTestObjects(image string) (*appsv1.Deployment, *appsv1.ReplicaSet, []corev1.Pod) {
	controller, replicas := true, int32(2)
	labels := map[string]string{"app": "web-main"}
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web-main", Namespace: "prod", UID: "site", Generation: 1, ResourceVersion: "1"}, Spec: appsv1.DeploymentSpec{
		Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "web-main", Image: image, Env: []corev1.EnvVar{{Name: "KEEP", Value: "existing"}}, VolumeMounts: []corev1.VolumeMount{{Name: "nginx", MountPath: "/etc/nginx/conf.d"}}}},
			Volumes:    []corev1.Volume{{Name: "nginx", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "existing-nginx"}}}}},
		}},
	}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2}}
	set := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "current-rs", Namespace: "prod", UID: "current-rs", Labels: labels, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: dep.UID, Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: *dep.Spec.Template.DeepCopy()}}
	var pods []corev1.Pod
	for i := 0; i < 2; i++ {
		pods = append(pods, corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("web-main-%d", i), Namespace: "prod", Labels: labels, OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", UID: set.UID, Controller: &controller}}}, Spec: *dep.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: readyConditions(true), ContainerStatuses: []corev1.ContainerStatus{{Name: "web-main", Image: image, Ready: true}}}})
	}
	return dep, set, pods
}

func TestFrontendRolloutOnlyChecksKubernetesReadiness(t *testing.T) {
	for _, tc := range []string{"ready", "not-ready", "wrong-image", "recreated", "later-generation"} {
		t.Run(tc, func(t *testing.T) {
			dep, set, pods := rolloutTestObjects("registry/prod/web-main:new")
			b := &webBaseline{Exists: true, UID: string(dep.UID), Namespace: "prod", DeploymentName: dep.Name}
			switch tc {
			case "not-ready":
				pods[0].Status.ContainerStatuses[0].Ready = false
			case "wrong-image":
				pods[0].Status.ContainerStatuses[0].Image = "registry/prod/web-main:old"
			case "recreated":
				b.UID = "deleted-deployment"
			case "later-generation":
				dep.Generation++
			}
			cs := fake.NewSimpleClientset(dep, set, &pods[0], &pods[1])
			err := verifyFrontendRollout(context.Background(), cs, b, "registry/prod/web-main:new", 1)
			if (err == nil) != (tc == "ready") {
				t.Fatalf("unexpected readiness result: %v", err)
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("verification issued an exec or mutation: %+v", action)
				}
			}
		})
	}
}

func TestFrontendImageUpdateDoesNotRequireRoutingPolicy(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Application{}, &model.Pipeline{}, &model.Settings{}, &model.ArtifactDeployRecord{}, &model.ArtifactDeployBatch{}); err != nil {
		t.Fatal(err)
	}
	main := model.Application{AppName: "web-main", AppType: "vue", VueRole: "main", Enabled: true}
	child := model.Application{AppName: "web-new", AppType: "vue", VueRole: "sub", AppCode: "42", Enabled: true}
	for _, app := range []*model.Application{&main, &child} {
		if err := repository.DB.Create(app).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"direct", "preparation", "precheck-failed", "lost-submission-response"} {
		t.Run(mode, func(t *testing.T) {
			dep, _, _ := rolloutTestObjects("registry/prod/web-main:old")
			var mu sync.Mutex
			var images []string
			var unexpected []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch {
				case strings.HasSuffix(r.URL.Path, "/deployments/web-main") && r.Method == "PUT":
					var next appsv1.Deployment
					if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					spec := next.Spec.DeepCopy()
					spec.Template.Spec.Containers[0].Image = webContainerImage(dep)
					if !reflect.DeepEqual(*spec, dep.Spec) {
						t.Error("image update changed existing deployment configuration")
					}
					next.Generation = dep.Generation + 1
					next.ResourceVersion = fmt.Sprint(next.Generation)
					next.Status = dep.Status
					next.Status.ObservedGeneration = next.Generation
					dep = &next
					images = append(images, webContainerImage(dep))
					if mode == "lost-submission-response" {
						w.WriteHeader(http.StatusInternalServerError)
						response = &metav1.Status{Status: "Failure", Reason: metav1.StatusReasonInternalError, Message: "simulated lost acknowledgement", Code: 500}
					} else {
						response = dep
					}
				case strings.HasSuffix(r.URL.Path, "/deployments/web-main") && r.Method == "GET":
					response = dep
				case strings.HasSuffix(r.URL.Path, "/replicasets"):
					_, set, _ := rolloutTestObjects(webContainerImage(dep))
					set.Spec.Template = *dep.Spec.Template.DeepCopy()
					response = &appsv1.ReplicaSetList{Items: []appsv1.ReplicaSet{*set}}
				case strings.HasSuffix(r.URL.Path, "/pods"):
					_, _, pods := rolloutTestObjects(webContainerImage(dep))
					response = &corev1.PodList{Items: pods}
				case strings.HasSuffix(r.URL.Path, "/exec"):
					commands := strings.Join(r.URL.Query()["command"], " ")
					if strings.Contains(commands, "wget") || strings.Contains(commands, "tar") || strings.Contains(commands, "__df_") {
						unexpected = append(unexpected, commands)
					}
					// Optional version collection may fail without blocking rollout.
					w.WriteHeader(http.StatusForbidden)
					response = &metav1.Status{Status: "Failure", Reason: metav1.StatusReasonForbidden, Message: "pod exec unavailable", Code: 403}
				default:
					unexpected = append(unexpected, r.Method+" "+r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					response = &metav1.Status{Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: 404}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer srv.Close()
			settings := repository.NewSettingsRepo()
			config := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\ncurrent-context: test\nusers:\n- name: test\n  user: {}\n", srv.URL)
			if err := settings.BatchUpdate(map[string]string{"k8s_kubeconfig_content": config, "customer_env": "客户现场"}); err != nil {
				t.Fatal(err)
			}
			k8s.ResetClient()
			defer k8s.ResetClient()
			cs, err := k8s.GetClient()
			if err != nil {
				t.Fatal(err)
			}
			b := &webBaseline{Exists: true, Cluster: srv.URL, Namespace: "prod", DeploymentName: main.AppName, UID: "site", Generation: 1, Image: webContainerImage(dep), ConfigMaps: map[string]string{"existing-nginx": "old-version"}, Regions: []frontend.Region{
				{AppID: main.ID, AppName: main.AppName, Role: "main", Path: "."},
				{AppID: child.ID, AppName: child.AppName, Role: "sub", AppCode: "42", Path: "apps/42"},
			}}
			if mode == "preparation" {
				b.PreparationImage = "registry/prod/web-main:assets"
			}
			if mode == "precheck-failed" {
				b.UID = "old-deployment"
			}
			p := &model.Pipeline{PipelineNo: "image-" + mode, ApplicationID: main.ID, AppName: main.AppName, AppType: "vue", K8sNamespace: "prod", ImageName: "registry/prod/web-main:new", TriggerUser: "test"}
			if err := repository.DB.Create(p).Error; err != nil {
				t.Fatal(err)
			}
			batch := model.ArtifactDeployBatch{DeployBatchNo: p.PipelineNo, VersionNo: "v1", Namespace: "prod"}
			record := model.ArtifactDeployRecord{DeployBatchNo: p.PipelineNo, VersionNo: "v1", PipelineID: p.ID, AppID: child.ID, AppName: child.AppName, AppType: "vue", VueRole: "sub", AppCode: "42", DeploymentName: main.AppName, Namespace: "prod", RuntimeVersionPath: "/usr/share/nginx/html/apps/42/config.json"}
			if err := repository.DB.Create(&batch).Error; err != nil {
				t.Fatal(err)
			}
			if err := repository.DB.Create(&record).Error; err != nil {
				t.Fatal(err)
			}
			pCtx := &types.PipelineContext{Pipeline: p, PipelineID: p.ID, AppName: main.AppName, AppType: "vue", VueRole: "main", ImageName: p.ImageName, K8sNamespace: "prod", OnLog: func(uint, uint, string, string) {}}
			b.NewImage = p.ImageName
			if err := saveWebBaseline(pCtx, b); err != nil {
				t.Fatal(err)
			}
			result, err := (&K8sDeployStage{}).runFrontendDeployment(context.Background(), cs, pCtx, "prod")
			mu.Lock()
			defer mu.Unlock()
			if len(unexpected) > 0 {
				t.Fatalf("rollout issued routing/file checks or changed other resources: %v", unexpected)
			}
			switch mode {
			case "precheck-failed":
				if err == nil || len(images) != 0 || !strings.Contains(result.OnlineState, "尚未向 Kubernetes 提交修改") {
					t.Fatalf("wrong precheck submission boundary: %v %+v %v", images, result, err)
				}
			case "lost-submission-response":
				if err == nil || len(images) != 1 || !strings.Contains(result.OnlineState, "可能已被接受") {
					t.Fatalf("lost response described as not deployed: %v %+v %v", images, result, err)
				}
			default:
				want := []string{p.ImageName}
				if mode == "preparation" {
					want = append([]string{b.PreparationImage}, want...)
				}
				if err != nil || result.ExitCode != 0 || !reflect.DeepEqual(images, want) || p.DeploymentProgress != "completed" {
					t.Fatalf("image-only rollout failed: images=%v result=%+v error=%v", images, result, err)
				}
				if err := repository.DB.First(&record, record.ID).Error; err != nil || record.DeployStatus != "success" {
					t.Fatalf("rollout result not saved: %+v %v", record, err)
				}
			}
		})
	}
}

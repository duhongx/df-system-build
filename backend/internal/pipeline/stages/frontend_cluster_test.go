package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestFrontendDeploymentRejectsRecreatedRegistration(t *testing.T) {
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Application{}); err != nil {
		t.Fatal(err)
	}
	app := model.Application{AppName: "web-identity", AppType: "vue", VueRole: "sub", AppCode: "42", Enabled: true}
	if err := repository.DB.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	b := &webBaseline{DeploymentName: "web-main", Regions: []frontend.Region{{AppID: app.ID, AppName: app.AppName, Role: "sub", AppCode: "42"}}}
	if err := validateDeploymentRegistrations(b); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Delete(&app).Error; err != nil {
		t.Fatal(err)
	}
	app.ID = 0
	if err := repository.DB.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentRegistrations(b); err == nil {
		t.Fatal("recreated registration accepted for old approval")
	}
}

// Explicit opt-in only. The settings file contains registry credentials and is
// supplied outside the repository. All cluster writes stay in a fresh namespace.
func TestFrontendClusterPublication(t *testing.T) {
	if os.Getenv("DF_FRONTEND_CLUSTER_TEST") != "1" {
		t.Skip("opt-in isolated frontend cluster test")
	}
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.AutoMigrate(&model.Application{}, &model.Pipeline{}, &model.Settings{}, &model.ArtifactDeployRecord{}, &model.ArtifactDeployBatch{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(os.Getenv("DF_FRONTEND_CLUSTER_SETTINGS"))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]string
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSettingsRepo().BatchUpdate(settings); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSettingsRepo().Set("customer_env", "本地"); err != nil {
		t.Fatal(err)
	}
	k8s.ResetClient()
	defer k8s.ResetClient()
	cs, err := k8s.GetClient()
	if err != nil {
		t.Fatal(err)
	}
	namespace := fmt.Sprintf("df-frontend-rules-%d", time.Now().Unix())
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"df-build-system/test": "frontend-rules"}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := cs.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{}); err != nil {
			t.Errorf("delete test namespace: %v", err)
		}
	})
	apps := map[string]*model.Application{
		"main": {AppName: "web-main", AppType: "vue", VueRole: "main", Enabled: true},
		"04":   {AppName: "web-existing", AppType: "vue", VueRole: "sub", AppCode: "04", Enabled: true, AdoptExistingDirectory: true},
		"42":   {AppName: "web-example", AppType: "vue", VueRole: "sub", AppCode: "42", Enabled: true},
		"cdn":  {AppName: "web-cdn", AppType: "vue", VueRole: "cdn", Enabled: true},
		"opm":  {AppName: "web-opm", AppType: "vue", VueRole: "standalone", Enabled: true},
	}
	for _, app := range apps {
		app.ArtifactName = app.DeriveArtifactName()
		if err := repository.DB.Create(app).Error; err != nil {
			t.Fatal(err)
		}
	}
	registry := settings["docker_registry_url"]
	workspaceRoot := t.TempDir()
	noop := func(uint, uint, string, string) {}
	// A minimal test-only configuration; no production ConfigMap is read/written.
	nginx := `server {
 listen 80; root /usr/share/nginx/html;
 location ^~ /apps/cdn/ { try_files $uri =404; expires 7d; }
 location ~* \.(js|css|map|png|svg|woff2?)$ { try_files $uri =404; expires 7d; }
 location ~* \.(html|json)$ { try_files $uri =404; add_header Cache-Control no-store always; }
 location ~ ^/apps/(?<app_code>[a-z0-9][a-z0-9_-]*)/ {
  if (!-f $document_root/apps/$app_code/index.html) { return 404; }
  try_files $uri $uri/ /apps/$app_code/index.html;
 }
 location / { try_files $uri $uri/ /index.html; add_header Cache-Control no-store always; }
}`
	baselineImage := func(name string) string {
		dir := filepath.Join(workspaceRoot, name, "baseline")
		mustWriteFile(t, filepath.Join(dir, "html/index.html"), `<script src=/js/base.1111.js></script>`)
		mustWriteFile(t, filepath.Join(dir, "html/js/base.1111.js"), "base")
		if name == "web-main" {
			mustWriteFile(t, filepath.Join(dir, "html/apps/04/index.html"), `<script src=/apps/04/js/old.1111.js></script>`)
			mustWriteFile(t, filepath.Join(dir, "html/apps/04/js/old.1111.js"), "old04")
			mustWriteFile(t, filepath.Join(dir, "html/apps/cdn/lib/1/lib.js"), "cdn")
			if err := os.MkdirAll(filepath.Join(dir, "html/apps/60"), 0755); err != nil {
				t.Fatal(err)
			}
		}
		mustWriteFile(t, filepath.Join(dir, "Dockerfile"), webMainDockerfile(registry, true))
		image := registry + "/prod/" + name + ":" + namespace + "-baseline"
		pc := &types.PipelineContext{AppType: "vue", ImageName: image, OnLog: noop}
		if _, err := (&BuildImageStage{}).execDockerBuild(ctx, pc, dir, image); err != nil {
			t.Fatal(err)
		}
		if _, err := (&PushImageStage{}).pushOne(ctx, pc); err != nil {
			t.Fatal(err)
		}
		return pc.ImageName
	}
	install := func(name, image string) {
		if _, err := cs.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}, Data: map[string]string{"site.conf": nginx}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		labels := map[string]string{"app": name}
		replicas := int32(2)
		dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Ports: []corev1.ContainerPort{{ContainerPort: 80}}, VolumeMounts: []corev1.VolumeMount{{Name: "nginx", MountPath: "/etc/nginx/conf.d", ReadOnly: true}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/index.html", Port: intstr.FromInt(80)}}, PeriodSeconds: 2}}}, Volumes: []corev1.Volume{{Name: "nginx", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}}}}}}
		if _, err := cs.AppsV1().Deployments(namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := service.WaitDeploymentRollout(ctx, cs, namespace, name, 2*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	install("web-main", baselineImage("web-main"))
	configure := func(content string) {
		cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, "web-main", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		cm.Data["site.conf"] = content
		if _, err := cs.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		dep, err := readWebDeployment(ctx, cs, namespace)
		if err != nil {
			t.Fatal(err)
		}
		dep.Spec.Template.Annotations = map[string]string{"test/config": fmt.Sprint(time.Now().UnixNano())}
		if _, err := cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := service.WaitDeploymentRollout(ctx, cs, namespace, "web-main", 2*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	configure(`server { listen 80; root /usr/share/nginx/html; location / { try_files $uri $uri/ /index.html; } }`)
	// Keep this existing routing policy throughout image publication. Hash-mode
	// applications and legacy Nginx policies are not rollout admission criteria.
	cacheHits, cacheSyncs := 0, 0
	publish := func(key, script string) *model.Pipeline {
		app := apps[key]
		p := &model.Pipeline{PipelineNo: fmt.Sprintf("site-%d", time.Now().UnixNano()), ApplicationID: app.ID, AppName: app.AppName, AppType: "vue", TriggerUser: "isolated-test", GitBranch: namespace, ArtifactName: app.ArtifactName, DeployMode: "artifact_image", K8sNamespace: namespace}
		if err := repository.DB.Create(p).Error; err != nil {
			t.Fatal(err)
		}
		log := func(_, _ uint, line, _ string) {
			if strings.Contains(line, "复用本地完整站点") {
				cacheHits++
			}
			if strings.Contains(line, "已同步本地完整站点") {
				cacheSyncs++
			}
		}
		pc := &types.PipelineContext{PipelineID: p.ID, Pipeline: p, AppName: p.AppName, AppType: "vue", VueRole: app.VueRole, AppCode: app.AppCode, ArtifactName: app.ArtifactName, GitBranch: p.GitBranch, DeployMode: p.DeployMode, K8sNamespace: namespace, OnLog: log, Workspace: &types.Workspace{BaseDir: workspaceRoot, AppName: app.AppName, PipelineID: p.ID}}
		if err := os.MkdirAll(pc.Workspace.SourceDir(), 0755); err != nil {
			t.Fatal(err)
		}
		prefix := "/"
		if app.VueRole == "sub" {
			prefix = "/apps/" + app.AppCode + "/"
		}
		files := map[string]string{"index.html": `<script src=` + prefix + `js/` + script + `.js></script>`, "js/" + script + ".js": script}
		if key == "42" {
			files["config.json"] = `{"xiTongId":"42"}`
		}
		writeTestZip(t, filepath.Join(pc.Workspace.SourceDir(), app.ArtifactName), files)
		hash, err := hashWebFile(filepath.Join(pc.Workspace.SourceDir(), app.ArtifactName))
		if err != nil {
			t.Fatal(err)
		}
		inputs, _ := json.Marshal([]model.FrontendArtifact{{FileName: app.ArtifactName, Role: app.VueRole, AppCode: app.AppCode, AppName: app.AppName, AppID: app.ID, SHA256: hash}})
		p.FrontendArtifactsJSON = string(inputs)
		if _, err := (&BuildImageStage{}).Run(ctx, pc); err != nil {
			t.Fatal(err)
		}
		p.ImageName = pc.ImageName
		if err := repository.NewPipelineRepo().SaveImageCheckpoint(p); err != nil {
			t.Fatal(err)
		}
		if _, err := (&PushImageStage{}).Run(ctx, pc); err != nil {
			t.Fatal(err)
		}
		p.ImageName = pc.ImageName
		if err := repository.NewPipelineRepo().SaveImageCheckpoint(p); err != nil {
			t.Fatal(err)
		}
		if _, err := (&K8sDeployStage{}).Run(ctx, pc); err != nil {
			t.Fatal(err)
		}
		var b webBaseline
		if err := json.Unmarshal([]byte(p.FrontendBaselineJSON), &b); err != nil {
			t.Fatal(err)
		}
		if !b.AssetsVerified || b.PreparationImage == "" || !strings.Contains(p.ImageName, "@sha256:") || p.DeploymentProgress != "completed" {
			t.Fatalf("C/F checkpoints missing: %+v", b)
		}
		_, identity, err := currentWebSite(ctx, cs, b.Cluster, namespace, b.target())
		if err != nil {
			t.Fatal(err)
		}
		store, err := newWebSiteStore(workspaceRoot, identity)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.read()
		if err != nil || state.Identity.Image != p.ImageName || state.SHA256 != b.FinalSHA256 {
			t.Fatalf("published complete site not saved: %v %v", state, err)
		}
		return p
	}
	first := publish("42", "new42.2222")
	second := publish("04", "new04.3333")
	if cacheHits != 1 || cacheSyncs != 1 {
		t.Fatalf("second child failed to reuse complete site: hits=%d syncs=%d", cacheHits, cacheSyncs)
	}
	// Customer environments always use the target Pod, even if a local snapshot
	// exists. Keep the customer's path independent of this local-only facility.
	if err := repository.NewSettingsRepo().Set("customer_env", "客户现场"); err != nil {
		t.Fatal(err)
	}
	copiedPod := false
	pc := &types.PipelineContext{Workspace: &types.Workspace{BaseDir: workspaceRoot}, OnLog: func(_, _ uint, line, _ string) {
		if strings.Contains(line, "开始复制运行中的前端") {
			copiedPod = true
		}
	}}
	if _, err := (&BuildImageStage{}).frontendBaseline(ctx, pc, namespace, filepath.Join(t.TempDir(), "customer-baseline"), "web-main"); err != nil {
		t.Fatal(err)
	}
	if !copiedPod {
		t.Fatal("customer environment incorrectly reused a local site")
	}
	if err := repository.NewSettingsRepo().Set("customer_env", "本地"); err != nil {
		t.Fatal(err)
	}
	if err := service.RollbackDeployBatch(ctx, first.PipelineNo, service.NewK8sDeploymentRollbacker(), nil); err == nil {
		t.Fatal("stale first-child rollback accepted")
	}
	if err := service.RollbackDeployBatch(ctx, second.PipelineNo, service.NewK8sDeploymentRollbacker(), nil); err != nil {
		t.Fatal(err)
	}
	_ = publish("04", "new04.5555")
	if cacheHits != 1 || cacheSyncs != 2 {
		t.Fatalf("rollback failed to invalidate saved site: hits=%d syncs=%d", cacheHits, cacheSyncs)
	}
	main, err := readWebDeployment(ctx, cs, namespace)
	if err != nil {
		t.Fatal(err)
	}
	mainImage := webContainerImage(main)
	install("web-opm", baselineImage("web-opm"))
	opm := publish("opm", "opm.4444")
	main, err = readWebDeployment(ctx, cs, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if webContainerImage(main) != mainImage {
		t.Fatal("standalone changed shared deployment")
	}
	if err := service.RollbackDeployBatch(ctx, opm.PipelineNo, service.NewK8sDeploymentRollbacker(), nil); err != nil {
		t.Fatal(err)
	}
	evidence := map[string]any{"namespace": namespace, "replicas": 2, "newSubApplication": true, "cdnAndEmptyDirectoryPreserved": true, "twoPhasePublication": true, "staleRollbackRejected": true, "compatibleRollback": true, "standaloneWithoutConfig": true, "imageRolloutIndependentOfRoutingPolicy": true, "localCompleteSiteReuse": true, "customerEnvironmentCopiesPod": copiedPod, "rollbackInvalidatesCompleteSite": true, "cacheHits": cacheHits, "cacheSyncs": cacheSyncs, "productionNamespaceWritten": false}
	if file := os.Getenv("DF_FRONTEND_EVIDENCE"); file != "" {
		data, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

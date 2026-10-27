package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	"github.com/docker/distribution/reference"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/remotecommand"
)

type webBaseline struct {
	PipelineID         uint              `json:"pipelineId,omitempty"`
	PreparedGeneration int64             `json:"preparedGeneration,omitempty"`
	AssetsVerified     bool              `json:"assetsVerified,omitempty"`
	FinalGeneration    int64             `json:"finalGeneration,omitempty"`
	DeploymentName     string            `json:"deploymentName,omitempty"`
	ResourceVersion    string            `json:"resourceVersion,omitempty"`
	PreparationImage   string            `json:"preparationImage,omitempty"`
	PreparationTag     string            `json:"preparationTag,omitempty"`
	FinalTag           string            `json:"finalTag,omitempty"`
	PreparationSHA256  string            `json:"preparationSha256,omitempty"`
	FinalSHA256        string            `json:"finalSha256,omitempty"`
	Regions            []frontend.Region `json:"regions,omitempty"`
	ConfigMaps         map[string]string `json:"configMaps,omitempty"`

	Exists        bool   `json:"exists"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace"`
	UID           string `json:"uid"`
	Generation    int64  `json:"generation"`
	Image         string `json:"image"`
	NewImage      string `json:"newImage"`
	ContentSHA256 string `json:"contentSha256"`
}

func readWebDeployment(ctx context.Context, cs kubernetes.Interface, namespace string, targets ...string) (*appsv1.Deployment, error) {
	target := "web-main"
	if len(targets) > 0 {
		target = targets[0]
	}
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, target, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 Deployment=%s/%s 失败: %w", namespace, target, err)
	}
	if webContainerImage(dep) == "" {
		return nil, fmt.Errorf("Deployment=%s/%s 中不存在目标容器 %s 或镜像为空，停止更新", namespace, target, target)
	}
	return dep, nil
}

func webContainerImage(dep *appsv1.Deployment) string {
	if dep != nil {
		for _, c := range dep.Spec.Template.Spec.Containers {
			if c.Name == frontendDeploymentName(dep) {
				return c.Image
			}
		}
	}
	return ""
}

func (s *BuildImageStage) copyFromPod(ctx context.Context, pCtx *types.PipelineContext, namespace, destination string, targets ...string) (*webBaseline, error) {
	target := "web-main"
	if len(targets) > 0 {
		target = targets[0]
	}
	log := webLogger(pCtx)
	log.printf("开始读取运行中的前端；命名空间=%s；Deployment=%s；导出目标=%s", namespace, target, destination)
	cs, err := k8s.GetClient()
	if err != nil {
		return nil, err
	}
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return nil, err
	}
	dep, err := readWebDeployment(ctx, cs, namespace, target)
	if err != nil {
		return nil, err
	}
	baseline := &webBaseline{Exists: dep != nil, Cluster: cfg.Host, Namespace: namespace, DeploymentName: target, ConfigMaps: map[string]string{}}
	if dep == nil {
		log.printf("Deployment=%s/%s 不存在，按首次部署校验制品", namespace, target)
		return baseline, nil
	}
	baseline.ResourceVersion = dep.ResourceVersion
	baseline.UID, baseline.Generation, baseline.Image = string(dep.UID), dep.Generation, webContainerImage(dep)
	log.printf("当前 Deployment=%s/%s；UID=%s；generation=%d；镜像=%s", namespace, target, baseline.UID, baseline.Generation, baseline.Image)
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas == 0 || dep.Status.ObservedGeneration < dep.Generation || dep.Status.UpdatedReplicas != replicas || dep.Status.AvailableReplicas < replicas || dep.Status.Replicas != replicas {
		return nil, fmt.Errorf("Deployment=%s/%s 尚未完成当前版本滚动更新，停止读取基线（generation=%d；observed=%d；期望副本=%d；当前=%d；已更新=%d；可用=%d）", namespace, target, dep.Generation, dep.Status.ObservedGeneration, replicas, dep.Status.Replicas, dep.Status.UpdatedReplicas, dep.Status.AvailableReplicas)
	}
	if dep.Spec.Selector == nil {
		return nil, fmt.Errorf("Deployment=%s/%s 缺少 Pod selector", namespace, target)
	}
	owned, err := currentDeploymentPods(ctx, cs, namespace, dep)
	if err != nil {
		return nil, err
	}
	for _, volume := range dep.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil {
			cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, volume.ConfigMap.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			baseline.ConfigMaps[cm.Name] = cm.ResourceVersion
		}
	}
	pod, ok := selectReadyPodForDeployment(dep, owned)
	if !ok {
		return nil, webReadyPodSelectionError(dep, owned)
	}
	log.printf("开始复制运行中的前端；Pod=%s/%s；容器=%s；来源=/usr/share/nginx/html；本地目标=%s", namespace, pod.Name, target, destination)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".pod-export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	request := cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod.Name).Namespace(namespace).SubResource("exec").
		Param("container", target).Param("command", "tar").Param("command", "cf").Param("command", "-").
		Param("command", "-C").Param("command", "/usr/share/nginx/html").Param("command", ".").Param("stdout", "true").Param("stderr", "true")
	executor, err := remotecommand.NewSPDYExecutor(cfg, "POST", request.URL())
	if err != nil {
		return nil, err
	}
	copyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	var diagnostics webDiagnosticBuffer
	go func() {
		err := executor.StreamWithContext(copyCtx, remotecommand.StreamOptions{Stdout: pw, Stderr: &diagnostics})
		_ = pw.CloseWithError(err)
		done <- err
	}()
	extractErr := UntarFromReader(pr, temporary)
	if extractErr == nil {
		// tar stops at its end marker; drain block padding so a valid producer
		// can finish instead of receiving a spurious closed-pipe error.
		_, extractErr = io.Copy(io.Discard, pr)
	}
	_ = pr.Close()
	if extractErr != nil {
		cancel()
	}
	streamErr := <-done
	detail := strings.TrimSpace(diagnostics.String())
	if detail != "" {
		log.printf("Pod=%s/%s tar 诊断：%s", namespace, pod.Name, detail)
	}
	if extractErr != nil {
		return nil, fmt.Errorf("导出前端目录失败（Pod=%s/%s；容器=%s；来源=/usr/share/nginx/html；目标=%s；tar诊断=%s）: %w", namespace, pod.Name, target, destination, detail, extractErr)
	}
	if streamErr != nil {
		return nil, fmt.Errorf("导出前端目录失败（Pod=%s/%s；目标=%s；tar诊断=%s）: %w", namespace, pod.Name, destination, detail, streamErr)
	}
	if err := requireWebIndex(temporary); err != nil {
		return nil, err
	}
	if _, err := frontend.ValidateReferences(temporary, "."); err != nil {
		return nil, fmt.Errorf("运行基线的主入口或引用资源损坏，停止更新: %w", err)
	}
	if _, err := webChildren(temporary); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(destination); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return nil, err
	}
	baseline.ContentSHA256, err = hashWebTree(destination)
	if err != nil {
		return nil, err
	}
	after, err := readWebDeployment(ctx, cs, namespace, target)
	if err != nil {
		return nil, err
	}
	if err := baseline.validate(after, cfg.Host, namespace); err != nil {
		return nil, fmt.Errorf("导出期间基线改变: %w", err)
	}
	currentPod, err := cs.CoreV1().Pods(namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if currentPod.UID != pod.UID {
		return nil, fmt.Errorf("导出期间 Pod 已替换")
	}
	if _, ok := selectReadyPodForDeployment(after, []corev1.Pod{*currentPod}); !ok {
		return nil, fmt.Errorf("导出期间 Pod 已失去 Ready 或镜像改变")
	}
	for _, volume := range dep.Spec.Template.Spec.Volumes {
		if volume.ConfigMap != nil {
			cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, volume.ConfigMap.Name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			if baseline.ConfigMaps[cm.Name] != cm.ResourceVersion {
				return nil, fmt.Errorf("导出期间 ConfigMap %s 已变化", cm.Name)
			}
		}
	}
	logWebTree(log, "线上前端目录复制完成，基线内容摘要已固定", destination)
	return baseline, nil
}

func selectReadyPodForDeployment(dep *appsv1.Deployment, pods []corev1.Pod) (*corev1.Pod, bool) {
	if dep == nil || dep.Spec.Selector == nil || webContainerImage(dep) == "" {
		return nil, false
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, false
	}
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			continue
		}
		specMatches := false
		for _, c := range pod.Spec.Containers {
			if c.Name == frontendDeploymentName(dep) && equivalentContainerImage(c.Image, webContainerImage(dep)) {
				specMatches = true
			}
		}
		if !specMatches {
			continue
		}
		for _, c := range pod.Status.ContainerStatuses {
			if c.Name == frontendDeploymentName(dep) && c.Ready && containerStatusMatches(c, webContainerImage(dep)) {
				return pod, true
			}
		}
	}
	return nil, false
}

func loadWebBaseline(pCtx *types.PipelineContext) (*webBaseline, error) {
	if pCtx.Pipeline == nil || pCtx.Pipeline.FrontendBaselineJSON == "" {
		return nil, fmt.Errorf("缺少前端构建基线，请重新构建后部署")
	}
	var baseline webBaseline
	if err := json.Unmarshal([]byte(pCtx.Pipeline.FrontendBaselineJSON), &baseline); err != nil {
		return nil, fmt.Errorf("前端构建基线无效: %w", err)
	}
	if baseline.NewImage != pCtx.ImageName || baseline.Namespace == "" || baseline.Cluster == "" {
		return nil, fmt.Errorf("前端构建基线与待部署镜像不一致，请重新构建")
	}
	return &baseline, nil
}

func (b *webBaseline) validate(dep *appsv1.Deployment, cluster, namespace string) error {
	if b.Cluster != cluster || b.Namespace != namespace {
		return fmt.Errorf("前端目标环境已变化，请在目标环境重新构建")
	}
	if b.Exists != (dep != nil) {
		return fmt.Errorf("%s 已创建或删除，原构建基线已失效，请重新构建", b.target())
	}
	expectedGeneration, expectedImage := b.Generation, b.Image
	if b.PreparedGeneration > 0 {
		expectedGeneration, expectedImage = b.PreparedGeneration, b.PreparationImage
		if dep == nil || dep.Annotations["df-build-system/pipeline"] != fmt.Sprint(b.PipelineID) {
			return fmt.Errorf("资源准备阶段的发布归属已变化")
		}
	}
	if dep != nil && (string(dep.UID) != b.UID || dep.Generation != expectedGeneration || webContainerImage(dep) != expectedImage) {
		return fmt.Errorf("%s 已发生更新，当前镜像基于旧版本构建，请重新构建后再部署", b.target())
	}
	return nil
}

func validateWebBaseline(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, namespace string) error {
	baseline, err := loadWebBaseline(pCtx)
	if err != nil {
		return err
	}
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return err
	}
	dep, err := readWebDeployment(ctx, cs, namespace, baseline.target())
	if err != nil {
		return err
	}
	if err := baseline.validate(dep, cfg.Host, namespace); err != nil {
		return err
	}
	return nil
}

func frontendDeploymentName(dep *appsv1.Deployment) string {
	if dep == nil || dep.Name == "" {
		return "web-main"
	}
	return dep.Name
}
func (b *webBaseline) target() string {
	if b.DeploymentName == "" {
		return "web-main"
	}
	return b.DeploymentName
}
func saveWebBaseline(pCtx *types.PipelineContext, b *webBaseline) error {
	if pCtx.Pipeline == nil {
		return fmt.Errorf("缺少前端流水线上下文")
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	pCtx.Pipeline.FrontendBaselineJSON = string(data)
	return nil
}

func containerStatusMatches(c corev1.ContainerStatus, image string) bool {
	if at := strings.Index(image, "@sha256:"); at >= 0 {
		return strings.Contains(c.ImageID, image[at+1:])
	}
	return equivalentContainerImage(c.Image, image)
}

// Container runtimes may report an implicit :latest (or a canonical Docker Hub
// name) that differs textually from the Deployment. Compare parsed references;
// never equate different explicit tags, registries, repositories or digests.
func equivalentContainerImage(actual, expected string) bool {
	a, err := reference.ParseNormalizedNamed(actual)
	if err != nil {
		return false
	}
	b, err := reference.ParseNormalizedNamed(expected)
	if err != nil {
		return false
	}
	return reference.TagNameOnly(a).String() == reference.TagNameOnly(b).String()
}

func webReadyPodSelectionError(dep *appsv1.Deployment, pods []corev1.Pod) error {
	var details []string
	for _, pod := range pods[:min(len(pods), 5)] {
		ready := corev1.ConditionUnknown
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady {
				ready = condition.Status
			}
		}
		var containers []string
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name == frontendDeploymentName(dep) {
				containers = append(containers, fmt.Sprintf("容器=%s,ready=%t,镜像=%s,镜像匹配=%t", status.Name, status.Ready, status.Image, containerStatusMatches(status, webContainerImage(dep))))
			}
		}
		if len(containers) == 0 {
			containers = append(containers, "缺少目标容器状态")
		}
		details = append(details, fmt.Sprintf("Pod=%s,phase=%s,PodReady=%s,删除中=%t,%s", pod.Name, pod.Status.Phase, ready, pod.DeletionTimestamp != nil, strings.Join(containers, "/")))
	}
	if len(details) == 0 {
		details = append(details, "未找到属于当前 Deployment 及当前 ReplicaSet 模板的 Pod")
	}
	return fmt.Errorf("Deployment=%s/%s 没有运行当前镜像且容器 Ready 的 Pod，停止更新（期望镜像=%s；当前版本 Pod 数=%d；%s）", dep.Namespace, frontendDeploymentName(dep), webContainerImage(dep), len(pods), strings.Join(details, "；"))
}

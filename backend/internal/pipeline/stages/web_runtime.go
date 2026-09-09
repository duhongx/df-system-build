package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/remotecommand"
)

type webBaseline struct {
	Exists        bool   `json:"exists"`
	Cluster       string `json:"cluster"`
	Namespace     string `json:"namespace"`
	UID           string `json:"uid"`
	Generation    int64  `json:"generation"`
	Image         string `json:"image"`
	NewImage      string `json:"newImage"`
	ContentSHA256 string `json:"contentSha256"`
}

func readWebDeployment(ctx context.Context, cs kubernetes.Interface, namespace string) (*appsv1.Deployment, error) {
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, "web-main", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 web-main Deployment 失败: %w", err)
	}
	if webContainerImage(dep) == "" {
		return nil, fmt.Errorf("Deployment 中不存在 web-main 目标容器，停止更新")
	}
	return dep, nil
}

func webContainerImage(dep *appsv1.Deployment) string {
	if dep != nil {
		for _, c := range dep.Spec.Template.Spec.Containers {
			if c.Name == "web-main" {
				return c.Image
			}
		}
	}
	return ""
}

func (s *BuildImageStage) copyFromPod(ctx context.Context, pCtx *types.PipelineContext, namespace, destination string) (*webBaseline, error) {
	log := webLogger(pCtx)
	log.printf("开始读取运行中的前端；命名空间=%s；Deployment=web-main；导出目标=%s", namespace, destination)
	cs, err := k8s.GetClient()
	if err != nil {
		return nil, err
	}
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return nil, err
	}
	dep, err := readWebDeployment(ctx, cs, namespace)
	if err != nil {
		return nil, err
	}
	baseline := &webBaseline{Exists: dep != nil, Cluster: cfg.Host, Namespace: namespace}
	if dep == nil {
		log.printf("Deployment=%s/web-main 不存在，本次无线上目录可复制，按首次部署校验制品", namespace)
		return baseline, nil
	}
	baseline.UID, baseline.Generation, baseline.Image = string(dep.UID), dep.Generation, webContainerImage(dep)
	log.printf("当前 Deployment=%s/web-main；UID=%s；generation=%d；镜像=%s", namespace, baseline.UID, baseline.Generation, baseline.Image)
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas == 0 || dep.Status.ObservedGeneration < dep.Generation || dep.Status.UpdatedReplicas != replicas || dep.Status.AvailableReplicas < replicas || dep.Status.Replicas != replicas {
		return nil, fmt.Errorf("web-main 尚未完成当前版本滚动更新，停止读取基线，请等待恢复后重试")
	}
	if dep.Spec.Selector == nil {
		return nil, fmt.Errorf("web-main 缺少 Pod selector")
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, fmt.Errorf("读取 web-main Pod 失败: %w", err)
	}
	// A label match alone does not establish ownership. Resolve ReplicaSets too.
	sets, err := cs.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, fmt.Errorf("读取 web-main ReplicaSet 失败: %w", err)
	}
	owners := map[string]bool{}
	for _, set := range sets.Items {
		owner := metav1.GetControllerOf(&set)
		if owner != nil && owner.Kind == "Deployment" && owner.UID == dep.UID {
			owners[string(set.UID)] = true
		}
	}
	var owned []corev1.Pod
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if owner != nil && owner.Kind == "ReplicaSet" && owners[string(owner.UID)] {
			owned = append(owned, pod)
		}
	}
	pod, ok := selectReadyPodForDeployment(dep, owned)
	if !ok {
		return nil, fmt.Errorf("web-main 没有运行当前镜像且容器 Ready 的 Pod，停止更新")
	}
	log.printf("开始复制运行中的 web-main；Pod=%s/%s；容器=web-main；来源=/usr/share/nginx/html；本地目标=%s；命令=tar cf - -C /usr/share/nginx/html .", namespace, pod.Name, destination)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".pod-export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	request := cs.CoreV1().RESTClient().Post().Resource("pods").Name(pod.Name).Namespace(namespace).SubResource("exec").
		Param("container", "web-main").Param("command", "tar").Param("command", "cf").Param("command", "-").
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
		return nil, fmt.Errorf("导出前端目录失败（Pod=%s/%s；容器=web-main；来源=/usr/share/nginx/html；目标=%s；tar诊断=%s）: %w", namespace, pod.Name, destination, detail, extractErr)
	}
	if streamErr != nil {
		return nil, fmt.Errorf("导出前端目录失败（Pod=%s/%s；目标=%s；tar诊断=%s）: %w", namespace, pod.Name, destination, detail, streamErr)
	}
	if err := requireWebIndex(temporary); err != nil {
		return nil, err
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
	logWebTree(log, "线上前端目录复制完成，主应用和子应用入口校验通过", destination)
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
			if c.Name == "web-main" && c.Image == webContainerImage(dep) {
				specMatches = true
			}
		}
		if !specMatches {
			continue
		}
		for _, c := range pod.Status.ContainerStatuses {
			if c.Name == "web-main" && c.Ready && c.Image == webContainerImage(dep) {
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
		return fmt.Errorf("web-main 已创建或删除，原构建基线已失效，请重新构建")
	}
	if dep != nil && (string(dep.UID) != b.UID || dep.Generation != b.Generation || webContainerImage(dep) != b.Image) {
		return fmt.Errorf("web-main 已发生更新，当前镜像基于旧版本构建，请重新构建后再部署")
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
	dep, err := readWebDeployment(ctx, cs, namespace)
	if err != nil {
		return err
	}
	return baseline.validate(dep, cfg.Host, namespace)
}

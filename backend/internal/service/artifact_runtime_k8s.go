package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

type K8sRuntimeVersionReader struct{}

func NewK8sRuntimeVersionReader() K8sRuntimeVersionReader {
	return K8sRuntimeVersionReader{}
}

type K8sDeploymentRollbacker struct{}

func NewK8sDeploymentRollbacker() K8sDeploymentRollbacker {
	return K8sDeploymentRollbacker{}
}

func (K8sDeploymentRollbacker) RollbackDeployment(ctx context.Context, namespace, deploymentName, image string) error {
	return rollbackDeployment(ctx, namespace, deploymentName, "", 0, image)
}

func (K8sDeploymentRollbacker) RollbackDeploymentIfCurrent(ctx context.Context, namespace, deploymentName, expected string, pipelineID uint, image string) error {
	return rollbackDeployment(ctx, namespace, deploymentName, expected, pipelineID, image)
}

func rollbackDeployment(ctx context.Context, namespace, deploymentName, expected string, pipelineID uint, image string) error {
	{
		unlock, err := k8s.LockDeployment(ctx, namespace, deploymentName)
		if err != nil {
			return err
		}
		defer unlock()
	}
	cs, err := k8s.GetClient()
	if err != nil {
		return err
	}
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	var expectedTree string
	if pipelineID != 0 {
		var p model.Pipeline
		if err := repository.DB.First(&p, pipelineID).Error; err != nil {
			return err
		}
		if p.FrontendBaselineJSON != "" {
			var b struct {
				Cluster            string            `json:"cluster"`
				UID                string            `json:"uid"`
				Generation         int64             `json:"generation"`
				FinalGeneration    int64             `json:"finalGeneration"`
				PreparedGeneration int64             `json:"preparedGeneration"`
				PreparationImage   string            `json:"preparationImage"`
				Image              string            `json:"image"`
				NewImage           string            `json:"newImage"`
				PreparationSHA256  string            `json:"preparationSha256"`
				ContentSHA256      string            `json:"contentSha256"`
				ConfigMaps         map[string]string `json:"configMaps"`
			}
			if err := json.Unmarshal([]byte(p.FrontendBaselineJSON), &b); err != nil {
				return err
			}
			cfg, err := k8s.GetRestConfig()
			if err != nil {
				return err
			}
			if cfg.Host != b.Cluster || string(dep.UID) != b.UID {
				return fmt.Errorf("回滚目标集群或 Deployment 身份已改变")
			}
			current := DeploymentContainerImage(dep, deploymentName)
			allowed := current == expected && dep.Generation == b.FinalGeneration
			// Legacy snapshots predate explicit final-generation checkpoints.
			// Accept only their immediate image-only successor, never later edits.
			if b.FinalGeneration == 0 && b.PreparedGeneration == 0 && b.NewImage == expected && current == expected && dep.Generation == b.Generation+1 {
				allowed = true
			}
			if current == b.PreparationImage && dep.Generation == b.PreparedGeneration && dep.Annotations["df-build-system/pipeline"] == fmt.Sprint(pipelineID) {
				allowed = true
				expected = current
			}
			if !allowed {
				return fmt.Errorf("当前发布序列不属于该批次，拒绝历史回滚")
			}
			for name, rv := range b.ConfigMaps {
				cm, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				if cm.ResourceVersion != rv {
					return fmt.Errorf("ConfigMap %s 已变化，不能仅回滚镜像", name)
				}
			}
			expectedTree = b.ContentSHA256
			if image == b.PreparationImage {
				expectedTree = b.PreparationSHA256
			}
		}
	}
	if expected != "" && (DeploymentContainerImage(dep, deploymentName) != expected || (pipelineID != 0 && dep.Annotations["df-build-system/pipeline"] != "" && dep.Annotations["df-build-system/pipeline"] != fmt.Sprint(pipelineID))) {
		return fmt.Errorf("目标已有其他发布，拒绝回滚历史批次；请基于最新版本创建定向回退任务")
	}
	if !SetDeploymentImage(dep, deploymentName, image) {
		return fmt.Errorf("Deployment %s 没有可更新的容器", deploymentName)
	}
	if _, err := cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		return err
	}
	if err := WaitDeploymentRollout(ctx, cs, namespace, deploymentName, 5*time.Minute); err != nil {
		return err
	}
	if expectedTree != "" {
		return verifyRollbackSite(ctx, cs, namespace, deploymentName, image, expectedTree)
	}
	return nil
}

func verifyRollbackSite(ctx context.Context, cs kubernetes.Interface, namespace, name, image, expectedTree string) error {
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return err
	}
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return err
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return err
	}
	var verified int32
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if !podReady(&pod) {
			return fmt.Errorf("回滚副本未就绪")
		}
		matches := false
		for _, c := range pod.Spec.Containers {
			if c.Name == name && c.Image == image {
				matches = true
			}
		}
		if !matches {
			return fmt.Errorf("回滚仍存在非目标镜像副本")
		}
		tmp, err := os.MkdirTemp("", "df-rollback-verify-")
		if err != nil {
			return err
		}
		err = func() error {
			defer os.RemoveAll(tmp)
			if err := frontend.ExportPodSite(ctx, cs, cfg, namespace, pod.Name, name, tmp); err != nil {
				return err
			}
			hash, err := frontend.TreeHash(tmp)
			if err != nil {
				return err
			}
			if hash != expectedTree {
				return fmt.Errorf("回滚副本 %s 的文件清单不匹配", pod.Name)
			}
			body, _, err := frontend.ProbePodHTTP(ctx, cs, cfg, namespace, pod.Name, name, "/index.html")
			if err != nil {
				return err
			}
			entry, err := os.ReadFile(filepath.Join(tmp, "index.html"))
			if err != nil {
				return err
			}
			if !bytes.Equal(body, entry) {
				return fmt.Errorf("回滚入口 HTTP 内容不匹配")
			}
			return nil
		}()
		if err != nil {
			return err
		}
		verified++
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != verified || verified == 0 {
		return fmt.Errorf("回滚副本验证不完整")
	}
	return nil
}

func (K8sRuntimeVersionReader) ReadBusinessVersion(ctx context.Context, record model.ArtifactDeployRecord) (string, error) {
	namespace := strings.TrimSpace(record.Namespace)
	if namespace == "" {
		var err error
		namespace, err = k8s.GetDefaultNamespace()
		if err != nil {
			return "", err
		}
	}
	deploymentName := strings.TrimSpace(record.DeploymentName)
	if deploymentName == "" {
		deploymentName = record.AppName
	}
	cs, err := k8s.GetClient()
	if err != nil {
		return "", err
	}
	pod, err := ReadyPodForDeployment(ctx, cs, namespace, deploymentName)
	if err != nil {
		return "", err
	}
	command := runtimeVersionCommand(record)
	if len(command) == 0 {
		return "", fmt.Errorf("无法确定版本读取命令")
	}
	out, err := ExecInPod(ctx, cs, namespace, pod.Name, command)
	if err != nil {
		return "", err
	}
	return compactJSON(out), nil
}

func CurrentDeploymentImage(ctx context.Context, namespace, deploymentName string) (string, error) {
	cs, err := k8s.GetClient()
	if err != nil {
		return "", err
	}
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return DeploymentContainerImage(dep, deploymentName), nil
}

func (K8sRuntimeVersionReader) CurrentDeploymentImage(ctx context.Context, namespace, deploymentName string) (string, error) {
	return CurrentDeploymentImage(ctx, namespace, deploymentName)
}

func ReadyPodForDeployment(ctx context.Context, cs kubernetes.Interface, namespace, deploymentName string) (*corev1.Pod, error) {
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, err
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}
	pod := SelectReadyPod(dep, pods.Items)
	if pod == nil {
		return nil, fmt.Errorf("未找到 %s 的 Ready Pod", deploymentName)
	}
	return pod, nil
}

func SelectReadyPod(deployment *appsv1.Deployment, pods []corev1.Pod) *corev1.Pod {
	if deployment == nil || deployment.Spec.Selector == nil {
		return nil
	}
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	if err != nil {
		return nil
	}
	var selected *corev1.Pod
	for i := range pods {
		pod := &pods[i]
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		if !podReady(pod) {
			continue
		}
		if selected == nil || pod.CreationTimestamp.After(selected.CreationTimestamp.Time) {
			selected = pod
		}
	}
	return selected
}

func DeploymentContainerImage(dep *appsv1.Deployment, preferredContainer string) string {
	if dep == nil {
		return ""
	}
	if strings.TrimSpace(preferredContainer) != "" {
		for _, c := range dep.Spec.Template.Spec.Containers {
			if c.Name == preferredContainer {
				return c.Image
			}
		}
	}
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		return dep.Spec.Template.Spec.Containers[0].Image
	}
	return ""
}

func SetDeploymentImage(dep *appsv1.Deployment, preferredContainer, image string) bool {
	if dep == nil || len(dep.Spec.Template.Spec.Containers) == 0 {
		return false
	}
	if strings.TrimSpace(preferredContainer) != "" {
		for i := range dep.Spec.Template.Spec.Containers {
			if dep.Spec.Template.Spec.Containers[i].Name == preferredContainer {
				dep.Spec.Template.Spec.Containers[i].Image = image
				return true
			}
		}
	}
	dep.Spec.Template.Spec.Containers[0].Image = image
	return true
}

func WaitDeploymentRollout(ctx context.Context, cs kubernetes.Interface, namespace, deploymentName string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		dep, err := cs.AppsV1().Deployments(namespace).Get(waitCtx, deploymentName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		replicas := int32(1)
		if dep.Spec.Replicas != nil {
			replicas = *dep.Spec.Replicas
		}
		if dep.Status.ObservedGeneration >= dep.Generation &&
			replicas > 0 && dep.Status.Replicas == replicas &&
			dep.Status.UpdatedReplicas == replicas && dep.Status.ReadyReplicas == replicas &&
			dep.Status.AvailableReplicas == replicas &&
			dep.Status.UnavailableReplicas == 0 {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("等待 Deployment %s 回滚完成超时", deploymentName)
		case <-ticker.C:
		}
	}
}

func ExecInPod(ctx context.Context, cs kubernetes.Interface, namespace, podName string, command []string) (string, error) {
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		return "", err
	}
	req := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Command: command,
		Stdout:  true,
		Stderr:  true,
	}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(cfg, "POST", req.URL())
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr}); err != nil {
		if strings.TrimSpace(stderr.String()) != "" {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runtimeVersionCommand(record model.ArtifactDeployRecord) []string {
	if record.AppType == "java" {
		return []string{"curl", "-s", "http://localhost:8080/actuator/info"}
	}
	path := strings.TrimSpace(record.RuntimeVersionPath)
	if path == "" {
		path = RuntimeVersionPathForApplication(model.Application{
			AppName: record.AppName,
			AppType: record.AppType,
			VueRole: record.VueRole,
			AppCode: record.AppCode,
		})
	}
	return []string{"cat", path}
}

func podReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

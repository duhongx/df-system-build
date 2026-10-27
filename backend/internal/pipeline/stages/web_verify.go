package stages

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// A frontend rollout verifies the submitted Deployment and ready target pods.
// HTTP routing and website content are outside this image-update operation.
func verifyFrontendRollout(ctx context.Context, cs kubernetes.Interface, b *webBaseline, image string, generation int64) error {
	dep, err := readWebDeployment(ctx, cs, b.Namespace, b.target())
	if err != nil {
		return err
	}
	if dep == nil || !equivalentContainerImage(webContainerImage(dep), image) {
		return fmt.Errorf("滚动更新的目标镜像已改变")
	}
	if (b.Exists && string(dep.UID) != b.UID) || (generation > 0 && dep.Generation != generation) {
		return fmt.Errorf("Deployment %s/%s 在滚动更新期间已改变", b.Namespace, b.target())
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas < 1 || dep.Status.ObservedGeneration < dep.Generation ||
		dep.Status.Replicas != replicas || dep.Status.UpdatedReplicas != replicas ||
		dep.Status.ReadyReplicas != replicas || dep.Status.AvailableReplicas != replicas || dep.Status.UnavailableReplicas != 0 {
		return fmt.Errorf("Deployment %s/%s 的目标副本尚未全部就绪", b.Namespace, b.target())
	}
	pods, err := currentDeploymentPods(ctx, cs, b.Namespace, dep)
	if err != nil {
		return err
	}
	if len(pods) != int(replicas) {
		return fmt.Errorf("Deployment %s/%s 的目标副本数量不完整", b.Namespace, b.target())
	}
	for _, pod := range pods {
		if _, ok := selectReadyPodForDeployment(dep, []corev1.Pod{pod}); !ok {
			return fmt.Errorf("Pod %s 未运行指定镜像或未 Ready", pod.Name)
		}
	}
	return nil
}

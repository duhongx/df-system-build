package stages

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/pipeline/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// Diagnostics supplement the original rollout error and never change the
// deployment. Bound both time and output, including when the task was canceled.
func collectRolloutDiagnostics(cs kubernetes.Interface, namespace, name string, pc *types.PipelineContext) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out strings.Builder
	write := func(format string, args ...any) {
		remaining := 32*1024 - out.Len()
		if remaining <= 0 {
			return
		}
		line := fmt.Sprintf(format, args...) + "\n"
		if len(line) > remaining {
			line = strings.ToValidUTF8(line[:remaining], "")
		}
		out.WriteString(line)
		if pc != nil && pc.OnLog != nil {
			pc.OnLog(pc.PipelineID, 0, strings.TrimSuffix(line, "\n"), "stderr")
		}
	}
	write("[更新失败诊断] Deployment=%s/%s；仅采集本次新副本，最多 3 个 Pod；日志每段最多 50 行、4 KiB；总时限 10 秒", namespace, name)
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		write("采集 Deployment 状态失败（保留原始更新错误）: %v", err)
		return out.String()
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	write("副本：期望=%d，更新=%d，就绪=%d，可用=%d", replicas, dep.Status.UpdatedReplicas, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas)
	for _, condition := range dep.Status.Conditions {
		write("Deployment 条件：%s=%s；原因=%s；详情=%s", condition.Type, condition.Status, condition.Reason, condition.Message)
	}
	pods, err := currentDeploymentPods(ctx, cs, namespace, dep)
	if err != nil {
		write("采集本次 Pod 失败（保留原始更新错误）: %v", err)
		return out.String()
	}
	if len(pods) == 0 {
		write("尚未发现本次更新的 Pod")
	}
	// Failed/not-ready pods first, so healthy replicas cannot displace the cause.
	sort.SliceStable(pods, func(i, j int) bool { return diagnosticPodReady(pods[j]) && !diagnosticPodReady(pods[i]) })
	if len(pods) > 3 {
		write("共有 %d 个 Pod，仅展示前 3 个未就绪优先的 Pod", len(pods))
		pods = pods[:3]
	}
	for _, pod := range pods {
		write("Pod=%s；阶段=%s；原因=%s；详情=%s", pod.Name, pod.Status.Phase, pod.Status.Reason, pod.Status.Message)
		for _, condition := range pod.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				write("Pod 条件：%s=%s；原因=%s；详情=%s", condition.Type, condition.Status, condition.Reason, condition.Message)
			}
		}
		statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
		if len(statuses) > 3 {
			write("容器数量=%d，仅展示前 3 个容器", len(statuses))
			statuses = statuses[:3]
		}
		for _, status := range statuses {
			write("容器=%s；镜像=%s；Ready=%t；重启次数=%d", status.Name, status.Image, status.Ready, status.RestartCount)
			if waiting := status.State.Waiting; waiting != nil {
				write("等待原因=%s；详情=%s", waiting.Reason, waiting.Message)
			}
			for _, term := range []struct {
				label string
				state *corev1.ContainerStateTerminated
			}{{"当前退出", status.State.Terminated}, {"上次退出", status.LastTerminationState.Terminated}} {
				if term.state != nil {
					write("%s：退出码=%d；信号=%d；原因=%s；详情=%s", term.label, term.state.ExitCode, term.state.Signal, term.state.Reason, term.state.Message)
				}
			}
			for _, previous := range []bool{true, false} {
				if previous && status.RestartCount == 0 && status.LastTerminationState.Terminated == nil {
					continue
				}
				label := "当前日志"
				if previous {
					label = "上次退出日志"
				}
				body, err := readDiagnosticPodLog(ctx, cs, namespace, pod.Name, status.Name, previous)
				if err != nil {
					write("%s/%s %s采集失败（保留原始更新错误）: %v", pod.Name, status.Name, label, err)
					continue
				}
				write("%s/%s %s：\n%s", pod.Name, status.Name, label, body)
			}
		}
		selector := fields.OneTermEqualSelector("involvedObject.uid", string(pod.UID)).String()
		events, err := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: selector, Limit: 100})
		if err != nil {
			write("Pod %s 事件采集失败（保留原始更新错误）: %v", pod.Name, err)
			continue
		}
		sort.SliceStable(events.Items, func(i, j int) bool { return events.Items[i].LastTimestamp.Before(&events.Items[j].LastTimestamp) })
		start := len(events.Items) - 10
		if start < 0 {
			start = 0
		}
		for _, event := range events.Items[start:] {
			write("Pod 事件：%s；原因=%s；次数=%d；详情=%s", event.Type, event.Reason, event.Count, event.Message)
		}
	}
	return strings.TrimSpace(out.String())
}

func diagnosticPodReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func readDiagnosticPodLog(ctx context.Context, cs kubernetes.Interface, namespace, pod, container string, previous bool) (string, error) {
	tail, limit := int64(50), int64(4096)
	stream, err := cs.CoreV1().Pods(namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, Previous: previous, TailLines: &tail, LimitBytes: &limit, Timestamps: true}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, limit))
	return strings.ToValidUTF8(string(data), ""), err
}

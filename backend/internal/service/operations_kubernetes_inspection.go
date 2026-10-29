package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"df-build-server/internal/componentprobe"
	"df-build-server/internal/inspection"
	platformk8s "df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"strings"
)

func (s *OperationsService) collectKubernetes(ctx context.Context, row model.ComponentConnection, secret string) []inspection.Observation {
	return s.collectKubernetesInto(ctx, row, secret, nil)
}

func (s *OperationsService) collectKubernetesInto(ctx context.Context, row model.ComponentConnection, secret string, result *opsprobe.JobResult) []inspection.Observation {
	var client kubernetes.Interface
	var err error
	if row.Source == "environment" {
		client, err = platformk8s.GetClient()
	} else {
		cfg, e := clientcmd.RESTConfigFromKubeConfig([]byte(secret))
		err = e
		if err == nil {
			cfg.Timeout = 15 * time.Second
			client, err = kubernetes.NewForConfig(cfg)
		}
	}
	if err != nil {
		return []inspection.Observation{componentprobe.Failed("cloudhis-1", "Kubernetes 连接", fmt.Errorf("加载集群连接失败，请核对 kubeconfig"))}
	}
	out := []inspection.Observation{}
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		out = append(out, componentprobe.Failed("cloudhis-7", "Kubernetes 节点状态", fmt.Errorf("读取节点失败：%w", err)))
	} else {
		o := componentprobe.Observation("cloudhis-7", "Kubernetes 节点状态", fmt.Sprintf("共 %d 个节点", len(nodes.Items)), "NORMAL")
		t := inspection.Table{Columns: []string{"节点", "角色", "状态", "压力/不可调度", "版本"}}
		masters := 0
		for _, n := range nodes.Items {
			if result != nil {
				addresses := []string{n.Name}
				for _, a := range n.Status.Addresses {
					if a.Address != "" {
						addresses = append(addresses, a.Address)
					}
				}
				result.MemberGroups = append(result.MemberGroups, addresses)
				result.MemberAddresses = append(result.MemberAddresses, addresses...)
			}
			ready := false
			pressure := ""
			role := "worker"
			if _, ok := n.Labels["node-role.kubernetes.io/control-plane"]; ok {
				role = "control-plane"
			}
			if _, ok := n.Labels["node-role.kubernetes.io/master"]; ok {
				role = "control-plane"
			}
			if role == "control-plane" {
				masters++
			}
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady {
					ready = c.Status == corev1.ConditionTrue
				}
				if (c.Type == corev1.NodeMemoryPressure || c.Type == corev1.NodeDiskPressure || c.Type == corev1.NodePIDPressure) && c.Status != corev1.ConditionFalse {
					pressure += string(c.Type) + " "
				}
			}
			if n.Spec.Unschedulable {
				pressure += "不可调度"
			}
			status := "Ready"
			if !ready {
				status = "NotReady"
			}
			if !ready || pressure != "" {
				o.Status = "WARNING"
			}
			t.Rows = append(t.Rows, []string{n.Name, role, status, pressure, n.Status.NodeInfo.KubeletVersion})
		}
		if len(nodes.Items) == 0 {
			o.Status = "UNKNOWN"
			o.Reason = "集群未返回任何节点"
		}
		o.Tables = []inspection.Table{t}
		out = append(out, componentprobe.Observation("cloudhis-1", "Kubernetes 部署情况", fmt.Sprintf("节点 %d，控制平面标签节点 %d；依据实际节点与角色标签记录", len(nodes.Items), masters), "RECORDED"), o)
	}
	var pods []corev1.Pod
	next := ""
	for {
		page, e := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{Limit: 500, Continue: next})
		if e != nil {
			return append(out, componentprobe.Failed("kubernetes-pods", "Kubernetes Pod 状态", fmt.Errorf("读取全命名空间 Pod 失败：%w", e)))
		}
		pods = append(pods, page.Items...)
		next = page.Continue
		if next == "" {
			break
		}
		if len(pods) >= 20000 {
			return append(out, componentprobe.Failed("kubernetes-pods", "Kubernetes Pod 状态", fmt.Errorf("Pod 超过 20000 个，本轮未取得完整列表")))
		}
	}
	var services []corev1.Pod
	for _, p := range pods {
		if strings.HasPrefix(p.Name, "nacos-") || strings.HasPrefix(p.Name, "seata-") {
			services = append(services, p)
		}
	}
	service := kubernetesPodObservation(services, false)
	service.Code = "cloudhis-9"
	service.Name = "Nacos 与 Seata"
	if len(services) == 0 {
		service.Status = "NOT_APPLICABLE"
		service.Reason = "完整 Pod 列表中未发现 Nacos 或 Seata 服务 Pod；宿主机部署由主机专项另外检查"
	}
	return append(out, kubernetesPodObservation(pods, true), service, kubernetesPodObservation(pods, false))
}

func kubernetesPodObservation(pods []corev1.Pod, system bool) inspection.Observation {
	code, name := "cloudhis-10", "Kubernetes 应用 Pod"
	if system {
		code, name = "cloudhis-8", "Kubernetes 系统 Pod"
	}
	o := componentprobe.Observation(code, name, "", "NORMAL")
	abnormal := inspection.Table{Title: "当前异常 Pod", Columns: []string{"Pod", "命名空间", "就绪", "状态", "原因"}}
	history := inspection.Table{Title: "历史重启记录（不等于当前异常）", Columns: []string{"Pod", "命名空间", "累计重启", "最近终止"}}
	total, completed, restarts := 0, 0, 0
	for _, p := range pods {
		if (p.Namespace == "kube-system") != system {
			continue
		}
		total++
		if p.Status.Phase == corev1.PodSucceeded {
			completed++
			continue
		}
		ready := 0
		count := len(p.Spec.Containers)
		restart := int32(0)
		var recent time.Time
		reason := p.Status.Reason
		for _, v := range p.Status.ContainerStatuses {
			if v.Ready {
				ready++
			}
		}
		for _, v := range append(append([]corev1.ContainerStatus{}, p.Status.ContainerStatuses...), p.Status.InitContainerStatuses...) {
			restart += v.RestartCount
			if v.State.Waiting != nil && v.State.Waiting.Reason != "" {
				reason = v.State.Waiting.Reason
			}
			if v.LastTerminationState.Terminated != nil && v.LastTerminationState.Terminated.FinishedAt.Time.After(recent) {
				recent = v.LastTerminationState.Terminated.FinishedAt.Time
			}
		}
		if p.Status.Phase != corev1.PodRunning || ready != count || p.DeletionTimestamp != nil {
			abnormal.Rows = append(abnormal.Rows, []string{p.Name, p.Namespace, fmt.Sprintf("%d/%d", ready, count), string(p.Status.Phase), reason})
		}
		if restart > 0 {
			restarts++
			at := "未提供"
			if !recent.IsZero() {
				at = recent.Format(time.RFC3339)
			}
			history.Rows = append(history.Rows, []string{p.Name, p.Namespace, strconv.Itoa(int(restart)), at})
		}
	}
	o.Value = fmt.Sprintf("共 %d 个 Pod，当前异常 %d，已成功结束 %d；%d 个有历史重启记录", total, len(abnormal.Rows), completed, restarts)
	if len(abnormal.Rows) > 0 {
		o.Status = "WARNING"
		o.Tables = append(o.Tables, abnormal)
	}
	if len(history.Rows) > 0 {
		o.Tables = append(o.Tables, history)
	}
	o.Threshold = "关注非 Running、未就绪及退出中的 Pod；Succeeded 任务不算异常；累计重启单独记录。"
	return o
}

package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type KubernetesHandler struct {
	settingsRepo  *repository.SettingsRepo
	runtimeReader service.ArtifactDeployRuntimeReader
}

func NewKubernetesHandler() *KubernetesHandler {
	return &KubernetesHandler{
		settingsRepo:  repository.NewSettingsRepo(),
		runtimeReader: service.NewK8sRuntimeVersionReader(),
	}
}

func (h *KubernetesHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/kubernetes")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/overview", h.Overview)
		g.GET("/namespaces", h.Namespaces)
		g.GET("/nodes", h.Nodes)
		g.GET("/configmaps", h.ConfigMaps)
		g.GET("/configmaps/:name", h.GetConfigMap)
		g.PUT("/configmaps/:name", h.UpdateConfigMap)
		g.GET("/ingresses", h.Ingresses)
		g.GET("/deployments", h.Deployments)
		g.POST("/deployments/runtime-versions/sync", h.SyncDeploymentRuntimeVersions)
		g.GET("/pods", h.Pods)
		g.GET("/services", h.Services)
		g.POST("/services/:name/update-ports", h.UpdateServicePorts)
		g.DELETE("/services/:name", h.DeleteService)
		g.GET("/pods/:name/logs", h.PodLogs)
		g.POST("/deployments/:name/restart", h.RestartDeployment)
		g.POST("/deployments/:name/scale", h.ScaleDeployment)
		g.POST("/deployments/:name/image", h.UpdateImage)
		g.GET("/deployments/:name/tags", h.GetImageTags)
		g.POST("/deployments/:name/resources", h.UpdateResources)
		g.GET("/top/nodes", h.TopNodes)
		g.GET("/top/pods", h.TopPods)
		g.GET("/resource/:kind/:name", h.GetResourceYAML)
	}
}

func (h *KubernetesHandler) getNS(c *gin.Context) string {
	ns := c.DefaultQuery("namespace", "")
	if ns == "" {
		ns = k8s.GetDefaultNamespace()
	}
	return ns
}

// Overview returns cluster summary
func (h *KubernetesHandler) Overview(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12301, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	ctx := c.Request.Context()

	nodes, _ := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	pods, _ := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	deps, _ := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	svcs, _ := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})

	podRunning := 0
	if pods != nil {
		for _, p := range pods.Items {
			if p.Status.Phase == corev1.PodRunning {
				podRunning++
			}
		}
	}

	nodeCount, podCount, depCount, svcCount := 0, 0, 0, 0
	if nodes != nil {
		nodeCount = len(nodes.Items)
	}
	if pods != nil {
		podCount = len(pods.Items)
	}
	if deps != nil {
		depCount = len(deps.Items)
	}
	if svcs != nil {
		svcCount = len(svcs.Items)
	}

	response.OK(c, gin.H{
		"namespace": ns, "nodeCount": nodeCount,
		"podCount": podCount, "podRunning": podRunning,
		"deploymentCount": depCount, "serviceCount": svcCount,
	})
}

// Namespaces returns all namespaces
func (h *KubernetesHandler) Namespaces(c *gin.Context) {
	names, err := k8s.ListNamespaces(c.Request.Context())
	if err != nil {
		response.Fail(c, 12301, "获取命名空间失败: "+err.Error())
		return
	}
	response.OK(c, names)
}

// Nodes returns node list
func (h *KubernetesHandler) Nodes(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12302, "K8s 连接失败: "+err.Error())
		return
	}
	nodeList, err := cs.CoreV1().Nodes().List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 12302, "获取节点失败: "+err.Error())
		return
	}

	var nodes []gin.H
	for _, n := range nodeList.Items {
		status := "NotReady"
		for _, cond := range n.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				status = "Ready"
			}
		}
		roles := ""
		for label := range n.Labels {
			if strings.HasPrefix(label, "node-role.kubernetes.io/") {
				roles += strings.TrimPrefix(label, "node-role.kubernetes.io/") + ","
			}
		}
		roles = strings.TrimRight(roles, ",")
		if roles == "" {
			roles = "<none>"
		}

		internalIP := ""
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				internalIP = addr.Address
			}
		}

		nodes = append(nodes, gin.H{
			"name": n.Name, "status": status, "roles": roles,
			"version":    n.Status.NodeInfo.KubeletVersion,
			"internalIP": internalIP,
			"os":         n.Status.NodeInfo.OSImage,
			"kernel":     n.Status.NodeInfo.KernelVersion,
			"runtime":    n.Status.NodeInfo.ContainerRuntimeVersion,
			"age":        formatAge(n.CreationTimestamp.Time),
		})
	}
	response.OK(c, nodes)
}

// Deployments returns deployment list
func (h *KubernetesHandler) Deployments(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12303, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	list, err := cs.AppsV1().Deployments(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 12303, "获取 Deployments 失败: "+err.Error())
		return
	}
	runtimeVersions, _ := service.ListDeploymentRuntimeVersions(ns)
	response.OK(c, gin.H{"items": list.Items, "runtimeVersions": runtimeVersions})
}

func (h *KubernetesHandler) SyncDeploymentRuntimeVersions(c *gin.Context) {
	ns := h.getNS(c)
	var req struct {
		Deployments []string `json:"deployments"`
	}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&req)
	}
	runtimeVersions, err := service.SyncDeploymentRuntimeVersions(c.Request.Context(), ns, req.Deployments, h.runtimeReader)
	if err != nil {
		response.Fail(c, 12303, "同步运行版本失败: "+err.Error())
		return
	}
	response.OK(c, gin.H{"runtimeVersions": runtimeVersions})
}

// Pods returns pod list
func (h *KubernetesHandler) Pods(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12304, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	list, err := cs.CoreV1().Pods(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 12304, "获取 Pods 失败: "+err.Error())
		return
	}
	response.OK(c, list)
}

// Services returns service list
func (h *KubernetesHandler) Services(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12305, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	list, err := cs.CoreV1().Services(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 12305, "获取 Services 失败: "+err.Error())
		return
	}
	response.OK(c, list)
}

// ConfigMaps returns configmap list
func (h *KubernetesHandler) ConfigMaps(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12316, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	list, err := cs.CoreV1().ConfigMaps(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 12316, "获取 ConfigMaps 失败: "+err.Error())
		return
	}
	response.OK(c, list)
}

// GetConfigMap returns a single configmap
func (h *KubernetesHandler) GetConfigMap(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12319, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")
	cm, err := cs.CoreV1().ConfigMaps(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12319, "获取 ConfigMap 失败: "+err.Error())
		return
	}
	response.OK(c, cm)
}

// UpdateConfigMap updates a configmap's data
func (h *KubernetesHandler) UpdateConfigMap(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12320, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	var req struct {
		Data map[string]string `json:"data" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12320, "参数错误")
		return
	}

	cm, err := cs.CoreV1().ConfigMaps(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12320, "ConfigMap 不存在: "+err.Error())
		return
	}

	cm.Data = req.Data
	_, err = cs.CoreV1().ConfigMaps(ns).Update(c.Request.Context(), cm, metav1.UpdateOptions{})
	if err != nil {
		response.Fail(c, 12320, "更新 ConfigMap 失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "ConfigMap 已更新", nil)
}

// Ingresses returns ingress list
func (h *KubernetesHandler) Ingresses(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12317, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	list, err := cs.NetworkingV1().Ingresses(ns).List(c.Request.Context(), metav1.ListOptions{})
	if err != nil {
		response.Fail(c, 12317, "获取 Ingress 失败: "+err.Error())
		return
	}
	response.OK(c, list)
}

// PodLogs returns logs for a pod
func (h *KubernetesHandler) PodLogs(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12306, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")
	container := c.Query("container")
	tailLines := int64(200)
	if t, err := strconv.ParseInt(c.DefaultQuery("tail", "200"), 10, 64); err == nil {
		tailLines = t
	}

	opts := &corev1.PodLogOptions{TailLines: &tailLines}
	if container != "" {
		opts.Container = container
	}

	req := cs.CoreV1().Pods(ns).GetLogs(name, opts)
	stream, err := req.Stream(c.Request.Context())
	if err != nil {
		response.Fail(c, 12306, "获取日志失败: "+err.Error())
		return
	}
	defer stream.Close()

	logBytes, _ := io.ReadAll(stream)
	response.OK(c, gin.H{"logs": string(logBytes)})
}

// RestartDeployment performs a rollout restart
func (h *KubernetesHandler) RestartDeployment(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12307, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	// Patch with restart annotation
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, time.Now().Format(time.RFC3339))
	_, err = cs.AppsV1().Deployments(ns).Patch(c.Request.Context(), name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		response.Fail(c, 12307, "重启失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "重启成功", nil)
}

// ScaleDeployment scales a deployment
func (h *KubernetesHandler) ScaleDeployment(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12308, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	var req struct {
		Replicas int32 `json:"replicas" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12308, "参数错误")
		return
	}

	scale, err := cs.AppsV1().Deployments(ns).GetScale(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12308, "获取 Scale 失败: "+err.Error())
		return
	}

	scale.Spec.Replicas = req.Replicas
	_, err = cs.AppsV1().Deployments(ns).UpdateScale(c.Request.Context(), name, scale, metav1.UpdateOptions{})
	if err != nil {
		response.Fail(c, 12308, "扩缩容失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "扩缩容成功", nil)
}

// UpdateImage updates the container image
func (h *KubernetesHandler) UpdateImage(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12310, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	var req struct {
		Image     string `json:"image" binding:"required"`
		Container string `json:"container"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12310, "参数错误")
		return
	}

	container := req.Container
	if container == "" {
		container = name
	}

	patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"%s","image":"%s"}]}}}}`, container, req.Image)
	_, err = cs.AppsV1().Deployments(ns).Patch(c.Request.Context(), name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		response.Fail(c, 12310, "更新镜像失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "镜像已更新", nil)
}

// UpdateResources updates CPU/memory
func (h *KubernetesHandler) UpdateResources(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12311, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	var req struct {
		Container     string `json:"container"`
		CPURequest    string `json:"cpuRequest"`
		CPULimit      string `json:"cpuLimit"`
		MemoryRequest string `json:"memoryRequest"`
		MemoryLimit   string `json:"memoryLimit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12311, "参数错误")
		return
	}

	container := req.Container
	if container == "" {
		container = name
	}

	// Get current deployment
	dep, err := cs.AppsV1().Deployments(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12311, "Deployment 不存在: "+err.Error())
		return
	}

	// Find and update container resources
	for i, cont := range dep.Spec.Template.Spec.Containers {
		if cont.Name == container {
			if dep.Spec.Template.Spec.Containers[i].Resources.Requests == nil {
				dep.Spec.Template.Spec.Containers[i].Resources.Requests = corev1.ResourceList{}
			}
			if dep.Spec.Template.Spec.Containers[i].Resources.Limits == nil {
				dep.Spec.Template.Spec.Containers[i].Resources.Limits = corev1.ResourceList{}
			}
			if req.CPURequest != "" {
				dep.Spec.Template.Spec.Containers[i].Resources.Requests[corev1.ResourceCPU] = resource.MustParse(req.CPURequest)
			}
			if req.MemoryRequest != "" {
				dep.Spec.Template.Spec.Containers[i].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(req.MemoryRequest)
			}
			if req.CPULimit != "" {
				dep.Spec.Template.Spec.Containers[i].Resources.Limits[corev1.ResourceCPU] = resource.MustParse(req.CPULimit)
			}
			if req.MemoryLimit != "" {
				dep.Spec.Template.Spec.Containers[i].Resources.Limits[corev1.ResourceMemory] = resource.MustParse(req.MemoryLimit)
			}
			break
		}
	}

	_, err = cs.AppsV1().Deployments(ns).Update(c.Request.Context(), dep, metav1.UpdateOptions{})
	if err != nil {
		response.Fail(c, 12311, "更新资源配置失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "资源配置已更新", nil)
}

// UpdateServicePorts updates service type and ports
func (h *KubernetesHandler) UpdateServicePorts(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12314, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	var req struct {
		Type  string `json:"type"`
		Ports []struct {
			Port       int32  `json:"port"`
			TargetPort int32  `json:"targetPort"`
			NodePort   int32  `json:"nodePort"`
			Protocol   string `json:"protocol"`
		} `json:"ports"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 12314, "参数错误")
		return
	}

	svc, err := cs.CoreV1().Services(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12314, "Service 不存在: "+err.Error())
		return
	}

	svc.Spec.Type = corev1.ServiceType(req.Type)
	svc.Spec.Ports = make([]corev1.ServicePort, len(req.Ports))
	for i, p := range req.Ports {
		protocol := corev1.ProtocolTCP
		if p.Protocol == "UDP" {
			protocol = corev1.ProtocolUDP
		}
		svc.Spec.Ports[i] = corev1.ServicePort{
			Port:       p.Port,
			TargetPort: intstr.FromInt(int(p.TargetPort)),
			Protocol:   protocol,
		}
		if p.NodePort > 0 && req.Type == "NodePort" {
			svc.Spec.Ports[i].NodePort = p.NodePort
		}
	}

	_, err = cs.CoreV1().Services(ns).Update(c.Request.Context(), svc, metav1.UpdateOptions{})
	if err != nil {
		response.Fail(c, 12314, "更新 Service 失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "Service 已更新", nil)
}

// DeleteService deletes a service
func (h *KubernetesHandler) DeleteService(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12315, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	err = cs.CoreV1().Services(ns).Delete(c.Request.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		response.Fail(c, 12315, "删除 Service 失败: "+err.Error())
		return
	}
	response.OKWithMessage(c, "Service 已删除", nil)
}

// GetImageTags queries Nexus Docker Registry for available tags
func (h *KubernetesHandler) GetImageTags(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12318, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	name := c.Param("name")

	dep, err := cs.AppsV1().Deployments(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
	if err != nil {
		response.Fail(c, 12318, "Deployment 不存在")
		return
	}

	if len(dep.Spec.Template.Spec.Containers) == 0 {
		response.Fail(c, 12318, "Deployment 没有容器")
		return
	}

	currentImage := dep.Spec.Template.Spec.Containers[0].Image
	registryURL, _ := h.settingsRepo.GetByKey("docker_registry_url")
	if registryURL == "" {
		response.Fail(c, 12318, "Docker 镜像仓库未配置")
		return
	}

	// Extract repo name
	repoName := currentImage
	if strings.HasPrefix(repoName, registryURL+"/") {
		repoName = strings.TrimPrefix(repoName, registryURL+"/")
	}
	if idx := strings.LastIndex(repoName, ":"); idx > 0 {
		repoName = repoName[:idx]
	}

	// Query Nexus V2 API
	registryUser, _ := h.settingsRepo.GetByKey("docker_registry_user")
	registryPass, _ := h.settingsRepo.GetByKey("docker_registry_password")

	tagsURL := fmt.Sprintf("http://%s/v2/%s/tags/list", registryURL, repoName)
	client := &http.Client{Timeout: 10 * time.Second}
	req2, _ := http.NewRequest("GET", tagsURL, nil)
	if registryUser != "" && registryPass != "" {
		req2.SetBasicAuth(registryUser, registryPass)
	}

	resp, err := client.Do(req2)
	if err != nil {
		response.Fail(c, 12318, "查询镜像仓库失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		response.Fail(c, 12318, fmt.Sprintf("镜像仓库返回 %d", resp.StatusCode))
		return
	}

	var tagsResp struct {
		Tags []string `json:"tags"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &tagsResp)

	var images []string
	for i := len(tagsResp.Tags) - 1; i >= 0; i-- {
		images = append(images, fmt.Sprintf("%s/%s:%s", registryURL, repoName, tagsResp.Tags[i]))
	}

	response.OK(c, gin.H{"currentImage": currentImage, "repository": repoName, "images": images})
}

// TopNodes returns node metrics (requires metrics-server)
func (h *KubernetesHandler) TopNodes(c *gin.Context) {
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12312, "K8s 连接失败: "+err.Error())
		return
	}

	result := cs.CoreV1().RESTClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").Do(c.Request.Context())
	raw, err := result.Raw()
	if err != nil {
		response.Fail(c, 12312, "获取节点 metrics 失败: "+err.Error())
		return
	}

	var nodeMetrics metricsv1beta1.NodeMetricsList
	if err := json.Unmarshal(raw, &nodeMetrics); err != nil {
		response.Fail(c, 12312, "解析 metrics 失败: "+err.Error())
		return
	}

	var nodes []gin.H
	for _, nm := range nodeMetrics.Items {
		cpuUsage := nm.Usage.Cpu().MilliValue()
		memUsage := nm.Usage.Memory().Value() / (1024 * 1024)
		nodes = append(nodes, gin.H{
			"name":     nm.Name,
			"cpuUsage": fmt.Sprintf("%dm", cpuUsage),
			"memUsage": fmt.Sprintf("%dMi", memUsage),
		})
	}
	response.OK(c, nodes)
}

// TopPods returns pod metrics
func (h *KubernetesHandler) TopPods(c *gin.Context) {
	ns := h.getNS(c)
	cs, _ := k8s.GetClient()

	result := cs.CoreV1().RESTClient().Get().AbsPath(fmt.Sprintf("/apis/metrics.k8s.io/v1beta1/namespaces/%s/pods", ns)).Do(c.Request.Context())
	raw, err := result.Raw()
	if err != nil {
		response.Fail(c, 12313, "获取 Pod metrics 失败: "+err.Error())
		return
	}

	var podMetrics metricsv1beta1.PodMetricsList
	if err := json.Unmarshal(raw, &podMetrics); err != nil {
		response.Fail(c, 12313, "解析 metrics 失败: "+err.Error())
		return
	}

	var pods []gin.H
	for _, pm := range podMetrics.Items {
		var cpuTotal int64
		var memTotal int64
		for _, cont := range pm.Containers {
			cpuTotal += cont.Usage.Cpu().MilliValue()
			memTotal += cont.Usage.Memory().Value() / (1024 * 1024)
		}
		pods = append(pods, gin.H{
			"name":     pm.Name,
			"cpuUsage": fmt.Sprintf("%dm", cpuTotal),
			"memUsage": fmt.Sprintf("%dMi", memTotal),
		})
	}
	response.OK(c, pods)
}

// GetResourceYAML returns YAML of any resource
func (h *KubernetesHandler) GetResourceYAML(c *gin.Context) {
	// For YAML output we still use a simple approach via the REST client
	cs, err := k8s.GetClient()
	if err != nil {
		response.Fail(c, 12309, "K8s 连接失败: "+err.Error())
		return
	}
	ns := h.getNS(c)
	kind := c.Param("kind")
	name := c.Param("name")

	var result []byte
	switch kind {
	case "deployment", "deployments":
		obj, e := cs.AppsV1().Deployments(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
		if e != nil {
			response.Fail(c, 12309, e.Error())
			return
		}
		result, _ = json.MarshalIndent(obj, "", "  ")
	case "service", "services":
		obj, e := cs.CoreV1().Services(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
		if e != nil {
			response.Fail(c, 12309, e.Error())
			return
		}
		result, _ = json.MarshalIndent(obj, "", "  ")
	case "configmap", "configmaps":
		obj, e := cs.CoreV1().ConfigMaps(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
		if e != nil {
			response.Fail(c, 12309, e.Error())
			return
		}
		result, _ = json.MarshalIndent(obj, "", "  ")
	case "pod", "pods":
		obj, e := cs.CoreV1().Pods(ns).Get(c.Request.Context(), name, metav1.GetOptions{})
		if e != nil {
			response.Fail(c, 12309, e.Error())
			return
		}
		result, _ = json.MarshalIndent(obj, "", "  ")
	default:
		response.Fail(c, 12309, "不支持的资源类型: "+kind)
		return
	}
	response.OK(c, gin.H{"yaml": string(result)})
}

func formatAge(t time.Time) string {
	d := time.Since(t)
	if d.Hours() > 24*365 {
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
	if d.Hours() > 24 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d.Hours() > 1 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

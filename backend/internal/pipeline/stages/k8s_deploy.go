package stages

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

type K8sDeployStage struct{}

func (s *K8sDeployStage) Name() string { return "K8s 部署" }
func (s *K8sDeployStage) Code() string { return "K8S_DEPLOY" }

// templatePlan describes which K8s resource templates an app needs.
type templatePlan struct {
	deploymentCode string // e.g. deployment-java / deployment-web
	serviceCode    string // e.g. service-java / service-web
	configMapCode  string // e.g. configmap-web-main (empty = no ConfigMap)
	wantIngress    bool
	skipAll        bool // vue role=sub merges into web-main
}

// resolvePlan figures out which resources an application needs based on its
// type, vue role and whether it is a Java gateway.
func resolvePlan(pCtx *types.PipelineContext) templatePlan {
	plan := templatePlan{}
	switch pCtx.AppType {
	case "java":
		plan.deploymentCode = "deployment-java"
		plan.serviceCode = "service-java"
		plan.wantIngress = pCtx.IsGateway
	case "vue":
		switch pCtx.VueRole {
		case "sub":
			plan.skipAll = true
		case "main":
			plan.deploymentCode = "deployment-web"
			plan.serviceCode = "service-web"
			plan.configMapCode = "configmap-web-main"
			plan.wantIngress = true
		case "standalone":
			plan.deploymentCode = "deployment-web"
			plan.serviceCode = "service-web"
			// Standalone apps use convention-based ConfigMap: configmap-{appName}
			plan.configMapCode = "configmap-web-standalone"
			plan.wantIngress = true
		default:
			plan.deploymentCode = "deployment-web"
			plan.serviceCode = "service-web"
			plan.wantIngress = true
		}
	}
	return plan
}

func (s *K8sDeployStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	if pCtx.ImageName == "" {
		errMsg := "镜像名称为空，无法执行 K8s 部署"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	cs, err := k8s.GetClient()
	if err != nil {
		errMsg := fmt.Sprintf("K8s 客户端初始化失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	namespace := pCtx.K8sNamespace
	if namespace == "" {
		namespace = k8s.GetDefaultNamespace()
	}

	plan := resolvePlan(pCtx)

	// Vue sub apps merge into web-main image; nothing to deploy.
	if plan.skipAll {
		pCtx.OnLog(pCtx.PipelineID, 0, "Vue 子应用，已合并到 web-main 镜像，无需独立部署 ✓", "stdout")
		return &types.StageResult{ExitCode: 0}, nil
	}

	// Determine deployment name: vue main keeps the shared deployment name
	deploymentName := pCtx.AppName
	if pCtx.AppType == "vue" && pCtx.VueRole == "main" {
		deploymentName = "web-main"
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("K8s 命名空间: %s", namespace), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Deployment: %s", deploymentName), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像: %s", pCtx.ImageName), "stdout")

	// 1) ConfigMap (vue main or standalone)
	if plan.configMapCode != "" {
		configMapCode := plan.configMapCode
		// For standalone apps, use convention: configmap-{appName}
		if pCtx.AppType == "vue" && pCtx.VueRole == "standalone" {
			configMapCode = "configmap-" + pCtx.AppName
		}
		// Only apply if there's content OR if it's a template-driven ConfigMap
		if pCtx.ConfigMapContent != "" || (pCtx.AppType == "vue" && pCtx.VueRole == "standalone") {
			if err := s.applyConfigMap(ctx, cs, pCtx, namespace, configMapCode); err != nil {
				// For standalone, ConfigMap is optional — log warning but don't fail
				if pCtx.VueRole == "standalone" {
					pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("ConfigMap 模板 '%s' 未找到，跳过", configMapCode), "stdout")
				} else {
					return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
				}
			}
		}
	}

	// 2) Deployment (create or update image)
	if err := s.applyDeployment(ctx, cs, pCtx, namespace, deploymentName, plan.deploymentCode); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	// 3) Service (always alongside Deployment)
	if err := s.applyService(ctx, cs, pCtx, namespace, deploymentName, plan.serviceCode); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	// 4) Ingress (zero, one, or many)
	if plan.wantIngress {
		ingresses := pCtx.Ingresses
		// Backward-compat: synthesize a single entry from IngressHost.
		if len(ingresses) == 0 && pCtx.IngressHost != "" {
			ingresses = []model.IngressConfig{{Name: pCtx.AppName, Host: pCtx.IngressHost}}
		}

		// Java gateway iterates all entries; vue main/standalone only uses the first.
		if pCtx.AppType == "vue" && len(ingresses) > 1 {
			ingresses = ingresses[:1]
		}

		for _, ing := range ingresses {
			if ing.Host == "" {
				continue
			}
			name := ing.Name
			if name == "" {
				name = pCtx.AppName
			}
			if err := s.applyIngress(ctx, cs, pCtx, namespace, deploymentName, name, ing.Host); err != nil {
				return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
			}
		}
	}

	// Wait for rollout
	pCtx.OnLog(pCtx.PipelineID, 0, "等待 Deployment 滚动更新完成...", "stdout")
	if err := s.waitForRollout(ctx, cs, namespace, deploymentName, pCtx); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "K8s 部署完成 ✓", "stdout")
	logger.Log.Infof("K8s deployment updated: %s/%s -> %s", namespace, deploymentName, pCtx.ImageName)
	return &types.StageResult{ExitCode: 0}, nil
}

// renderResource loads a config item template and renders it with the
// standard variable set used across K8s resource templates.
func (s *K8sDeployStage) renderResource(pCtx *types.PipelineContext, namespace, deploymentName, templateCode string, overrides map[string]string) (string, error) {
	configRepo := repository.NewConfigItemRepo()
	tpl, err := configRepo.GetByCode(templateCode)
	if err != nil {
		errMsg := fmt.Sprintf("K8s 模板 '%s' 不存在，请在配置项管理中创建", templateCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return "", fmt.Errorf("%s", errMsg)
	}

	settingsRepo := repository.NewSettingsRepo()
	registryURL, _ := settingsRepo.GetByKey("docker_registry_url")
	skywalkingGraphqlUrl, _ := settingsRepo.GetByKey("skywalking_graphql_url")

	// Indent ConfigMap content for YAML embedding (used by deployment templates that inline conf)
	indentedCM := ""
	if pCtx.ConfigMapContent != "" {
		for _, line := range strings.Split(pCtx.ConfigMapContent, "\n") {
			indentedCM += "    " + line + "\n"
		}
	}

	nodeIP := k8s.GetFirstNodeIP(context.Background())

	// First Ingress acts as default for templates that still use ${ingressHost}
	defaultIngressHost := pCtx.IngressHost
	if defaultIngressHost == "" && len(pCtx.Ingresses) > 0 {
		defaultIngressHost = pCtx.Ingresses[0].Host
	}

	servicePort := "8080"
	if pCtx.AppType == "vue" {
		servicePort = "80"
	}

	vars := map[string]string{
		"registryUrl":          registryURL,
		"appName":              deploymentName,
		"branch":               pCtx.GitBranch,
		"namespace":            namespace,
		"imageName":            pCtx.ImageName,
		"artifactName":         pCtx.ArtifactName,
		"nodePort":             fmt.Sprintf("%d", pCtx.NodePort),
		"ingressHost":          defaultIngressHost,
		"servicePort":          servicePort,
		"configMapContent":     indentedCM,
		"nodeIP":               nodeIP,
		"skywalkingGraphqlUrl": skywalkingGraphqlUrl,
	}
	for k, v := range overrides {
		vars[k] = v
	}

	return renderTemplate(tpl.Content, vars), nil
}

// applyDeployment creates or updates the Deployment for an application.
func (s *K8sDeployStage) applyDeployment(ctx context.Context, cs *kubernetes.Clientset, pCtx *types.PipelineContext, namespace, deploymentName, templateCode string) error {
	pCtx.OnLog(pCtx.PipelineID, 0, "检查 Deployment 是否存在...", "stdout")
	_, err := cs.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err == nil {
		// Exists: just patch the image
		pCtx.OnLog(pCtx.PipelineID, 0, "Deployment 已存在，更新镜像...", "stdout")
		patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"containers":[{"name":"%s","image":"%s"}]}}}}`, deploymentName, pCtx.ImageName)
		if _, err := cs.AppsV1().Deployments(namespace).Patch(ctx, deploymentName, k8stypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			errMsg := fmt.Sprintf("更新镜像失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, "镜像已更新", "stdout")
		return nil
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "Deployment 不存在，从模板创建...", "stdout")
	yamlContent, err := s.renderResource(pCtx, namespace, deploymentName, templateCode, nil)
	if err != nil {
		return err
	}

	var dep appsv1.Deployment
	if err := yaml.Unmarshal([]byte(yamlContent), &dep); err != nil {
		errMsg := fmt.Sprintf("Deployment 模板解析失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	dep.Namespace = namespace

	if _, err := cs.AppsV1().Deployments(namespace).Create(ctx, &dep, metav1.CreateOptions{}); err != nil {
		errMsg := fmt.Sprintf("创建 Deployment 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "Deployment 已创建", "stdout")
	return nil
}

// applyService creates or updates the Service. When NodePort==0 the rendered
// YAML is post-processed into a ClusterIP service.
func (s *K8sDeployStage) applyService(ctx context.Context, cs *kubernetes.Clientset, pCtx *types.PipelineContext, namespace, deploymentName, templateCode string) error {
	yamlContent, err := s.renderResource(pCtx, namespace, deploymentName, templateCode, nil)
	if err != nil {
		return err
	}

	if pCtx.NodePort == 0 {
		yamlContent = convertNodePortToClusterIP(yamlContent)
	}

	var svc corev1.Service
	if err := yaml.Unmarshal([]byte(yamlContent), &svc); err != nil {
		errMsg := fmt.Sprintf("Service 模板解析失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	svc.Namespace = namespace

	existing, err := cs.CoreV1().Services(namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			errMsg := fmt.Sprintf("查询 Service 失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		if _, err := cs.CoreV1().Services(namespace).Create(ctx, &svc, metav1.CreateOptions{}); err != nil {
			errMsg := fmt.Sprintf("创建 Service 失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Service %s 已创建", svc.Name), "stdout")
		return nil
	}

	// Update spec while preserving cluster-assigned fields
	svc.ResourceVersion = existing.ResourceVersion
	svc.Spec.ClusterIP = existing.Spec.ClusterIP
	svc.Spec.ClusterIPs = existing.Spec.ClusterIPs
	if _, err := cs.CoreV1().Services(namespace).Update(ctx, &svc, metav1.UpdateOptions{}); err != nil {
		errMsg := fmt.Sprintf("更新 Service 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Service %s 已更新", svc.Name), "stdout")
	return nil
}

// applyConfigMap creates or updates a ConfigMap from the configured template.
func (s *K8sDeployStage) applyConfigMap(ctx context.Context, cs *kubernetes.Clientset, pCtx *types.PipelineContext, namespace, templateCode string) error {
	yamlContent, err := s.renderResource(pCtx, namespace, pCtx.AppName, templateCode, nil)
	if err != nil {
		return err
	}

	var cm corev1.ConfigMap
	if err := yaml.Unmarshal([]byte(yamlContent), &cm); err != nil {
		errMsg := fmt.Sprintf("ConfigMap 模板解析失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	cm.Namespace = namespace

	existing, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, cm.Name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			errMsg := fmt.Sprintf("查询 ConfigMap 失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		if _, err := cs.CoreV1().ConfigMaps(namespace).Create(ctx, &cm, metav1.CreateOptions{}); err != nil {
			errMsg := fmt.Sprintf("创建 ConfigMap 失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("ConfigMap %s 已创建", cm.Name), "stdout")
		return nil
	}

	cm.ResourceVersion = existing.ResourceVersion
	if _, err := cs.CoreV1().ConfigMaps(namespace).Update(ctx, &cm, metav1.UpdateOptions{}); err != nil {
		errMsg := fmt.Sprintf("更新 ConfigMap 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("ConfigMap %s 已更新", cm.Name), "stdout")
	return nil
}

// applyIngress renders the shared ingress template with per-ingress name
// and host overrides, then creates or updates the Ingress.
func (s *K8sDeployStage) applyIngress(ctx context.Context, cs *kubernetes.Clientset, pCtx *types.PipelineContext, namespace, serviceName, ingressName, host string) error {
	overrides := map[string]string{
		"ingressName": ingressName,
		"appName":     ingressName, // backwards-compat for templates referencing ${appName}
		"ingressHost": host,
	}
	yamlContent, err := s.renderResource(pCtx, namespace, ingressName, "ingress", overrides)
	if err != nil {
		return err
	}

	var ing netv1.Ingress
	if err := yaml.Unmarshal([]byte(yamlContent), &ing); err != nil {
		errMsg := fmt.Sprintf("Ingress 模板解析失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}

	// Force the metadata.name to the configured ingress name (template default
	// uses ${appName} which we override above, but be defensive).
	ing.Name = ingressName
	ing.Namespace = namespace

	// Make sure the Ingress backend points to the actual Service name.
	for ri := range ing.Spec.Rules {
		rule := &ing.Spec.Rules[ri]
		if rule.HTTP == nil {
			continue
		}
		for pi := range rule.HTTP.Paths {
			if rule.HTTP.Paths[pi].Backend.Service != nil {
				rule.HTTP.Paths[pi].Backend.Service.Name = serviceName
			}
		}
	}

	existing, err := cs.NetworkingV1().Ingresses(namespace).Get(ctx, ingressName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			errMsg := fmt.Sprintf("查询 Ingress 失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		if _, err := cs.NetworkingV1().Ingresses(namespace).Create(ctx, &ing, metav1.CreateOptions{}); err != nil {
			errMsg := fmt.Sprintf("创建 Ingress 失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Ingress %s -> %s 已创建", ingressName, host), "stdout")
		return nil
	}

	ing.ResourceVersion = existing.ResourceVersion
	if _, err := cs.NetworkingV1().Ingresses(namespace).Update(ctx, &ing, metav1.UpdateOptions{}); err != nil {
		errMsg := fmt.Sprintf("更新 Ingress 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Ingress %s -> %s 已更新", ingressName, host), "stdout")
	return nil
}

// convertNodePortToClusterIP rewrites a rendered Service YAML so that it does
// not request a NodePort. Used when the application has NodePort=0.
func convertNodePortToClusterIP(yamlContent string) string {
	// type: NodePort -> type: ClusterIP
	yamlContent = strings.ReplaceAll(yamlContent, "type: NodePort", "type: ClusterIP")

	// Strip any nodePort: <num> lines (whole line including trailing newline)
	re := regexp.MustCompile(`(?m)^\s*nodePort:\s*\d+\s*$\n?`)
	yamlContent = re.ReplaceAllString(yamlContent, "")
	return yamlContent
}

func (s *K8sDeployStage) waitForRollout(ctx context.Context, cs *kubernetes.Clientset, namespace, name string, pCtx *types.PipelineContext) error {
	timeout := 300 * time.Second // 5 minutes for slow-starting services
	deadline := time.Now().Add(timeout)

	// Get the deployment's generation after update
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("获取 Deployment 失败: %v", err)
	}
	targetGeneration := dep.Generation

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			errMsg := "K8s 部署超时或已取消"
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return ctx.Err()
		default:
		}

		dep, err = cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		// Ensure we're looking at the latest update
		if dep.Status.ObservedGeneration < targetGeneration {
			time.Sleep(3 * time.Second)
			continue
		}

		// Check for failed conditions
		for _, cond := range dep.Status.Conditions {
			if cond.Type == "Progressing" && cond.Status == "False" {
				return fmt.Errorf("Deployment 更新失败: %s", cond.Message)
			}
		}

		// Check Pod status for fatal errors (ImagePullBackOff, CrashLoopBackOff)
		if errMsg := s.checkPodErrors(ctx, cs, namespace, name, pCtx); errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}

		// Check if rollout is complete
		if dep.Status.UpdatedReplicas == *dep.Spec.Replicas &&
			dep.Status.ReadyReplicas == *dep.Spec.Replicas &&
			dep.Status.AvailableReplicas == *dep.Spec.Replicas &&
			dep.Status.UnavailableReplicas == 0 {
			return nil
		}

		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("等待中... (Ready: %d/%d, Updated: %d)",
			dep.Status.ReadyReplicas, *dep.Spec.Replicas, dep.Status.UpdatedReplicas), "stdout")
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("Deployment 滚动更新超时 (300s)")
}

// checkPodErrors checks if any new pods have fatal errors
func (s *K8sDeployStage) checkPodErrors(ctx context.Context, cs *kubernetes.Clientset, namespace, deployName string, pCtx *types.PipelineContext) string {
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + deployName,
	})
	if err != nil {
		return ""
	}

	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				switch reason {
				case "ImagePullBackOff", "ErrImagePull":
					msg := fmt.Sprintf("Pod %s 镜像拉取失败: %s", pod.Name, cs.State.Waiting.Message)
					pCtx.OnLog(pCtx.PipelineID, 0, msg, "stderr")
					return msg
				case "CrashLoopBackOff":
					msg := fmt.Sprintf("Pod %s 启动失败 (CrashLoopBackOff): %s", pod.Name, cs.State.Waiting.Message)
					pCtx.OnLog(pCtx.PipelineID, 0, msg, "stderr")
					return msg
				case "CreateContainerConfigError":
					msg := fmt.Sprintf("Pod %s 配置错误: %s", pod.Name, cs.State.Waiting.Message)
					pCtx.OnLog(pCtx.PipelineID, 0, msg, "stderr")
					return msg
				}
			}
		}
	}
	return ""
}

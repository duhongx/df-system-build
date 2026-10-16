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
	"df-build-server/internal/service"
	"df-build-server/pkg/logger"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		case "sub", "cdn":
			plan.deploymentCode = "deployment-web"
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

func (s *K8sDeployStage) Run(ctx context.Context, pCtx *types.PipelineContext) (result *types.StageResult, runErr error) {
	phase := "precheck"
	defer func() {
		if pCtx.AppType == "vue" {
			return
		} // frontend runner records its own C/F submission boundaries.
		if runErr == nil {
			return
		}
		states := map[string]string{
			"precheck":   "尚未由本阶段提交线上修改",
			"preparing":  "部署准备阶段；配置资源可能已创建，尚未提交 Deployment 更新",
			"submitting": "Deployment 请求可能已被接受，当前结果未确认",
			"submitted":  "Deployment 更新请求已被接受，滚动结果尚未验证",
			"completed":  "指定镜像滚动更新已完成，应用已就绪；后续记录保存可能失败",
		}
		runErr = fmt.Errorf("%w；部署进度=%s；线上状态=%s", runErr, phase, states[phase])
		if result != nil {
			result.Error = runErr.Error()
		}
	}()
	checkpoint := func(next string) error {
		if err := service.RecordDeploymentProgress(pCtx.PipelineID, next, pCtx.ImageName); err != nil {
			return fmt.Errorf("记录部署进度 %s 失败，停止后续操作: %w", next, err)
		}
		phase = next
		if pCtx.Pipeline != nil {
			pCtx.Pipeline.DeploymentProgress = next
		}
		return nil
	}
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
		namespace, err = k8s.GetDefaultNamespace()
		if err != nil {
			return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
		}
	}

	plan := resolvePlan(pCtx)
	if pCtx.AppType == "vue" {
		return s.runFrontendDeployment(ctx, cs, pCtx, namespace)
	}

	// Vue sub apps merge into web-main image and still need to roll web-main.
	if plan.skipAll {
		pCtx.OnLog(pCtx.PipelineID, 0, "Vue 子应用，已合并到 web-main 镜像，无需独立部署 ✓", "stdout")
		return &types.StageResult{ExitCode: 0}, nil
	}

	// Determine deployment name: vue main/sub both update the shared web-main deployment.
	deploymentName := pCtx.AppName
	if pCtx.AppType == "vue" && (pCtx.VueRole == "main" || pCtx.VueRole == "sub") {
		deploymentName = "web-main"
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("K8s 命名空间: %s", namespace), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Deployment: %s", deploymentName), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像: %s", pCtx.ImageName), "stdout")

	sharedWeb := pCtx.AppType == "vue" && (pCtx.VueRole == "main" || pCtx.VueRole == "sub")
	if sharedWeb {
		if err := validateWebBaseline(ctx, cs, pCtx, namespace); err != nil {
			return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
		}
	}

	reader := service.NewK8sRuntimeVersionReader()
	beforeImages, imageErr := service.ImagesBeforeDeployment(ctx, namespace, deploymentName, pCtx.PipelineID)
	if imageErr == nil {
		if err := service.CaptureBeforeDeployImages(ctx, pCtx.PipelineID, deploymentName, beforeImages, reader); err != nil {
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("记录更新前版本失败: %v", err), "stderr")
			return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
		} else {
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("已记录更新前目标镜像=%s；可回滚镜像=%s", beforeImages.Desired, beforeImages.Rollback), "stdout")
			if beforeImages.Rollback == "" {
				pCtx.OnLog(pCtx.PipelineID, 0, "当前目标镜像尚未完成滚动且没有可恢复镜像记录；继续本次更新，暂无旧镜像可供回滚", "stderr")
			}
		}
	} else {
		if !apierrors.IsNotFound(imageErr) {
			return &types.StageResult{ExitCode: 1, Error: imageErr.Error()}, imageErr
		}
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("未读取到更新前镜像: %v", imageErr), "stdout")
	}

	if err := checkpoint("preparing"); err != nil {
		return &types.StageResult{ExitCode: 1}, err
	}

	// 1) ConfigMap (vue main or standalone)
	if plan.configMapCode != "" {
		configMapCode := plan.configMapCode
		// For standalone apps, use convention: configmap-{appName}
		if pCtx.AppType == "vue" && pCtx.VueRole == "standalone" {
			configMapCode = "configmap-" + pCtx.AppName
		}
		// Only apply if there's content OR if it's a template-driven ConfigMap
		if pCtx.ConfigMapContent != "" || (pCtx.AppType == "vue" && pCtx.VueRole == "standalone") {
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("[配置检查] 命名空间=%s；ConfigMap 模板=%s", namespace, configMapCode), "stdout")
			if err := s.applyConfigMap(ctx, cs, pCtx, namespace, configMapCode); err != nil {
				return &types.StageResult{ExitCode: 1, Error: err.Error()}, fmt.Errorf("处理 ConfigMap %s/%s 失败，停止部署: %w", namespace, pCtx.AppName, err)
			}
		}
	}

	// 2) Deployment (create or update image)
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("[部署提交] 开始创建或更新 Deployment=%s/%s；目标镜像=%s", namespace, deploymentName, pCtx.ImageName), "stdout")
	if err := checkpoint("submitting"); err != nil {
		return &types.StageResult{ExitCode: 1}, err
	}
	if err := s.applyDeployment(ctx, cs, pCtx, namespace, deploymentName, plan.deploymentCode); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	phase = "submitted" // API accepted, even if saving the next checkpoint fails.
	if err := checkpoint("submitted"); err != nil {
		return &types.StageResult{ExitCode: 1}, err
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("[部署提交完成] Kubernetes 已接受 Deployment=%s/%s 的镜像更新；尚需等待滚动更新和验证", namespace, deploymentName), "stdout")

	// 3) Service (when this app owns a service)
	if plan.serviceCode != "" {
		if err := s.applyService(ctx, cs, pCtx, namespace, deploymentName, plan.serviceCode); err != nil {
			return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
		}
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

	phase = "completed" // Rollout already succeeded even if the following database write fails.
	if pCtx.Pipeline != nil {
		pCtx.Pipeline.DeploymentProgress = "completed"
	}
	if err := checkpoint("completed"); err != nil {
		return &types.StageResult{ExitCode: 1}, err
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "[滚动完成] 指定镜像已就绪；保存部署结果并采集版本信息（采集失败不影响部署结果）", "stdout")
	if err := service.CaptureAfterDeployVersions(ctx, pCtx.PipelineID, deploymentName, pCtx.ImageName, reader); err != nil {
		errMsg := fmt.Sprintf("滚动更新已完成，但保存部署结果失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "K8s 部署完成 ✓", "stdout")
	logger.Log.Infof("K8s deployment updated: %s/%s -> %s", namespace, deploymentName, pCtx.ImageName)
	return &types.StageResult{ExitCode: 0}, nil
}

// renderResource loads a config item template and renders it with the
// standard variable set used across K8s resource templates.
func (s *K8sDeployStage) renderResource(ctx context.Context, pCtx *types.PipelineContext, namespace, deploymentName, templateCode string, overrides map[string]string) (string, error) {
	configRepo := repository.NewConfigItemRepo()
	tpl, err := configRepo.GetByCode(templateCode)
	if err != nil {
		errMsg := fmt.Sprintf("读取 K8s 模板 '%s' 失败: %v", templateCode, err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return "", fmt.Errorf("%s", errMsg)
	}

	settingsRepo := repository.NewSettingsRepo()
	registryURL, skywalkingGraphqlUrl := "", ""
	if strings.Contains(tpl.Content, "${registryUrl}") {
		registryURL, err = settingsRepo.Read("docker_registry_url", true)
		if err != nil {
			return "", err
		}
	}
	if strings.Contains(tpl.Content, "${skywalkingGraphqlUrl}") {
		skywalkingGraphqlUrl, err = settingsRepo.Read("skywalking_graphql_url", false)
		if err != nil {
			return "", err
		}
	}

	// Indent ConfigMap content for YAML embedding (used by deployment templates that inline conf)
	indentedCM := ""
	if pCtx.ConfigMapContent != "" {
		for _, line := range strings.Split(pCtx.ConfigMapContent, "\n") {
			indentedCM += "    " + line + "\n"
		}
	}

	nodeIP := ""
	if strings.Contains(tpl.Content, "${nodeIP}") {
		nodeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		nodeIP, err = k8s.GetFirstNodeIP(nodeCtx)
		if err != nil {
			return "", err
		}
	}

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
func (s *K8sDeployStage) applyDeployment(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, namespace, deploymentName, templateCode string) error {
	pCtx.OnLog(pCtx.PipelineID, 0, "检查 Deployment 是否存在...", "stdout")
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("读取 Deployment 失败: %w", err)
	}
	sharedWeb := pCtx.AppType == "vue" && (pCtx.VueRole == "main" || pCtx.VueRole == "sub")
	if sharedWeb {
		baseline, loadErr := loadWebBaseline(pCtx)
		if loadErr != nil {
			return loadErr
		}
		cfg, cfgErr := k8s.GetRestConfig()
		if cfgErr != nil {
			return cfgErr
		}
		if apierrors.IsNotFound(err) {
			dep = nil
		}
		if err := baseline.validate(dep, cfg.Host, namespace); err != nil {
			return err
		}
		if dep != nil {
			for i := range dep.Spec.Template.Spec.Containers {
				if dep.Spec.Template.Spec.Containers[i].Name == "web-main" {
					dep.Spec.Template.Spec.Containers[i].Image = pCtx.ImageName
				}
			}
			// Use the fetched resourceVersion. Conflicts fail without overwriting
			// a Deployment that changed after the baseline check.
			_, err := cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{})
			return err
		}
	}
	if err == nil {
		// Exists: just patch the image
		pCtx.OnLog(pCtx.PipelineID, 0, "Deployment 已存在，更新镜像...", "stdout")
		found := false
		for i := range dep.Spec.Template.Spec.Containers {
			if dep.Spec.Template.Spec.Containers[i].Name == deploymentName {
				dep.Spec.Template.Spec.Containers[i].Image = pCtx.ImageName
				found = true
			}
		}
		if !found {
			return fmt.Errorf("Deployment 缺少目标容器 %s", deploymentName)
		}
		k8s.ApplyDeploymentStrategy(dep, pCtx.AppType == "java")
		k8s.EnsureDeploymentProgress(dep)
		if _, err := cs.AppsV1().Deployments(namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
			errMsg := fmt.Sprintf("更新镜像失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return fmt.Errorf("%s", errMsg)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, "镜像已更新", "stdout")
		return nil
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "Deployment 不存在，从模板创建...", "stdout")
	yamlContent, err := s.renderResource(ctx, pCtx, namespace, deploymentName, templateCode, nil)
	if err != nil {
		return err
	}

	var created appsv1.Deployment
	if err := yaml.Unmarshal([]byte(yamlContent), &created); err != nil {
		errMsg := fmt.Sprintf("Deployment 模板解析失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	created.Namespace = namespace
	k8s.ApplyDeploymentStrategy(&created, pCtx.AppType == "java")
	k8s.EnsureDeploymentProgress(&created)
	if sharedWeb && (created.Name != "web-main" || webContainerImage(&created) != pCtx.ImageName) {
		return fmt.Errorf("前端 Deployment 模板的名称、容器或镜像与本次更新不一致")
	}

	if _, err := cs.AppsV1().Deployments(namespace).Create(ctx, &created, metav1.CreateOptions{}); err != nil {
		errMsg := fmt.Sprintf("创建 Deployment 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "Deployment 已创建", "stdout")
	return nil
}

// applyService creates or updates the Service. When NodePort==0 the rendered
// YAML is post-processed into a ClusterIP service.
func (s *K8sDeployStage) applyService(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, namespace, deploymentName, templateCode string) error {
	if _, err := cs.CoreV1().Services(namespace).Get(ctx, deploymentName, metav1.GetOptions{}); err == nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Service %s 已存在，跳过", deploymentName), "stdout")
		return nil
	} else if !apierrors.IsNotFound(err) {
		errMsg := fmt.Sprintf("查询 Service 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}

	yamlContent, err := s.renderResource(ctx, pCtx, namespace, deploymentName, templateCode, nil)
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

	if _, err := cs.CoreV1().Services(namespace).Create(ctx, &svc, metav1.CreateOptions{}); err != nil {
		errMsg := fmt.Sprintf("创建 Service 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Service %s 已创建", svc.Name), "stdout")
	return nil
}

// applyConfigMap creates or updates a ConfigMap from the configured template.
func (s *K8sDeployStage) applyConfigMap(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, namespace, templateCode string) error {
	configMapName := pCtx.AppName
	if _, err := cs.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{}); err == nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("ConfigMap %s 已存在，跳过", configMapName), "stdout")
		return nil
	} else if !apierrors.IsNotFound(err) {
		errMsg := fmt.Sprintf("查询 ConfigMap 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}

	yamlContent, err := s.renderResource(ctx, pCtx, namespace, pCtx.AppName, templateCode, nil)
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

	if _, err := cs.CoreV1().ConfigMaps(namespace).Create(ctx, &cm, metav1.CreateOptions{}); err != nil {
		errMsg := fmt.Sprintf("创建 ConfigMap 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("ConfigMap %s 已创建", cm.Name), "stdout")
	return nil
}

// applyIngress renders the shared ingress template with per-ingress name
// and host overrides, then creates or updates the Ingress.
func (s *K8sDeployStage) applyIngress(ctx context.Context, cs kubernetes.Interface, pCtx *types.PipelineContext, namespace, serviceName, ingressName, host string) error {
	if _, err := cs.NetworkingV1().Ingresses(namespace).Get(ctx, ingressName, metav1.GetOptions{}); err == nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Ingress %s 已存在，跳过", ingressName), "stdout")
		return nil
	} else if !apierrors.IsNotFound(err) {
		errMsg := fmt.Sprintf("查询 Ingress 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}

	overrides := map[string]string{
		"ingressName": ingressName,
		"appName":     ingressName, // backwards-compat for templates referencing ${appName}
		"ingressHost": host,
	}
	yamlContent, err := s.renderResource(ctx, pCtx, namespace, ingressName, "ingress", overrides)
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

	if _, err := cs.NetworkingV1().Ingresses(namespace).Create(ctx, &ing, metav1.CreateOptions{}); err != nil {
		errMsg := fmt.Sprintf("创建 Ingress 失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return fmt.Errorf("%s", errMsg)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Ingress %s -> %s 已创建", ingressName, host), "stdout")
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

func (s *K8sDeployStage) waitForRollout(ctx context.Context, cs kubernetes.Interface, namespace, name string, pCtx *types.PipelineContext) (runErr error) {
	defer func() {
		if runErr != nil {
			details := collectRolloutDiagnostics(cs, namespace, name, pCtx)
			if details != "" {
				runErr = fmt.Errorf("%w\n%s", runErr, details)
			}
		}
	}()
	// Get the deployment's generation after update
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("获取 Deployment 失败: %v", err)
	}
	timeout := k8s.DeploymentWaitTimeout(dep)
	deadline := time.Now().Add(timeout)
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("部署等待期限=%ds，按启动探针和 Deployment 进度期限计算", int64(timeout/time.Second)), "stdout")
	targetGeneration, targetUID := dep.Generation, dep.UID
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	if replicas < 1 {
		return fmt.Errorf("Deployment %s/%s 副本数为 0，无法确认应用可用", namespace, name)
	}

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
			return fmt.Errorf("读取滚动更新状态失败（%s/%s）: %w", namespace, name, err)
		}

		if dep.UID != targetUID || dep.Generation != targetGeneration {
			return fmt.Errorf("Deployment %s/%s 在等待期间发生变化，停止本次验证", namespace, name)
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
		if errMsg := s.checkPodErrors(ctx, cs, namespace, dep, pCtx); errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}

		// Check if rollout is complete
		if dep.Status.Replicas == replicas &&
			dep.Status.UpdatedReplicas == replicas &&
			dep.Status.ReadyReplicas == replicas &&
			dep.Status.AvailableReplicas == replicas &&
			dep.Status.UnavailableReplicas == 0 {
			return nil
		}

		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("等待中... (Ready: %d/%d, Updated: %d)",
			dep.Status.ReadyReplicas, replicas, dep.Status.UpdatedReplicas), "stdout")
		time.Sleep(5 * time.Second)
	}

	return fmt.Errorf("Deployment 更新等待超时 (%ds)", int64(timeout/time.Second))
}

// checkPodErrors checks if any new pods have fatal errors
func (s *K8sDeployStage) checkPodErrors(ctx context.Context, cs kubernetes.Interface, namespace string, dep *appsv1.Deployment, pCtx *types.PipelineContext) string {
	pods, err := currentDeploymentPods(ctx, cs, namespace, dep)
	if err != nil {
		return err.Error()
	}
	for _, pod := range pods {
		statuses := append(append([]corev1.ContainerStatus{}, pod.Status.InitContainerStatuses...), pod.Status.ContainerStatuses...)
		for _, cs := range statuses {
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

func currentDeploymentPods(ctx context.Context, cs kubernetes.Interface, namespace string, dep *appsv1.Deployment) ([]corev1.Pod, error) {
	if dep.Spec.Selector == nil {
		return nil, fmt.Errorf("Deployment 缺少 Pod selector，无法检查更新状态")
	}
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("解析 Deployment selector 失败: %v", err)
	}
	options := metav1.ListOptions{LabelSelector: selector.String()}
	sets, err := cs.AppsV1().ReplicaSets(namespace).List(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("查询本次发布 ReplicaSet 失败: %v", err)
	}
	currentSets := map[string]bool{}
	for _, set := range sets.Items {
		owner := metav1.GetControllerOf(&set)
		if owner == nil || owner.Kind != "Deployment" || owner.UID != dep.UID {
			continue
		}
		expected, actual := dep.Spec.Template.DeepCopy(), set.Spec.Template.DeepCopy()
		delete(expected.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		delete(actual.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		if apiequality.Semantic.DeepEqual(expected, actual) {
			currentSets[string(set.UID)] = true
		}
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("查询本次发布 Pod 失败: %v", err)
	}

	var current []corev1.Pod
	for _, pod := range pods.Items {
		owner := metav1.GetControllerOf(&pod)
		if pod.DeletionTimestamp != nil || owner == nil || owner.Kind != "ReplicaSet" || !currentSets[string(owner.UID)] {
			continue
		}
		current = append(current, pod)
	}
	return current, nil
}

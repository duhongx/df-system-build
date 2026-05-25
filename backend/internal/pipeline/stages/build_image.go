package stages

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/docker"
	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

// webMainBuildMutexes ensures only one web-main image build runs at a time per namespace.
var webMainBuildMutexes = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: make(map[string]*sync.Mutex)}

func getWebMainMutex(namespace string) *sync.Mutex {
	webMainBuildMutexes.Lock()
	defer webMainBuildMutexes.Unlock()
	if webMainBuildMutexes.m[namespace] == nil {
		webMainBuildMutexes.m[namespace] = &sync.Mutex{}
	}
	return webMainBuildMutexes.m[namespace]
}

type BuildImageStage struct{}

func (s *BuildImageStage) Name() string { return "构建镜像" }
func (s *BuildImageStage) Code() string { return "BUILD_IMAGE" }

func (s *BuildImageStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	// Get registry settings
	settingsRepo := repository.NewSettingsRepo()
	registryURL, _ := settingsRepo.GetByKey("docker_registry_url")
	if registryURL == "" {
		errMsg := "Docker 镜像仓库地址未配置，请在设置中配置"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	// Determine build strategy based on app type and vue role
	if pCtx.AppType == "vue" && (pCtx.VueRole == "main" || pCtx.VueRole == "sub") {
		return s.buildWebMainImage(ctx, pCtx, settingsRepo, registryURL)
	}

	// Java apps and standalone Vue apps use the standard flow
	return s.buildStandardImage(ctx, pCtx, settingsRepo, registryURL)
}

// buildStandardImage handles Java apps and standalone Vue apps
func (s *BuildImageStage) buildStandardImage(ctx context.Context, pCtx *types.PipelineContext, settingsRepo *repository.SettingsRepo, registryURL string) (*types.StageResult, error) {
	// Get Dockerfile template based on app type
	dockerfileCode := ""
	if pCtx.AppType == "java" {
		dockerfileCode = "dockerfile-java"
	} else {
		dockerfileCode = "dockerfile-web"
	}

	configRepo := repository.NewConfigItemRepo()
	configItem, err := configRepo.GetByCode(dockerfileCode)
	if err != nil {
		errMsg := fmt.Sprintf("Dockerfile 模板 '%s' 不存在，请在配置项管理中创建", dockerfileCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	// Build image tag
	timestamp := time.Now().Format("20060102150405")
	imageName := fmt.Sprintf("%s/%s:%s-%s",
		strings.TrimRight(registryURL, "/"), pCtx.AppName, pCtx.GitBranch, timestamp)
	imageLatest := fmt.Sprintf("%s/%s:%s-latest",
		strings.TrimRight(registryURL, "/"), pCtx.AppName, pCtx.GitBranch)

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像: %s", imageName), "stdout")

	// Prepare build context
	sourceDir := pCtx.Workspace.SourceDir()
	dockerDir := pCtx.Workspace.PipelineDir() + "/docker-build"
	os.MkdirAll(dockerDir, 0755)

	// Render and write Dockerfile
	dockerfileContent := renderTemplate(configItem.Content, map[string]string{
		"registryUrl": registryURL, "appName": pCtx.AppName,
		"branch": pCtx.GitBranch, "artifactName": pCtx.ArtifactName, "version": timestamp,
	})
	os.WriteFile(filepath.Join(dockerDir, "Dockerfile"), []byte(dockerfileContent), 0644)
	pCtx.OnLog(pCtx.PipelineID, 0, "Dockerfile 已生成", "stdout")

	// Copy artifact
	var artifactSrc string
	if pCtx.AppType == "vue" {
		// For Vue apps: extract dist directory into docker build context
		artifactSrc = filepath.Join(sourceDir, pCtx.ArtifactName)
		distDir := filepath.Join(dockerDir, "dist")
		os.MkdirAll(distDir, 0755)
		if err := Unzip(artifactSrc, distDir); err != nil {
			errMsg := fmt.Sprintf("解压制品失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return &types.StageResult{ExitCode: 1, Error: errMsg}, err
		}
	} else {
		// For Java apps: copy jar file
		artifactSrc = filepath.Join(sourceDir, "build", "libs", pCtx.ArtifactName)
		if err := copyFile(artifactSrc, filepath.Join(dockerDir, pCtx.ArtifactName)); err != nil {
			errMsg := fmt.Sprintf("复制制品失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return &types.StageResult{ExitCode: 1, Error: errMsg}, err
		}
	}

	// Copy scripts for Java
	if pCtx.AppType == "java" {
		s.copyScripts(pCtx, dockerDir, registryURL)
	}

	// Build image
	return s.execDockerBuild(ctx, pCtx, dockerDir, imageName, imageLatest)
}

// buildWebMainImage handles web-main and web sub-app image builds
// Both produce a web-main image with the full html/ directory
func (s *BuildImageStage) buildWebMainImage(ctx context.Context, pCtx *types.PipelineContext, settingsRepo *repository.SettingsRepo, registryURL string) (*types.StageResult, error) {
	// Get K8s settings for namespace
	namespace := pCtx.K8sNamespace
	if namespace == "" {
		namespace = k8s.GetDefaultNamespace()
	}

	// Acquire web-main build lock (per namespace) to prevent concurrent builds from conflicting
	pCtx.OnLog(pCtx.PipelineID, 0, "等待 web-main 构建锁...", "stdout")
	mu := getWebMainMutex(namespace)
	mu.Lock()
	defer mu.Unlock()
	pCtx.OnLog(pCtx.PipelineID, 0, "已获取构建锁 ✓", "stdout")

	// Check context hasn't been canceled while waiting for lock
	if ctx.Err() != nil {
		errMsg := "等待构建锁期间超时或已取消"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, ctx.Err()
	}

	timestamp := time.Now().Format("20060102150405")
	// Image is always web-main regardless of whether we're updating main or sub
	imageName := fmt.Sprintf("%s/web-main:%s-%s",
		strings.TrimRight(registryURL, "/"), pCtx.GitBranch, timestamp)
	imageLatest := fmt.Sprintf("%s/web-main:%s-latest",
		strings.TrimRight(registryURL, "/"), pCtx.GitBranch)

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像: %s (微前端模式)", imageName), "stdout")

	sourceDir := pCtx.Workspace.SourceDir()
	dockerDir := pCtx.Workspace.PipelineDir() + "/docker-build"
	htmlDir := filepath.Join(dockerDir, "html")
	os.MkdirAll(dockerDir, 0755)

	// Get K8s settings for kubectl cp
	kubeconfigPath, _ := settingsRepo.GetByKey("k8s_kubeconfig_path")
	if kubeconfigPath == "" {
		kubeconfigPath = "/root/.kube/config"
	}

	// Try to get existing html from running web-main pod
	podCopied := s.copyFromPod(ctx, pCtx, kubeconfigPath, namespace, htmlDir)

	if pCtx.VueRole == "main" {
		// Updating web-main: preserve apps/, replace everything else
		if podCopied {
			pCtx.OnLog(pCtx.PipelineID, 0, "更新主应用: 保留 apps/ 目录，替换其他文件", "stdout")
			// Save apps directory
			appsDir := filepath.Join(htmlDir, "apps")
			appsTmp := filepath.Join(dockerDir, "apps-backup")
			if _, err := os.Stat(appsDir); err == nil {
				os.Rename(appsDir, appsTmp)
			}
			// Clear html dir and extract new main
			os.RemoveAll(htmlDir)
			os.MkdirAll(htmlDir, 0755)
			if err := s.extractZip(pCtx, filepath.Join(sourceDir, pCtx.ArtifactName), htmlDir); err != nil {
				return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
			}
			// Restore apps
			if _, err := os.Stat(appsTmp); err == nil {
				os.Rename(appsTmp, filepath.Join(htmlDir, "apps"))
				pCtx.OnLog(pCtx.PipelineID, 0, "apps/ 目录已恢复", "stdout")
			}
		} else {
			// First deploy or pod not found: extract main + bundle sub-apps from source dir
			pCtx.OnLog(pCtx.PipelineID, 0, "首次部署或 Pod 不存在，解压主应用", "stdout")
			os.MkdirAll(htmlDir, 0755)
			if err := s.extractZip(pCtx, filepath.Join(sourceDir, pCtx.ArtifactName), htmlDir); err != nil {
				return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
			}

			// Check if sub-app zips exist in source dir (batch upload first deploy)
			s.bundleSubApps(pCtx, sourceDir, htmlDir)
		}
	} else if pCtx.VueRole == "sub" {
		// Updating a sub-app: replace only apps/{appCode}/
		appCode := pCtx.AppCode
		if appCode == "" {
			errMsg := "子应用 appCode 为空，无法确定目标目录"
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
		}

		if podCopied {
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("更新子应用: 清理 apps/%s/ 并替换", appCode), "stdout")
			// Clear the sub-app directory
			subAppDir := filepath.Join(htmlDir, "apps", appCode)
			os.RemoveAll(subAppDir)
			os.MkdirAll(subAppDir, 0755)
			// Extract sub-app zip into apps/{appCode}/
			if err := s.extractZip(pCtx, filepath.Join(sourceDir, pCtx.ArtifactName), subAppDir); err != nil {
				return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
			}
		} else {
			// Pod doesn't exist - can't update sub-app without existing html
			errMsg := "web-main Pod 不存在，无法更新子应用。请先部署 web-main 主应用"
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
		}
	}

	// Write Dockerfile for web-main (simple COPY html)
	dockerfile := fmt.Sprintf(`FROM %s/base/nginx:v1

COPY html/ /usr/share/nginx/html/

EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
`, registryURL)
	os.WriteFile(filepath.Join(dockerDir, "Dockerfile"), []byte(dockerfile), 0644)
	pCtx.OnLog(pCtx.PipelineID, 0, "Dockerfile 已生成 (web-main 微前端模式)", "stdout")

	// Build image
	return s.execDockerBuild(ctx, pCtx, dockerDir, imageName, imageLatest)
}

// copyFromPod copies /usr/share/nginx/html from the running web-main pod using client-go
func (s *BuildImageStage) copyFromPod(ctx context.Context, pCtx *types.PipelineContext, kubeconfigPath, namespace, destDir string) bool {
	pCtx.OnLog(pCtx.PipelineID, 0, "尝试从 web-main Pod 拷贝 html 目录...", "stdout")

	cs, err := k8s.GetClient()
	if err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("K8s 客户端初始化失败: %v", err), "stderr")
		return false
	}

	// Find web-main pod
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=web-main",
	})
	if err != nil || len(pods.Items) == 0 {
		pCtx.OnLog(pCtx.PipelineID, 0, "未找到 web-main Pod (可能是首次部署)", "stdout")
		return false
	}

	podName := pods.Items[0].Name
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("找到 Pod: %s", podName), "stdout")

	// Use exec to run tar inside the pod and pipe to local
	cfg, err := k8s.GetRestConfig()
	if err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("获取 REST config 失败: %v", err), "stderr")
		return false
	}

	os.MkdirAll(destDir, 0755)

	// Execute: kubectl cp equivalent via exec tar
	execReq := cs.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("command", "tar").
		Param("command", "cf").
		Param("command", "-").
		Param("command", "-C").
		Param("command", "/usr/share/nginx").
		Param("command", "html").
		Param("stdout", "true").
		Param("stderr", "true")

	exec2, err := remotecommand.NewSPDYExecutor(cfg, "POST", execReq.URL())
	if err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("创建 exec 失败: %v", err), "stderr")
		return false
	}

	// Pipe tar output to local extraction using Go tar library
	pr, pw := io.Pipe()
	var execErr error
	go func() {
		execErr = exec2.Stream(remotecommand.StreamOptions{Stdout: pw, Stderr: io.Discard})
		pw.Close()
	}()

	// Extract tar to destDir using Go standard library
	if err := UntarFromReader(pr, destDir); err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("解压 tar 失败: %v", err), "stderr")
		return false
	}

	if execErr != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Pod exec 失败: %v", execErr), "stderr")
		return false
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "html 目录拷贝完成 ✓", "stdout")
	return true
}

// extractZip extracts a zip file to a target directory using Go standard library
func (s *BuildImageStage) extractZip(pCtx *types.PipelineContext, zipPath, targetDir string) error {
	if err := Unzip(zipPath, targetDir); err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("解压失败: %v", err), "stderr")
		return fmt.Errorf("解压 %s 失败: %v", filepath.Base(zipPath), err)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("解压完成: %s → %s", filepath.Base(zipPath), targetDir), "stdout")
	return nil
}

// bundleSubApps looks for sub-app zip files in sourceDir and extracts them into html/apps/{appCode}/
// This is used during first deploy when web-main.zip and sub-app zips are uploaded together.
func (s *BuildImageStage) bundleSubApps(pCtx *types.PipelineContext, sourceDir, htmlDir string) {
	// Load all sub-apps from database
	appRepo := repository.NewApplicationRepo()
	apps, _, _ := appRepo.List(repository.AppListParams{PageSize: 1000, AppType: "vue"})

	appsDir := filepath.Join(htmlDir, "apps")
	bundled := 0

	for _, app := range apps {
		if app.VueRole != "sub" || app.AppCode == "" {
			continue
		}

		// Look for {appCode}.zip in source dir
		zipPath := filepath.Join(sourceDir, app.AppCode+".zip")
		if _, err := os.Stat(zipPath); os.IsNotExist(err) {
			continue
		}

		// Extract to html/apps/{appCode}/
		subDir := filepath.Join(appsDir, app.AppCode)
		os.MkdirAll(subDir, 0755)
		if err := Unzip(zipPath, subDir); err != nil {
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("子应用 %s 解压失败: %v", app.AppCode, err), "stderr")
			continue
		}
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("子应用 %s 已打包到 apps/%s/", app.AppName, app.AppCode), "stdout")
		bundled++
	}

	if bundled > 0 {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("共打包 %d 个子应用", bundled), "stdout")
	}
}

// copyScripts copies app.sh and delete_app.sh from config items
func (s *BuildImageStage) copyScripts(pCtx *types.PipelineContext, dockerDir, registryURL string) {
	configRepo := repository.NewConfigItemRepo()
	settingsRepo := repository.NewSettingsRepo()

	// Read environment config for script variables
	skywalkingOapUrl, _ := settingsRepo.GetByKey("skywalking_oap_url")
	nacosUser, _ := settingsRepo.GetByKey("nacos_user")
	nacosPass, _ := settingsRepo.GetByKey("nacos_password")

	for _, scriptCode := range []string{"app-sh-java", "delete-app-java"} {
		scriptItem, err := configRepo.GetByCode(scriptCode)
		if err == nil && scriptItem.Content != "" {
			scriptName := "app.sh"
			if scriptCode == "delete-app-java" {
				scriptName = "delete_app.sh"
			}
			scriptContent := renderTemplate(scriptItem.Content, map[string]string{
				"registryUrl":     registryURL,
				"appName":         pCtx.AppName,
				"branch":          pCtx.GitBranch,
				"artifactName":    pCtx.ArtifactName,
				"skywalkingOapUrl": skywalkingOapUrl,
				"nacosUser":       nacosUser,
				"nacosPass":       nacosPass,
			})
			os.WriteFile(filepath.Join(dockerDir, scriptName), []byte(scriptContent), 0755)
			pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("脚本 %s 已生成", scriptName), "stdout")
		}
	}
}

// execDockerBuild runs docker build and returns the result
func (s *BuildImageStage) execDockerBuild(ctx context.Context, pCtx *types.PipelineContext, dockerDir, imageName, imageLatest string) (*types.StageResult, error) {
	pCtx.OnLog(pCtx.PipelineID, 0, "开始构建 Docker 镜像...", "stdout")

	cli, err := docker.NewClient("")
	if err != nil {
		errMsg := fmt.Sprintf("Docker 不可用: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	// Login to registry before build (FROM may reference private images)
	settingsRepo := repository.NewSettingsRepo()
	registryURL, _ := settingsRepo.GetByKey("docker_registry_url")
	registryUser, _ := settingsRepo.GetByKey("docker_registry_user")
	registryPass, _ := settingsRepo.GetByKey("docker_registry_password")

	var authConfigs map[string]docker.AuthConfig
	if registryURL != "" && registryUser != "" && registryPass != "" {
		authConfigs = map[string]docker.AuthConfig{
			registryURL: {Username: registryUser, Password: registryPass},
		}
	}

	cmdReader, err := cli.BuildImageWithAuth(ctx, dockerDir, authConfigs, imageName, imageLatest)
	if err != nil {
		errMsg := fmt.Sprintf("Docker build 启动失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	docker.ReadLines(cmdReader, func(line string) {
		pCtx.OnLog(pCtx.PipelineID, 0, line, "stdout")
	})

	cmdReader.Close()
	exitCode := cmdReader.ExitCode

	if ctx.Err() != nil {
		errMsg := "镜像构建超时或已取消"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, ctx.Err()
	}

	if exitCode != 0 {
		errMsg := fmt.Sprintf("镜像构建失败 (exit code: %d)", exitCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: exitCode, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	pCtx.ImageName = imageName
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("镜像构建完成 ✓ %s", imageName), "stdout")
	logger.Log.Infof("Docker image built: %s", imageName)

	return &types.StageResult{ExitCode: 0}, nil
}

// renderTemplate replaces ${var} placeholders with values
func renderTemplate(content string, vars map[string]string) string {
	result := content
	for k, v := range vars {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
	}
	return result
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("读取源文件失败: %w", err)
	}
	return os.WriteFile(dst, data, 0644)
}

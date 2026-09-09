package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df-build-server/internal/docker"
	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
	"gorm.io/gorm"
)

type BuildImageStage struct{}

func (s *BuildImageStage) Name() string { return "构建镜像" }
func (s *BuildImageStage) Code() string { return "BUILD_IMAGE" }

func (s *BuildImageStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	// Get registry settings
	settingsRepo := repository.NewSettingsRepo()
	registryURL, err := readImageSetting(settingsRepo, "docker_registry_url", true)
	if err != nil {
		errMsg := err.Error()
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
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
		errMsg := fmt.Sprintf("读取 Dockerfile 模板 %s 失败: %v", dockerfileCode, err)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			errMsg = fmt.Sprintf("Dockerfile 模板 %s 不存在，请在配置项管理中创建", dockerfileCode)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	if strings.TrimSpace(configItem.Content) == "" {
		err := fmt.Errorf("Dockerfile 模板 %s 内容为空", dockerfileCode)
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	// Build image tag
	timestamp := time.Now().Format("20060102150405")
	imageName := fmt.Sprintf("%s/%s:%s-%s-p%d",
		strings.TrimRight(registryURL, "/"), pCtx.AppName, pCtx.GitBranch, timestamp, pCtx.PipelineID)

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像: %s", imageName), "stdout")

	// Prepare build context
	sourceDir := pCtx.Workspace.SourceDir()
	dockerDir := pCtx.Workspace.PipelineDir() + "/docker-build"
	if err := os.MkdirAll(dockerDir, 0755); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, fmt.Errorf("创建镜像构建目录 %s 失败: %w", dockerDir, err)
	}

	// Render and write Dockerfile
	dockerfileContent := renderTemplate(configItem.Content, map[string]string{
		"registryUrl": registryURL, "appName": pCtx.AppName,
		"branch": pCtx.GitBranch, "artifactName": pCtx.ArtifactName, "version": timestamp,
	})
	if err := os.WriteFile(filepath.Join(dockerDir, "Dockerfile"), []byte(dockerfileContent), 0644); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, fmt.Errorf("写入 Dockerfile %s 失败: %w", dockerDir, err)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("Dockerfile 已生成；模板=%s；路径=%s", dockerfileCode, filepath.Join(dockerDir, "Dockerfile")), "stdout")

	// Copy artifact
	var artifactSrc string
	if pCtx.AppType == "vue" {
		webLogger(pCtx).printf("本流水线更新独立应用=%s；制品=%s；目标 Deployment=%s；不合并到 web-main", pCtx.AppName, pCtx.ArtifactName, pCtx.AppName)
		// For Vue apps: extract dist directory into docker build context
		artifactSrc = filepath.Join(sourceDir, pCtx.ArtifactName)
		distDir := filepath.Join(dockerDir, "dist")
		if err := os.MkdirAll(distDir, 0755); err != nil {
			return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
		}
		if err := webFileStep(webLogger(pCtx), "解压独立应用制品", artifactSrc, distDir, func() error { return extractWebZip(pCtx, artifactSrc, distDir) }); err != nil {
			errMsg := fmt.Sprintf("解压制品失败: %v", err)
			pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
			return &types.StageResult{ExitCode: 1, Error: errMsg}, err
		}
		logWebTree(webLogger(pCtx), "独立应用制品解压完成", distDir)
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
		if err := s.copyScripts(pCtx, dockerDir, registryURL); err != nil {
			pCtx.OnLog(pCtx.PipelineID, 0, err.Error(), "stderr")
			return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
		}
	}

	// Build image
	return s.execDockerBuild(ctx, pCtx, dockerDir, imageName)
}

// buildWebMainImage assembles the exact uploaded regions over a verified live baseline.
func (s *BuildImageStage) buildWebMainImage(ctx context.Context, pCtx *types.PipelineContext, settingsRepo *repository.SettingsRepo, registryURL string) (*types.StageResult, error) {
	log := webLogger(pCtx)
	fail := func(err error) (*types.StageResult, error) {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	artifacts, err := frontendArtifacts(pCtx)
	if err != nil {
		return fail(err)
	}
	namespace := pCtx.K8sNamespace
	if namespace == "" {
		namespace, err = k8s.GetDefaultNamespace()
		if err != nil {
			return fail(err)
		}
		pCtx.K8sNamespace = namespace
	}
	log.printf("本流水线合并 %d 个制品到共享 Deployment=%s/web-main", len(artifacts), namespace)
	dockerDir := filepath.Join(pCtx.Workspace.PipelineDir(), "docker-build")
	if err := webFileStep(log, "清空本地镜像构建工作目录", "本地工作空间", dockerDir, func() error { return os.RemoveAll(dockerDir) }); err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(dockerDir, 0755); err != nil {
		return fail(err)
	}
	inputDir, err := os.MkdirTemp(pCtx.Workspace.PipelineDir(), ".web-inputs-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(inputDir)
	plan, err := prepareWebUpdate(pCtx.Workspace.SourceDir(), inputDir, artifacts, log)
	if err != nil {
		return fail(err)
	}
	baselineDir := filepath.Join(inputDir, "baseline")
	baseline, err := s.copyFromPod(ctx, pCtx, namespace, baselineDir)
	if err != nil {
		return fail(err)
	}
	htmlDir := filepath.Join(dockerDir, "html")
	updated, preserved, err := plan.assemble(baselineDir, htmlDir, !baseline.Exists)
	if err != nil {
		return fail(err)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "本次替换: "+strings.Join(updated, "、"), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, "保持不变: "+strings.Join(preserved, "、"), "stdout")
	timestamp := time.Now().Format("20060102150405")
	imageName := fmt.Sprintf("%s/web-main:%s-%s-p%d", strings.TrimRight(registryURL, "/"), pCtx.GitBranch, timestamp, pCtx.PipelineID)
	baseline.NewImage = imageName
	encoded, err := json.Marshal(baseline)
	if err != nil {
		return fail(err)
	}
	if pCtx.Pipeline == nil {
		return fail(fmt.Errorf("缺少前端流水线上下文"))
	}
	pCtx.Pipeline.FrontendBaselineJSON = string(encoded)
	dockerfile := webMainDockerfile(registryURL)
	if err := os.WriteFile(filepath.Join(dockerDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return fail(err)
	}
	log.printf("Dockerfile 已写入=%s；基础镜像=%s/base/nginx:v1；先清空镜像静态目录，再复制 html/ 到 /usr/share/nginx/html/", filepath.Join(dockerDir, "Dockerfile"), strings.TrimRight(registryURL, "/"))
	return s.execDockerBuild(ctx, pCtx, dockerDir, imageName)
}

func webMainDockerfile(registryURL string) string {
	return fmt.Sprintf("FROM %s/base/nginx:v1\nRUN rm -rf /usr/share/nginx/html && mkdir -p /usr/share/nginx/html\nCOPY html/ /usr/share/nginx/html/\nEXPOSE 80\nCMD [\"nginx\", \"-g\", \"daemon off;\"]\n", strings.TrimRight(registryURL, "/"))
}

func replaceWebZip(pCtx *types.PipelineContext, zipPath, targetDir string) error {
	return extractWebZip(pCtx, zipPath, targetDir)
}

func extractWebZip(pCtx *types.PipelineContext, zipPath, targetDir string, logs ...webLog) error {
	log := webLogger(pCtx)
	if len(logs) > 0 {
		log = logs[0]
	}
	parent := filepath.Dir(targetDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(parent, ".webzip-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	log.printf("开始解压 ZIP；来源=%s；临时目录=%s", zipPath, tmpDir)
	if err := Unzip(zipPath, tmpDir); err != nil {
		return err
	}

	sourceDir := tmpDir
	distDir := filepath.Join(tmpDir, "dist")
	if info, err := os.Stat(distDir); err == nil && info.IsDir() {
		sourceDir = distDir
	}

	log.printf("ZIP 解压完成；识别站点根目录=%s；将其内容写入=%s", sourceDir, targetDir)
	if err := requireWebIndex(sourceDir); err != nil {
		return err
	}
	nextDir, err := os.MkdirTemp(parent, ".webcopy-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(nextDir)
	if err := copyDirContents(sourceDir, nextDir); err != nil {
		return err
	}
	if err := os.RemoveAll(targetDir); err != nil {
		return err
	}
	return os.Rename(nextDir, targetDir)
}

func copyDirContents(sourceDir, targetDir string) error {
	return filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		targetPath := filepath.Join(targetDir, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("前端目录不允许符号链接或特殊文件: %s", path)
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		return copyFile(path, targetPath)
	})
}

// copyScripts copies app.sh and delete_app.sh from config items
func (s *BuildImageStage) copyScripts(pCtx *types.PipelineContext, dockerDir, registryURL string) error {
	configRepo := repository.NewConfigItemRepo()
	settingsRepo := repository.NewSettingsRepo()

	// Read environment config for script variables
	values := make(map[string]string)
	for _, key := range []string{"skywalking_oap_url", "nacos_user", "nacos_password"} {
		value, err := readImageSetting(settingsRepo, key, false)
		if err != nil {
			return err
		}
		values[key] = value
	}

	for _, scriptCode := range []string{"app-sh-java", "delete-app-java"} {
		scriptItem, err := configRepo.GetByCode(scriptCode)
		if err != nil {
			return fmt.Errorf("读取 Java 脚本模板 %s 失败: %w", scriptCode, err)
		}
		if strings.TrimSpace(scriptItem.Content) == "" {
			return fmt.Errorf("Java 脚本模板 %s 内容为空", scriptCode)
		}
		scriptName := "app.sh"
		if scriptCode == "delete-app-java" {
			scriptName = "delete_app.sh"
		}
		scriptContent := renderTemplate(scriptItem.Content, map[string]string{
			"registryUrl":      registryURL,
			"appName":          pCtx.AppName,
			"branch":           pCtx.GitBranch,
			"artifactName":     pCtx.ArtifactName,
			"skywalkingOapUrl": values["skywalking_oap_url"],
			"nacosUser":        values["nacos_user"],
			"nacosPass":        values["nacos_password"],
		})
		if err := os.WriteFile(filepath.Join(dockerDir, scriptName), []byte(scriptContent), 0755); err != nil {
			return fmt.Errorf("写入 Java 脚本 %s 失败: %w", filepath.Join(dockerDir, scriptName), err)
		}
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("脚本 %s 已生成", scriptName), "stdout")
	}
	return nil
}

// execDockerBuild runs docker build and returns the result
func (s *BuildImageStage) execDockerBuild(ctx context.Context, pCtx *types.PipelineContext, dockerDir, imageName string) (*types.StageResult, error) {
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("开始制作并上传 Docker 构建上下文；本地目录=%s；Dockerfile=%s；目标镜像=%s", dockerDir, filepath.Join(dockerDir, "Dockerfile"), imageName), "stdout")

	// Read credentials before contacting Docker; database failures are not anonymous auth.
	registry, err := readImageRegistry(repository.NewSettingsRepo())
	if err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	registryURL, registryUser, registryPass := registry.url, registry.user, registry.password
	cli, err := docker.NewClient(ctx, "")
	if err != nil {
		errMsg := fmt.Sprintf("Docker 不可用: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	var authConfigs map[string]docker.AuthConfig
	if registryURL != "" && registryUser != "" && registryPass != "" {
		authConfigs = map[string]docker.AuthConfig{
			registryURL: {Username: registryUser, Password: registryPass},
		}
	}

	cmdReader, err := cli.BuildImageWithAuth(ctx, dockerDir, authConfigs, imageName)
	if err != nil {
		errMsg := fmt.Sprintf("Docker build 启动失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	readErr := docker.ReadLines(cmdReader, func(line string) {
		pCtx.OnLog(pCtx.PipelineID, 0, line, "stdout")
	})

	closeErr := cmdReader.Close()
	if readErr != nil || closeErr != nil {
		err := fmt.Errorf("镜像构建失败；镜像=%s；日志读取=%v；Docker结果=%v", imageName, readErr, closeErr)
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
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

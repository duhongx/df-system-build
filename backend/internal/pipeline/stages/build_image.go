package stages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"df-build-server/internal/docker"
	"df-build-server/internal/frontend"
	"df-build-server/internal/k8s"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
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
	if pCtx.AppType == "vue" {
		return s.buildWebMainImage(ctx, pCtx, settingsRepo, registryURL)
	}

	// Java applications retain the existing standard flow
	return s.buildStandardImage(ctx, pCtx, settingsRepo, registryURL)
}

// buildStandardImage handles Java applications only.
func (s *BuildImageStage) buildStandardImage(ctx context.Context, pCtx *types.PipelineContext, settingsRepo *repository.SettingsRepo, registryURL string) (*types.StageResult, error) {
	dockerfileCode := "dockerfile-java"

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
	now := time.Now()
	timestamp := now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("20060102150405")
	imageName, err := buildImageName(pCtx, registryURL, pCtx.AppName, now)
	if err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像: %s", imageName), "stdout")

	// Prepare build context
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

	artifactDir, err := sourceArtifactDirectory(pCtx)
	if err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	if err := copyFile(filepath.Join(artifactDir, pCtx.ArtifactName), filepath.Join(dockerDir, pCtx.ArtifactName)); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
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
	target := pCtx.AppName
	if pCtx.VueRole != "standalone" {
		target = "web-main"
	}
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
	pCtx.Pipeline.K8sNamespace = namespace
	log.printf("本流水线更新 %d 个制品；Deployment=%s/%s；角色=%s", len(artifacts), namespace, target, pCtx.VueRole)
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
	artifacts = plan.inputs
	baselineDir := filepath.Join(inputDir, "baseline")
	baseline, err := s.copyFromPod(ctx, pCtx, namespace, baselineDir, target)
	if err != nil {
		return fail(err)
	}
	htmlDir := filepath.Join(dockerDir, "html")
	var required uint64
	if baseline.Exists {
		files, err := frontend.Inventory(baselineDir)
		if err != nil {
			return fail(err)
		}
		for _, file := range files {
			required += uint64(file.Size) * 3
		}
	}
	for _, artifact := range artifacts {
		n, err := frontend.ExpandedSize(filepath.Join(pCtx.Workspace.SourceDir(), artifact.FileName))
		if err != nil {
			return fail(err)
		}
		required += n * 3
	}
	if err := frontend.RequireSpace(pCtx.Workspace.PipelineDir(), required); err != nil {
		return fail(err)
	}
	updated, preserved, err := plan.assemble(baselineDir, htmlDir, !baseline.Exists)
	if err != nil {
		return fail(err)
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "本次替换: "+strings.Join(updated, "、"), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, "保持不变: "+strings.Join(preserved, "、"), "stdout")
	imageName, err := buildImageName(pCtx, registryURL, target, time.Now())
	if err != nil {
		return fail(err)
	}
	baseline.NewImage, baseline.FinalTag = imageName, imageName
	baseline.PipelineID = pCtx.PipelineID
	if err := validateFrontendRegistrations(pCtx, artifacts, plan, baselineDir); err != nil {
		return fail(err)
	}
	// Validate selected entries and every locally referenced script/style against
	// the assembled site, including explicitly declared shared CDN dependencies.
	for _, region := range plan.regions {
		if region.Role != "cdn" {
			if _, err := frontend.ValidateReferences(htmlDir, region.Path); err != nil {
				return fail(fmt.Errorf("%s: %w", region.Path, err))
			}
		}
	}
	baseline.Regions = plan.regions
	if err := service.CreateFrontendRegionRecords(pCtx.Pipeline, plan.regions); err != nil {
		return fail(err)
	}
	baseline.FinalSHA256, err = hashWebTree(htmlDir)
	if err != nil {
		return fail(err)
	}
	dockerfile := webMainDockerfile(registryURL)
	if err := os.WriteFile(filepath.Join(dockerDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
		return fail(err)
	}
	if baseline.Exists {
		prepDir := filepath.Join(pCtx.Workspace.PipelineDir(), "docker-assets")
		if err := os.RemoveAll(prepDir); err != nil {
			return fail(err)
		}
		if err := prepareResourceSite(baselineDir, htmlDir, filepath.Join(prepDir, "html")); err != nil {
			return fail(err)
		}
		baseline.PreparationSHA256, err = hashWebTree(filepath.Join(prepDir, "html"))
		if err != nil {
			return fail(err)
		}
		if baseline.PreparationSHA256 != baseline.ContentSHA256 && baseline.PreparationSHA256 != baseline.FinalSHA256 {
			if err := os.WriteFile(filepath.Join(prepDir, "Dockerfile"), []byte(dockerfile), 0644); err != nil {
				return fail(err)
			}
			baseline.PreparationImage = preparationImageName(imageName)
			baseline.PreparationTag = baseline.PreparationImage
			if _, err := s.execDockerBuild(ctx, pCtx, prepDir, baseline.PreparationImage); err != nil {
				return fail(err)
			}
		}
	}
	if err := saveWebBaseline(pCtx, baseline); err != nil {
		return fail(err)
	}
	return s.execDockerBuild(ctx, pCtx, dockerDir, imageName)
}

func preparationImageName(image string) string {
	at := strings.LastIndex(image, ":")
	tag := image[at+1:]
	if len(tag) > 121 {
		tag = strings.TrimLeft(tag[len(tag)-121:], ".-")
	}
	return image[:at+1] + tag + "-assets"
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
	return extractFrontendZip(zipPath, targetDir, false, log)
}

func extractFrontendZip(zipPath, targetDir string, resourceOnly bool, log webLog) error {
	parent := filepath.Dir(targetDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(parent, ".webzip-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	expanded, err := frontend.ExpandedSize(zipPath)
	if err != nil {
		return err
	}
	if err := frontend.RequireSpace(parent, expanded*3); err != nil {
		return err
	}

	log.printf("开始解压 ZIP；来源=%s；临时目录=%s", zipPath, tmpDir)
	if err := Unzip(zipPath, tmpDir); err != nil {
		return err
	}

	sourceDir := tmpDir
	distDir := filepath.Join(tmpDir, "dist")
	if info, err := os.Stat(distDir); err == nil && info.IsDir() {
		if _, err := os.Lstat(filepath.Join(tmpDir, "index.html")); err == nil {
			return fmt.Errorf("制品同时包含根入口和 dist 入口，无法确定应用范围")
		}
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			return err
		}
		if len(entries) != 1 {
			return fmt.Errorf("dist 包外包含额外内容，制品目录存在歧义")
		}
		sourceDir = distDir
	}

	log.printf("ZIP 解压完成；识别站点根目录=%s；将其内容写入=%s", sourceDir, targetDir)
	if err := requireWebIndex(sourceDir); err != nil && !resourceOnly {
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
func copyFile(src, dst string) (resultErr error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("读取源文件失败: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if err := out.Close(); resultErr == nil {
			resultErr = err
		}
	}()
	_, err = io.Copy(out, in)
	return err
}

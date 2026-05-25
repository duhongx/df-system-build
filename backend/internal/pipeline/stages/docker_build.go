package stages

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"df-build-server/internal/docker"
	"df-build-server/internal/pipeline/types"
	"df-build-server/pkg/logger"
)

type DockerBuildStage struct{}

func (s *DockerBuildStage) Name() string { return "编译" }
func (s *DockerBuildStage) Code() string { return "DOCKER_BUILD" }

func (s *DockerBuildStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	// Determine execution mode based on the BuildConfig's BuildMode field
	execType := "docker"
	if pCtx.BuildConfig != nil && pCtx.BuildConfig.BuildMode == "local" {
		execType = "local"
	}

	if pCtx.BuildConfig != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("编译配置: %s (mode=%s)", pCtx.BuildConfig.Name, pCtx.BuildConfig.BuildMode), "stdout")
	}

	if execType == "local" {
		return s.runLocal(ctx, pCtx)
	}
	return s.runDocker(ctx, pCtx)
}

// runLocal executes the build command directly on the host
func (s *DockerBuildStage) runLocal(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	buildCmd := pCtx.BuildCommand

	if pCtx.AppType == "vue" && pCtx.InstallCommand != "" {
		buildCmd = pCtx.InstallCommand + " && " + pCtx.BuildCommand
	}

	sourceDir := pCtx.Workspace.SourceDir()

	// Verify source directory exists (git clone should have created it)
	if _, err := os.Stat(sourceDir); os.IsNotExist(err) {
		errMsg := fmt.Sprintf("源码目录不存在: %s (请检查代码拉取阶段是否成功)", sourceDir)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "执行模式: 本地 (host)", "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("工作目录: %s", sourceDir), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("执行命令: %s", pCtx.BuildCommand), "stdout")

	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", buildCmd)
	cmd.Dir = sourceDir

	// Add extra env vars from build config
	if pCtx.BuildConfig != nil && pCtx.BuildConfig.EnvVars != "" {
		cmd.Env = append(cmd.Environ(), parseEnvVars(pCtx.BuildConfig.EnvVars)...)
	}

	// Pipe stdout and stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		errMsg := fmt.Sprintf("创建输出管道失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		errMsg := fmt.Sprintf("启动命令失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	// Stream output
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		pCtx.OnLog(pCtx.PipelineID, 0, scanner.Text(), "stdout")
	}

	err = cmd.Wait()

	if ctx.Err() != nil {
		errMsg := "构建超时或已取消"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, ctx.Err()
	}

	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		errMsg := fmt.Sprintf("编译失败 (exit code: %d)", exitCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: exitCode, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	logger.Log.Info("Local build completed successfully")
	pCtx.OnLog(pCtx.PipelineID, 0, "编译完成 ✓", "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

// runDocker executes the build command inside a Docker container (existing logic)
func (s *DockerBuildStage) runDocker(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	image := "ops-builder-gradle:jdk8"
	buildCmd := pCtx.BuildCommand

	if pCtx.BuildConfig != nil {
		image = pCtx.BuildConfig.DockerImage
	}

	if pCtx.AppType == "vue" && pCtx.InstallCommand != "" {
		buildCmd = pCtx.InstallCommand + " && " + pCtx.BuildCommand
	}

	// Append md5sum to verify artifact integrity
	artifactPath := ""
	if pCtx.AppType == "vue" {
		artifactPath = "/workspace/" + pCtx.ArtifactName
	} else {
		artifactPath = "/workspace/build/libs/" + pCtx.ArtifactName
	}
	buildCmd = buildCmd + fmt.Sprintf(" && echo '---MD5---' && md5sum %s", artifactPath)

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("执行模式: Docker 容器"), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("使用镜像: %s", image), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("执行命令: %s", pCtx.BuildCommand), "stdout")

	sourceDir := pCtx.Workspace.SourceDir()

	var cacheMounts []docker.CacheMount
	if pCtx.BuildConfig != nil && pCtx.BuildConfig.CacheMounts != "" {
		cacheMounts = docker.ParseCacheMounts(pCtx.BuildConfig.CacheMounts)
	}

	cli, err := docker.NewClient("")
	if err != nil {
		errMsg := fmt.Sprintf("Docker 不可用: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	// Pull image (non-fatal)
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("检查镜像: %s", image), "stdout")
	if err := cli.PullImage(ctx, image); err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("镜像拉取跳过: %v", err), "stdout")
	}

	// Run container
	pCtx.OnLog(pCtx.PipelineID, 0, "启动构建容器...", "stdout")

	opts := docker.ContainerOpts{
		Image:       image,
		Command:     []string{"sh", "-c", buildCmd},
		WorkDir:     "/workspace",
		SourceDir:   sourceDir,
		CacheMounts: cacheMounts,
		CPULimit:    "2",
		MemLimit:    "4g",
	}

	if pCtx.BuildConfig != nil {
		if pCtx.BuildConfig.CPULimit != "" {
			opts.CPULimit = pCtx.BuildConfig.CPULimit
		}
		if pCtx.BuildConfig.MemoryLimit != "" {
			opts.MemLimit = pCtx.BuildConfig.MemoryLimit
		}
	}

	cmdReader, err := cli.RunContainer(ctx, opts)
	if err != nil {
		errMsg := fmt.Sprintf("容器启动失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	// Stream output and capture md5 from output after ---MD5--- marker
	var captureMD5 bool
	docker.ReadLines(cmdReader, func(line string) {
		if strings.TrimSpace(line) == "---MD5---" {
			captureMD5 = true
			return
		}
		if captureMD5 {
			// Parse md5sum output: "hash  filename"
			parts := strings.Fields(strings.TrimSpace(line))
			if len(parts) >= 1 && len(parts[0]) == 32 {
				pCtx.ArtifactMD5 = parts[0]
				pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("制品 MD5: %s", pCtx.ArtifactMD5), "stdout")
			}
			captureMD5 = false
			return
		}
		pCtx.OnLog(pCtx.PipelineID, 0, line, "stdout")
	})

	// Wait for exit
	cmdReader.Close()
	exitCode := cmdReader.ExitCode

	if ctx.Err() != nil {
		errMsg := "构建超时或已取消"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, ctx.Err()
	}

	if exitCode != 0 {
		errMsg := fmt.Sprintf("编译失败 (exit code: %d)", exitCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: exitCode, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	logger.Log.Info("Docker build completed successfully")
	pCtx.OnLog(pCtx.PipelineID, 0, "编译完成 ✓", "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

// parseEnvVars parses "KEY=VALUE\nKEY2=VALUE2" format into a slice
func parseEnvVars(envStr string) []string {
	var envs []string
	for _, line := range strings.Split(envStr, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.Contains(line, "=") {
			envs = append(envs, line)
		}
	}
	return envs
}

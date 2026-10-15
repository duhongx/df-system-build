package stages

import (
	"bufio"
	"context"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type LocalBuildStage struct{}

func (s *LocalBuildStage) Name() string { return "本机编译" }
func (s *LocalBuildStage) Code() string { return "LOCAL_BUILD" }

func (s *LocalBuildStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	if err := repository.RequireSourceBuild(); err != nil {
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	if pCtx.AppType == "vue" && pCtx.VueRole == "cdn" {
		pCtx.OnLog(pCtx.PipelineID, 0, "公共资源任务使用仓库 cdn 目录，跳过 Vue 编译", "stdout")
		return &types.StageResult{}, nil
	}
	if pCtx.BuildConfig == nil || pCtx.BuildConfig.Status != "online" {
		err := fmt.Errorf("编译配置不可用，请配置本机编译工具和命令后启用")
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	pCtx.OnLog(pCtx.PipelineID, 0, "编译配置: "+pCtx.BuildConfig.Name, "stdout")
	return s.runLocal(ctx, pCtx)
}

func (s *LocalBuildStage) runLocal(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
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

	pCtx.OnLog(pCtx.PipelineID, 0, "执行模式: 本机编译", "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("工作目录: %s", sourceDir), "stdout")
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("执行命令: %s", pCtx.BuildCommand), "stdout")

	cmd := exec.CommandContext(ctx, "bash", "-c", buildCmd)
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

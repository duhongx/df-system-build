package stages

import (
	"context"
	"fmt"

	"df-build-server/internal/docker"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
)

type PushImageStage struct{}

func (s *PushImageStage) Name() string { return "推送镜像" }
func (s *PushImageStage) Code() string { return "PUSH_IMAGE" }

func (s *PushImageStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	if pCtx.ImageName == "" {
		errMsg := "镜像名称为空，BUILD_IMAGE 阶段可能未成功"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	registry, err := readImageRegistry(repository.NewSettingsRepo())
	if err != nil {
		pCtx.OnLog(pCtx.PipelineID, 0, err.Error(), "stderr")
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}
	registryURL, registryUser, registryPass := registry.url, registry.user, registry.password

	cli, err := docker.NewClient(ctx, "")
	if err != nil {
		errMsg := fmt.Sprintf("Docker 不可用: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	// Docker login to registry
	authStr := "e30=" // empty auth fallback
	if registryUser != "" && registryPass != "" {
		authStr = docker.GetAuthStr(registryUser, registryPass)
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("使用凭据推送到: %s", registryURL), "stdout")
	}

	// Push the timestamped image
	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("推送镜像: %s", pCtx.ImageName), "stdout")
	cmdReader, err := cli.PushImageWithAuth(ctx, pCtx.ImageName, authStr)
	if err != nil {
		errMsg := fmt.Sprintf("推送镜像启动失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	readErr := docker.ReadLines(cmdReader, func(line string) {
		pCtx.OnLog(pCtx.PipelineID, 0, line, "stdout")
	})
	closeErr := cmdReader.Close()
	if readErr != nil || closeErr != nil || ctx.Err() != nil {
		err := fmt.Errorf("推送镜像失败；镜像=%s；日志读取=%v；Docker结果=%v；上下文=%v", pCtx.ImageName, readErr, closeErr, ctx.Err())
		return &types.StageResult{ExitCode: 1, Error: err.Error()}, err
	}

	if cmdReader.ExitCode != 0 {
		errMsg := fmt.Sprintf("推送镜像失败 (exit code: %d)", cmdReader.ExitCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: cmdReader.ExitCode, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("目标镜像推送流处理完成；镜像=%s；退出码=%d", pCtx.ImageName, cmdReader.ExitCode), "stdout")

	pCtx.OnLog(pCtx.PipelineID, 0, "镜像推送完成 ✓", "stdout")
	logger.Log.Infof("Docker image pushed: %s", pCtx.ImageName)

	return &types.StageResult{ExitCode: 0}, nil
}

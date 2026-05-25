package stages

import (
	"context"
	"fmt"
	"strings"

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

	// Get registry credentials
	settingsRepo := repository.NewSettingsRepo()
	registryURL, _ := settingsRepo.GetByKey("docker_registry_url")
	registryUser, _ := settingsRepo.GetByKey("docker_registry_user")
	registryPass, _ := settingsRepo.GetByKey("docker_registry_password")

	if registryURL == "" {
		errMsg := "Docker 镜像仓库地址未配置"
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	cli, err := docker.NewClient("")
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

	docker.ReadLines(cmdReader, func(line string) {
		pCtx.OnLog(pCtx.PipelineID, 0, line, "stdout")
	})
	cmdReader.Close()

	if cmdReader.ExitCode != 0 {
		errMsg := fmt.Sprintf("推送镜像失败 (exit code: %d)", cmdReader.ExitCode)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: cmdReader.ExitCode, Error: errMsg}, fmt.Errorf("%s", errMsg)
	}

	// Also push the latest tag
	// imageName format: registry/appName:branch-timestamp → registry/appName:branch-latest
	parts := strings.SplitN(pCtx.ImageName, ":", 2)
	latestTag := pCtx.ImageName // fallback
	if len(parts) == 2 {
		latestTag = parts[0] + ":" + pCtx.GitBranch + "-latest"
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("推送 latest 标签: %s", latestTag), "stdout")
	cmdReader2, err := cli.PushImageWithAuth(ctx, latestTag, authStr)
	if err != nil {
		// Non-fatal: latest tag push failure doesn't fail the stage
		pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("latest 标签推送失败(非致命): %v", err), "stderr")
	} else {
		docker.ReadLines(cmdReader2, func(line string) {
			pCtx.OnLog(pCtx.PipelineID, 0, line, "stdout")
		})
		cmdReader2.Close()
	}

	pCtx.OnLog(pCtx.PipelineID, 0, "镜像推送完成 ✓", "stdout")
	logger.Log.Infof("Docker image pushed: %s", pCtx.ImageName)

	return &types.StageResult{ExitCode: 0}, nil
}

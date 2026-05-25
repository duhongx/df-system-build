package stages

import (
	"context"
	"fmt"

	"df-build-server/internal/pipeline/types"
)

type ZipPackageStage struct{}

func (s *ZipPackageStage) Name() string { return "打包制品" }
func (s *ZipPackageStage) Code() string { return "ZIP_PACKAGE" }

func (s *ZipPackageStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	sourceDir := pCtx.Workspace.SourceDir()
	zipName := pCtx.ArtifactName
	zipPath := fmt.Sprintf("%s/%s", sourceDir, zipName)

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("打包 dist → %s", zipName), "stdout")

	if err := ZipDirectory(sourceDir, "dist", zipPath); err != nil {
		errMsg := fmt.Sprintf("打包失败: %v", err)
		pCtx.OnLog(pCtx.PipelineID, 0, errMsg, "stderr")
		return &types.StageResult{ExitCode: 1, Error: errMsg}, err
	}

	pCtx.OnLog(pCtx.PipelineID, 0, fmt.Sprintf("打包完成: %s", zipName), "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

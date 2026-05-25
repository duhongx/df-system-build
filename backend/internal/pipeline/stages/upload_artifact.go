package stages

import (
	"context"

	"df-build-server/internal/pipeline/types"
)

type UploadArtifactStage struct{}

func (s *UploadArtifactStage) Name() string { return "上传制品" }
func (s *UploadArtifactStage) Code() string { return "UPLOAD_ARTIFACT" }

func (s *UploadArtifactStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	// This stage is only used in upload_only mode (master branch)
	// DevOps branch does not upload to remote servers
	pCtx.OnLog(pCtx.PipelineID, 0, "跳过 (DevOps 模式不需要制品上传)", "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

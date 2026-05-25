package stages

import (
	"context"

	"df-build-server/internal/pipeline/types"
)

type CheckRemoteDirStage struct{}

func (s *CheckRemoteDirStage) Name() string { return "检查远端目录" }
func (s *CheckRemoteDirStage) Code() string { return "CHECK_REMOTE_DIR" }

func (s *CheckRemoteDirStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	// This stage is only used in upload_only mode (master branch)
	// DevOps branch does not upload to remote servers
	pCtx.OnLog(pCtx.PipelineID, 0, "跳过 (DevOps 模式不需要远端目录检查)", "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

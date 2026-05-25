package stages

import (
	"context"

	"df-build-server/internal/pipeline/types"
)

type NotifyStage struct{}

func (s *NotifyStage) Name() string { return "发送通知" }
func (s *NotifyStage) Code() string { return "NOTIFY" }

func (s *NotifyStage) Run(ctx context.Context, pCtx *types.PipelineContext) (*types.StageResult, error) {
	// Notification is sent by the scheduler after pipeline completes (with final status).
	// This stage only serves as a visual indicator in the pipeline UI.
	// Actual sending happens in scheduler.executePipeline() to avoid duplicate notifications
	// and to ensure the correct final status (SUCCESS/FAILED) is used.
	pCtx.OnLog(pCtx.PipelineID, 0, "通知将在流水线结束后发送", "stdout")
	return &types.StageResult{ExitCode: 0}, nil
}

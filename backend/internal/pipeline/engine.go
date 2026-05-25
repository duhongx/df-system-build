package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
	"df-build-server/pkg/sse"
)

// ErrImageReady is returned when pipeline pauses after image push (manual mode)
var ErrImageReady = errors.New("IMAGE_READY")

type Engine struct {
	stageRepo *repository.StageRepo
	logRepo   *repository.StageLogRepo
	sseHub    *sse.Hub
}

func NewEngine() *Engine {
	return &Engine{
		stageRepo: repository.NewStageRepo(),
		logRepo:   repository.NewStageLogRepo(),
		sseHub:    sse.DefaultHub,
	}
}

func (e *Engine) Execute(ctx context.Context, p *model.Pipeline, pCtx *types.PipelineContext) error {
	stageDefs := ResolveStagesWithMode(p.AppType, p.DeployMode)
	if stageDefs == nil {
		return fmt.Errorf("unsupported app type: %s", p.AppType)
	}

	// Check if resuming from IMAGE_READY (stages already exist)
	existingStages, _ := e.stageRepo.FindByPipelineID(p.ID)
	var stageRecords []*model.PipelineStage

	if len(existingStages) > 0 && p.Status == "DEPLOYING" {
		// Resuming: use existing stage records
		stageRecords = make([]*model.PipelineStage, len(existingStages))
		for i := range existingStages {
			stageRecords[i] = &existingStages[i]
		}
	} else {
		// Fresh start: create stage records
		stageRecords = make([]*model.PipelineStage, len(stageDefs))
		for i, def := range stageDefs {
			stage := &model.PipelineStage{
				PipelineID: p.ID,
				StageCode:  def.Code,
				StageName:  def.Name,
				StageOrder: i + 1,
				Status:     "PENDING",
			}
			e.stageRepo.Create(stage)
			stageRecords[i] = stage
		}
	}

	// Execute stages sequentially
	for i, def := range stageDefs {
		stage := stageRecords[i]

		// Skip already completed stages (when resuming)
		if stage.Status == "SUCCESS" {
			continue
		}

		select {
		case <-ctx.Done():
			e.markStageStatus(stage, "FAILED", "构建超时或已取消")
			e.skipRemaining(stageRecords, i+1)
			return fmt.Errorf("build timeout or canceled")
		default:
		}

		// Mark running
		now := time.Now()
		stage.StartTime = &now
		stage.Status = "RUNNING"
		e.stageRepo.Update(stage)

		e.sseHub.Publish(p.ID, sse.Event{Type: "stage_start", Data: fmt.Sprintf(`{"stageId":%d,"code":"%s"}`, stage.ID, stage.StageCode)})

		// Set log callback with current stage ID
		currentStageID := stage.ID
		pCtx.OnLog = func(pipelineID, _ uint, line string, stream string) {
			e.logRepo.AppendLog(pipelineID, currentStageID, line, stream)
			e.sseHub.Publish(pipelineID, sse.Event{Type: "log", Data: line})
		}

		// Run
		result, err := def.Runner.Run(ctx, pCtx)

		// Record completion
		endTime := time.Now()
		stage.EndTime = &endTime
		dur := int(endTime.Sub(now).Seconds())
		stage.DurationSeconds = &dur

		if err != nil || (result != nil && result.ExitCode != 0) {
			errMsg := ""
			if err != nil {
				errMsg = err.Error()
			} else if result != nil {
				errMsg = result.Error
			}
			e.markStageStatus(stage, "FAILED", errMsg)
			e.skipRemaining(stageRecords, i+1)
			e.sseHub.Publish(p.ID, sse.Event{Type: "stage_failed", Data: errMsg})
			return fmt.Errorf("stage %s failed: %s", def.Code, errMsg)
		}

		exitCode := 0
		stage.ExitCode = &exitCode
		stage.Status = "SUCCESS"
		e.stageRepo.Update(stage)
		e.sseHub.Publish(p.ID, sse.Event{Type: "stage_success", Data: fmt.Sprintf(`{"stageId":%d}`, stage.ID)})
		logger.Log.Infof("Stage %s completed in %ds", def.Code, dur)

		// Pause point: after PUSH_IMAGE in manual/deploy_with_approval mode, pause for user confirmation
		if (p.DeployMode == "manual" || p.DeployMode == "deploy_with_approval") && def.Code == "PUSH_IMAGE" {
			// Mark remaining stages as PENDING (they'll be executed later)
			e.sseHub.Publish(p.ID, sse.Event{Type: "image_ready", Data: "镜像已就绪，等待确认部署"})
			return ErrImageReady
		}
	}

	return nil
}

func (e *Engine) markStageStatus(stage *model.PipelineStage, status, errMsg string) {
	stage.Status = status
	stage.ErrorMessage = errMsg
	exitCode := 1
	stage.ExitCode = &exitCode
	e.stageRepo.Update(stage)
}

func (e *Engine) skipRemaining(stages []*model.PipelineStage, fromIndex int) {
	for i := fromIndex; i < len(stages); i++ {
		stages[i].Status = "SKIPPED"
		e.stageRepo.Update(stages[i])
	}
}

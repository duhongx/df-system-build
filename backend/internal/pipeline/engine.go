package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/k8s"
	"df-build-server/internal/model"
	"df-build-server/internal/pipeline/types"
	"df-build-server/internal/repository"
	"df-build-server/internal/service"
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

	// Keep the existing shared web-main target serial through build, push,
	// deployment and verification. Manual approval releases the lock; its persisted
	// baseline is checked again at deployment time.
	if pCtx.AppType == "vue" && (pCtx.VueRole == "main" || pCtx.VueRole == "sub") {
		if pCtx.K8sNamespace == "" {
			var err error
			pCtx.K8sNamespace, err = k8s.GetDefaultNamespace()
			if err != nil {
				return err
			}
		}
		unlock, err := k8s.LockDeployment(ctx, pCtx.K8sNamespace, "web-main")
		if err != nil {
			return fmt.Errorf("等待 web-main 更新完成: %w", err)
		}
		defer unlock()
	}

	// Check if resuming from IMAGE_READY (stages already exist)
	existingStages, err := e.stageRepo.FindByPipelineID(p.ID)
	if err != nil {
		return err
	}
	resume := pCtx.ResumeDeployment || p.Status == "DEPLOYING"
	if resume {
		if err := validateResumeStages(existingStages, stageDefs, p.ImageName); err != nil {
			return err
		}
	} else if len(existingStages) > 0 {
		return fmt.Errorf("流水线已有执行阶段，禁止重复构建；请创建新任务")
	}
	var stageRecords []*model.PipelineStage

	if resume {
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
			if err := e.stageRepo.Create(stage); err != nil {
				return err
			}
			stageRecords[i] = stage
		}
	}

	loggedScope := false
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
		if err := e.stageRepo.Update(stage); err != nil {
			return err
		}

		e.sseHub.Publish(p.ID, sse.Event{Type: "stage_start", Data: fmt.Sprintf(`{"stageId":%d,"code":"%s"}`, stage.ID, stage.StageCode)})

		// Set log callback with current stage ID
		currentStageID := stage.ID
		pCtx.OnLog = func(pipelineID, _ uint, line string, stream string) {
			entry, logErr := e.logRepo.AppendLog(pipelineID, currentStageID, line, stream)
			if logErr != nil {
				logger.Log.Errorf("保存流水线 %d 阶段 %d 日志失败: %v", pipelineID, currentStageID, logErr)
			}
			if payload, err := json.Marshal(entry); err == nil {
				e.sseHub.Publish(pipelineID, sse.Event{Type: "stage_log", Data: string(payload)})
			}
			e.sseHub.Publish(pipelineID, sse.Event{Type: "log", Data: line})
		}

		if !loggedScope {
			pCtx.OnLog(p.ID, 0, fmt.Sprintf("[更新任务] 流水线=%s；应用=%s；角色=%s；appCode=%s；命名空间=%s；模式=%s；恢复部署=%t", p.PipelineNo, pCtx.AppName, pCtx.VueRole, pCtx.AppCode, pCtx.K8sNamespace, pCtx.DeployMode, resume), "stdout")
			if p.FrontendUpdateSummary != "" {
				pCtx.OnLog(p.ID, 0, "[批次选择] 以下是提交时选择的前端应用；各独立应用由各自流水线执行，执行结果分别查看", "stdout")
				for _, line := range strings.Split(p.FrontendUpdateSummary, "\n") {
					pCtx.OnLog(p.ID, 0, "[批次选择] "+line, "stdout")
				}
			}
			loggedScope = true
		}
		pCtx.OnLog(p.ID, 0, fmt.Sprintf("[阶段开始] %s (%s)", def.Name, def.Code), "stdout")

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
			pCtx.OnLog(p.ID, 0, fmt.Sprintf("[阶段失败] %s (%s)；耗时=%ds；原因=%s；停止后续步骤", def.Name, def.Code, dur, errMsg), "stderr")
			if def.Code == "K8S_DEPLOY" {
				pCtx.OnLog(p.ID, 0, "[线上状态] 部署阶段失败，已提交的 Kubernetes 操作可能仍在执行；本错误不代表线上未改变，请结合提交日志和集群状态确认", "stderr")
			}
			e.markStageStatus(stage, "FAILED", errMsg)
			e.skipRemaining(stageRecords, i+1)
			e.sseHub.Publish(p.ID, sse.Event{Type: "stage_failed", Data: errMsg})
			return fmt.Errorf("stage %s failed: %s", def.Code, errMsg)
		}

		exitCode := 0
		stage.ExitCode = &exitCode
		stage.Status = "SUCCESS"
		if pCtx.ImageName != "" && (def.Code == "BUILD_IMAGE" || def.Code == "PUSH_IMAGE") {
			p.ImageName = pCtx.ImageName
			if err := repository.NewPipelineRepo().SaveImageCheckpoint(p); err != nil {
				e.markStageStatus(stage, "FAILED", "保存镜像构建结果失败: "+err.Error())
				e.skipRemaining(stageRecords, i+1)
				return err
			}
		}
		if def.Code == "PUSH_IMAGE" {
			if err := service.MarkPipelineImageBuilt(p.ID, pCtx.ImageName); err != nil {
				msg := "保存已推送镜像记录失败，停止部署: " + err.Error()
				pCtx.OnLog(p.ID, 0, msg, "stderr")
				e.markStageStatus(stage, "FAILED", msg)
				e.skipRemaining(stageRecords, i+1)
				return fmt.Errorf("%s", msg)
			}
		}
		if err := e.stageRepo.Update(stage); err != nil {
			return err
		}
		pCtx.OnLog(p.ID, 0, fmt.Sprintf("[阶段完成] %s (%s)；耗时=%ds", def.Name, def.Code, dur), "stdout")
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

// Confirmation must resume the exact completed build, not create or guess stages.
func validateResumeStages(existing []model.PipelineStage, defs []StageDefinition, image string) error {
	if image == "" || len(existing) != len(defs) {
		return fmt.Errorf("待部署流水线缺少完整阶段或镜像，请重新构建")
	}
	beforeDeploy := true
	for i, def := range defs {
		stage := existing[i]
		if stage.StageCode != def.Code {
			return fmt.Errorf("待部署阶段顺序不一致，请重新构建")
		}
		if def.Code == "K8S_DEPLOY" {
			beforeDeploy = false
		}
		if beforeDeploy && stage.Status != "SUCCESS" {
			return fmt.Errorf("构建和推送尚未完成，不能确认部署")
		}
		if !beforeDeploy && stage.Status != "PENDING" {
			return fmt.Errorf("部署阶段已执行，禁止重复确认")
		}
	}
	return nil
}

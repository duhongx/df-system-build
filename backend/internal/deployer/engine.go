package deployer

import (
	"context"
	"fmt"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
)

// Engine orchestrates the deployment of all components
type Engine struct {
	planRepo *repository.DeployPlanRepo
	logRepo  *repository.DeployLogRepo
}

func NewEngine() *Engine {
	return &Engine{
		planRepo: repository.NewDeployPlanRepo(),
		logRepo:  repository.NewDeployLogRepo(),
	}
}

// Execute runs the full deployment based on a plan
func (e *Engine) Execute(ctx context.Context, executionID uint, plan *model.DeployPlan, packageDir string, onLog func(component, line string)) error {
	components := GetComponents()

	for _, comp := range components {
		// Check if this component is in the plan
		assignment := plan.GetAssignment(comp.Code())
		if assignment == nil {
			continue // Not in plan, skip
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Build deploy opts
		opts := &DeployOpts{
			Hosts:      assignment.Hosts,
			Params:     assignment.Params,
			PackageDir: packageDir,
			OnLog: func(line string) {
				if onLog != nil {
					onLog(comp.Code(), line)
				}
				e.logRepo.AppendLog(executionID, comp.Code(), line)
			},
		}

		// Execute deploy
		opts.OnLog(fmt.Sprintf("========== 开始部署: %s ==========", comp.Name()))
		startTime := time.Now()

		err := comp.Deploy(ctx, opts)
		duration := int(time.Since(startTime).Seconds())

		if err != nil {
			opts.OnLog(fmt.Sprintf("❌ 部署失败 (%ds): %v", duration, err))
			e.planRepo.UpdateComponentStatus(executionID, comp.Code(), "FAILED", err.Error())
			return fmt.Errorf("组件 %s 部署失败: %w", comp.Name(), err)
		}

		// Verify
		opts.OnLog("验证中...")
		if err := comp.Verify(ctx, opts); err != nil {
			opts.OnLog(fmt.Sprintf("❌ 验证失败: %v", err))
			e.planRepo.UpdateComponentStatus(executionID, comp.Code(), "VERIFY_FAILED", err.Error())
			return fmt.Errorf("组件 %s 验证失败: %w", comp.Name(), err)
		}

		opts.OnLog(fmt.Sprintf("✅ 部署完成 (%ds)", duration))
		e.planRepo.UpdateComponentStatus(executionID, comp.Code(), "SUCCESS", "")
		logger.Log.Infof("Component %s deployed successfully in %ds", comp.Code(), duration)
	}

	return nil
}

// CleanupComponent runs the cleanup logic for a single component
func (e *Engine) CleanupComponent(ctx context.Context, plan *model.DeployPlan, componentCode, packageDir string, onLog func(line string)) error {
	comp := GetComponent(componentCode)
	if comp == nil {
		return fmt.Errorf("组件 %s 不存在", componentCode)
	}

	assignment := plan.GetAssignment(componentCode)
	if assignment == nil {
		return fmt.Errorf("组件 %s 不在部署方案中", componentCode)
	}

	opts := &DeployOpts{
		Hosts:      assignment.Hosts,
		Params:     assignment.Params,
		PackageDir: packageDir,
		OnLog:      onLog,
	}

	onLog(fmt.Sprintf("开始清理: %s", comp.Name()))
	if err := comp.Cleanup(ctx, opts); err != nil {
		onLog(fmt.Sprintf("清理失败: %v", err))
		return err
	}
	onLog("清理完成 ✓")
	return nil
}

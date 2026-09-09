package scheduler

import (
	"fmt"

	"df-build-server/internal/model"
	"df-build-server/internal/pipeline"
	"df-build-server/internal/repository"
)

// StartRollout submits step two using the persisted image and completed stages.
func (s *BuildScheduler) StartRollout(id uint) (*model.Pipeline, error) {
	repo := repository.NewPipelineRepo()
	p, err := repo.FindByID(id)
	if err != nil {
		return nil, fmt.Errorf("读取更新任务失败: %w", err)
	}
	if p.Status != "IMAGE_READY" {
		return nil, fmt.Errorf("任务 %s 的镜像尚未制作完成，或已提交滚动更新（当前状态：%s）", p.PipelineNo, p.Status)
	}
	stages, err := repository.NewStageRepo().FindByPipelineID(id)
	if err != nil {
		return nil, fmt.Errorf("读取镜像制作结果失败: %w", err)
	}
	if err := pipeline.ValidateRolloutStages(stages, pipeline.ResolveStagesWithMode(p.AppType, p.DeployMode), p.ImageName); err != nil {
		return nil, err
	}
	started, err := repo.StartRollout(id)
	if err != nil {
		return nil, fmt.Errorf("保存滚动更新状态失败: %w", err)
	}
	if !started {
		return nil, fmt.Errorf("任务 %s 已提交滚动更新或状态已变化，请刷新", p.PipelineNo)
	}
	s.EnqueueDeployment(id)
	p.Status = "DEPLOYING"
	return p, nil
}

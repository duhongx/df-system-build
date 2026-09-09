package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The persisted batch number identifies the background rollback task. An HTTP
// disconnect never owns its lifetime; duplicate submissions return the same task.
func StartRollbackDeployBatch(batchNo string, rollbacker DeploymentRollbacker, reader RuntimeVersionReader) (*model.ArtifactDeployBatch, error) {
	if repository.DB == nil || rollbacker == nil {
		return nil, fmt.Errorf("回滚服务未初始化")
	}
	batchNo = strings.TrimSpace(batchNo)
	if batchNo == "" {
		return nil, fmt.Errorf("部署批次不能为空")
	}
	var batch model.ArtifactDeployBatch
	var count int64
	start := false
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("deploy_batch_no = ?", batchNo).First(&batch).Error; err != nil {
			return fmt.Errorf("读取回滚批次失败: %w", err)
		}
		if batch.Status == "rolling_back" || batch.Status == "rolled_back" {
			return nil
		}
		if err := tx.Model(&model.ArtifactDeployRecord{}).Where("deploy_batch_no = ?", batchNo).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("部署批次没有部署记录")
		}
		if err := tx.Model(&batch).Updates(map[string]any{"status": "rolling_back", "error_message": "", "finished_at": nil, "rolled_back_count": 0}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.ArtifactDeployRecord{}).Where("deploy_batch_no = ?", batchNo).Updates(map[string]any{"rollback_status": "pending", "rolled_back_at": nil}).Error; err != nil {
			return err
		}
		start = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if start {
		go func() {
			// Each target has its own five-minute budget below; allow bounded
			// version metadata collection and result persistence for every record.
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(count)*6*time.Minute+time.Minute)
			defer cancel()
			if err := RollbackDeployBatch(ctx, batchNo, rollbacker, reader); err != nil {
				msg := "后台回滚未全部完成: " + err.Error()
				if saveErr := failRollbackJob(batchNo, msg); saveErr != nil {
					msg += "；保存回滚异常失败: " + saveErr.Error()
				}
				if logger.Log != nil {
					logger.Log.Error(msg)
				}
			}
		}()
	}
	return &batch, nil
}

func failRollbackJob(batchNo, message string) error {
	var unfinished []model.ArtifactDeployRecord
	if err := repository.DB.Where("deploy_batch_no = ? AND rollback_status IN ?", batchNo, []string{"pending", "running"}).Find(&unfinished).Error; err != nil {
		return err
	}
	now := time.Now()
	if err := markRollbackGroupFailed(unfinished, message, now); err != nil {
		return err
	}
	if err := updateDeployBatchCountersByRecords([]model.ArtifactDeployRecord{{DeployBatchNo: batchNo}}); err != nil {
		return err
	}
	return repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", batchNo).Updates(map[string]any{"status": "rollback_failed", "error_message": message, "finished_at": &now}).Error
}

// Single backend instance: a process restart cannot resume an unobserved cluster
// operation. Preserve completed groups and report interruption for the remainder.
func RecoverInterruptedRollbacks() error {
	var batches []model.ArtifactDeployBatch
	if err := repository.DB.Where("status = ?", "rolling_back").Find(&batches).Error; err != nil {
		return err
	}
	for _, batch := range batches {
		if err := failRollbackJob(batch.DeployBatchNo, "服务重启中断回滚；已提交的 Kubernetes 操作可能仍在执行，请核对各目标镜像和滚动状态后处理"); err != nil {
			return err
		}
	}
	return nil
}

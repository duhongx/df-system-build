package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"fmt"
	"gorm.io/gorm"
	"log"
)

// Claim before returning to the browser. A disconnect must not cancel a submitted DDL plan.
func (s *PostgreSQLService) StartSQLViewDependencyTask(ctx context.Context, id uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	task, items, err := s.prepareSQLViewDependencyExecution(ctx, id, username)
	if err != nil {
		return nil, nil, err
	}
	workerTask := *task
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("视图任务 %d 异常中断: %v", id, recovered)
				settleViewTaskPhase(id, "EXECUTING", username)
			}
		}()
		if _, _, err := s.executeClaimedSQLViewDependencyTask(context.WithoutCancel(ctx), &workerTask, items, username); err != nil {
			log.Printf("视图任务 %d 执行结束: %v", id, err)
		}
	}()
	return task, items, nil
}

// The original per-column SQL is retained; each row links to the actual database statement.
func bindSQLViewChangesToSteps(tx *gorm.DB, taskID uint, steps []model.SQLViewDependencyStep) error {
	var changes []model.SQLViewDependencyChange
	if err := tx.Where("task_id = ?", taskID).Order("change_order ASC, id ASC").Find(&changes).Error; err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	} // Low-level recovery plans may have no column changes.
	index := 0
	for _, step := range steps {
		if step.StepName != "ALTER_TABLE" {
			continue
		}
		refs, err := parseViewAlterColumnRefs(step.SQLContent, true)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if index >= len(changes) {
				return fmt.Errorf("变更步骤与原始字段记录数量不一致")
			}
			change := changes[index]
			if change.SchemaName != ref.schema || change.TableName != ref.table || change.ColumnName != ref.column {
				return fmt.Errorf("变更步骤与原始字段顺序不一致")
			}
			result := tx.Model(&model.SQLViewDependencyChange{}).Where("id = ?", change.ID).Updates(map[string]any{"execution_step_id": step.ID, "status": "PENDING", "error_message": ""})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("原始字段记录不存在")
			}
			index++
		}
	}
	if index > 0 && index != len(changes) {
		return fmt.Errorf("变更步骤缺少原始字段")
	}
	return nil
}

func updateSQLViewStepAndChanges(id uint, fields map[string]any, status, message string) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		var step model.SQLViewDependencyStep
		if err := tx.First(&step, id).Error; err != nil {
			return err
		}
		result := tx.Model(&model.SQLViewDependencyStep{}).Where("id = ?", id).Updates(fields)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("视图执行步骤记录不存在")
		}
		if step.StepName != "ALTER_TABLE" {
			return nil
		}
		if status == "SUCCESS" {
			var task model.SQLViewDependencyTask
			if err := tx.Where("id = ?", step.TaskID).Limit(1).Find(&task).Error; err != nil {
				return err
			}
			if task.ExecutionMode == SQLViewDependencyExecutionModeTransaction {
				status = "APPLIED"
			}
		}
		return tx.Model(&model.SQLViewDependencyChange{}).Where("execution_step_id = ?", id).Updates(map[string]any{"status": status, "error_message": message}).Error
	})
}

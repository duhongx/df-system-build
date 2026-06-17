package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type DeploymentRollbacker interface {
	RollbackDeployment(ctx context.Context, namespace, deploymentName, image string) error
}

func RollbackDeployBatch(ctx context.Context, deployBatchNo string, rollbacker DeploymentRollbacker, reader RuntimeVersionReader) error {
	if repository.DB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	deployBatchNo = strings.TrimSpace(deployBatchNo)
	if deployBatchNo == "" {
		return fmt.Errorf("部署批次不能为空")
	}
	if rollbacker == nil {
		return fmt.Errorf("回滚执行器不能为空")
	}

	var records []model.ArtifactDeployRecord
	if err := repository.DB.Where("deploy_batch_no = ?", deployBatchNo).Order("deployment_name ASC, app_name ASC").Find(&records).Error; err != nil {
		return err
	}
	if len(records) == 0 {
		return fmt.Errorf("部署批次不存在或没有部署记录")
	}

	groups := groupRollbackRecords(records)
	now := time.Now()
	failures := make([]string, 0)
	for _, group := range groups {
		if strings.TrimSpace(group.image) == "" {
			failures = append(failures, group.key+": 缺少更新前镜像")
			markRollbackGroupFailed(group.records, "缺少更新前镜像", now)
			continue
		}
		if err := rollbacker.RollbackDeployment(ctx, group.namespace, group.deploymentName, group.image); err != nil {
			msg := "回滚 Deployment 镜像失败: " + err.Error()
			failures = append(failures, group.key+": "+msg)
			markRollbackGroupFailed(group.records, msg, now)
			continue
		}
		for _, record := range group.records {
			updates := map[string]any{
				"rollback_status": "success",
				"status":          "rolled_back",
				"rolled_back_at":  &now,
			}
			if reader != nil {
				restored, err := reader.ReadBusinessVersion(ctx, record)
				if err != nil {
					msg := "读取回滚后业务版本失败: " + err.Error()
					updates["rollback_status"] = "failed"
					updates["status"] = "failed"
					updates["error_message"] = mergeRecordError(record.ErrorMessage, msg)
					failures = append(failures, record.AppName+" "+msg)
				} else {
					restored = compactJSON(restored)
					updates["restored_business_version_json"] = restored
					if strings.TrimSpace(record.BeforeBusinessVersionJSON) != "" && !BusinessVersionsEqual(record.AppType, record.BeforeBusinessVersionJSON, restored) {
						msg := "回滚后业务版本与更新前版本不一致"
						updates["rollback_status"] = "failed"
						updates["status"] = "failed"
						updates["error_message"] = mergeRecordError(record.ErrorMessage, msg)
						failures = append(failures, record.AppName+" "+msg)
					}
				}
			}
			if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}

	var refreshed []model.ArtifactDeployRecord
	if err := repository.DB.Where("deploy_batch_no = ?", deployBatchNo).Find(&refreshed).Error; err != nil {
		return err
	}
	updateDeployBatchCountersByRecords(refreshed)
	if len(failures) > 0 {
		repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", deployBatchNo).Updates(map[string]any{
			"status":        "rollback_failed",
			"error_message": strings.Join(failures, "；"),
			"finished_at":   &now,
		})
		return errors.New(strings.Join(failures, "；"))
	}
	repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", deployBatchNo).Updates(map[string]any{
		"status":       "rolled_back",
		"finished_at":  &now,
		"failed_count": 0,
	})
	return nil
}

type rollbackRecordGroup struct {
	key            string
	namespace      string
	deploymentName string
	image          string
	records        []model.ArtifactDeployRecord
}

func groupRollbackRecords(records []model.ArtifactDeployRecord) []rollbackRecordGroup {
	byKey := map[string]*rollbackRecordGroup{}
	keys := make([]string, 0)
	for _, record := range records {
		namespace := strings.TrimSpace(record.Namespace)
		deploymentName := strings.TrimSpace(record.DeploymentName)
		key := namespace + "/" + deploymentName
		group := byKey[key]
		if group == nil {
			group = &rollbackRecordGroup{
				key:            key,
				namespace:      namespace,
				deploymentName: deploymentName,
				image:          record.BeforeImage,
			}
			byKey[key] = group
			keys = append(keys, key)
		}
		if strings.TrimSpace(group.image) == "" {
			group.image = record.BeforeImage
		}
		group.records = append(group.records, record)
	}
	sort.Strings(keys)
	groups := make([]rollbackRecordGroup, 0, len(keys))
	for _, key := range keys {
		groups = append(groups, *byKey[key])
	}
	return groups
}

func markRollbackGroupFailed(records []model.ArtifactDeployRecord, errMsg string, at time.Time) {
	for _, record := range records {
		_ = repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(map[string]any{
			"rollback_status": "failed",
			"status":          "failed",
			"error_message":   mergeRecordError(record.ErrorMessage, errMsg),
			"rolled_back_at":  &at,
		}).Error
	}
}

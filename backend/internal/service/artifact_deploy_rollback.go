package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type guardedDeploymentRollbacker interface {
	RollbackDeploymentIfCurrent(ctx context.Context, namespace, deploymentName, expected string, pipelineID uint, image string) error
}

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
	failures := make([]string, 0)
	for _, group := range groups {
		now := time.Now()
		// Preparation never submitted an image; there is nothing to undo.
		unsubmitted := group.image == ""
		for _, record := range group.records {
			if record.DeployStatus != "skipped" && record.DeployStatus != "pending" {
				unsubmitted = false
			}
		}
		if unsubmitted {
			for _, record := range group.records {
				if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Update("rollback_status", "skipped").Error; err != nil {
					return err
				}
			}
			continue
		}

		if strings.TrimSpace(group.image) == "" {
			failures = append(failures, group.key+": 缺少可回滚镜像")
			if err := markRollbackGroupFailed(group.records, "缺少可回滚镜像", now); err != nil {
				failures = append(failures, err.Error())
			}
			continue
		}
		if err := validateRollbackSequence(group); err != nil {
			failures = append(failures, group.key+": "+err.Error())
			if saveErr := markRollbackGroupFailed(group.records, err.Error(), now); saveErr != nil {
				failures = append(failures, saveErr.Error())
			}
			continue
		}
		for _, record := range group.records {
			if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(map[string]any{"rollback_status": "running", "status": "rolling_back"}).Error; err != nil {
				return err
			}
		}
		targetCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		var rollbackErr error
		if guarded, ok := rollbacker.(guardedDeploymentRollbacker); ok {
			rollbackErr = guarded.RollbackDeploymentIfCurrent(targetCtx, group.namespace, group.deploymentName, group.records[0].AfterImage, group.records[0].PipelineID, compatibleRollbackImage(group))
		} else {
			rollbackErr = rollbacker.RollbackDeployment(targetCtx, group.namespace, group.deploymentName, group.image)
		}
		cancel()
		if err := rollbackErr; err != nil {
			msg := "回滚 Deployment 镜像失败: " + err.Error()
			failures = append(failures, group.key+": "+msg)
			if err := markRollbackGroupFailed(group.records, msg, now); err != nil {
				failures = append(failures, err.Error())
			}
			continue
		}
		now = time.Now()
		for _, record := range group.records {
			updates := map[string]any{
				"rollback_status": "success",
				"status":          "rolled_back",
				"rolled_back_at":  &now,
			}
			version, warning := readOptionalVersion(ctx, reader, record)
			if record.AppType == "vue" && compatibleRollbackImage(group) == group.image {
				warning = mergeRecordError(warning, "恢复历史原镜像；该版本可能不包含已打开新页面所需资源，请核对旧客户端兼容性")
			}
			updates["restored_business_version_json"] = version
			updates["version_warning"] = mergeRecordError(record.VersionWarning, warning)
			if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}

	now := time.Now()
	var refreshed []model.ArtifactDeployRecord
	if err := repository.DB.Where("deploy_batch_no = ?", deployBatchNo).Find(&refreshed).Error; err != nil {
		return err
	}
	if err := updateDeployBatchCountersByRecords(refreshed); err != nil {
		return err
	}
	if len(failures) > 0 {
		if err := repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", deployBatchNo).Updates(map[string]any{
			"status":        "rollback_failed",
			"error_message": strings.Join(failures, "；"),
			"finished_at":   &now,
		}).Error; err != nil {
			return fmt.Errorf("%s；保存回滚失败记录失败: %w", strings.Join(failures, "；"), err)
		}
		return errors.New(strings.Join(failures, "；"))
	}
	return repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", deployBatchNo).Updates(map[string]any{
		"status":      "rolled_back",
		"finished_at": &now,
	}).Error
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

func markRollbackGroupFailed(records []model.ArtifactDeployRecord, errMsg string, at time.Time) error {
	for _, record := range records {
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(map[string]any{
			"rollback_status": "failed",
			"status":          "failed",
			"error_message":   mergeRecordError(record.ErrorMessage, errMsg),
			"rolled_back_at":  &at,
		}).Error; err != nil {
			return fmt.Errorf("保存 %s 回滚失败记录失败: %w", record.AppName, err)
		}
	}
	return nil
}

func validateRollbackSequence(group rollbackRecordGroup) error {
	var maxID uint
	expected := group.records[0].AfterImage
	for _, record := range group.records {
		if record.ID > maxID {
			maxID = record.ID
		}
		if record.AfterImage != expected {
			return fmt.Errorf("同一目标的发布记录不一致，拒绝回滚")
		}
	}
	var count int64
	if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("namespace = ? AND deployment_name = ? AND id > ? AND (deploy_status IN ? OR status IN ?)", group.namespace, group.deploymentName, maxID, []string{"deploying", "success", "failed"}, []string{"deployed", "rolling_back", "rolled_back"}).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("该目标已有后续发布，拒绝回滚历史批次；请创建基于最新基线的定向回退")
	}
	return nil
}

func compatibleRollbackImage(group rollbackRecordGroup) string {
	var pipeline model.Pipeline
	if group.records[0].PipelineID == 0 || repository.DB.First(&pipeline, group.records[0].PipelineID).Error != nil {
		return group.image
	}
	var b struct {
		PreparationImage string `json:"preparationImage"`
		AssetsVerified   bool   `json:"assetsVerified"`
	}
	if json.Unmarshal([]byte(pipeline.FrontendBaselineJSON), &b) == nil && b.AssetsVerified && b.PreparationImage != "" {
		return b.PreparationImage
	}
	return group.image
}

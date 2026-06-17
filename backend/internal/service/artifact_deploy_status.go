package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

type RuntimeVersionReader interface {
	ReadBusinessVersion(ctx context.Context, record model.ArtifactDeployRecord) (string, error)
}

func CaptureBeforeDeployVersions(ctx context.Context, pipelineID uint, deploymentName, beforeImage string, reader RuntimeVersionReader) error {
	records, err := deployRecordsForPipelineDeployment(pipelineID, deploymentName)
	if err != nil {
		return err
	}
	for _, record := range records {
		updates := map[string]any{
			"before_image":  beforeImage,
			"deploy_status": "deploying",
			"status":        "deploying",
		}
		if reader != nil && strings.TrimSpace(beforeImage) != "" {
			if version, err := reader.ReadBusinessVersion(ctx, record); err == nil {
				updates["before_business_version_json"] = compactJSON(version)
			} else {
				updates["error_message"] = mergeRecordError(record.ErrorMessage, "读取更新前业务版本失败: "+err.Error())
			}
		}
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	updateDeployBatchCountersByRecords(records)
	return nil
}

func CaptureAfterDeployVersions(ctx context.Context, pipelineID uint, deploymentName, afterImage string, reader RuntimeVersionReader) error {
	records, err := deployRecordsForPipelineDeployment(pipelineID, deploymentName)
	if err != nil {
		return err
	}
	var failures []string
	now := time.Now()
	for _, record := range records {
		updates := map[string]any{
			"after_image":   afterImage,
			"deploy_status": "success",
			"deployed_at":   &now,
		}
		version := ""
		if reader != nil {
			version, err = reader.ReadBusinessVersion(ctx, record)
			if err != nil {
				updates["verify_status"] = "failed"
				updates["status"] = "failed"
				updates["error_message"] = mergeRecordError(record.ErrorMessage, "读取更新后业务版本失败: "+err.Error())
				failures = append(failures, fmt.Sprintf("%s 读取更新后业务版本失败", record.AppName))
			}
		}
		if err == nil {
			version = compactJSON(version)
			updates["after_business_version_json"] = version
			if strings.TrimSpace(record.PackageVersionJSON) == "" {
				updates["verify_status"] = "skipped"
				updates["status"] = "deployed"
			} else if BusinessVersionsEqual(record.AppType, record.PackageVersionJSON, version) {
				updates["verify_status"] = "success"
				updates["status"] = "deployed"
			} else {
				updates["verify_status"] = "failed"
				updates["status"] = "failed"
				updates["error_message"] = mergeRecordError(record.ErrorMessage, "更新后业务版本与更新包不一致")
				failures = append(failures, fmt.Sprintf("%s 业务版本不一致", record.AppName))
			}
		}
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	updateDeployBatchCountersByRecords(records)
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "；"))
	}
	return nil
}

func MarkPipelineImageReady(pipelineID uint, imageName string) {
	updates := map[string]any{
		"after_image":     imageName,
		"build_status":    "success",
		"deploy_status":   "waiting",
		"verify_status":   "pending",
		"status":          "image_ready",
		"rollback_status": "none",
	}
	repository.DB.Model(&model.ArtifactDeployRecord{}).Where("pipeline_id = ?", pipelineID).Updates(updates)
	updateDeployBatchCountersByPipeline(pipelineID)
}

func MarkPipelineBuildFailed(pipelineID uint, errMsg string) {
	MarkPipelineFailed(pipelineID, errMsg)
}

func MarkPipelineFailed(pipelineID uint, errMsg string) {
	var records []model.ArtifactDeployRecord
	if err := repository.DB.Where("pipeline_id = ?", pipelineID).Find(&records).Error; err != nil {
		return
	}
	for _, record := range records {
		updates := map[string]any{
			"status":        "failed",
			"error_message": mergeRecordError(record.ErrorMessage, errMsg),
		}
		if strings.TrimSpace(record.AfterImage) == "" {
			updates["build_status"] = "failed"
			updates["deploy_status"] = "skipped"
			updates["verify_status"] = "skipped"
		} else if record.DeployStatus != "success" {
			updates["deploy_status"] = "failed"
		}
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return
		}
	}
	updateDeployBatchCountersByPipeline(pipelineID)
}

func deployRecordsForPipelineDeployment(pipelineID uint, deploymentName string) ([]model.ArtifactDeployRecord, error) {
	var records []model.ArtifactDeployRecord
	query := repository.DB.Where("pipeline_id = ?", pipelineID)
	if strings.TrimSpace(deploymentName) != "" {
		query = query.Where("deployment_name = ?", deploymentName)
	}
	err := query.Order("id ASC").Find(&records).Error
	return records, err
}

func updateDeployBatchCountersByPipeline(pipelineID uint) {
	var records []model.ArtifactDeployRecord
	if err := repository.DB.Where("pipeline_id = ?", pipelineID).Find(&records).Error; err != nil {
		return
	}
	updateDeployBatchCountersByRecords(records)
}

func updateDeployBatchCountersByRecords(records []model.ArtifactDeployRecord) {
	seen := map[string]bool{}
	for _, record := range records {
		if record.DeployBatchNo == "" || seen[record.DeployBatchNo] {
			continue
		}
		seen[record.DeployBatchNo] = true
		var all []model.ArtifactDeployRecord
		if err := repository.DB.Where("deploy_batch_no = ?", record.DeployBatchNo).Find(&all).Error; err != nil {
			continue
		}
		total, success, failed, rolledBack := len(all), 0, 0, 0
		status := "building"
		for _, r := range all {
			switch r.Status {
			case "deployed":
				success++
			case "failed":
				failed++
			}
			if r.RollbackStatus == "success" {
				rolledBack++
			}
		}
		if failed > 0 {
			status = "failed"
		} else if total > 0 && success == total {
			status = "deployed"
		} else {
			waiting := false
			for _, r := range all {
				if r.Status == "image_ready" {
					waiting = true
					break
				}
			}
			if waiting {
				status = "image_ready"
			}
		}
		repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", record.DeployBatchNo).Updates(map[string]any{
			"total_count":       total,
			"success_count":     success,
			"failed_count":      failed,
			"rolled_back_count": rolledBack,
			"status":            status,
		})
	}
}

func mergeRecordError(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" || strings.Contains(existing, next) {
		return existing
	}
	return existing + "；" + next
}

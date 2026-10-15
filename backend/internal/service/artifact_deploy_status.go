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
)

type RuntimeVersionReader interface {
	ReadBusinessVersion(ctx context.Context, record model.ArtifactDeployRecord) (string, error)
}

// Version metadata is informational and never gates deployment or rollback.
func readOptionalVersion(ctx context.Context, reader RuntimeVersionReader, record model.ArtifactDeployRecord) (string, string) {
	if record.AppType == "vue" && record.VueRole == "cdn" {
		return "", ""
	}
	if reader == nil {
		return "", ""
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	version, err := reader.ReadBusinessVersion(readCtx, record)
	if err != nil {
		warning := fmt.Sprintf("应用 %s 版本信息采集失败（路径=%s，不影响部署或回滚结果）: %v", record.AppName, record.RuntimeVersionPath, err)
		if logger.Log != nil {
			logger.Log.Warn(warning)
		}
		return "", warning
	}
	return compactJSON(version), ""
}

func CaptureBeforeDeployVersions(ctx context.Context, pipelineID uint, deploymentName, beforeImage string, reader RuntimeVersionReader) error {
	return CaptureBeforeDeployImages(ctx, pipelineID, deploymentName, BeforeDeploymentImages{Desired: beforeImage, Rollback: beforeImage}, reader)
}

func CaptureBeforeDeployImages(ctx context.Context, pipelineID uint, deploymentName string, images BeforeDeploymentImages, reader RuntimeVersionReader) error {
	records, err := deployRecordsForPipelineDeployment(pipelineID, deploymentName)
	if err != nil {
		return err
	}
	for _, record := range records {
		updates := map[string]any{"before_image": images.Rollback, "before_desired_image": images.Desired, "deploy_status": "deploying", "status": "deploying"}
		if strings.TrimSpace(images.Desired) != "" {
			version, warning := readOptionalVersion(ctx, reader, record)
			updates["before_business_version_json"] = version
			updates["version_warning"] = mergeRecordError(record.VersionWarning, warning)
		}
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	return updateDeployBatchCountersByRecords(records)
}

func CaptureAfterDeployVersions(ctx context.Context, pipelineID uint, deploymentName, afterImage string, reader RuntimeVersionReader, technicalVerified ...bool) error {
	records, err := deployRecordsForPipelineDeployment(pipelineID, deploymentName)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, record := range records {
		version, warning := readOptionalVersion(ctx, reader, record)
		updates := map[string]any{
			"after_image": afterImage, "deploy_status": "success", "deployed_at": &now,
			"status": "deployed", "verify_status": "skipped",
			"after_business_version_json": version,
			"version_warning":             mergeRecordError(record.VersionWarning, warning),
		}
		if len(technicalVerified) > 0 && technicalVerified[0] {
			updates["verify_status"] = "success"
		}
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	return updateDeployBatchCountersByRecords(records)
}

// RecordDeploymentProgress is persisted before each potentially mutating boundary.
// A lost response or process restart must never be described as "deployment skipped".
func RecordDeploymentProgress(pipelineID uint, phase, image string) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Pipeline{}).Where("id = ?", pipelineID).Update("deployment_progress", phase).Error; err != nil {
			return err
		}
		updates := map[string]any{"after_image": image, "build_status": "success", "deploy_status": "deploying", "status": "deploying"}
		if phase == "completed" || phase == "verifying" {
			updates["deploy_status"] = "success"
			updates["verify_status"] = "skipped"
			var p model.Pipeline
			if err := tx.Select("frontend_baseline_json").First(&p, pipelineID).Error; err != nil {
				return err
			}
			if p.FrontendBaselineJSON != "" {
				if phase == "completed" {
					updates["verify_status"] = "success"
				} else {
					updates["verify_status"] = "running"
				}
			}
		}
		return tx.Model(&model.ArtifactDeployRecord{}).Where("pipeline_id = ?", pipelineID).Updates(updates).Error
	})
}

func MarkPipelineImageBuilt(pipelineID uint, image string) error {
	return repository.DB.Model(&model.ArtifactDeployRecord{}).Where("pipeline_id = ?", pipelineID).Updates(map[string]any{
		"after_image": image, "build_status": "success",
	}).Error
}

func MarkPipelineImageReady(pipelineID uint, imageName string) error {
	updates := map[string]any{
		"after_image":     imageName,
		"build_status":    "success",
		"deploy_status":   "waiting",
		"verify_status":   "pending",
		"status":          "image_ready",
		"rollback_status": "none",
	}
	if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("pipeline_id = ?", pipelineID).Updates(updates).Error; err != nil {
		return err
	}
	return updateDeployBatchCountersByPipeline(pipelineID)
}

func MarkPipelineBuildFailed(pipelineID uint, errMsg string) error {
	return MarkPipelineFailed(pipelineID, errMsg)
}

func MarkPipelineFailed(pipelineID uint, errMsg string, deploymentProgress ...string) error {
	completed := len(deploymentProgress) > 0 && deploymentProgress[0] == "completed"
	var records []model.ArtifactDeployRecord
	if err := repository.DB.Where("pipeline_id = ?", pipelineID).Find(&records).Error; err != nil {
		return err
	}
	for _, record := range records {
		updates := map[string]any{
			"status":        "failed",
			"error_message": mergeRecordError(record.ErrorMessage, errMsg),
		}
		if completed {
			updates["build_status"] = "success"
			updates["deploy_status"] = "success"
			if record.VerifyStatus != "success" {
				updates["verify_status"] = "skipped"
			}
		} else if record.DeployStatus == "deploying" {
			updates["build_status"] = "success"
			updates["deploy_status"] = "failed"
			updates["verify_status"] = "skipped"
		} else if record.DeployStatus == "success" {
			if record.VerifyStatus != "success" && record.VerifyStatus != "skipped" {
				updates["verify_status"] = "failed"
			}
		} else if strings.TrimSpace(record.AfterImage) == "" {
			updates["build_status"] = "failed"
			updates["deploy_status"] = "skipped"
			updates["verify_status"] = "skipped"
		} else if record.DeployStatus != "success" {
			updates["deploy_status"] = "failed"
		}
		if err := repository.DB.Model(&model.ArtifactDeployRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return err
		}
	}
	return updateDeployBatchCountersByPipeline(pipelineID)
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

func updateDeployBatchCountersByPipeline(pipelineID uint) error {
	var records []model.ArtifactDeployRecord
	if err := repository.DB.Where("pipeline_id = ?", pipelineID).Find(&records).Error; err != nil {
		return err
	}
	return updateDeployBatchCountersByRecords(records)
}

func updateDeployBatchCountersByRecords(records []model.ArtifactDeployRecord) error {
	seen := map[string]bool{}
	for _, record := range records {
		if record.DeployBatchNo == "" || seen[record.DeployBatchNo] {
			continue
		}
		seen[record.DeployBatchNo] = true
		var all []model.ArtifactDeployRecord
		if err := repository.DB.Where("deploy_batch_no = ?", record.DeployBatchNo).Find(&all).Error; err != nil {
			return err
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
		if err := repository.DB.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", record.DeployBatchNo).Updates(map[string]any{
			"total_count":       total,
			"success_count":     success,
			"failed_count":      failed,
			"rolled_back_count": rolledBack,
			"status":            gorm.Expr("CASE WHEN status = 'rolling_back' THEN status ELSE ? END", status),
		}).Error; err != nil {
			return fmt.Errorf("保存批次 %s 汇总结果失败: %w", record.DeployBatchNo, err)
		}
	}
	return nil
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

package service

import (
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"

	"gorm.io/gorm"
)

type ArtifactDeployRecordInput struct {
	VersionNo    string
	Namespace    string
	DeployMode   string
	TriggerUser  string
	Pipeline     model.Pipeline
	VersionItems []model.ArtifactVersionItem
	Applications []model.Application
}

func CreateArtifactDeployRecords(input ArtifactDeployRecordInput) error {
	if repository.DB == nil {
		return fmt.Errorf("数据库未初始化")
	}
	versionNo := strings.TrimSpace(input.VersionNo)
	if versionNo == "" {
		return fmt.Errorf("版本号不能为空")
	}
	if len(input.VersionItems) == 0 {
		return nil
	}

	namespace := strings.TrimSpace(input.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	deployMode := strings.TrimSpace(input.DeployMode)
	if deployMode == "" {
		deployMode = "immediate"
	}
	deployBatchNo := versionNo
	appByID := make(map[uint]model.Application, len(input.Applications))
	for _, app := range input.Applications {
		appByID[app.ID] = app
	}

	return repository.DB.Transaction(func(tx *gorm.DB) error {
		var batch model.ArtifactDeployBatch
		err := tx.Where("deploy_batch_no = ?", deployBatchNo).First(&batch).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		now := time.Now()
		if err == gorm.ErrRecordNotFound {
			batch = model.ArtifactDeployBatch{
				DeployBatchNo: deployBatchNo,
				VersionNo:     versionNo,
				Namespace:     namespace,
				DeployMode:    deployMode,
				Status:        "building",
				TriggerUser:   input.TriggerUser,
				StartedAt:     &now,
			}
			if err := tx.Create(&batch).Error; err != nil {
				return err
			}
		} else {
			updates := map[string]any{
				"namespace":    namespace,
				"deploy_mode":  deployMode,
				"trigger_user": input.TriggerUser,
			}
			if batch.StartedAt == nil {
				updates["started_at"] = &now
			}
			if batch.Status == "" || batch.Status == "available" {
				updates["status"] = "building"
			}
			if err := tx.Model(&batch).Updates(updates).Error; err != nil {
				return err
			}
		}

		for _, item := range input.VersionItems {
			app, ok := appByID[item.AppID]
			if !ok {
				app = model.Application{ID: item.AppID, AppName: item.AppName, AppType: item.AppType}
			}
			record := model.ArtifactDeployRecord{
				DeployBatchNo:         deployBatchNo,
				VersionNo:             versionNo,
				ArtifactVersionItemID: item.ID,
				PipelineID:            input.Pipeline.ID,
				AppID:                 item.AppID,
				AppName:               firstNonEmpty(item.AppName, app.AppName),
				AppCode:               app.AppCode,
				AppType:               firstNonEmpty(item.AppType, app.AppType),
				VueRole:               app.VueRole,
				FileName:              item.FileName,
				Namespace:             namespace,
				DeploymentName:        DeploymentNameForApplication(app),
				RuntimeVersionPath:    RuntimeVersionPathForApplication(app),
				PackageVersionJSON:    item.PackageVersionJSON,
				BuildStatus:           "pending",
				DeployStatus:          "pending",
				VerifyStatus:          "pending",
				RollbackStatus:        "none",
				Status:                "building",
			}
			if err := tx.Where("deploy_batch_no = ? AND app_id = ? AND file_name = ?", deployBatchNo, item.AppID, item.FileName).
				Assign(record).
				FirstOrCreate(&model.ArtifactDeployRecord{}).Error; err != nil {
				return err
			}
		}

		var total int64
		if err := tx.Model(&model.ArtifactDeployRecord{}).Where("deploy_batch_no = ?", deployBatchNo).Count(&total).Error; err != nil {
			return err
		}
		return tx.Model(&model.ArtifactDeployBatch{}).Where("deploy_batch_no = ?", deployBatchNo).Update("total_count", int(total)).Error
	})
}

func DeploymentNameForApplication(app model.Application) string {
	if app.AppType == "vue" && app.VueRole == "sub" {
		return "web-main"
	}
	if app.AppType == "vue" && app.VueRole == "main" {
		return firstNonEmpty(app.AppName, "web-main")
	}
	return app.AppName
}

func RuntimeVersionPathForApplication(app model.Application) string {
	if app.AppType == "java" {
		return "http://localhost:8080/actuator/info"
	}
	if app.AppType == "vue" && app.VueRole == "sub" && strings.TrimSpace(app.AppCode) != "" {
		return "/usr/share/nginx/html/apps/" + strings.Trim(strings.TrimSpace(app.AppCode), "/") + "/config.json"
	}
	return "/usr/share/nginx/html/config.json"
}

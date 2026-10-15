package service

import (
	"df-build-server/internal/frontend"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"fmt"
	"gorm.io/gorm"
)

// One record per actual region, including explicitly embedded bundle members.
func CreateFrontendRegionRecords(p *model.Pipeline, regions []frontend.Region) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		var existing []model.ArtifactDeployRecord
		if err := tx.Where("pipeline_id = ?", p.ID).Find(&existing).Error; err != nil {
			return err
		}
		batchNo, versionNo := p.PipelineNo, p.PipelineNo
		if len(existing) > 0 {
			batchNo, versionNo = existing[0].DeployBatchNo, existing[0].VersionNo
		} else {
			batch := model.ArtifactDeployBatch{DeployBatchNo: batchNo, VersionNo: versionNo, Namespace: p.K8sNamespace, DeployMode: p.DeployMode, Status: "building", TriggerUser: p.TriggerUser}
			if err := tx.Create(&batch).Error; err != nil {
				return err
			}
		}
		seen := map[uint]bool{}
		for _, record := range existing {
			seen[record.AppID] = true
		}
		for _, region := range regions {
			var app model.Application
			if err := tx.Where("app_name = ?", region.AppName).First(&app).Error; err != nil {
				return err
			}
			if seen[app.ID] {
				continue
			}
			record := model.ArtifactDeployRecord{DeployBatchNo: batchNo, VersionNo: versionNo, PipelineID: p.ID, AppID: app.ID, AppName: app.AppName, AppType: "vue", VueRole: app.VueRole, AppCode: app.AppCode, Namespace: p.K8sNamespace, DeploymentName: app.DeploymentTarget(), RuntimeVersionPath: RuntimeVersionPathForApplication(app), FileName: p.ArtifactName, BuildStatus: "pending", DeployStatus: "pending", VerifyStatus: "pending", RollbackStatus: "none", Status: "building"}
			if err := tx.Create(&record).Error; err != nil {
				return fmt.Errorf("记录区域 %s 失败: %w", region.Path, err)
			}
		}
		return nil
	})
}

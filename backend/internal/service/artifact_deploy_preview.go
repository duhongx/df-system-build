package service

import (
	"context"
	"strings"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type ArtifactDeployRuntimeReader interface {
	CurrentDeploymentImage(ctx context.Context, namespace, deploymentName string) (string, error)
	ReadBusinessVersion(ctx context.Context, record model.ArtifactDeployRecord) (string, error)
}

func BuildArtifactDeployPreviewRecords(ctx context.Context, versionNo, namespace string, reader ArtifactDeployRuntimeReader) ([]model.ArtifactDeployRecord, error) {
	versionNo = strings.TrimSpace(versionNo)
	if versionNo == "" {
		return nil, nil
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "default"
	}
	var items []model.ArtifactVersionItem
	if err := repository.DB.
		Where("version_no = ? AND deployable = ?", versionNo, true).
		Order("id ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	appIDs := make([]uint, 0, len(items))
	seenAppIDs := map[uint]bool{}
	for _, item := range items {
		if item.AppID == 0 || seenAppIDs[item.AppID] {
			continue
		}
		appIDs = append(appIDs, item.AppID)
		seenAppIDs[item.AppID] = true
	}
	appByID := map[uint]model.Application{}
	if len(appIDs) > 0 {
		var apps []model.Application
		if err := repository.DB.Where("id IN ?", appIDs).Find(&apps).Error; err != nil {
			return nil, err
		}
		for _, app := range apps {
			appByID[app.ID] = app
		}
	}

	records := make([]model.ArtifactDeployRecord, 0, len(items))
	for _, item := range items {
		app := appByID[item.AppID]
		if app.ID == 0 {
			app = model.Application{ID: item.AppID, AppName: item.AppName, AppType: item.AppType}
		}
		record := model.ArtifactDeployRecord{
			DeployBatchNo:         versionNo,
			VersionNo:             versionNo,
			ArtifactVersionItemID: item.ID,
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
			BuildStatus:           "not_started",
			DeployStatus:          "not_started",
			VerifyStatus:          "not_started",
			RollbackStatus:        "none",
			Status:                "current",
		}

		if reader != nil {
			image, err := reader.CurrentDeploymentImage(ctx, namespace, record.DeploymentName)
			if err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				record.ErrorMessage = "读取当前镜像失败: " + err.Error()
			} else {
				record.BeforeImage = image
				if version, err := reader.ReadBusinessVersion(ctx, record); err == nil {
					record.BeforeBusinessVersionJSON = compactJSON(version)
				} else {
					record.ErrorMessage = mergeRecordError(record.ErrorMessage, "读取当前业务版本失败: "+err.Error())
				}
			}
		}
		records = append(records, record)
	}
	return records, nil
}

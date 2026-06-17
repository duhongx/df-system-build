package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"

	"gorm.io/gorm"
)

func ListDeploymentRuntimeVersions(namespace string) ([]model.DeploymentRuntimeVersion, error) {
	if repository.DB == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "default"
	}
	var versions []model.DeploymentRuntimeVersion
	err := repository.DB.
		Where("namespace = ?", namespace).
		Order("deployment_name ASC, app_type ASC, app_name ASC, runtime_version_path ASC").
		Find(&versions).Error
	return versions, err
}

func SyncDeploymentRuntimeVersions(ctx context.Context, namespace string, deploymentNames []string, reader ArtifactDeployRuntimeReader) ([]model.DeploymentRuntimeVersion, error) {
	if repository.DB == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	if reader == nil {
		reader = NewK8sRuntimeVersionReader()
	}
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "default"
	}
	deploymentFilter := stringSet(deploymentNames)

	var apps []model.Application
	if err := repository.DB.Where("enabled = ?", true).Order("app_name ASC").Find(&apps).Error; err != nil {
		return nil, err
	}
	targetsByDeployment := map[string][]model.Application{}
	for _, app := range apps {
		deploymentName := DeploymentNameForApplication(app)
		if deploymentName == "" {
			continue
		}
		if len(deploymentFilter) > 0 && !deploymentFilter[deploymentName] {
			continue
		}
		targetsByDeployment[deploymentName] = append(targetsByDeployment[deploymentName], app)
	}

	deployments := make([]string, 0, len(targetsByDeployment))
	for name := range targetsByDeployment {
		deployments = append(deployments, name)
	}
	sort.Strings(deployments)

	now := time.Now()
	for _, deploymentName := range deployments {
		image, imageErr := reader.CurrentDeploymentImage(ctx, namespace, deploymentName)
		for _, app := range targetsByDeployment[deploymentName] {
			record := deploymentRuntimeRecord(namespace, app)
			row := model.DeploymentRuntimeVersion{
				Namespace:           namespace,
				DeploymentName:      deploymentName,
				AppID:               app.ID,
				AppName:             app.AppName,
				AppCode:             app.AppCode,
				AppType:             app.AppType,
				VueRole:             app.VueRole,
				RuntimeVersionPath:  record.RuntimeVersionPath,
				Image:               image,
				BusinessVersionJSON: "",
				Status:              "synced",
				ErrorMessage:        "",
				SyncedAt:            now,
			}
			if imageErr != nil {
				row.Status = "failed"
				row.ErrorMessage = "读取当前镜像失败: " + imageErr.Error()
				if err := upsertDeploymentRuntimeVersion(row); err != nil {
					return nil, err
				}
				continue
			}
			version, versionErr := reader.ReadBusinessVersion(ctx, record)
			if versionErr != nil {
				row.Status = "failed"
				row.ErrorMessage = "读取当前业务版本失败: " + versionErr.Error()
			} else {
				row.BusinessVersionJSON = compactJSON(version)
			}
			if err := upsertDeploymentRuntimeVersion(row); err != nil {
				return nil, err
			}
		}
	}
	return ListDeploymentRuntimeVersions(namespace)
}

func BuildArtifactDeployPreviewRecordsFromCache(ctx context.Context, versionNo, namespace string) ([]model.ArtifactDeployRecord, error) {
	if repository.DB == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
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

	apps, err := applicationsByIDForItems(items)
	if err != nil {
		return nil, err
	}

	records := make([]model.ArtifactDeployRecord, 0, len(items))
	for _, item := range items {
		app := apps[item.AppID]
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
		cache, found, err := findDeploymentRuntimeVersion(repository.DB.WithContext(ctx), record)
		if err != nil {
			return nil, err
		}
		if found {
			record.BeforeImage = cache.Image
			record.BeforeBusinessVersionJSON = cache.BusinessVersionJSON
			if cache.Status != "synced" {
				record.ErrorMessage = cache.ErrorMessage
			}
		} else {
			record.ErrorMessage = "未采集当前运行版本，请先在 Kubernetes Deployments 页面同步版本"
		}
		records = append(records, record)
	}
	return records, nil
}

func deploymentRuntimeRecord(namespace string, app model.Application) model.ArtifactDeployRecord {
	return model.ArtifactDeployRecord{
		Namespace:          namespace,
		AppID:              app.ID,
		AppName:            app.AppName,
		AppCode:            app.AppCode,
		AppType:            app.AppType,
		VueRole:            app.VueRole,
		DeploymentName:     DeploymentNameForApplication(app),
		RuntimeVersionPath: RuntimeVersionPathForApplication(app),
	}
}

func applicationsByIDForItems(items []model.ArtifactVersionItem) (map[uint]model.Application, error) {
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
	if len(appIDs) == 0 {
		return appByID, nil
	}
	var apps []model.Application
	if err := repository.DB.Where("id IN ?", appIDs).Find(&apps).Error; err != nil {
		return nil, err
	}
	for _, app := range apps {
		appByID[app.ID] = app
	}
	return appByID, nil
}

func findDeploymentRuntimeVersion(db *gorm.DB, record model.ArtifactDeployRecord) (model.DeploymentRuntimeVersion, bool, error) {
	var cached model.DeploymentRuntimeVersion
	query := db.Where("namespace = ? AND deployment_name = ?", record.Namespace, record.DeploymentName)
	if record.AppID != 0 {
		query = query.Where("app_id = ?", record.AppID)
	} else if strings.TrimSpace(record.AppCode) != "" {
		query = query.Where("app_code = ?", strings.TrimSpace(record.AppCode))
	} else if strings.TrimSpace(record.AppName) != "" {
		query = query.Where("app_name = ?", strings.TrimSpace(record.AppName))
	} else {
		query = query.Where("runtime_version_path = ?", record.RuntimeVersionPath)
	}
	err := query.First(&cached).Error
	if err == nil {
		return cached, true, nil
	}
	if err == gorm.ErrRecordNotFound && strings.TrimSpace(record.RuntimeVersionPath) != "" {
		err = db.Where("namespace = ? AND deployment_name = ? AND runtime_version_path = ?", record.Namespace, record.DeploymentName, record.RuntimeVersionPath).
			First(&cached).Error
		if err == nil {
			return cached, true, nil
		}
	}
	if err == gorm.ErrRecordNotFound {
		return model.DeploymentRuntimeVersion{}, false, nil
	}
	return model.DeploymentRuntimeVersion{}, false, err
}

func upsertDeploymentRuntimeVersion(row model.DeploymentRuntimeVersion) error {
	var existing model.DeploymentRuntimeVersion
	err := repository.DB.
		Where("namespace = ? AND deployment_name = ? AND app_name = ? AND runtime_version_path = ?",
			row.Namespace, row.DeploymentName, row.AppName, row.RuntimeVersionPath).
		First(&existing).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if err == gorm.ErrRecordNotFound {
		return repository.DB.Create(&row).Error
	}
	return repository.DB.Model(&existing).Updates(map[string]any{
		"app_id":                row.AppID,
		"app_code":              row.AppCode,
		"app_type":              row.AppType,
		"vue_role":              row.VueRole,
		"image":                 row.Image,
		"business_version_json": row.BusinessVersionJSON,
		"status":                row.Status,
		"error_message":         row.ErrorMessage,
		"synced_at":             row.SyncedAt,
	}).Error
}

func stringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result[value] = true
		}
	}
	return result
}

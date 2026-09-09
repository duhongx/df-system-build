package handler

import (
	"fmt"
	"path/filepath"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func usedArtifactFiles(db *gorm.DB, versionNo string) (map[string]bool, error) {
	used := map[string]bool{}
	var names []string
	if err := db.Model(&model.ArtifactUpdateClaim{}).Where("version_no = ?", versionNo).Pluck("file_name", &names).Error; err != nil {
		return nil, err
	}
	for _, name := range names {
		used[name] = true
	}
	// Existing deployments also consume their input, including failed/rolled-back ones.
	if err := db.Model(&model.ArtifactDeployRecord{}).Where("version_no = ?", versionNo).Distinct("file_name").Pluck("file_name", &names).Error; err != nil {
		return nil, err
	}
	for _, name := range names {
		used[name] = true
	}
	return used, nil
}

func markArtifactUsage(version *model.ArtifactVersion, items []model.ArtifactVersionItem) error {
	used, err := usedArtifactFiles(repository.DB, version.VersionNo)
	if err != nil {
		return err
	}
	version.RemainingDeployableCount = 0
	for i := range items {
		items[i].UpdateUsed = used[items[i].FileName]
		if items[i].Deployable && !items[i].UpdateUsed {
			version.RemainingDeployableCount++
		}
	}
	return nil
}

func markVersionUsage(versions []model.ArtifactVersion) error {
	for i := range versions {
		var items []model.ArtifactVersionItem
		if err := repository.DB.Where("version_no = ?", versions[i].VersionNo).Find(&items).Error; err != nil {
			return err
		}
		if err := markArtifactUsage(&versions[i], items); err != nil {
			return err
		}
	}
	return nil
}

// Reserve all selected inputs before creating or enqueueing any pipeline.
func claimArtifactUpdates(versionNo, sourceDir string, files []string) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		var version model.ArtifactVersion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("version_no = ?", versionNo).First(&version).Error; err != nil {
			return fmt.Errorf("读取上传版本失败，请重新上传: %w", err)
		}
		expected, err := filepath.Abs(firstNonEmptyString(version.TargetPath, version.LocalDir))
		if err != nil {
			return err
		}
		actual, err := filepath.Abs(sourceDir)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("制品目录与上传版本不一致，请重新上传")
		}
		used, err := usedArtifactFiles(tx, versionNo)
		if err != nil {
			return fmt.Errorf("读取制品使用记录失败: %w", err)
		}
		claims := make([]model.ArtifactUpdateClaim, 0, len(files))
		for _, file := range files {
			if used[file] {
				return fmt.Errorf("制品 %s 已提交过更新，不能再次更新；如需重试请重新上传生成新版本", file)
			}
			used[file] = true
			var count int64
			if err := tx.Model(&model.ArtifactVersionItem{}).Where("version_no = ? AND file_name = ?", versionNo, file).Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("制品 %s 不属于该上传版本或明细重复，请重新上传", file)
			}
			claims = append(claims, model.ArtifactUpdateClaim{VersionNo: versionNo, FileName: file})
		}
		return tx.Create(&claims).Error
	})
}

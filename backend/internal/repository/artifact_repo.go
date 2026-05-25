package repository

import "df-build-server/internal/model"

type ArtifactRepo struct{}

func NewArtifactRepo() *ArtifactRepo { return &ArtifactRepo{} }

func (r *ArtifactRepo) Create(a *model.Artifact) error {
	return DB.Create(a).Error
}

func (r *ArtifactRepo) List(page, pageSize int, search string) ([]model.Artifact, int64, error) {
	var list []model.Artifact
	var total int64

	query := DB.Model(&model.Artifact{})
	if search != "" {
		query = query.Where("app_name LIKE ? OR artifact_name LIKE ?", "%"+search+"%", "%"+search+"%")
	}
	query.Count(&total)

	if pageSize <= 0 {
		pageSize = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize
	err := query.Order("id DESC").Offset(offset).Limit(pageSize).Find(&list).Error
	return list, total, err
}

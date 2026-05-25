package repository

import (
	"df-build-server/internal/model"

	"gorm.io/gorm"
)

type ApplicationRepo struct{}

func NewApplicationRepo() *ApplicationRepo {
	return &ApplicationRepo{}
}

type AppListParams struct {
	Page     int
	PageSize int
	Search   string
	AppType  string
}

func (r *ApplicationRepo) List(params AppListParams) ([]model.Application, int64, error) {
	var apps []model.Application
	var total int64

	query := DB.Model(&model.Application{})

	if params.Search != "" {
		query = query.Where("app_name LIKE ? OR git_repo LIKE ?", "%"+params.Search+"%", "%"+params.Search+"%")
	}
	if params.AppType != "" {
		query = query.Where("app_type = ?", params.AppType)
	}

	query.Count(&total)

	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.Page <= 0 {
		params.Page = 1
	}
	offset := (params.Page - 1) * params.PageSize

	err := query.Order("id DESC").Offset(offset).Limit(params.PageSize).Find(&apps).Error
	return apps, total, err
}

func (r *ApplicationRepo) FindByID(id uint) (*model.Application, error) {
	var app model.Application
	err := DB.First(&app, id).Error
	if err != nil {
		return nil, err
	}
	return &app, nil
}

func (r *ApplicationRepo) FindByName(name string) (*model.Application, error) {
	var app model.Application
	err := DB.Where("app_name = ?", name).First(&app).Error
	if err != nil {
		return nil, err
	}
	return &app, nil
}

func (r *ApplicationRepo) Create(app *model.Application) error {
	return DB.Create(app).Error
}

func (r *ApplicationRepo) Update(app *model.Application) error {
	return DB.Save(app).Error
}

func (r *ApplicationRepo) Delete(id uint) error {
	return DB.Delete(&model.Application{}, id).Error
}

func (r *ApplicationRepo) ExistsByName(name string) bool {
	var count int64
	DB.Model(&model.Application{}).Where("app_name = ?", name).Count(&count)
	return count > 0
}

func (r *ApplicationRepo) ExistsByNameExcludeID(name string, excludeID uint) bool {
	var count int64
	DB.Model(&model.Application{}).Where("app_name = ? AND id != ?", name, excludeID).Count(&count)
	return count > 0
}

func (r *ApplicationRepo) ListAll() ([]model.Application, error) {
	var apps []model.Application
	err := DB.Order("app_name ASC").Find(&apps).Error
	return apps, err
}

func (r *ApplicationRepo) UpdateBuildStatus(id uint, status string) error {
	now := gorm.Expr("CURRENT_TIMESTAMP")
	return DB.Model(&model.Application{}).Where("id = ?", id).
		Updates(map[string]interface{}{"last_build_status": status, "last_build_time": now}).Error
}

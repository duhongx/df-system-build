package repository

import "df-build-server/internal/model"

type BuildConfigRepo struct{}

func NewBuildConfigRepo() *BuildConfigRepo { return &BuildConfigRepo{} }

func (r *BuildConfigRepo) List() ([]model.BuildConfig, error) {
	var list []model.BuildConfig
	err := DB.Order("id ASC").Find(&list).Error
	return list, err
}

func (r *BuildConfigRepo) FindByID(id uint) (*model.BuildConfig, error) {
	var bc model.BuildConfig
	err := DB.First(&bc, id).Error
	return &bc, err
}

func (r *BuildConfigRepo) Create(bc *model.BuildConfig) error { return DB.Create(bc).Error }

func (r *BuildConfigRepo) Update(bc *model.BuildConfig) error { return DB.Save(bc).Error }

func (r *BuildConfigRepo) Delete(id uint) error { return DB.Delete(&model.BuildConfig{}, id).Error }

func (r *BuildConfigRepo) ExistsByName(name string) bool {
	var count int64
	DB.Model(&model.BuildConfig{}).Where("name = ?", name).Count(&count)
	return count > 0
}

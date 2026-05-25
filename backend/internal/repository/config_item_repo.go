package repository

import (
	"df-build-server/internal/model"

	"gorm.io/gorm"
)

type ConfigItemRepo struct {
	db *gorm.DB
}

func NewConfigItemRepo() *ConfigItemRepo {
	return &ConfigItemRepo{db: DB}
}

func (r *ConfigItemRepo) List(category string) ([]model.ConfigItem, error) {
	var items []model.ConfigItem
	q := r.db
	if category != "" {
		q = q.Where("category = ?", category)
	}
	err := q.Order("category, name").Find(&items).Error
	return items, err
}

func (r *ConfigItemRepo) GetByID(id uint) (*model.ConfigItem, error) {
	var item model.ConfigItem
	err := r.db.First(&item, id).Error
	return &item, err
}

func (r *ConfigItemRepo) GetByCode(code string) (*model.ConfigItem, error) {
	var item model.ConfigItem
	err := r.db.Where("code = ?", code).First(&item).Error
	return &item, err
}

func (r *ConfigItemRepo) Create(item *model.ConfigItem) error {
	return r.db.Create(item).Error
}

func (r *ConfigItemRepo) Update(item *model.ConfigItem) error {
	return r.db.Save(item).Error
}

func (r *ConfigItemRepo) Delete(id uint) error {
	return r.db.Delete(&model.ConfigItem{}, id).Error
}

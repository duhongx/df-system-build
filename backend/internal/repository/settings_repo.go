package repository

import (
	"errors"
	"fmt"
	"strings"

	"df-build-server/internal/model"
	"gorm.io/gorm"
)

type SettingsRepo struct{}

func NewSettingsRepo() *SettingsRepo { return &SettingsRepo{} }

func (r *SettingsRepo) GetAll() ([]model.Settings, error) {
	var list []model.Settings
	err := DB.Find(&list).Error
	return list, err
}

func (r *SettingsRepo) GetByKey(key string) (string, error) {
	var s model.Settings
	// GORM will quote column names per-dialect (key is a reserved word in some DBs).
	// Use struct-field ordering via Where clause that references the Key struct field.
	err := DB.Where(&model.Settings{Key: key}).First(&s).Error
	if err != nil {
		return "", err
	}
	return s.Value, nil
}

func (r *SettingsRepo) Set(key, value string) error {
	// Upsert-like behavior: update if exists, otherwise create
	var existing model.Settings
	result := DB.Where(&model.Settings{Key: key}).First(&existing)
	if result.Error == nil {
		existing.Value = value
		return DB.Save(&existing).Error
	}
	return DB.Create(&model.Settings{Key: key, Value: value}).Error
}

func (r *SettingsRepo) BatchUpdate(settings map[string]string) error {
	for k, v := range settings {
		if err := r.Set(k, v); err != nil {
			return err
		}
	}
	return nil
}

// Read distinguishes a missing optional setting from a storage failure.
func (r *SettingsRepo) Read(key string, required bool) (string, error) {
	return ReadSetting(r, key, required)
}
func ReadSetting(reader interface{ GetByKey(string) (string, error) }, key string, required bool) (string, error) {
	value, err := reader.GetByKey(key)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("读取配置项 %s 失败: %w", key, err)
	}
	if required && (err != nil || strings.TrimSpace(value) == "") {
		return "", fmt.Errorf("必填配置项 %s 未配置，请在设置中配置", key)
	}
	return value, nil
}

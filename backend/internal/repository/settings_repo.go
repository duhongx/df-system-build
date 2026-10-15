package repository

import (
	"errors"
	"fmt"
	"strings"

	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
	"gorm.io/gorm"
)

type SettingsRepo struct{}

func NewSettingsRepo() *SettingsRepo { return &SettingsRepo{} }

func (r *SettingsRepo) GetAll() ([]model.Settings, error) {
	var list []model.Settings
	err := DB.Find(&list).Error
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Value, err = decodePostgreSQLSetting(list[i].Key, list[i].Value)
		if err != nil {
			return nil, err
		}
	}
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
	return decodePostgreSQLSetting(key, s.Value)
}

func (r *SettingsRepo) Set(key, value string) error {
	return setSetting(DB, key, value)
}

func setSetting(db *gorm.DB, key, value string) error {
	if isPostgreSQLPasswordSetting(key) && value != "" {
		encrypted, err := crypto.Encrypt(value)
		if err != nil {
			return fmt.Errorf("加密配置项 %s 失败", key)
		}
		value = postgresSettingCipherPrefix + encrypted
	}
	// Upsert-like behavior: update if exists, otherwise create
	var existing model.Settings
	result := db.Where(&model.Settings{Key: key}).First(&existing)
	if result.Error == nil {
		existing.Value = value
		return db.Save(&existing).Error
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}
	return db.Create(&model.Settings{Key: key, Value: value}).Error
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

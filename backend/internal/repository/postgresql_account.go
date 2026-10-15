package repository

import (
	"fmt"
	"sort"
	"strings"

	"df-build-server/internal/model"
	"df-build-server/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PostgreSQLAccountRepo struct{}

func NewPostgreSQLAccountRepo() *PostgreSQLAccountRepo { return &PostgreSQLAccountRepo{} }

const postgresSettingCipherPrefix = "enc:v1:"

func isPostgreSQLPasswordSetting(key string) bool {
	return key == "postgresql_password" || key == "postgresql_admin_password"
}

func decodePostgreSQLSetting(key, value string) (string, error) {
	if !isPostgreSQLPasswordSetting(key) || !strings.HasPrefix(value, postgresSettingCipherPrefix) {
		return value, nil
	}
	plain, err := crypto.Decrypt(strings.TrimPrefix(value, postgresSettingCipherPrefix))
	if err != nil {
		return "", fmt.Errorf("配置项 %s 无法解密，请检查平台加密密钥", key)
	}
	return plain, nil
}

func migratePostgreSQLPasswords() error {
	var rows []model.Settings
	if err := DB.Where("key IN ?", []string{"postgresql_password", "postgresql_admin_password"}).Find(&rows).Error; err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			if row.Value == "" || strings.HasPrefix(row.Value, postgresSettingCipherPrefix) {
				continue
			}
			encrypted, err := crypto.Encrypt(row.Value)
			if err != nil {
				return fmt.Errorf("迁移 PostgreSQL 密码加密失败")
			}
			if err := tx.Model(&model.Settings{}).Where("id = ?", row.ID).Update("value", postgresSettingCipherPrefix+encrypted).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PostgreSQLAccountRepo) List() ([]model.PostgreSQLAccountPassword, error) {
	list := []model.PostgreSQLAccountPassword{}
	err := DB.Select("username", "updated_at").Order("username").Find(&list).Error
	return list, err
}

func (r *PostgreSQLAccountRepo) SetPassword(username, password string) error {
	return r.SavePasswords(map[string]string{username: password}, nil)
}

// Save all pending passwords and their connection/mode settings together.
// Unselected accounts retain their existing credentials.
func (r *PostgreSQLAccountRepo) SavePasswords(passwords, settings map[string]string) error {
	usernames := make([]string, 0, len(passwords))
	for name := range passwords {
		usernames = append(usernames, name)
	}
	sort.Strings(usernames)
	rows := make([]model.PostgreSQLAccountPassword, 0, len(usernames))
	for _, name := range usernames {
		encrypted, err := crypto.Encrypt(passwords[name])
		if err != nil {
			return fmt.Errorf("加密账号密码失败")
		}
		rows = append(rows, model.PostgreSQLAccountPassword{Username: name, PasswordEncrypted: encrypted})
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "username"}}, DoUpdates: clause.AssignmentColumns([]string{"password_encrypted", "updated_at"})}).Create(&rows).Error; err != nil {
				return err
			}
		}
		for key, value := range settings {
			if err := setSetting(tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *PostgreSQLAccountRepo) Delete(username string) error {
	return DB.Where("username = ?", username).Delete(&model.PostgreSQLAccountPassword{}).Error
}

func PostgreSQLPasswordMode(mode string) (string, error) {
	switch mode {
	case "", "shared":
		return "shared", nil
	case "individual":
		return "individual", nil
	default:
		return "", fmt.Errorf("业务密码方式无效，请选择所有业务用户同一个密码或业务用户密码不同")
	}
}

// The selected mode exclusively determines which credentials may be used.
func (r *PostgreSQLAccountRepo) ResolvePassword(username, common, mode string) (string, string, error) {
	mode, err := PostgreSQLPasswordMode(mode)
	if err != nil {
		return "", "", err
	}
	if mode == "shared" {
		if strings.TrimSpace(common) == "" {
			return "", "common", fmt.Errorf("统一业务密码未配置")
		}
		return common, "common", nil
	}
	var row model.PostgreSQLAccountPassword
	result := DB.Where("username = ?", username).Limit(1).Find(&row)
	if result.Error != nil {
		return "", "", fmt.Errorf("读取账号 %s 的密码配置失败", username)
	}
	if result.RowsAffected == 0 {
		return "", "override", fmt.Errorf("业务用户密码不同：账号 %s 尚未配置密码", username)
	}
	password, err := crypto.Decrypt(row.PasswordEncrypted)
	if err != nil || password == "" {
		return "", "override", fmt.Errorf("账号 %s 的专用密码无法解密，请重新配置", username)
	}
	return password, "override", nil
}

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

// Credentials are request-scoped. Never put this in SQLExecuteOptions, file
// snapshots or audit JSON: those objects are persisted for queue/history use.
type SQLRetryExecutionAccount struct {
	Username       string `json:"username"`
	PasswordSource string `json:"passwordSource"`
	Password       string `json:"password"`
}

type SQLRetryAccountChoice struct {
	Username       string `json:"username"`
	PasswordSource string `json:"passwordSource"`
}

func (s *PostgreSQLService) ListSQLRetryAccounts() ([]SQLRetryAccountChoice, error) {
	accounts, err := repository.NewPostgreSQLAccountRepo().List()
	if err != nil {
		return nil, err
	}
	mode, err := s.settingsRepo.Read("postgresql_password_mode", false)
	if err != nil {
		return nil, err
	}
	mode, err = repository.PostgreSQLPasswordMode(mode)
	if err != nil {
		return nil, err
	}
	choices := map[string]string{}
	for _, account := range accounts {
		if account.Username != "postgres" {
			choices[account.Username] = "override"
		}
	}
	if mode == "shared" {
		common, err := s.settingsRepo.Read("postgresql_password", false)
		if err != nil {
			return nil, err
		}
		if common != "" {
			target, err := currentReleaseSQLTarget()
			if err != nil {
				return nil, err
			}
			var names []string
			if err := repository.DB.Model(&model.SQLChangeFile{}).Distinct("schema_name").
				Where("is_deleted = false AND database_target = ? AND schema_name <> ''", target).Pluck("schema_name", &names).Error; err != nil {
				return nil, err
			}
			for _, name := range names {
				if name != "postgres" {
					choices[name] = "common"
				}
			}
			for name := range choices {
				choices[name] = "common"
			}
		} else {
			choices = map[string]string{}
		}
	}
	adminPassword, err := s.settingsRepo.Read("postgresql_admin_password", false)
	if err != nil {
		return nil, err
	}
	if adminPassword != "" {
		choices["postgres"] = "admin"
	}
	list := make([]SQLRetryAccountChoice, 0, len(choices))
	for name, source := range choices {
		list = append(list, SQLRetryAccountChoice{Username: name, PasswordSource: source})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return list, nil
}

func (s *PostgreSQLService) loadSQLRetryAccountConfig(account SQLRetryExecutionAccount) (PostgreSQLConfig, error) {
	name := strings.TrimSpace(account.Username)
	if name == "" || len(name) > 63 || validateSchemaName(name) != nil {
		return PostgreSQLConfig{}, fmt.Errorf("请输入有效的数据库账号")
	}
	if account.PasswordSource != "configured" && account.PasswordSource != "manual" {
		return PostgreSQLConfig{}, fmt.Errorf("请选择使用已配置密码或临时输入密码")
	}
	var cfg PostgreSQLConfig
	for _, item := range []struct {
		key      string
		value    *string
		required bool
	}{
		{"postgresql_host", &cfg.Host, true}, {"postgresql_port", &cfg.Port, false},
		{"postgresql_database", &cfg.Database, true},
	} {
		value, err := s.settingsRepo.Read(item.key, item.required)
		if err != nil {
			return cfg, err
		}
		*item.value = value
	}
	if cfg.Port == "" {
		cfg.Port = "5432"
	}
	cfg.User = name
	if account.PasswordSource == "manual" {
		if account.Password == "" || len(account.Password) > 16384 || strings.ContainsRune(account.Password, '\x00') {
			return cfg, fmt.Errorf("请填写有效的临时密码")
		}
		cfg.Password = account.Password
		return cfg, nil
	}
	// postgres uses the independently configured administrator password. Never
	// substitute the common business password for administrator credentials.
	if name == "postgres" {
		var err error
		cfg.Password, err = s.settingsRepo.Read("postgresql_admin_password", true)
		return cfg, err
	}
	mode, err := s.settingsRepo.Read("postgresql_password_mode", false)
	if err != nil {
		return cfg, err
	}
	mode, err = repository.PostgreSQLPasswordMode(mode)
	if err != nil {
		return cfg, err
	}
	common := ""
	if mode == "shared" {
		common, err = s.settingsRepo.Read("postgresql_password", true)
		if err != nil {
			return cfg, err
		}
	}
	cfg.Password, _, err = repository.NewPostgreSQLAccountRepo().ResolvePassword(name, common, mode)
	return cfg, err
}

// Check login only. A fully qualified cross-schema SQL can be valid even if
// the chosen user lacks USAGE on the file's default schema. PostgreSQL decides
// the required object privileges when the explicitly selected retry executes.
func verifySQLRetryAccount(ctx context.Context, cfg PostgreSQLConfig) error {
	db, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var actual string
	if err := db.QueryRowContext(probe, "SELECT current_user").Scan(&actual); err != nil {
		return fmt.Errorf("账号 %s 的连接校验失败，未提交重试: %w", cfg.User, err)
	}
	if actual != cfg.User {
		return fmt.Errorf("连接后的数据库账号为 %s，与选择的 %s 不同，未提交重试", actual, cfg.User)
	}
	return nil
}

func recordSQLRetrySkippedAccountSetting(tx *gorm.DB, file *model.SQLChangeFile, row model.SQLChangeStatement, cfg PostgreSQLConfig, username string) error {
	record := newSQLStatementRecord(file, row, "SESSION_RESTORE", username)
	no, now := false, time.Now()
	record.Submitted, record.FinishedAt, record.Status = &no, &now, "SKIPPED"
	record.DatabaseHost, record.DatabasePort, record.DatabaseName, record.DatabaseUser = cfg.Host, cfg.Port, cfg.Database, cfg.User
	record.TransactionOutcome = ""
	record.Message = fmt.Sprintf("切换账号重试：不恢复此前的角色／会话身份设置，使用本次选择的连接账号 %s；原语句结果保留", cfg.User)
	return tx.Create(&record).Error
}

func recordSQLRetryAccount(tx *gorm.DB, file *model.SQLChangeFile, row model.SQLChangeStatement, cfg PostgreSQLConfig, username string) error {
	record := newSQLStatementRecord(file, row, "RETRY_ACCOUNT", username)
	no, now := false, time.Now()
	record.Submitted, record.FinishedAt, record.Status = &no, &now, "PENDING"
	record.DatabaseHost, record.DatabasePort, record.DatabaseName, record.DatabaseUser = cfg.Host, cfg.Port, cfg.Database, cfg.User
	previous := row.DatabaseUser
	if previous == "" {
		previous = file.SchemaName
	}
	record.Message = fmt.Sprintf("本次重试连接账号：%s → %s；保留文件 schema %s；后续文件执行仍使用原配置", previous, cfg.User, file.SchemaName)
	record.TransactionOutcome = ""
	return tx.Create(&record).Error
}

package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/repository"
)

type PostgreSQLBusinessAccount struct {
	Username    string `json:"username"`
	HasOverride bool   `json:"hasOverride"`
	Discovered  bool   `json:"discovered"`
}

type PostgreSQLAccountTestResult struct {
	Username       string `json:"username"`
	PasswordSource string `json:"passwordSource"`
	Success        bool   `json:"success"`
	Message        string `json:"message"`
}

type PostgreSQLConnectionTestReport struct {
	Success  bool                          `json:"success"`
	Message  string                        `json:"message"`
	Results  []PostgreSQLAccountTestResult `json:"results"`
	Accounts []PostgreSQLBusinessAccount   `json:"accounts"`
}

func ValidatePostgreSQLBusinessUsername(username string) error {
	if !strings.HasPrefix(username, "df_") || len(username) > 63 || !validSchemaNameRegex.MatchString(username) {
		return fmt.Errorf("业务账号必须是 df_ 开头的有效账号名")
	}
	return nil
}

func PostgreSQLAdminConfig(values map[string]string) PostgreSQLConfig {
	cfg := PostgreSQLConfig{Host: values["postgresql_host"], Port: values["postgresql_port"], Database: values["postgresql_database"], User: "postgres", Password: values["postgresql_admin_password"]}
	if cfg.Port == "" {
		cfg.Port = "5432"
	}
	if cfg.Database == "" {
		cfg.Database = "postgres"
	}
	return cfg
}

func probePostgreSQL(ctx context.Context, cfg PostgreSQLConfig) (*sql.DB, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("PostgreSQL 主机地址未填写")
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("账号 %s 的密码未配置", cfg.User)
	}
	db, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(probeCtx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func discoverPostgreSQLAccounts(ctx context.Context, db *sql.DB) ([]PostgreSQLBusinessAccount, error) {
	rows, err := db.QueryContext(ctx, "SELECT rolname FROM pg_roles WHERE rolcanlogin AND left(rolname, 3) = 'df_' ORDER BY rolname")
	if err != nil {
		return nil, fmt.Errorf("读取 df_ 业务账号失败: %w", err)
	}
	defer rows.Close()
	accounts := make(map[string]PostgreSQLBusinessAccount)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		accounts[name] = PostgreSQLBusinessAccount{Username: name, Discovered: true}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	overrides, err := repository.NewPostgreSQLAccountRepo().List()
	if err != nil {
		return nil, fmt.Errorf("读取专用密码配置失败")
	}
	for _, row := range overrides {
		item := accounts[row.Username]
		item.Username, item.HasOverride = row.Username, true
		accounts[row.Username] = item
	}
	list := make([]PostgreSQLBusinessAccount, 0, len(accounts))
	for _, item := range accounts {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Username < list[j].Username })
	return list, nil
}

func DiscoverPostgreSQLAccounts(ctx context.Context, values map[string]string) ([]PostgreSQLBusinessAccount, error) {
	mode, err := repository.PostgreSQLPasswordMode(values["postgresql_password_mode"])
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	db, err := probePostgreSQL(ctx, PostgreSQLAdminConfig(values))
	if err != nil {
		return nil, fmt.Errorf("管理员连接失败: %w", err)
	}
	defer db.Close()
	accounts, err := discoverPostgreSQLAccounts(ctx, db)
	return postgresAccountsForMode(accounts, mode), err
}

func postgresAccountsForMode(accounts []PostgreSQLBusinessAccount, mode string) []PostgreSQLBusinessAccount {
	if mode == "individual" {
		return accounts
	}
	filtered := make([]PostgreSQLBusinessAccount, 0, len(accounts))
	for _, account := range accounts {
		if account.Discovered {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

func TestPostgreSQLBusinessAccount(ctx context.Context, values map[string]string, username string) PostgreSQLAccountTestResult {
	result := PostgreSQLAccountTestResult{Username: username}
	if err := ValidatePostgreSQLBusinessUsername(username); err != nil {
		result.Message = err.Error()
		return result
	}
	cfg := PostgreSQLAdminConfig(values)
	cfg.User = username
	password, source, err := repository.NewPostgreSQLAccountRepo().ResolvePassword(username, values["postgresql_password"], values["postgresql_password_mode"])
	result.PasswordSource = source
	if err != nil {
		result.Message = err.Error()
		return result
	}
	cfg.Password = password
	db, err := probePostgreSQL(ctx, cfg)
	if err != nil {
		result.Message = "连接失败: " + err.Error()
		return result
	}
	db.Close()
	result.Success, result.Message = true, "连接成功"
	return result
}

// Return detailed failures as data so one failed account does not hide others.
func TestPostgreSQLConnections(ctx context.Context, values map[string]string) PostgreSQLConnectionTestReport {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	report := PostgreSQLConnectionTestReport{Results: []PostgreSQLAccountTestResult{}, Accounts: []PostgreSQLBusinessAccount{}}
	mode, err := repository.PostgreSQLPasswordMode(values["postgresql_password_mode"])
	if err != nil {
		report.Message = err.Error()
		return report
	}
	admin := PostgreSQLAccountTestResult{Username: "postgres", PasswordSource: "admin"}
	db, err := probePostgreSQL(ctx, PostgreSQLAdminConfig(values))
	if err != nil {
		admin.Message = "管理员连接失败: " + err.Error()
		report.Results = append(report.Results, admin)
		report.Message = admin.Message
		return report
	}
	defer db.Close()
	admin.Success, admin.Message = true, "管理员连接成功"
	report.Results = append(report.Results, admin)
	accounts, err := discoverPostgreSQLAccounts(ctx, db)
	if err != nil {
		report.Message = err.Error()
		return report
	}
	accounts = postgresAccountsForMode(accounts, mode)
	report.Accounts = accounts
	results := make([]PostgreSQLAccountTestResult, len(accounts))
	slots := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			results[i] = TestPostgreSQLBusinessAccount(ctx, values, name)
		}(i, account.Username)
	}
	wg.Wait()
	report.Results = append(report.Results, results...)
	failed := 0
	for _, r := range results {
		if !r.Success {
			failed++
		}
	}
	report.Success = failed == 0
	if len(accounts) == 0 {
		report.Message = "管理员连接成功，未发现 df_ 开头且允许登录的业务账号"
	} else {
		report.Message = fmt.Sprintf("管理员连接成功；业务账号 %d 个，成功 %d 个，失败 %d 个", len(accounts), len(accounts)-failed, failed)
	}
	return report
}

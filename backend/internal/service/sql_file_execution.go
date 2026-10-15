package service

import (
	"context"
	"database/sql"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"errors"
	"fmt"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"gorm.io/gorm"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const releaseSQLStatementTimeout = 60 * time.Second

func parseReleaseSQLFileName(name string) (string, int, string, error) {
	invalid := fmt.Errorf("SQL 文件名格式应为 版本__排序号__schema.sql，例如 V2.0.0__001__his.sql")
	if name != strings.TrimSpace(name) || filepath.Base(name) != name || !strings.EqualFold(filepath.Ext(name), ".sql") {
		return "", 0, "", invalid
	}
	parts := strings.Split(name, "__")
	if len(parts) < 3 || strings.TrimSpace(parts[0]) == "" {
		return "", 0, "", invalid
	}
	order, err := strconv.Atoi(parts[1])
	schema := strings.SplitN(parts[2], ".", 2)[0]
	if err != nil || order < 0 || schema == "" || len(schema) > 63 || validateSchemaName(schema) != nil {
		return "", 0, "", invalid
	}
	return parts[0], order, schema, nil
}

func splitReleaseSQL(content string) ([]model.SQLChangeStatement, error) {
	parts, err := pg_query.SplitWithScanner(content, true)
	if err != nil {
		return nil, fmt.Errorf("SQL 拆分失败: %w", err)
	}
	items := make([]model.SQLChangeStatement, 0, len(parts))
	cursor := 0
	for _, raw := range parts {
		offset := strings.Index(content[cursor:], raw)
		if offset < 0 {
			return nil, fmt.Errorf("无法定位 SQL 原始行号")
		}
		start := cursor + offset
		cursor = start + len(raw)
		leading := sqlLeadingTriviaLength(raw)
		text := strings.TrimSpace(strings.TrimSuffix(raw[leading:], ";"))
		if text == "" {
			continue
		}
		items = append(items, model.SQLChangeStatement{LineNumber: 1 + strings.Count(content[:start+leading], "\n"), SQLContent: text, ExecuteStatus: "PENDING"})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("SQL 内容为空")
	}
	return items, nil
}

// Keep SQL text intact; skip only whitespace and comments before the statement.
func sqlLeadingTriviaLength(s string) int {
	i := 0
	for i < len(s) {
		switch {
		case strings.ContainsRune(" \t\r\n", rune(s[i])):
			i++
		case strings.HasPrefix(s[i:], "--"):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case strings.HasPrefix(s[i:], "/*"):
			i += 2
			depth := 1
			for i < len(s) && depth > 0 {
				if strings.HasPrefix(s[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(s[i:], "*/") {
					depth--
					i += 2
				} else {
					i++
				}
			}
		default:
			return i
		}
	}
	return i
}

func (s *PostgreSQLService) loadReleaseSQLConfig(schema string) (PostgreSQLConfig, error) {
	if schema == "" || validateSchemaName(schema) != nil {
		return PostgreSQLConfig{}, fmt.Errorf("SQL 文件缺少有效 schema，请按 版本__排序号__schema.sql 重新上传")
	}
	var cfg PostgreSQLConfig
	mode, err := s.settingsRepo.Read("postgresql_password_mode", false)
	if err != nil {
		return cfg, err
	}
	mode, err = repository.PostgreSQLPasswordMode(mode)
	if err != nil {
		return cfg, err
	}
	for _, setting := range []struct {
		key      string
		value    *string
		required bool
	}{
		{"postgresql_host", &cfg.Host, true}, {"postgresql_port", &cfg.Port, false},
		{"postgresql_database", &cfg.Database, true},
	} {
		value, err := s.settingsRepo.Read(setting.key, setting.required)
		if err != nil {
			return cfg, err
		}
		*setting.value = value
	}
	if cfg.Port == "" {
		cfg.Port = "5432"
	}
	cfg.User = schema
	if mode == "shared" {
		cfg.Password, err = s.settingsRepo.Read("postgresql_password", true)
		if err != nil {
			return cfg, err
		}
	}
	password, _, err := repository.NewPostgreSQLAccountRepo().ResolvePassword(schema, cfg.Password, mode)
	if err != nil {
		return cfg, err
	}
	cfg.Password = password
	return cfg, nil
}

func openReleaseSQLDatabase(cfg PostgreSQLConfig) (*sql.DB, error) {
	u := url.URL{Scheme: "postgresql", User: url.UserPassword(cfg.User, cfg.Password)}
	q := url.Values{"host": {cfg.Host}, "port": {cfg.Port}, "dbname": {cfg.Database}, "sslmode": {"disable"}, "connect_timeout": {"5"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}

func saveReleaseSQLFile(file *model.SQLChangeFile, status, message, username string) error {
	now := time.Now()
	file.ExecuteStatus = status
	file.ExecuteMessage = message
	file.ExecuteUser = username
	file.ExecuteTime = &now
	result := repository.DB.Model(&model.SQLChangeFile{}).Where("id = ? AND is_deleted = false AND execute_status = ?", file.ID, "RUNNING").Updates(map[string]any{"execute_status": status, "execute_message": message, "execute_user": username, "execute_time": now})
	if result.Error != nil {
		return fmt.Errorf("保存 SQL 文件 %s 执行状态失败: %w", file.FileName, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("SQL 文件执行权已失效，停止执行")
	}
	return nil
}

// Ordinary release SQL uses the schema account, one connection per file and
// per-statement autocommit. No risk analysis, rewriting or automatic owner fix.
func (s *PostgreSQLService) runReleaseSQLFile(ctx context.Context, file *model.SQLChangeFile, statements []model.SQLChangeStatement, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	failed := func(err error) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
		message := fmt.Sprintf("文件 %s 执行失败: %v", file.FileName, err)
		if saveErr := saveReleaseSQLFile(file, "FAILED", message, username); saveErr != nil {
			return file, statements, errors.Join(err, saveErr)
		}
		return file, statements, nil
	}
	remaining := 0
	for _, stmt := range statements {
		if stmt.ExecuteStatus != "SUCCESS" && stmt.ExecuteStatus != "SKIPPED" {
			remaining++
		}
	}
	if remaining == 0 {
		return file, statements, saveReleaseSQLFile(file, "SUCCESS", "文件内所有 SQL 已成功或跳过", username)
	}
	cfg, err := s.loadReleaseSQLConfig(file.SchemaName)
	if err != nil {
		return failed(err)
	}
	db, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		return failed(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return failed(fmt.Errorf("连接数据库 %s（账号/schema：%s）失败: %w", cfg.Database, cfg.User, err))
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT set_config('search_path', $1, false)", quoteIdent(file.SchemaName)); err != nil {
		return failed(err)
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '60s'"); err != nil {
		return failed(err)
	}
	for i := range statements {
		stmt := &statements[i]
		if stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED" {
			continue
		}
		if ctx.Err() != nil {
			return file, statements, saveReleaseSQLFile(file, "CANCELED", "执行已停止，已成功提交的 SQL 不会撤销", username)
		}
		progress := fmt.Sprintf("数据库 %s，账号/schema %s，文件 %s，第 %d/%d 句（原文件第 %d 行）", cfg.Database, cfg.User, file.FileName, i+1, len(statements), stmt.LineNumber)
		if err := saveReleaseSQLFile(file, "RUNNING", progress, username); err != nil {
			return file, statements, err
		}
		stmt.ExecuteStatus = "RUNNING"
		stmt.ExecuteMessage = "执行中"
		stmt.ExecuteUser = username
		if err := repository.DB.Save(stmt).Error; err != nil {
			return failed(fmt.Errorf("第 %d 行执行前保存记录失败，未执行本句: %w", stmt.LineNumber, err))
		}
		started := time.Now()
		// Stop at a statement boundary: let the current statement return its result.
		execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseSQLStatementTimeout+5*time.Second)
		result, execErr := conn.ExecContext(execCtx, stmt.SQLContent)
		cancel()
		now := time.Now()
		stmt.ExecuteTime = &now
		stmt.DurationMs = time.Since(started).Milliseconds()
		stmt.SQLState = pgState(execErr)
		stmt.AffectedRows = 0
		stmt.ExecuteStatus = "SUCCESS"
		stmt.ExecuteMessage = "执行成功"
		if execErr == nil {
			stmt.AffectedRows, _ = result.RowsAffected()
		} else if skip, message := skipMessageForSQLError(execErr, options); skip {
			stmt.ExecuteStatus = "SKIPPED"
			stmt.ExecuteMessage = message
		} else {
			stmt.ExecuteStatus = "FAILED"
			stmt.ExecuteMessage = execErr.Error()
			if stmt.SQLState == "" {
				stmt.ExecuteStatus = "UNKNOWN"
				stmt.ExecuteMessage = "数据库未返回确定结果，请核实后处理: " + execErr.Error()
			}
		}
		if err := repository.DB.Save(stmt).Error; err != nil {
			saveErr := fmt.Errorf("第 %d 行已提交执行，但保存执行结果失败，请核实数据库结果，不能直接重试: %w", stmt.LineNumber, err)
			stmt.ExecuteStatus = "UNKNOWN"
			stmt.ExecuteMessage = saveErr.Error()
			if retryErr := repository.DB.Save(stmt).Error; retryErr != nil {
				saveErr = errors.Join(saveErr, retryErr)
			}
			return failed(saveErr)
		}
		if stmt.ExecuteStatus == "FAILED" || stmt.ExecuteStatus == "UNKNOWN" {
			return failed(fmt.Errorf("第 %d 行 SQL：%s\nSQLSTATE：%s\n%s；后续语句未执行", stmt.LineNumber, stmt.SQLContent, stmt.SQLState, stmt.ExecuteMessage))
		}
	}
	success, skipped := 0, 0
	for _, stmt := range statements {
		if stmt.ExecuteStatus == "SUCCESS" {
			success++
		}
		if stmt.ExecuteStatus == "SKIPPED" {
			skipped++
		}
	}
	return file, statements, saveReleaseSQLFile(file, "SUCCESS", fmt.Sprintf("执行完成：成功 %d，跳过 %d", success, skipped), username)
}

// Startup only: interrupted SQL must not be replayed automatically.
func RecoverInterruptedSQLFiles() error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.SQLChangeStatement{}).Where("execute_status = ?", "RUNNING").Updates(map[string]any{"execute_status": "UNKNOWN", "execute_message": "服务中断，执行结果待核实；确认已生效后可手工跳过"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeFile{}).Where("execute_status = ?", "RUNNING").Updates(map[string]any{"execute_status": "FAILED", "execute_message": "服务中断，执行已停止，请查看语句执行结果"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLViewDependencyTask{}).Where("status IN ?", []string{"EXECUTING", "RESTORING", "VERIFYING"}).Updates(map[string]any{"status": "UNKNOWN", "outcome_uncertain": true, "execute_message": "服务中断，字段变更及视图结果待核实；请核实并恢复对象，不能直接重放变更"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLViewDependencyTask{}).Where("status IN ?", []string{"ANALYZING", "PRECHECKING"}).Updates(map[string]any{"status": "FAILED", "execute_message": "服务中断，分析或预检未完成"}).Error; err != nil {
			return err
		}
		return tx.Model(&model.SQLViewDependencyStep{}).Where("status = ?", "RUNNING").Updates(map[string]any{"status": "UNKNOWN", "error_message": "服务中断，步骤结果待核实"}).Error
	})
}

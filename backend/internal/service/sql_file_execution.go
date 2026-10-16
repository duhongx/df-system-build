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

// Upload accepts SQL filenames independently of execution metadata.
func releaseSQLUploadMetadata(name string) (string, int, string, error) {
	if strings.TrimSpace(name) == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\\\x00") || !strings.EqualFold(filepath.Ext(name), ".sql") {
		return "", 0, "", fmt.Errorf("请选择 SQL 文件")
	}
	if len([]rune(name)) > 255 {
		return "", 0, "", fmt.Errorf("SQL 文件名超过 255 个字符")
	}
	parts := strings.Split(strings.TrimSuffix(name, filepath.Ext(name)), "__")
	version, order, schema := "", 0, ""
	if len(parts) >= 2 {
		if len([]rune(parts[0])) <= 64 {
			version = parts[0]
		}
		if value, err := strconv.Atoi(parts[1]); err == nil && value >= 0 {
			order = value
		}
	}
	if len(parts) >= 3 {
		candidate := strings.SplitN(parts[2], ".", 2)[0]
		if candidate != "" && len(candidate) <= 63 && validateSchemaName(candidate) == nil {
			schema = candidate
		}
	}
	return version, order, schema, nil
}

func releaseSQLExecutionIssue(file *model.SQLChangeFile, statements []model.SQLChangeStatement) error {
	if file.SchemaName == "" || validateSchemaName(file.SchemaName) != nil {
		return fmt.Errorf("无法识别执行账号/schema；执行时会过滤此文件，可标记不执行或删除")
	}
	if len(statements) == 0 {
		if _, err := splitReleaseSQL(file.FileContent); err != nil {
			return err
		}
		return fmt.Errorf("SQL 文件没有可执行的语句")
	}
	return validateReleaseSQLTransactions(statements)
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
		return PostgreSQLConfig{}, fmt.Errorf("无法识别执行账号/schema，可标记不执行或删除此文件")
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
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.SQLChangeFile{}).Where("id = ? AND is_deleted = false AND execute_status = ?", file.ID, "RUNNING").Updates(map[string]any{"execute_status": status, "execute_message": message, "execute_user": username, "execute_time": now})
		if result.Error != nil {
			return fmt.Errorf("保存 SQL 文件 %s 执行状态失败: %w", file.FileName, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("SQL 文件执行权已失效，停止执行")
		}
		return updateSQLExecutionSnapshot(tx, file)
	})
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
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return failed(err)
	}
	if err := requireReleaseSQLTarget(file, target); err != nil {
		return failed(err)
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
	if err := requireReleaseSQLTarget(file, releaseSQLTarget(cfg)); err != nil {
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
		restoreSession := stmt.ExecuteStatus == "SUCCESS" && isReleaseSQLSessionSetting(stmt.SQLContent)
		if (stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED") && !restoreSession {
			continue
		}
		stop, stopErr := releaseSQLBatchStopped(ctx, file.ExecutionBatchID)
		if stopErr != nil {
			return failed(stopErr)
		}
		if stop {
			return file, statements, saveReleaseSQLFile(file, "CANCELED", "执行已停止，已成功提交的 SQL 不会撤销", username)
		}
		if restoreSession {
			if err := restoreReleaseSQLSession(ctx, conn, file, *stmt, cfg, username); err != nil {
				return failed(err)
			}
			continue
		}
		progress := fmt.Sprintf("数据库 %s，账号/schema %s，文件 %s，第 %d/%d 句（原文件第 %d 行）", cfg.Database, cfg.User, file.FileName, i+1, len(statements), stmt.LineNumber)
		if err := saveReleaseSQLFile(file, "RUNNING", progress, username); err != nil {
			return file, statements, err
		}
		before := *stmt
		record := newSQLStatementRecord(file, before, "EXECUTE", username)
		record.BatchID = file.ExecutionBatchID
		record.DatabaseHost, record.DatabasePort, record.DatabaseName, record.DatabaseUser = cfg.Host, cfg.Port, cfg.Database, cfg.User
		record.Status = "RUNNING"
		stmt.ExecuteStatus, stmt.ExecuteMessage, stmt.ExecuteUser, stmt.SkipKind = "RUNNING", "执行中", username, ""
		if err := repository.DB.Transaction(func(tx *gorm.DB) error {
			if err := ensureLegacySQLStatementRecord(tx, file, before); err != nil {
				return err
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			return tx.Save(stmt).Error
		}); err != nil {
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
		skip, skipMessage := false, ""
		if execErr != nil {
			skip, skipMessage = skipReleaseSQLError(stmt.SQLContent, execErr, options)
		}
		var indexErr error
		if execErr == nil || (skip && stmt.SQLState == "42P07") {
			indexErr = validateReleaseSQLIndex(ctx, conn, stmt.SQLContent)
		}
		if indexErr != nil {
			stmt.ExecuteStatus, stmt.ExecuteMessage = "FAILED", indexErr.Error()
			if execErr != nil {
				stmt.ExecuteMessage = execErr.Error() + "；" + stmt.ExecuteMessage
			}
		} else if execErr == nil {
			stmt.AffectedRows, _ = result.RowsAffected()
		} else if skip {
			stmt.ExecuteStatus = "SKIPPED"
			stmt.SkipKind = "AUTO"
			stmt.ExecuteMessage = skipMessage
		} else {
			stmt.ExecuteStatus = "FAILED"
			stmt.ExecuteMessage = execErr.Error()
			if stmt.SQLState == "" {
				stmt.ExecuteStatus = "UNKNOWN"
				stmt.ExecuteMessage = "数据库未返回确定结果，请核实后处理: " + execErr.Error()
			}
		}
		record.Status, record.Message, record.SQLState, record.FinishedAt = stmt.ExecuteStatus, stmt.ExecuteMessage, stmt.SQLState, &now
		record.AffectedRows, record.DurationMs, record.SkipKind = stmt.AffectedRows, stmt.DurationMs, stmt.SkipKind
		if stmt.SkipKind == "AUTO" {
			record.Action = "AUTO_SKIP"
		}
		if stmt.ExecuteStatus != "UNKNOWN" {
			yes := true
			record.Submitted = &yes
		}
		if err := repository.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(stmt).Error; err != nil {
				return err
			}
			return tx.Save(&record).Error
		}); err != nil {
			saveErr := fmt.Errorf("第 %d 行已提交执行，但保存执行结果失败，请核实数据库结果，不能直接重试: %w", stmt.LineNumber, err)
			stmt.ExecuteStatus, stmt.ExecuteMessage = "UNKNOWN", saveErr.Error()
			record.Status, record.Message = "UNKNOWN", saveErr.Error()
			if retryErr := repository.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Save(stmt).Error; err != nil {
					return err
				}
				return tx.Save(&record).Error
			}); retryErr != nil {
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
		var interrupted []model.SQLChangeFile
		if err := tx.Where("execute_status IN ?", []string{"RUNNING", "QUEUED"}).Find(&interrupted).Error; err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&model.SQLStatementRecord{}).Where("status = ?", "RUNNING").Updates(map[string]any{"status": "UNKNOWN", "message": "服务中断，执行结果待核实", "finished_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeFile{}).Where("execute_status = ?", "QUEUED").Updates(map[string]any{"execute_status": "CANCELED", "execute_message": "服务中断，等待中的文件未执行"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeBatch{}).Where("execute_status = ? AND file_ids_json <> ''", "RUNNING").Updates(map[string]any{"execute_status": "FAILED", "execute_message": "服务中断，队列已停止，请核实后重新选择文件执行"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeStatement{}).Where("execute_status = ?", "RUNNING").Updates(map[string]any{"execute_status": "UNKNOWN", "execute_message": "服务中断，执行结果待核实；确认已生效后可手工跳过"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeFile{}).Where("execute_status = ?", "RUNNING").Updates(map[string]any{"execute_status": "FAILED", "execute_message": "服务中断，执行已停止，请查看语句执行结果"}).Error; err != nil {
			return err
		}
		for _, before := range interrupted {
			var file model.SQLChangeFile
			if err := tx.First(&file, before.ID).Error; err != nil {
				return err
			}
			if err := updateSQLExecutionSnapshot(tx, &file); err != nil {
				return err
			}
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

// Ordinary scripts use a narrow allowlist, not a global suppression by SQLSTATE.
func skipReleaseSQLError(sqlText string, err error, options SQLExecuteOptions) (bool, string) {
	tree, parseErr := pg_query.Parse(sqlText)
	if parseErr != nil || len(tree.Stmts) != 1 {
		return false, ""
	}
	stmt := tree.Stmts[0].Stmt
	code := pgState(err)
	switch {
	case stmt.GetInsertStmt() != nil && options.SkipUniqueConstraint && code == "23505":
		return true, "配置数据已存在"
	case stmt.GetCreateStmt() != nil && options.SkipExistsTable && code == "42P07":
		return true, "表已存在"
	case stmt.GetIndexStmt() != nil && options.SkipExistsTable && code == "42P07":
		return true, "索引已存在"
	case stmt.GetAlterTableStmt() != nil && options.SkipExistsColumn && code == "42701":
		if len(stmt.GetAlterTableStmt().Cmds) != 1 {
			return false, ""
		}
		for _, cmd := range stmt.GetAlterTableStmt().Cmds {
			if cmd.GetAlterTableCmd() == nil || cmd.GetAlterTableCmd().Subtype != pg_query.AlterTableType_AT_AddColumn {
				return false, ""
			}
		}
		return true, "字段已存在"
	case stmt.GetDropStmt() != nil && stmt.GetDropStmt().RemoveType == pg_query.ObjectType_OBJECT_VIEW && len(stmt.GetDropStmt().Objects) == 1 && code == "42P01":
		return true, "视图不存在"
	}
	return false, ""
}

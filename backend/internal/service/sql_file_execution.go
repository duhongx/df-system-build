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
	return validateReleaseSQLTransactionStates(statements)
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
	// The managed PostgreSQL instance requires TLS (hostssl rules in
	// pg_hba.conf). Keep this connection encrypted for admin probes, business
	// account checks, and SQL execution.
	q := url.Values{"host": {cfg.Host}, "port": {cfg.Port}, "dbname": {cfg.Database}, "sslmode": {"require"}, "connect_timeout": {"5"}}
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

const releaseSQLViewPairRollbackMessage = "视图组合事务失败：DROP VIEW 已回滚，原视图保持不变"

func releaseSQLNode(sqlText string) *pg_query.Node {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || len(tree.Stmts) != 1 {
		return nil
	}
	return tree.Stmts[0].Stmt
}

func releaseSQLViewName(parts []string, defaultSchema string) (string, string, bool) {
	switch len(parts) {
	case 1:
		return defaultSchema, parts[0], defaultSchema != "" && parts[0] != ""
	case 2:
		return parts[0], parts[1], parts[0] != "" && parts[1] != ""
	default:
		return "", "", false
	}
}

func releaseSQLDropViewName(node *pg_query.Node) ([]string, bool) {
	drop := node.GetDropStmt()
	if drop == nil || drop.GetRemoveType() != pg_query.ObjectType_OBJECT_VIEW || drop.GetConcurrent() || len(drop.GetObjects()) != 1 {
		return nil, false
	}
	list := drop.GetObjects()[0].GetList()
	if list == nil || len(list.GetItems()) == 0 {
		return nil, false
	}
	parts := make([]string, 0, len(list.GetItems()))
	for _, item := range list.GetItems() {
		ident := item.GetString_()
		if ident == nil || ident.GetSval() == "" {
			return nil, false
		}
		parts = append(parts, ident.GetSval())
	}
	return parts, true
}

func isReleaseSQLViewPair(drop, create model.SQLChangeStatement, schema string) bool {
	dropParts, ok := releaseSQLDropViewName(releaseSQLNode(drop.SQLContent))
	if !ok {
		return false
	}
	createNode := releaseSQLNode(create.SQLContent)
	view := createNode.GetViewStmt()
	if view == nil || view.GetView() == nil || view.GetView().GetCatalogname() != "" {
		return false
	}
	dropSchema, dropName, ok := releaseSQLViewName(dropParts, schema)
	if !ok {
		return false
	}
	createSchema := view.GetView().GetSchemaname()
	if createSchema == "" {
		createSchema = schema
	}
	return createSchema == dropSchema && view.GetView().GetRelname() == dropName
}

func releaseSQLViewPairIndex(statements []model.SQLChangeStatement, index int, schema string) int {
	if index < 0 || index >= len(statements) {
		return -1
	}
	if index > 0 && isReleaseSQLViewPair(statements[index-1], statements[index], schema) {
		return index - 1
	}
	if index+1 < len(statements) && isReleaseSQLViewPair(statements[index], statements[index+1], schema) {
		return index + 1
	}
	return -1
}

// executeReleaseSQLViewPair executes a matching DROP VIEW and CREATE VIEW as
// one PostgreSQL transaction, so a failed create restores the prior view.
func executeReleaseSQLViewPair(ctx context.Context, file *model.SQLChangeFile, drop, create *model.SQLChangeStatement, conn *sql.Conn, cfg PostgreSQLConfig, username string, index, total int) error {
	progress := fmt.Sprintf("数据库 %s，连接账号 %s，schema %s，文件 %s，第 %d-%d/%d 句（视图 DROP + CREATE 事务）", cfg.Database, cfg.User, file.SchemaName, file.FileName, index+1, index+2, total)
	if err := saveReleaseSQLFile(file, "RUNNING", progress, username); err != nil {
		return err
	}
	beforeDrop, beforeCreate := *drop, *create
	dropRecord := newSQLStatementRecord(file, beforeDrop, "EXECUTE", username)
	createRecord := newSQLStatementRecord(file, beforeCreate, "EXECUTE", username)
	for _, record := range []*model.SQLStatementRecord{&dropRecord, &createRecord} {
		record.BatchID = file.ExecutionBatchID
		record.DatabaseHost, record.DatabasePort, record.DatabaseName, record.DatabaseUser = cfg.Host, cfg.Port, cfg.Database, cfg.User
		record.Status = "RUNNING"
	}
	drop.DatabaseUser = cfg.User
	drop.ExecuteStatus, drop.ExecuteMessage, drop.ExecuteUser, drop.SkipKind = "RUNNING", "视图事务执行中", username, ""
	create.DatabaseUser = cfg.User
	create.ExecuteStatus, create.ExecuteMessage, create.ExecuteUser, create.SkipKind = "RUNNING", "视图事务执行中", username, ""
	if err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensureLegacySQLStatementRecord(tx, file, beforeDrop); err != nil {
			return err
		}
		if err := ensureLegacySQLStatementRecord(tx, file, beforeCreate); err != nil {
			return err
		}
		if err := tx.Create(&dropRecord).Error; err != nil {
			return err
		}
		if err := tx.Create(&createRecord).Error; err != nil {
			return err
		}
		if err := tx.Save(drop).Error; err != nil {
			return err
		}
		return tx.Save(create).Error
	}); err != nil {
		return fmt.Errorf("视图组合执行前保存记录失败，未执行 DROP/CREATE: %w", err)
	}

	started := time.Now()
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseSQLStatementTimeout+5*time.Second)
	defer cancel()
	transaction, err := conn.BeginTx(execCtx, nil)
	if err != nil {
		return saveReleaseSQLViewPairResult(drop, create, &dropRecord, &createRecord, err, false, false, time.Since(started), username)
	}
	defer transaction.Rollback()
	// A missing view is an allowed DROP outcome. Use a savepoint so the
	// PostgreSQL transaction remains usable after that error.
	if _, err := transaction.ExecContext(execCtx, "SAVEPOINT dfops_view_drop"); err != nil {
		return saveReleaseSQLViewPairResult(drop, create, &dropRecord, &createRecord, err, false, false, time.Since(started), username, transaction.Rollback())
	}
	dropStarted := time.Now()
	_, dropErr := transaction.ExecContext(execCtx, drop.SQLContent)
	drop.DurationMs = time.Since(dropStarted).Milliseconds()
	dropAbsent := pgState(dropErr) == "42P01"
	if dropAbsent {
		_, dropErr = transaction.ExecContext(execCtx, "ROLLBACK TO SAVEPOINT dfops_view_drop")
	}
	if dropErr != nil {
		rollbackErr := transaction.Rollback()
		return saveReleaseSQLViewPairResult(drop, create, &dropRecord, &createRecord, dropErr, true, false, time.Since(started), username, rollbackErr)
	}
	if _, err := transaction.ExecContext(execCtx, "RELEASE SAVEPOINT dfops_view_drop"); err != nil {
		return saveReleaseSQLViewPairResult(drop, create, &dropRecord, &createRecord, err, true, false, time.Since(started), username, transaction.Rollback())
	}
	createStarted := time.Now()
	_, createErr := transaction.ExecContext(execCtx, create.SQLContent)
	create.DurationMs = time.Since(createStarted).Milliseconds()
	if createErr != nil {
		rollbackErr := transaction.Rollback()
		return saveReleaseSQLViewPairResult(drop, create, &dropRecord, &createRecord, createErr, true, true, time.Since(started), username, rollbackErr)
	}
	if commitErr := transaction.Commit(); commitErr != nil {
		return saveReleaseSQLViewPairResult(drop, create, &dropRecord, &createRecord, commitErr, true, true, time.Since(started), username, commitErr)
	}
	now := time.Now()
	for _, stmt := range []*model.SQLChangeStatement{drop, create} {
		stmt.ExecuteStatus, stmt.ExecuteMessage, stmt.SQLState = "SUCCESS", "视图组合事务执行成功", ""
		stmt.ManualFailure, stmt.SkipKind = false, ""
		stmt.ExecuteTime, stmt.ExecuteUser = &now, username
	}
	for _, record := range []*model.SQLStatementRecord{&dropRecord, &createRecord} {
		yes := true
		record.Submitted = &yes
		record.Status, record.Message, record.FinishedAt = "SUCCESS", "视图组合事务执行成功", &now
		record.DurationMs = time.Since(started).Milliseconds()
	}
	if dropAbsent {
		drop.ExecuteStatus, drop.SkipKind, drop.ExecuteMessage = "SKIPPED", "AUTO", "视图不存在；后续 CREATE VIEW 已在同一事务中成功提交"
		drop.SQLState = "42P01"
		dropRecord.Status, dropRecord.Action, dropRecord.SkipKind = "SKIPPED", "AUTO_SKIP", "AUTO"
		dropRecord.Message, dropRecord.SQLState = drop.ExecuteMessage, drop.SQLState
	}
	return saveReleaseSQLViewPairStates(drop, create, &dropRecord, &createRecord)
}

func saveReleaseSQLViewPairResult(drop, create *model.SQLChangeStatement, dropRecord, createRecord *model.SQLStatementRecord, execErr error, dropSubmitted, createSubmitted bool, elapsed time.Duration, username string, rollbackErrors ...error) error {
	now := time.Now()
	rollbackErr := errors.Join(rollbackErrors...)
	status := "FAILED"
	if rollbackErr != nil {
		status = "UNKNOWN"
	}
	drop.ExecuteStatus, create.ExecuteStatus = status, status
	drop.ManualFailure, create.ManualFailure = false, false
	drop.ExecuteUser, create.ExecuteUser = username, username
	drop.ExecuteTime, create.ExecuteTime = &now, &now
	drop.SQLState, create.SQLState = "", ""
	if dropSubmitted && !createSubmitted {
		drop.SQLState = pgState(execErr)
	}
	if createSubmitted {
		create.SQLState = pgState(execErr)
	}
	drop.ExecuteMessage = releaseSQLViewPairRollbackMessage
	create.ExecuteMessage = "CREATE VIEW 失败，组合事务已回滚：" + SQLExecutionErrorMessage(execErr)
	if status == "UNKNOWN" {
		drop.ExecuteMessage = "视图组合事务回滚结果待核实：" + SQLExecutionErrorMessage(rollbackErr)
		create.ExecuteMessage = "视图组合事务结果待核实：" + SQLExecutionErrorMessage(execErr) + "；回滚失败：" + SQLExecutionErrorMessage(rollbackErr)
	}
	if !dropSubmitted {
		drop.ExecuteMessage = "视图组合事务未能启动：" + SQLExecutionErrorMessage(execErr)
	}
	if dropSubmitted && !createSubmitted {
		create.ExecuteMessage = "未执行 CREATE VIEW，因为 DROP VIEW 失败；事务已回滚：" + SQLExecutionErrorMessage(execErr)
	}
	dropRecord.Status, dropRecord.Message, dropRecord.SQLState, dropRecord.FinishedAt = drop.ExecuteStatus, drop.ExecuteMessage, drop.SQLState, &now
	createRecord.Status, createRecord.Message, createRecord.SQLState, createRecord.FinishedAt = create.ExecuteStatus, create.ExecuteMessage, create.SQLState, &now
	no, yes := false, true
	dropRecord.Submitted, createRecord.Submitted = &no, &no
	if dropSubmitted {
		dropRecord.Submitted = &yes
	}
	if createSubmitted {
		createRecord.Submitted = &yes
	}
	dropRecord.DurationMs, createRecord.DurationMs = drop.DurationMs, create.DurationMs
	dropRecord.Action, createRecord.Action = "EXECUTE", "EXECUTE"
	if status == "UNKNOWN" {
		drop.ExecuteStatus, create.ExecuteStatus = "UNKNOWN", "UNKNOWN"
	}
	if err := saveReleaseSQLViewPairStates(drop, create, dropRecord, createRecord); err != nil {
		return err
	}
	return fmt.Errorf("DROP/CREATE 事务失败，SQLSTATE %s：%s", pgState(execErr), create.ExecuteMessage)
}

func saveReleaseSQLViewPairStates(drop, create *model.SQLChangeStatement, dropRecord, createRecord *model.SQLStatementRecord) error {
	if err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(drop).Error; err != nil {
			return err
		}
		if err := tx.Save(create).Error; err != nil {
			return err
		}
		if err := tx.Save(dropRecord).Error; err != nil {
			return err
		}
		return tx.Save(createRecord).Error
	}); err != nil {
		drop.ExecuteStatus, create.ExecuteStatus = "UNKNOWN", "UNKNOWN"
		drop.ExecuteMessage = "视图组合执行结果已提交或回滚，但保存记录失败，请核实数据库对象后再操作：" + err.Error()
		create.ExecuteMessage = drop.ExecuteMessage
		_ = repository.DB.Transaction(func(tx *gorm.DB) error {
			if saveErr := tx.Save(drop).Error; saveErr != nil {
				return saveErr
			}
			return tx.Save(create).Error
		})
		return fmt.Errorf("视图组合执行结果保存失败，执行状态待核实: %w", err)
	}
	return nil
}

// Ordinary release SQL uses the schema account, one connection per file and
// per-statement autocommit. No risk analysis, rewriting or automatic owner fix.
func (s *PostgreSQLService) runReleaseSQLFile(ctx context.Context, file *model.SQLChangeFile, statements []model.SQLChangeStatement, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	return s.runReleaseSQLFileWithConfig(ctx, file, statements, username, options, nil)
}

// An alternate login is confined to one explicit retry. The original file's
// schema/search_path, identity and subsequent execution configuration stay intact.
func (s *PostgreSQLService) runReleaseSQLFileWithConfig(ctx context.Context, file *model.SQLChangeFile, statements []model.SQLChangeStatement, username string, options SQLExecuteOptions, retryConfig *PostgreSQLConfig) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	failed := func(err error) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
		message := fmt.Sprintf("文件 %s 执行失败: %s", file.FileName, SQLExecutionErrorMessage(err))
		if saveErr := saveReleaseSQLFile(file, "FAILED", message, username); saveErr != nil {
			return file, statements, errors.Join(err, saveErr)
		}
		return file, statements, nil
	}
	if err := validateReleaseSQLTransactionStates(statements); err != nil {
		return failed(err)
	}
	groups, _ := releaseSQLTransactionGroups(statements)
	groupsByStart := map[int]releaseSQLTransactionGroup{}
	for _, group := range groups {
		groupsByStart[group.Start] = group
	}
	annotateReleaseSQLTransactionGroups(statements)
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return failed(err)
	}
	if err := requireReleaseSQLTarget(file, target); err != nil {
		return failed(err)
	}
	remaining := 0
	deferredFailures := 0
	for _, stmt := range statements {
		if stmt.ManualFailure && stmt.ExecuteStatus == "FAILED" {
			deferredFailures++
			continue
		}
		if stmt.ExecuteStatus != "SUCCESS" && stmt.ExecuteStatus != "SKIPPED" {
			remaining++
		}
	}
	if remaining == 0 {
		if deferredFailures > 0 {
			return file, statements, saveReleaseSQLFile(file, "FAILED", fmt.Sprintf("执行完成：仍有 %d 条 SQL 已标记失败，待单独重试", deferredFailures), username)
		}
		return file, statements, saveReleaseSQLFile(file, "SUCCESS", "文件内所有 SQL 已成功或跳过", username)
	}
	var cfg PostgreSQLConfig
	if retryConfig != nil {
		cfg = *retryConfig
	} else {
		cfg, err = s.loadReleaseSQLConfig(file.SchemaName)
		if err != nil {
			return failed(err)
		}
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
		return failed(fmt.Errorf("连接数据库 %s（连接账号：%s）失败: %w", cfg.Database, cfg.User, err))
	}
	defer conn.Close()
	// Keep the login's database/role defaults (including public or a configured
	// extension schema). Replacing the path with only the business schema hides
	// unqualified extension functions such as uuid_generate_v1(). The script's
	// later SET/RESET commands still take precedence and are restored on retry.
	if _, err := conn.ExecContext(ctx, `SELECT pg_catalog.set_config('search_path',
		$1 || CASE WHEN pg_catalog.current_setting('search_path') = '' THEN ''
		ELSE ', ' || pg_catalog.current_setting('search_path') END, false)`, quoteIdent(file.SchemaName)); err != nil {
		return failed(fmt.Errorf("初始化 SQL 搜索路径失败: %w", err))
	}
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = '60s'"); err != nil {
		return failed(err)
	}
	for i := 0; i < len(statements); i++ {
		stmt := &statements[i]
		if group, ok := groupsByStart[i]; ok {
			block := statements[group.Start : group.End+1]
			if block[0].ExecuteStatus == "SUCCESS" || block[0].ExecuteStatus == "SKIPPED" {
				if !group.Rollback && block[0].TransactionOutcome == "COMMITTED" {
					for _, row := range block {
						if row.ExecuteStatus == "SUCCESS" && isReleaseSQLSessionSetting(row.SQLContent) {
							if err := restoreReleaseSQLSession(ctx, conn, file, row, cfg, username); err != nil {
								return failed(err)
							}
						}
					}
				}
			} else if !(block[0].ManualFailure && block[0].ExecuteStatus == "FAILED") {
				if err := executeReleaseSQLTransactionGroup(ctx, file, block, conn, cfg, username, options); err != nil {
					if errors.Is(err, errReleaseSQLTransactionStopped) {
						return file, statements, saveReleaseSQLFile(file, "CANCELED", err.Error(), username)
					}
					return failed(err)
				}
			}
			i = group.End
			continue
		}
		if stmt.ManualFailure && stmt.ExecuteStatus == "FAILED" {
			continue
		}
		stop, stopErr := releaseSQLBatchStopped(ctx, file.ExecutionBatchID)
		if stopErr != nil {
			return failed(stopErr)
		}
		if stop {
			return file, statements, saveReleaseSQLFile(file, "CANCELED", "执行已停止，已成功提交的 SQL 不会撤销", username)
		}
		if i+1 < len(statements) && stmt.ExecuteStatus != "SUCCESS" && stmt.ExecuteStatus != "SKIPPED" &&
			statements[i+1].ExecuteStatus != "SUCCESS" && statements[i+1].ExecuteStatus != "SKIPPED" &&
			!stmt.ManualFailure && !statements[i+1].ManualFailure &&
			isReleaseSQLViewPair(*stmt, statements[i+1], file.SchemaName) {
			if err := executeReleaseSQLViewPair(ctx, file, stmt, &statements[i+1], conn, cfg, username, i, len(statements)); err != nil {
				return failed(fmt.Errorf("第 %d-%d 行视图组合执行失败: %w", stmt.LineNumber, statements[i+1].LineNumber, err))
			}
			i++
			continue
		}
		restoreSession := stmt.ExecuteStatus == "SUCCESS" && isReleaseSQLSessionSetting(stmt.SQLContent)
		if (stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED") && !restoreSession {
			continue
		}
		if restoreSession {
			if err := restoreReleaseSQLSession(ctx, conn, file, *stmt, cfg, username); err != nil {
				return failed(err)
			}
			continue
		}
		executable, err := releaseSQLColumnExecutionText(stmt.SQLContent)
		if err != nil {
			return failed(err)
		}
		progress := fmt.Sprintf("数据库 %s，连接账号 %s，schema %s，文件 %s，第 %d/%d 句（原文件第 %d 行）", cfg.Database, cfg.User, file.SchemaName, file.FileName, i+1, len(statements), stmt.LineNumber)
		if err := saveReleaseSQLFile(file, "RUNNING", progress, username); err != nil {
			return file, statements, err
		}
		before := *stmt
		record := newSQLStatementRecord(file, before, "EXECUTE", username)
		record.BatchID = file.ExecutionBatchID
		record.DatabaseHost, record.DatabasePort, record.DatabaseName, record.DatabaseUser = cfg.Host, cfg.Port, cfg.Database, cfg.User
		record.Status = "RUNNING"
		stmt.DatabaseUser = cfg.User
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
		result, execErr := conn.ExecContext(execCtx, executable)
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
		if execErr == nil {
			indexErr = validateReleaseSQLIndex(ctx, conn, stmt.SQLContent)
		} else if skip {
			indexErr = validateReleaseSQLSkip(ctx, conn, stmt.SQLContent, stmt.SQLState)
		}
		if indexErr != nil {
			stmt.ExecuteStatus = "FAILED"
			if execErr != nil {
				stmt.ExecuteMessage = fmt.Sprintf("数据库原始错误：%s；dfops 校验：%s；未自动跳过本条 SQL", SQLExecutionErrorMessage(execErr), SQLExecutionErrorMessage(indexErr))
			} else {
				stmt.ExecuteMessage = "数据库已返回执行成功，但 dfops 索引状态校验未通过：" + SQLExecutionErrorMessage(indexErr)
			}
		} else if execErr == nil {
			stmt.AffectedRows, _ = result.RowsAffected()
		} else if skip {
			stmt.ExecuteStatus = "SKIPPED"
			stmt.SkipKind = "AUTO"
			stmt.ExecuteMessage = skipMessage
		} else {
			stmt.ExecuteStatus = "FAILED"
			stmt.ExecuteMessage = SQLExecutionErrorMessage(execErr)
			if stmt.SQLState == "" {
				stmt.ExecuteStatus = "UNKNOWN"
				stmt.ExecuteMessage = "数据库未返回确定结果，请核实后处理: " + SQLExecutionErrorMessage(execErr)
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
	success, skipped, deferredFailures := 0, 0, 0
	for _, stmt := range statements {
		if stmt.ExecuteStatus == "SUCCESS" {
			success++
		}
		if stmt.ExecuteStatus == "SKIPPED" {
			skipped++
		}
		if stmt.ManualFailure && stmt.ExecuteStatus == "FAILED" {
			deferredFailures++
		}
	}
	if deferredFailures > 0 {
		return file, statements, saveReleaseSQLFile(file, "FAILED", fmt.Sprintf("执行完成：成功 %d，跳过 %d，%d 条 SQL 已标记失败，待单独重试", success, skipped, deferredFailures), username)
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
		if err := tx.Model(&model.SQLStatementRecord{}).Where("status = ? AND transaction_group_id > 0", "RUNNING").Update("transaction_outcome", "UNKNOWN").Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeStatement{}).Where("execute_status = ? AND transaction_outcome <> ''", "RUNNING").Update("transaction_outcome", "UNKNOWN").Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLStatementRecord{}).Where("status = ?", "RUNNING").Updates(map[string]any{"status": "UNKNOWN", "message": "服务中断，执行结果待核实", "finished_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeFile{}).Where("execute_status = ?", "QUEUED").Updates(map[string]any{"execute_status": "CANCELED", "execute_message": "服务中断，等待中的文件未执行"}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.SQLChangeBatch{}).Where("execute_status = ? AND file_ids_json <> ''", "RUNNING").Updates(map[string]any{"execute_status": "FAILED", "outcome": "INTERRUPTED", "finished_at": now, "execute_message": "服务中断，队列已停止，请核实后重新选择文件执行"}).Error; err != nil {
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
		if err := tx.Model(&model.SQLViewDependencyChange{}).Where("status IN ? AND task_id IN (SELECT id FROM sql_view_dependency_tasks WHERE status = ?)", []string{"RUNNING", "APPLIED"}, "UNKNOWN").Updates(map[string]any{"status": "UNKNOWN", "error_message": "服务中断，字段变更结果待核实"}).Error; err != nil {
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
	case stmt.GetAlterTableStmt() != nil && (options.SkipExistsColumn || releaseSQLColumnMissingOK(stmt)) && code == "42701":
		if len(stmt.GetAlterTableStmt().Cmds) != 1 {
			return false, ""
		}
		for _, cmd := range stmt.GetAlterTableStmt().Cmds {
			if cmd.GetAlterTableCmd() == nil || cmd.GetAlterTableCmd().Subtype != pg_query.AlterTableType_AT_AddColumn {
				return false, ""
			}
		}
		return true, "字段已存在"
	case options.SkipExistsTable && code == "42710":
		if _, constraint := releaseSQLAddedConstraint(sqlText); constraint != nil {
			return true, "约束已存在，已确认目标表及约束定义一致"
		}
	case stmt.GetDropStmt() != nil && stmt.GetDropStmt().RemoveType == pg_query.ObjectType_OBJECT_VIEW && len(stmt.GetDropStmt().Objects) == 1 && code == "42P01":
		return true, "视图不存在"
	}
	return false, ""
}

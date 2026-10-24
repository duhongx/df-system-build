package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"gorm.io/gorm"
)

type sqlViewContextSetting struct {
	ID   uint   `json:"id"`
	Line int    `json:"line"`
	SQL  string `json:"sql"`
}

// The descriptor is captured without running uploaded SQL. Resolution probes a
// disposable source-account connection using only the supported SET/RESETs.
// No passwords or arbitrary custom GUC values are stored in the task.
type sqlViewSourceContext struct {
	Version  int                     `json:"version"`
	Schema   string                  `json:"schema"`
	Settings []sqlViewContextSetting `json:"settings"`
	Resolved bool                    `json:"resolved,omitempty"`
	Values   map[string]string       `json:"values,omitempty"`
}

type sqlViewContextAtStatement struct {
	Context sqlViewSourceContext
	Err     error
}

var sqlViewContextNames = []string{
	"search_path", "TimeZone", "DateStyle", "IntervalStyle",
	"standard_conforming_strings", "extra_float_digits", "bytea_output",
	"lc_monetary", "default_text_search_config", "xmloption",
}

func supportedSQLViewContextSetting(name string) bool {
	for _, known := range sqlViewContextNames {
		if strings.EqualFold(name, known) {
			return true
		}
	}
	return false
}

func validateResolvedSQLViewContext(value sqlViewSourceContext) error {
	if value.Version != 1 || !value.Resolved || len(value.Values) != len(sqlViewContextNames) {
		return fmt.Errorf("缺少已解析的源执行上下文，字段变更未执行")
	}
	for _, name := range sqlViewContextNames {
		if _, ok := value.Values[name]; !ok {
			return fmt.Errorf("源执行上下文缺少 %s，字段变更未执行", name)
		}
	}
	return nil
}

func collectSQLViewSourceContexts(file model.SQLChangeFile, rows []model.SQLChangeStatement) map[uint]sqlViewContextAtStatement {
	annotateReleaseSQLTransactionGroups(rows)
	result := make(map[uint]sqlViewContextAtStatement, len(rows))
	settings := []sqlViewContextSetting{}
	var issue error
	if file.SchemaName == "" || validateSchemaName(file.SchemaName) != nil {
		issue = fmt.Errorf("源文件未配置有效的执行账号/schema，无法保留原搜索路径，请先配置后转入")
	}
	for _, row := range rows {
		result[row.ID] = sqlViewContextAtStatement{Context: sqlViewSourceContext{Version: 1, Schema: file.SchemaName, Settings: append([]sqlViewContextSetting{}, settings...)}, Err: issue}
		node := releaseSQLNode(row.SQLContent)
		setting := node.GetVariableSetStmt()
		if setting == nil || row.ExecuteStatus == "SKIPPED" || row.ManualFailure && row.ExecuteStatus == "FAILED" {
			continue
		}
		// A local setting in an earlier transaction has ended before an outside
		// ALTER; mixed selected transactions are rejected by the transfer parser.
		if setting.IsLocal || row.TransactionOutcome == "ROLLED_BACK" || row.TransactionOutcome == "NOT_EXECUTED" {
			continue
		}
		if row.ExecuteStatus == "FAILED" || row.ExecuteStatus == "UNKNOWN" || row.ExecuteStatus == "TIMEOUT" || row.ExecuteStatus == "PARTIAL_FAILED" {
			issue = fmt.Errorf("第 %d 行会话配置尚未成功，请先修复或明确暂缓，再转入后续 SQL", row.LineNumber)
			continue
		}
		if row.TransactionGroupID > 0 && row.TransactionOutcome != "COMMITTED" {
			issue = fmt.Errorf("第 %d 行会话配置属于尚未提交的事务，不能脱离原事务恢复", row.LineNumber)
			continue
		}
		switch strings.ToLower(setting.Name) {
		case "lock_timeout", "statement_timeout", "idle_in_transaction_session_timeout", "client_min_messages":
			continue // The task's own timeouts and message policy take precedence.
		}
		if setting.Kind == pg_query.VariableSetKind_VAR_SET_CURRENT ||
			(setting.Kind != pg_query.VariableSetKind_VAR_RESET_ALL && !supportedSQLViewContextSetting(setting.Name)) {
			issue = fmt.Errorf("第 %d 行会话设置 %s 不能安全转入管理员任务，请保留原上下文单独处理", row.LineNumber, setting.Name)
			continue
		}
		settings = append(settings, sqlViewContextSetting{ID: row.ID, Line: row.LineNumber, SQL: row.SQLContent})
	}
	return result
}

func sqlViewContextDescriptor(value sqlViewSourceContext) string {
	value.Resolved, value.Values = false, nil
	data, _ := json.Marshal(value)
	return string(data)
}

func commonSQLViewSourceContext(rows []model.SQLChangeStatement, contexts map[uint]sqlViewContextAtStatement) (sqlViewSourceContext, error) {
	var common sqlViewSourceContext
	var descriptor string
	for _, row := range rows {
		if releaseSQLNode(row.SQLContent).GetTransactionStmt() != nil {
			continue
		}
		value, ok := contexts[row.ID]
		if !ok {
			return common, fmt.Errorf("缺少第 %d 行的源执行上下文", row.LineNumber)
		}
		if value.Err != nil {
			return common, value.Err
		}
		key := sqlViewContextDescriptor(value.Context)
		if descriptor != "" && descriptor != key {
			return common, fmt.Errorf("选中 SQL 使用不同的 search_path 或会话配置，请按原上下文分别转入")
		}
		common, descriptor = value.Context, key
	}
	if descriptor == "" {
		return common, fmt.Errorf("没有可恢复上下文的字段变更")
	}
	return common, nil
}

func readSQLViewContextValues(ctx context.Context, conn *sql.Conn) (map[string]string, error) {
	values := make(map[string]string, len(sqlViewContextNames))
	for _, name := range sqlViewContextNames {
		var value string
		if err := conn.QueryRowContext(ctx, "SELECT pg_catalog.current_setting($1)", name).Scan(&value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}

func (s *PostgreSQLService) ensureSQLViewSourceContext(ctx context.Context, task *model.SQLViewDependencyTask) error {
	if task.SourceFileID == 0 {
		return nil
	}
	var value sqlViewSourceContext
	if task.SourceContextJSON != "" {
		if err := json.Unmarshal([]byte(task.SourceContextJSON), &value); err != nil {
			return fmt.Errorf("源执行上下文快照无效: %w", err)
		}
		if value.Version != 1 {
			return fmt.Errorf("不支持的源执行上下文版本")
		}
		if value.Resolved {
			return validateResolvedSQLViewContext(value)
		}
	} else {
		// Existing linked tasks are upgraded on their first analysis/execution.
		var file model.SQLChangeFile
		var all []model.SQLChangeStatement
		var source []sqlViewSource
		if err := repository.DB.First(&file, task.SourceFileID).Error; err != nil {
			return err
		}
		if err := repository.DB.Where("file_id = ?", file.ID).Order("line_number,id").Find(&all).Error; err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(task.SourceStatementsJSON), &source); err != nil {
			return err
		}
		wanted := map[uint]bool{}
		for _, row := range source {
			wanted[row.ID] = true
		}
		var selected []model.SQLChangeStatement
		for _, row := range all {
			if wanted[row.ID] {
				selected = append(selected, row)
			}
		}
		var err error
		value, err = commonSQLViewSourceContext(selected, collectSQLViewSourceContexts(file, all))
		if err != nil {
			return err
		}
	}
	if value.Version != 1 || validateSchemaName(value.Schema) != nil {
		return fmt.Errorf("源执行上下文的执行账号/schema 无效")
	}
	// No platform transaction is held while loading credentials or probing the
	// source connection (the platform pool may contain only one connection).
	cfg, err := s.loadReleaseSQLConfig(value.Schema)
	if err != nil {
		return fmt.Errorf("读取源账号配置失败: %w", err)
	}
	if releaseSQLTarget(cfg) != task.DatabaseTarget {
		return fmt.Errorf("源账号连接目标已变化，不能解析原执行上下文")
	}
	db, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := db.Conn(probe)
	if err != nil {
		return fmt.Errorf("读取源账号搜索路径失败: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(probe, `SELECT pg_catalog.set_config('search_path', $1 || CASE WHEN pg_catalog.current_setting('search_path') = '' THEN '' ELSE ', ' || pg_catalog.current_setting('search_path') END, false)`, quoteIdent(value.Schema)); err != nil {
		return err
	}
	for _, setting := range value.Settings {
		parsed := releaseSQLNode(setting.SQL).GetVariableSetStmt()
		if parsed == nil || parsed.IsLocal || parsed.Kind == pg_query.VariableSetKind_VAR_SET_CURRENT ||
			(parsed.Kind != pg_query.VariableSetKind_VAR_RESET_ALL && !supportedSQLViewContextSetting(parsed.Name)) {
			return fmt.Errorf("第 %d 行的源会话配置快照不受支持，字段变更未执行", setting.Line)
		}
		if _, err := conn.ExecContext(probe, setting.SQL); err != nil {
			return fmt.Errorf("恢复第 %d 行源会话设置失败，字段变更未执行: %w", setting.Line, err)
		}
	}
	value.Values, err = readSQLViewContextValues(probe, conn)
	if err != nil {
		return err
	}
	// Pin physical schemas so "$user" never resolves as the administrator.
	var schemasJSON string
	if err := conn.QueryRowContext(probe, "SELECT pg_catalog.array_to_json(pg_catalog.current_schemas(false))::text").Scan(&schemasJSON); err != nil {
		return err
	}
	var schemas []string
	if err := json.Unmarshal([]byte(schemasJSON), &schemas); err != nil {
		return err
	}
	for i, schema := range schemas {
		if schema == "pg_temp" || strings.HasPrefix(schema, "pg_temp_") || strings.HasPrefix(schema, "pg_toast_temp_") {
			return fmt.Errorf("源搜索路径包含临时 schema，不能安全转入独立视图任务")
		}
		schemas[i] = quoteIdent(schema)
	}
	value.Values["search_path"] = strings.Join(schemas, ", ")
	value.Resolved = true
	snapshot, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result := repository.DB.Model(&model.SQLViewDependencyTask{}).Where("id = ? AND status = ? AND COALESCE(source_context_json, '') = ?", task.ID, task.Status, task.SourceContextJSON).Update("source_context_json", string(snapshot))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("任务上下文已变化，请重新加载")
	}
	task.SourceContextJSON = string(snapshot)
	return nil
}

func validateSQLViewSourceContext(tx *gorm.DB, task model.SQLViewDependencyTask, file model.SQLChangeFile, sources []sqlViewSource) error {
	if task.SourceContextJSON == "" {
		return nil // Legacy tasks capture a descriptor before running any ALTER.
	}
	var snapshot sqlViewSourceContext
	if err := json.Unmarshal([]byte(task.SourceContextJSON), &snapshot); err != nil {
		return err
	}
	var all []model.SQLChangeStatement
	if err := tx.Where("file_id = ?", file.ID).Order("line_number,id").Find(&all).Error; err != nil {
		return err
	}
	wanted := map[uint]bool{}
	for _, row := range sources {
		wanted[row.ID] = true
	}
	var selected []model.SQLChangeStatement
	for _, row := range all {
		if wanted[row.ID] {
			selected = append(selected, row)
		}
	}
	current, err := commonSQLViewSourceContext(selected, collectSQLViewSourceContexts(file, all))
	if err != nil {
		return err
	}
	if snapshot.Version != 1 || sqlViewContextDescriptor(snapshot) != sqlViewContextDescriptor(current) {
		return fmt.Errorf("源文件 schema 或前置会话配置已变化，请撤回并重新转入，不能沿用旧执行上下文")
	}
	return nil
}

type sqlViewConnectionContext struct {
	Source, Baseline map[string]string
	SourceActive     bool
}

func newSQLViewConnectionContext(ctx context.Context, conn *sql.Conn, task model.SQLViewDependencyTask) (*sqlViewConnectionContext, error) {
	if task.SourceFileID == 0 {
		return nil, nil
	}
	var snapshot sqlViewSourceContext
	if err := json.Unmarshal([]byte(task.SourceContextJSON), &snapshot); err != nil {
		return nil, err
	}
	if err := validateResolvedSQLViewContext(snapshot); err != nil {
		return nil, err
	}
	baseline, err := readSQLViewContextValues(ctx, conn)
	if err != nil {
		return nil, err
	}
	return &sqlViewConnectionContext{Source: snapshot.Values, Baseline: baseline}, nil
}

func (c *sqlViewConnectionContext) apply(ctx context.Context, conn *sql.Conn, text string) error {
	if c == nil {
		return nil
	}
	alter := releaseSQLNode(text).GetAlterTableStmt() != nil
	if alter == c.SourceActive {
		return nil
	}
	values := c.Baseline
	if alter {
		values = c.Source
	}
	for _, name := range sqlViewContextNames {
		if _, err := conn.ExecContext(ctx, "SELECT pg_catalog.set_config($1, $2, false)", name, values[name]); err != nil {
			return fmt.Errorf("切换字段变更／视图恢复上下文失败: %w", err)
		}
	}
	c.SourceActive = alter
	return nil
}

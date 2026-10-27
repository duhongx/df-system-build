package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	pg_query "github.com/pganalyze/pg_query_go/v6"
)

func isReleaseSQLAccountSetting(content string) bool {
	node := releaseSQLNode(content)
	if node == nil || node.GetVariableSetStmt() == nil {
		return false
	}
	name := node.GetVariableSetStmt().Name
	return strings.EqualFold(name, "role") || strings.EqualFold(name, "session_authorization")
}

func isReleaseSQLSessionSetting(content string) bool {
	tree, err := pg_query.Parse(content)
	if err != nil || len(tree.Stmts) != 1 {
		return false
	}
	setting := tree.Stmts[0].Stmt.GetVariableSetStmt()
	return setting != nil && !setting.IsLocal && setting.Kind != pg_query.VariableSetKind_VAR_SET_CURRENT && setting.Name != "TRANSACTION"
}

// Only SET/RESET are replayed on a fresh connection, in their original order.
// Business DDL/DML and manually skipped settings are never replayed.
func restoreReleaseSQLSession(ctx context.Context, conn *sql.Conn, file *model.SQLChangeFile, stmt model.SQLChangeStatement, cfg PostgreSQLConfig, username string) error {
	record := newSQLStatementRecord(file, stmt, "SESSION_RESTORE", username)
	record.BatchID, record.Status = file.ExecutionBatchID, "RUNNING"
	record.DatabaseHost, record.DatabasePort, record.DatabaseName, record.DatabaseUser = cfg.Host, cfg.Port, cfg.Database, cfg.User
	if err := repository.DB.Create(&record).Error; err != nil {
		return fmt.Errorf("第 %d 行恢复会话前保存记录失败，未恢复会话: %w", stmt.LineNumber, err)
	}
	started := time.Now()
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseSQLStatementTimeout+5*time.Second)
	_, execErr := conn.ExecContext(execCtx, stmt.SQLContent)
	cancel()
	now := time.Now()
	record.FinishedAt, record.DurationMs = &now, time.Since(started).Milliseconds()
	record.Status, record.Message = "SUCCESS", "续执行：恢复已成功的 SET/RESET 会话配置"
	yes := true
	record.Submitted = &yes
	if execErr != nil {
		record.Status, record.SQLState, record.Message = "FAILED", pgState(execErr), SQLExecutionErrorMessage(execErr)
		if record.SQLState == "" {
			record.Status, record.Submitted = "UNKNOWN", nil
		}
	}
	if err := repository.DB.Save(&record).Error; err != nil {
		record.Status, record.Message = "UNKNOWN", "恢复会话后保存结果失败: "+err.Error()
		_ = repository.DB.Save(&record).Error
		return fmt.Errorf("第 %d 行恢复会话记录保存失败，后续 SQL 未执行: %w", stmt.LineNumber, err)
	}
	if execErr != nil {
		return fmt.Errorf("第 %d 行恢复会话配置失败，后续 SQL 未执行: %w", stmt.LineNumber, execErr)
	}
	return nil
}

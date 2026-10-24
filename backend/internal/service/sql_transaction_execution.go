package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

var errReleaseSQLTransactionStopped = errors.New("事务组已停止并回滚")

func execReleaseSQLTransaction(ctx context.Context, conn *sql.Conn, text string) (sql.Result, error) {
	execCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseSQLStatementTimeout+5*time.Second)
	defer cancel()
	return conn.ExecContext(execCtx, text)
}

func saveReleaseSQLTransactionStates(rows []model.SQLChangeStatement, records []model.SQLStatementRecord) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			if err := tx.Save(&rows[i]).Error; err != nil {
				return err
			}
			if err := tx.Save(&records[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Keep the original BEGIN options and final COMMIT/ROLLBACK on the dedicated
// file connection. Results remain RUNNING until the transaction has settled.
func executeReleaseSQLTransactionGroup(ctx context.Context, file *model.SQLChangeFile, rows []model.SQLChangeStatement, conn *sql.Conn, cfg PostgreSQLConfig, username string, options SQLExecuteOptions) error {
	firstLine, lastLine := rows[0].LineNumber, rows[len(rows)-1].LineNumber
	label := fmt.Sprintf("第 %d-%d 行事务组", firstLine, lastLine)
	endsWithRollback := releaseSQLNode(rows[len(rows)-1].SQLContent).GetTransactionStmt().Kind.String() == "TRANS_STMT_ROLLBACK"
	records := make([]model.SQLStatementRecord, len(rows))
	before := append([]model.SQLChangeStatement(nil), rows...)
	if err := repository.DB.Transaction(func(tx *gorm.DB) error {
		for i := range rows {
			if err := ensureLegacySQLStatementRecord(tx, file, before[i]); err != nil {
				return err
			}
			records[i] = newSQLStatementRecord(file, before[i], "EXECUTE", username)
			records[i].BatchID = file.ExecutionBatchID
			records[i].TransactionGroupID = rows[0].ID
			records[i].DatabaseHost, records[i].DatabasePort = cfg.Host, cfg.Port
			records[i].DatabaseName, records[i].DatabaseUser = cfg.Database, cfg.User
			records[i].Status, records[i].Message, records[i].TransactionOutcome = "RUNNING", label+"等待执行，尚未提交", "NOT_EXECUTED"
			no := false
			records[i].Submitted = &no
			if err := tx.Create(&records[i]).Error; err != nil {
				return err
			}
			rows[i].DatabaseUser = cfg.User
			rows[i].ExecuteStatus, rows[i].ExecuteMessage, rows[i].ExecuteUser = "RUNNING", records[i].Message, username
			rows[i].TransactionOutcome, rows[i].ManualFailure, rows[i].SkipKind = "NOT_EXECUTED", false, ""
			rows[i].SQLState, rows[i].AffectedRows, rows[i].DurationMs, rows[i].ExecuteTime = "", 0, 0, nil
			if err := tx.Save(&rows[i]).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("%s执行前保存记录失败，未提交事务: %w", label, err)
	}

	started, settled := false, false
	defer func() {
		if !settled {
			_, _ = execReleaseSQLTransaction(ctx, conn, "ROLLBACK")
		}
	}()
	submitted := make([]bool, len(rows))
	skipped := make([]string, len(rows))
	finishFailure := func(index int, cause error, stopped, commitUncertain bool) error {
		var rollbackErr error
		if started {
			_, rollbackErr = execReleaseSQLTransaction(ctx, conn, "ROLLBACK")
		}
		settled = rollbackErr == nil
		unknown := commitUncertain || rollbackErr != nil
		now := time.Now()
		for i := range rows {
			status, outcome := "FAILED", "NOT_EXECUTED"
			message := fmt.Sprintf("%s在第 %d 行失败，本句未执行；整个事务组需一起重试：%v", label, rows[index].LineNumber, cause)
			if submitted[i] {
				outcome = "ROLLED_BACK"
				message = fmt.Sprintf("%s已整体回滚，本句的修改未提交；失败位置为第 %d 行：%v", label, rows[index].LineNumber, cause)
			}
			if i == index {
				outcome, message = "FAILED", fmt.Sprintf("%s失败，事务组已回滚：%v", label, cause)
			}
			if !started {
				message = fmt.Sprintf("%s未能开始，本句未执行：%v", label, cause)
				outcome = "NOT_EXECUTED"
			}
			if stopped {
				status = "CANCELED"
				outcome = "NOT_EXECUTED"
				if submitted[i] {
					outcome = "ROLLED_BACK"
				}
				message = label + "已按停止请求整体回滚，组内修改未提交；继续执行将从事务组开头重试"
				if !started {
					message = label + "已按停止请求取消，事务尚未开始，组内语句均未执行"
				}
			}
			if unknown {
				status, outcome = "UNKNOWN", "UNKNOWN"
				message = fmt.Sprintf("%s执行结果待核实，不能直接重试：%v", label, cause)
				if rollbackErr != nil {
					message += "；回滚未获确认：" + rollbackErr.Error()
				}
				if commitUncertain {
					message += "；COMMIT 未获确定结果，事务可能已经提交"
				}
			}
			rows[i].ExecuteStatus, rows[i].TransactionOutcome, rows[i].ExecuteMessage = status, outcome, message
			rows[i].AffectedRows, rows[i].ExecuteTime = 0, &now
			if i == index {
				if rows[i].SQLState == "" {
					rows[i].SQLState = pgState(cause)
				}
			} else {
				rows[i].SQLState = ""
			}
			records[i].Status, records[i].TransactionOutcome, records[i].Message = status, outcome, message
			records[i].SQLState, records[i].AffectedRows, records[i].DurationMs, records[i].FinishedAt = rows[i].SQLState, 0, rows[i].DurationMs, &now
			yes := submitted[i]
			records[i].Submitted = &yes
		}
		if err := saveReleaseSQLTransactionStates(rows, records); err != nil {
			for i := range rows {
				rows[i].ExecuteStatus, rows[i].TransactionOutcome = "UNKNOWN", "UNKNOWN"
				rows[i].ExecuteMessage = label + "已停止，但保存回滚结果失败，请核实后处理：" + err.Error()
				records[i].Status, records[i].TransactionOutcome, records[i].Message = "UNKNOWN", "UNKNOWN", rows[i].ExecuteMessage
			}
			_ = saveReleaseSQLTransactionStates(rows, records)
			return fmt.Errorf("%s结束后保存记录失败，结果待核实: %w", label, err)
		}
		if unknown {
			return fmt.Errorf("%s结果待核实：%v", label, cause)
		}
		if stopped {
			return errReleaseSQLTransactionStopped
		}
		return fmt.Errorf("%s在第 %d 行失败，已整体回滚：%w", label, rows[index].LineNumber, cause)
	}

	for i := range rows {
		stop, err := releaseSQLBatchStopped(ctx, file.ExecutionBatchID)
		if err != nil {
			return finishFailure(i, err, false, false)
		}
		if stop {
			return finishFailure(i, errReleaseSQLTransactionStopped, true, false)
		}
		progress := fmt.Sprintf("文件 %s，%s，第 %d 行；组内结果等待事务提交", file.FileName, label, rows[i].LineNumber)
		if err := saveReleaseSQLFile(file, "RUNNING", progress, username); err != nil {
			return finishFailure(i, err, false, false)
		}
		savepoint := ""
		// Only eligible DDL/DML need a recovery savepoint. In particular, do not
		// run metadata queries before the script's SET TRANSACTION commands.
		if i > 0 && i < len(rows)-1 {
			node := releaseSQLNode(rows[i].SQLContent)
			if node != nil && ((node.GetInsertStmt() != nil && options.SkipUniqueConstraint) ||
				((node.GetIndexStmt() != nil || node.GetCreateStmt() != nil) && options.SkipExistsTable) ||
				(node.GetAlterTableStmt() != nil && (options.SkipExistsColumn || options.SkipExistsTable || releaseSQLColumnMissingOK(node))) || node.GetDropStmt() != nil) {
				savepoint = fmt.Sprintf("dfops_skip_%d_%d", rows[0].ID, i)
				if _, err := execReleaseSQLTransaction(ctx, conn, "SAVEPOINT "+savepoint); err != nil {
					return finishFailure(i, err, false, false)
				}
			}
		}
		executable, err := releaseSQLColumnExecutionText(rows[i].SQLContent)
		if err != nil {
			return finishFailure(i, err, false, false)
		}
		began := time.Now()
		submitted[i] = true
		result, execErr := execReleaseSQLTransaction(ctx, conn, executable)
		rows[i].DurationMs = time.Since(began).Milliseconds()
		if i == 0 && execErr == nil {
			started = true
		}
		if execErr != nil {
			skip, reason := skipReleaseSQLError(rows[i].SQLContent, execErr, options)
			if !skip || savepoint == "" {
				return finishFailure(i, execErr, false, i == len(rows)-1 && !endsWithRollback && pgState(execErr) == "")
			}
			if _, err := execReleaseSQLTransaction(ctx, conn, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
				return finishFailure(i, errors.Join(execErr, err), false, false)
			}
			skipped[i], rows[i].SQLState = reason, pgState(execErr)
		}
		if i > 0 && i < len(rows)-1 {
			var validationErr error
			if execErr == nil {
				validationErr = validateReleaseSQLIndex(ctx, conn, rows[i].SQLContent)
			} else {
				validationErr = validateReleaseSQLSkip(ctx, conn, rows[i].SQLContent, rows[i].SQLState)
			}
			if validationErr != nil {
				if execErr != nil {
					validationErr = fmt.Errorf("数据库原始错误：%w；dfops 校验：%v；未自动跳过本条 SQL", execErr, validationErr)
				}
				return finishFailure(i, validationErr, false, false)
			}
		}
		if savepoint != "" {
			if _, err := execReleaseSQLTransaction(ctx, conn, "RELEASE SAVEPOINT "+savepoint); err != nil {
				return finishFailure(i, err, false, false)
			}
		}
		if result != nil && execErr == nil {
			rows[i].AffectedRows, _ = result.RowsAffected()
		}
		rows[i].TransactionOutcome, rows[i].ExecuteMessage = "PENDING_COMMIT", label+"内本句执行完成，等待事务提交"
		yes := true
		records[i].Submitted, records[i].TransactionOutcome, records[i].Message = &yes, rows[i].TransactionOutcome, rows[i].ExecuteMessage
		// Do not attempt any additional business SQL after COMMIT/ROLLBACK.
		if i < len(rows)-1 {
			if err := saveReleaseSQLTransactionStates(rows[i:i+1], records[i:i+1]); err != nil {
				return finishFailure(i, err, false, false)
			}
		}
	}
	settled = true
	now := time.Now()
	for i := range rows {
		status, outcome, message := "SUCCESS", "COMMITTED", label+"已整体提交"
		if skipped[i] != "" {
			status, message = "SKIPPED", skipped[i]+"；所属事务组已提交"
			rows[i].SkipKind, records[i].SkipKind, records[i].Action = "AUTO", "AUTO", "AUTO_SKIP"
		}
		if endsWithRollback {
			status, outcome, message = "SKIPPED", "ROLLED_BACK", label+"按脚本中的 ROLLBACK 整体回滚，组内修改未提交"
			rows[i].AffectedRows, rows[i].SkipKind = 0, "TRANSACTION_ROLLBACK"
			records[i].SkipKind = rows[i].SkipKind
		}
		rows[i].ExecuteStatus, rows[i].TransactionOutcome, rows[i].ExecuteMessage, rows[i].ExecuteTime = status, outcome, message, &now
		records[i].Status, records[i].TransactionOutcome, records[i].Message, records[i].FinishedAt = status, outcome, message, &now
		records[i].SQLState, records[i].AffectedRows, records[i].DurationMs = rows[i].SQLState, rows[i].AffectedRows, rows[i].DurationMs
	}
	if err := saveReleaseSQLTransactionStates(rows, records); err != nil {
		// The business transaction has settled; a retry after a platform DB
		// failure could duplicate writes. Persist uncertainty whenever possible.
		for i := range rows {
			rows[i].ExecuteStatus, rows[i].TransactionOutcome = "UNKNOWN", "UNKNOWN"
			rows[i].ExecuteMessage = label + "已结束，但保存提交/回滚记录失败，请核实后处理：" + err.Error()
			records[i].Status, records[i].TransactionOutcome, records[i].Message = "UNKNOWN", "UNKNOWN", rows[i].ExecuteMessage
		}
		_ = saveReleaseSQLTransactionStates(rows, records)
		return fmt.Errorf("%s保存最终结果失败: %w", label, err)
	}
	return nil
}

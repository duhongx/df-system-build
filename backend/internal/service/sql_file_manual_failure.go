package service

import (
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

type SQLFileFailureResult struct {
	File           *model.SQLChangeFile       `json:"file"`
	Statements     []model.SQLChangeStatement `json:"statements"`
	MarkedCount    int                        `json:"markedCount"`
	DeferredCount  int                        `json:"deferredCount"`
	PreservedCount int                        `json:"preservedCount"`
	LinkedCount    int                        `json:"linkedCount"`
}

// This changes handling state only. It never submits the uploaded SQL or
// rewrites completed results, linked view tasks, or execution-batch snapshots.
func (s *PostgreSQLService) MarkSQLFileFailed(fileID uint, username string) (*SQLFileFailureResult, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, err
	}
	result := &SQLFileFailureResult{}
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		file, err := lockSQLFile(tx, fileID)
		if err != nil {
			return err
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
			return fmt.Errorf("SQL 文件正在执行或等待执行，不能整文件标记失败")
		}
		if file.ExecuteStatus == "UNKNOWN" {
			return fmt.Errorf("文件执行结果待核实，不能整文件标记失败")
		}
		if file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" {
			return fmt.Errorf("已成功或已跳过的 SQL 文件不能标记失败")
		}
		if err := requireReleaseSQLTarget(file, target); err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, fileID, false); err != nil {
			return err
		}
		var rows []model.SQLChangeStatement
		if err := tx.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
		groups, err := releaseSQLTransactionGroups(rows)
		if err != nil {
			return err
		}
		eligible := make([]bool, len(rows))
		for i, row := range rows {
			switch row.ExecuteStatus {
			case "UNKNOWN", "RUNNING", "QUEUED", "APPLIED":
				return fmt.Errorf("第 %d 行正在执行或结果待核实，不能整文件标记失败", row.LineNumber)
			case "SUCCESS", "SKIPPED":
				result.PreservedCount++
				continue
			}
			if row.ViewDependencyTaskID > 0 {
				result.LinkedCount++
				continue
			}
			switch row.ExecuteStatus {
			case "", "PENDING", "FAILED", "PARTIAL_FAILED", "TIMEOUT", "CANCELED", "NOT_EXECUTABLE", "BLOCKED":
				eligible[i] = true
			default:
				return fmt.Errorf("第 %d 行状态 %s 不允许标记失败", row.LineNumber, row.ExecuteStatus)
			}
		}
		for _, group := range groups {
			count := 0
			for i := group.Start; i <= group.End; i++ {
				if eligible[i] {
					count++
				}
			}
			if count > 0 && count != group.End-group.Start+1 {
				return fmt.Errorf("第 %d-%d 行事务组包含成功、已跳过或已关联视图任务的语句，不能拆开标记失败；请核实后按整组处理", rows[group.Start].LineNumber, rows[group.End].LineNumber)
			}
		}
		annotateReleaseSQLTransactionGroups(rows)
		now := time.Now()
		for i := range rows {
			if !eligible[i] {
				continue
			}
			row := &rows[i]
			result.DeferredCount++
			if row.ExecuteStatus == "FAILED" && row.ManualFailure &&
				((row.TransactionStartLine == 0 && row.TransactionOutcome == "") ||
					(row.TransactionStartLine > 0 && row.TransactionOutcome == "MANUAL_FAILED")) {
				continue
			}
			if err := ensureLegacySQLStatementRecord(tx, file, *row); err != nil {
				return err
			}
			message := "整文件手动标记失败，已暂缓执行；本次未提交数据库，可在失败 SQL 清单中重试或转入视图依赖"
			if row.TransactionStartLine > 0 {
				message += fmt.Sprintf("；第 %d-%d 行保留为完整事务组", row.TransactionStartLine, row.TransactionEndLine)
			}
			if strings.TrimSpace(row.ExecuteMessage) != "" {
				message += "；原记录：" + row.ExecuteMessage
			}
			if err := appendSQLActionRecord(tx, file, *row, "FILE_FAIL", username, "FAILED", message); err != nil {
				return err
			}
			row.ExecuteStatus, row.ManualFailure, row.SkipKind = "FAILED", true, ""
			if row.TransactionStartLine > 0 {
				row.TransactionOutcome = "MANUAL_FAILED"
			} else {
				row.TransactionOutcome = ""
			}
			row.ExecuteMessage, row.ExecuteUser, row.ExecuteTime = message, username, &now
			if err := tx.Save(row).Error; err != nil {
				return err
			}
			result.MarkedCount++
		}
		if result.DeferredCount == 0 {
			return fmt.Errorf("文件没有可标记的失败或未执行 SQL；已完成及已关联视图任务的语句保持原状")
		}
		file, err = recomputeSQLFileStatusWithDB(tx, fileID, username)
		if err != nil {
			return err
		}
		file.ExecuteMessage = fmt.Sprintf("整文件已暂缓 %d 条 SQL，本次新标记 %d 条；保留成功或已跳过 %d 条、已关联视图任务 %d 条。请到失败 SQL 清单处理，本次未执行 SQL", result.DeferredCount, result.MarkedCount, result.PreservedCount, result.LinkedCount)
		// Keep the old immutable batch history, but show the current handling
		// state when this file is reopened instead of its old stopped batch.
		file.ExecutionBatchID = 0
		if err := tx.Save(file).Error; err != nil {
			return err
		}
		decorated := []model.SQLChangeFile{*file}
		if err := populateSQLFileCounts(tx, decorated); err != nil {
			return err
		}
		result.File, result.Statements = &decorated[0], rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

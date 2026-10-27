package service

import (
	"fmt"
	"strings"

	"df-build-server/internal/model"
	pg_query "github.com/pganalyze/pg_query_go/v6"
)

type releaseSQLTransactionGroup struct {
	Start, End int
	Rollback   bool
}

// Parse top-level transaction controls only. BEGIN/END inside DO and function
// bodies are part of their enclosing statement, never transaction boundaries.
func releaseSQLTransactionGroups(rows []model.SQLChangeStatement) ([]releaseSQLTransactionGroup, error) {
	var groups []releaseSQLTransactionGroup
	start := -1
	for i, row := range rows {
		node := releaseSQLNode(row.SQLContent)
		if node == nil {
			continue // Ordinary syntax errors belong to the execution result.
		}
		if control := node.GetTransactionStmt(); control != nil {
			if control.Chain {
				return nil, fmt.Errorf("第 %d 行使用 AND CHAIN，暂不支持连续事务；请保留事务边界并人工处理此文件", row.LineNumber)
			}
			switch control.Kind {
			case pg_query.TransactionStmtKind_TRANS_STMT_BEGIN, pg_query.TransactionStmtKind_TRANS_STMT_START:
				if start >= 0 {
					return nil, fmt.Errorf("第 %d 行重复开启事务，前一事务始于第 %d 行", row.LineNumber, rows[start].LineNumber)
				}
				start = i
			case pg_query.TransactionStmtKind_TRANS_STMT_COMMIT, pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK:
				if start < 0 {
					return nil, fmt.Errorf("第 %d 行的 COMMIT/ROLLBACK 没有对应的 BEGIN/START TRANSACTION", row.LineNumber)
				}
				groups = append(groups, releaseSQLTransactionGroup{Start: start, End: i, Rollback: control.Kind == pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK})
				start = -1
			default:
				return nil, fmt.Errorf("第 %d 行包含暂不支持的 SAVEPOINT 或两阶段事务控制；请保留原事务并人工处理此文件", row.LineNumber)
			}
			continue
		}
		if setting := node.GetVariableSetStmt(); setting != nil {
			if setting.Kind == pg_query.VariableSetKind_VAR_SET_CURRENT {
				return nil, fmt.Errorf("第 %d 行包含无法在续跑时准确恢复的 SET FROM CURRENT，请使用明确的配置值", row.LineNumber)
			}
			if (setting.IsLocal || setting.Name == "TRANSACTION") && start < 0 {
				return nil, fmt.Errorf("第 %d 行的 SET LOCAL/SET TRANSACTION 必须位于完整的 BEGIN…COMMIT 事务组内", row.LineNumber)
			}
		}
		if start >= 0 {
			if index := node.GetIndexStmt(); index != nil && index.Concurrent {
				return nil, fmt.Errorf("第 %d 行的 CREATE INDEX CONCURRENTLY 不能在事务组内执行；请保留事务语义并调整脚本", row.LineNumber)
			}
		}
	}
	if start >= 0 {
		return nil, fmt.Errorf("第 %d 行开启的事务缺少对应的 COMMIT/ROLLBACK，整个文件未执行", rows[start].LineNumber)
	}
	return groups, nil
}

func releaseSQLTransactionGroupAt(rows []model.SQLChangeStatement, index int) (*releaseSQLTransactionGroup, error) {
	groups, err := releaseSQLTransactionGroups(rows)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if index >= group.Start && index <= group.End {
			return &group, nil
		}
	}
	return nil, nil
}

func annotateReleaseSQLTransactionGroups(rows []model.SQLChangeStatement) {
	for i := range rows {
		rows[i].TransactionGroupID, rows[i].TransactionStartLine, rows[i].TransactionEndLine = 0, 0, 0
	}
	groups, _ := releaseSQLTransactionGroups(rows)
	for _, group := range groups {
		for i := group.Start; i <= group.End; i++ {
			rows[i].TransactionGroupID = rows[group.Start].ID
			rows[i].TransactionStartLine = rows[group.Start].LineNumber
			rows[i].TransactionEndLine = rows[group.End].LineNumber
		}
	}
}

func validateReleaseSQLTransactionStates(rows []model.SQLChangeStatement) error {
	groups, err := releaseSQLTransactionGroups(rows)
	if err != nil {
		return err
	}
	for _, group := range groups {
		complete, deferred := 0, 0
		hasSuccess := false
		for _, row := range rows[group.Start : group.End+1] {
			if row.ExecuteStatus == "SUCCESS" {
				hasSuccess = true
			}
			if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" {
				complete++
			}
			if row.ExecuteStatus == "FAILED" && row.ManualFailure {
				deferred++
			}
		}
		count := group.End - group.Start + 1
		if (complete > 0 && complete != count) || (deferred > 0 && deferred != count) {
			return fmt.Errorf("第 %d-%d 行事务组存在不一致的历史状态，不能只执行其中一部分；请核实后按整组处理", rows[group.Start].LineNumber, rows[group.End].LineNumber)
		}
		if hasSuccess && complete == count && rows[group.Start].TransactionOutcome != "COMMITTED" {
			return fmt.Errorf("第 %d-%d 行事务组缺少整体提交确认，不能直接沿用旧的单句成功状态；请先核实数据库结果", rows[group.Start].LineNumber, rows[group.End].LineNumber)
		}
	}
	return nil
}

// Inherit an explicit transaction only as an identical, fully completed group.
// Individual matches must never turn part of a changed transaction into success.
func releaseSQLInheritanceCandidates(previous, current []model.SQLChangeStatement) map[int]model.SQLChangeStatement {
	result := map[int]model.SQLChangeStatement{}
	oldGroups, oldErr := releaseSQLTransactionGroups(previous)
	newGroups, newErr := releaseSQLTransactionGroups(current)
	if oldErr != nil || newErr != nil {
		return result
	}
	oldGrouped, newGrouped := map[int]bool{}, map[int]bool{}
	groupKey := func(rows []model.SQLChangeStatement) string {
		parts := make([]string, len(rows))
		for i := range rows {
			parts[i] = rows[i].SQLContent
		}
		return strings.Join(parts, "\x00")
	}
	oldByGroup := map[string][][]model.SQLChangeStatement{}
	for _, group := range oldGroups {
		block := previous[group.Start : group.End+1]
		oldByGroup[groupKey(block)] = append(oldByGroup[groupKey(block)], block)
		for i := group.Start; i <= group.End; i++ {
			oldGrouped[i] = true
		}
	}
	for _, group := range newGroups {
		for i := group.Start; i <= group.End; i++ {
			newGrouped[i] = true
		}
		key := groupKey(current[group.Start : group.End+1])
		matches := oldByGroup[key]
		if len(matches) == 0 {
			continue
		}
		block := matches[0]
		oldByGroup[key] = matches[1:]
		completed := true
		for _, row := range block {
			if row.ExecuteStatus != "SUCCESS" && row.ExecuteStatus != "SKIPPED" {
				completed = false
			}
			if row.ExecuteStatus == "SUCCESS" && row.TransactionOutcome != "COMMITTED" {
				completed = false
			}
		}
		if completed {
			for offset, row := range block {
				result[group.Start+offset] = row
			}
		}
	}
	oldSingles := map[string][]model.SQLChangeStatement{}
	for i, row := range previous {
		if !oldGrouped[i] {
			oldSingles[row.SQLContent] = append(oldSingles[row.SQLContent], row)
		}
	}
	for i, row := range current {
		if newGrouped[i] {
			continue
		}
		matches := oldSingles[row.SQLContent]
		if len(matches) == 0 {
			continue
		}
		oldSingles[row.SQLContent] = matches[1:]
		if matches[0].ExecuteStatus == "SUCCESS" || matches[0].ExecuteStatus == "SKIPPED" {
			result[i] = matches[0]
		}
	}
	return result
}

// A text match is only reusable in the same session context. Reject ambiguous
// overwrites instead of replaying already committed business statements.
func validateReleaseSQLInheritanceContexts(previous, current []model.SQLChangeStatement) error {
	for _, row := range previous {
		if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" {
			if err := validateReleaseSQLTransactionStates(previous); err != nil {
				return fmt.Errorf("原文件事务历史需要核实，不能覆盖并重放: %w", err)
			}
			break
		}
	}
	candidates := releaseSQLInheritanceCandidates(previous, current)
	contexts := func(rows []model.SQLChangeStatement, inherited map[int]model.SQLChangeStatement, historical bool) []string {
		result := make([]string, len(rows))
		var settings []string
		var beforeTransaction []string
		for i, row := range rows {
			result[i] = strings.Join(settings, "\x00")
			if control := releaseSQLNode(row.SQLContent).GetTransactionStmt(); control != nil {
				switch control.Kind {
				case pg_query.TransactionStmtKind_TRANS_STMT_BEGIN, pg_query.TransactionStmtKind_TRANS_STMT_START:
					beforeTransaction = append([]string(nil), settings...)
				case pg_query.TransactionStmtKind_TRANS_STMT_ROLLBACK:
					settings = beforeTransaction
				}
			}
			status := row.ExecuteStatus
			if prior, ok := inherited[i]; ok {
				status = prior.ExecuteStatus
			}
			if isReleaseSQLSessionSetting(row.SQLContent) && (status == "SUCCESS" || (!historical && (status == "PENDING" || status == ""))) {
				settings = append(settings, row.SQLContent)
			}
		}
		return result
	}
	oldContexts := contexts(previous, nil, true)
	oldBySQL := make(map[string][]int, len(previous))
	for i, row := range previous {
		oldBySQL[row.SQLContent] = append(oldBySQL[row.SQLContent], i)
	}
	newContexts := contexts(current, candidates, false)
	for i, row := range current {
		matches := oldBySQL[row.SQLContent]
		if len(matches) == 0 {
			continue
		}
		oldIndex := matches[0]
		oldBySQL[row.SQLContent] = matches[1:]
		prior := previous[oldIndex]
		// Check text matches even inside changed transaction groups. Such a group
		// cannot inherit individual results, but a changed SET must not silently
		// replay already committed DML in a different schema/account either.
		if (prior.ExecuteStatus == "SUCCESS" || prior.ExecuteStatus == "SKIPPED") && oldContexts[oldIndex] != newContexts[i] {
			return fmt.Errorf("第 %d 行已完成 SQL 的前置 SET/RESET 会话配置已变化，不能沿用原执行结果或自动重放；请保留已完成部分，并将新上下文下需要执行的 SQL 另建文件", row.LineNumber)
		}
	}
	return nil
}

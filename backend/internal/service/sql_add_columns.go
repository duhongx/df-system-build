package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// Split only a pure list of ADD COLUMN actions. Parsing, rather than splitting
// commas, preserves numeric precision, defaults, expressions and quoted names.
// Mixed ALTER actions and SQL inside DO/function bodies retain their semantics.
func splitReleaseSQLAddColumns(text string) ([]string, error) {
	tree, err := pg_query.Parse(text)
	if err != nil || len(tree.Stmts) != 1 {
		return nil, nil // Leave syntax errors visible as ordinary execution errors.
	}
	alter := tree.Stmts[0].Stmt.GetAlterTableStmt()
	if alter == nil || alter.Objtype != pg_query.ObjectType_OBJECT_TABLE || len(alter.Cmds) < 2 {
		return nil, nil
	}
	for _, node := range alter.Cmds {
		cmd := node.GetAlterTableCmd()
		if cmd == nil || cmd.Subtype != pg_query.AlterTableType_AT_AddColumn || cmd.Def.GetColumnDef() == nil {
			return nil, nil
		}
	}
	parts := make([]string, 0, len(alter.Cmds))
	for i := range alter.Cmds {
		one := proto.Clone(tree).(*pg_query.ParseResult)
		oneAlter := one.Stmts[0].Stmt.GetAlterTableStmt()
		oneAlter.Cmds = oneAlter.Cmds[i : i+1]
		text, err := pg_query.Deparse(one)
		if err != nil {
			return nil, fmt.Errorf("拆分 ADD COLUMN 失败: %w", err)
		}
		parts = append(parts, strings.TrimSpace(strings.TrimSuffix(text, ";")))
	}
	return parts, nil
}

func releaseSQLSplitText(parts []string) string {
	// Keep delimiters outside line comments and added IDs before following SQL
	// in the established (line_number, id) order.
	return strings.Join(parts, "\n;\n") + "\n"
}

func releaseSQLColumnMissingOK(node *pg_query.Node) bool {
	alter := node.GetAlterTableStmt()
	if alter == nil || len(alter.Cmds) != 1 {
		return false
	}
	cmd := alter.Cmds[0].GetAlterTableCmd()
	return cmd != nil && cmd.Subtype == pg_query.AlterTableType_AT_AddColumn && cmd.MissingOk
}

// IF NOT EXISTS reports only a NOTICE, indistinguishable from a new column in
// sql.Result. Use the equivalent error+skip path so the audit records SKIPPED,
// while honoring explicit IF NOT EXISTS even when the global toggle is off.
func releaseSQLColumnExecutionText(text string) (string, error) {
	tree, err := pg_query.Parse(text)
	if err != nil || len(tree.Stmts) != 1 || !releaseSQLColumnMissingOK(tree.Stmts[0].Stmt) {
		return text, nil
	}
	tree.Stmts[0].Stmt.GetAlterTableStmt().Cmds[0].GetAlterTableCmd().MissingOk = false
	executable, err := pg_query.Deparse(tree)
	if err != nil {
		return "", fmt.Errorf("准备 ADD COLUMN IF NOT EXISTS 执行语句失败: %w", err)
	}
	return executable, nil
}

// Used before saving an edited fragment, so each generated column receives the
// same audit/provenance handling as other one-to-many SQL edits.
func expandReleaseSQLAddColumnContent(content string) (string, error) {
	rows, err := splitReleaseSQL(content)
	if err != nil {
		return "", err
	}
	for i := len(rows) - 1; i >= 0; i-- {
		parts, err := splitReleaseSQLAddColumns(rows[i].SQLContent)
		if err != nil {
			return "", err
		}
		if len(parts) == 0 {
			continue
		}
		content, rows, err = replaceReleaseSQLStatement(content, rows, i, releaseSQLSplitText(parts))
		if err != nil {
			return "", err
		}
	}
	return content, nil
}

// Caller holds the file identity lock in a metadata transaction. No business
// SQL is executed here. Completed, uncertain and manually deferred results must
// never be turned into new executable work by a migration.
func normalizeReleaseSQLAddColumns(tx *gorm.DB, file *model.SQLChangeFile, rows []model.SQLChangeStatement, username string) ([]model.SQLChangeStatement, int, error) {
	if file.IsDeleted || file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" || file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" {
		return rows, 0, nil
	}
	for _, row := range rows {
		if row.ExecuteStatus == "RUNNING" || row.ExecuteStatus == "UNKNOWN" {
			return rows, 0, nil
		}
		if file.DatabaseTarget == "" && row.ExecuteStatus != "PENDING" {
			return rows, 0, nil // Do not hide an unconfirmed legacy database target.
		}
	}
	protected := map[uint]bool{}
	groups, err := releaseSQLTransactionGroups(rows)
	if err != nil {
		return rows, 0, nil // Keep unsupported/incomplete transactions intact.
	}
	for _, group := range groups {
		deferred := 0
		for _, row := range rows[group.Start : group.End+1] {
			if row.ExecuteStatus == "FAILED" && row.ManualFailure {
				deferred++
			}
		}
		for _, row := range rows[group.Start : group.End+1] {
			if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" || (deferred > 0 && deferred != group.End-group.Start+1) {
				for _, peer := range rows[group.Start : group.End+1] {
					protected[peer.ID] = true
				}
				break
			}
		}
	}
	annotateReleaseSQLTransactionGroups(rows)
	changed := 0
	resetGroups := map[uint]bool{}
	for i := len(rows) - 1; i >= 0; i-- {
		original := rows[i]
		if protected[original.ID] || (original.ExecuteStatus != "PENDING" && original.ExecuteStatus != "FAILED" && original.ExecuteStatus != "CANCELED") {
			continue
		}
		parts, err := splitReleaseSQLAddColumns(original.SQLContent)
		if err != nil {
			return nil, 0, err
		}
		if len(parts) == 0 {
			continue
		}
		if original.TransactionGroupID > 0 && !original.ManualFailure {
			resetGroups[original.TransactionGroupID] = true
		}
		replacement := releaseSQLSplitText(parts)
		content, positions, err := replaceReleaseSQLStatement(file.FileContent, rows, i, replacement)
		if err != nil {
			return nil, 0, err
		}
		if len(content) > 50*1024*1024 {
			return nil, 0, fmt.Errorf("拆分后的 SQL 文件超过 50MB")
		}
		if err := ensureLegacySQLStatementRecord(tx, file, original); err != nil {
			return nil, 0, err
		}
		audit := model.SQLStatementEdit{FileID: file.ID, StatementID: original.ID, BeforeSQL: original.SQLContent, AfterSQL: replacement, BeforeStatus: original.ExecuteStatus, BeforeMessage: original.ExecuteMessage, BeforeSQLState: original.SQLState, Editor: username}
		if err := tx.Create(&audit).Error; err != nil {
			return nil, 0, err
		}
		next := make([]model.SQLChangeStatement, 0, len(positions))
		next = append(next, rows[:i]...)
		now := time.Now()
		for j, text := range parts {
			row := model.SQLChangeStatement{FileID: file.ID, SQLContent: text, ExecuteStatus: "PENDING", LineNumber: positions[i+j].LineNumber}
			if j == 0 {
				row.ID, row.CreatedAt = original.ID, original.CreatedAt
			}
			row.ExecuteMessage = fmt.Sprintf("自动拆分原第 %d 行的 %d 个 ADD COLUMN，本句为第 %d 个字段，等待执行", original.LineNumber, len(parts), j+1)
			if original.ManualFailure {
				row.ExecuteStatus, row.ManualFailure = "FAILED", true
				row.ExecuteMessage += "；原语句已手动暂缓，请单独重试"
				row.TransactionOutcome = original.TransactionOutcome
			}
			row.UpdatedAt = now
			if err := tx.Save(&row).Error; err != nil {
				return nil, 0, err
			}
			before := original
			before.ID, before.SQLContent, before.LineNumber = row.ID, text, row.LineNumber
			record := newSQLStatementRecord(file, before, "AUTO_SPLIT", username)
			no := false
			record.Status, record.Message, record.TransactionOutcome = row.ExecuteStatus, row.ExecuteMessage, row.TransactionOutcome
			record.Submitted, record.FinishedAt = &no, &now
			if j > 0 {
				record.SourceStatementID = original.ID
			}
			if err := tx.Create(&record).Error; err != nil {
				return nil, 0, err
			}
			next = append(next, row)
		}
		next = append(next, rows[i+1:]...)
		for k := range next {
			next[k].LineNumber = positions[k].LineNumber
			if err := tx.Model(&model.SQLChangeStatement{}).Where("id = ?", next[k].ID).Update("line_number", next[k].LineNumber).Error; err != nil {
				return nil, 0, err
			}
		}
		file.FileContent, rows = content, next
		changed++
	}
	if changed > 0 {
		annotateReleaseSQLTransactionGroups(rows)
		// A rolled-back group is one retry unit. Leaving its old BEGIN/body
		// failed while only new columns are pending would cause continuation to
		// defer half the group and then reject its inconsistent history state.
		for i := range rows {
			row := &rows[i]
			if !resetGroups[row.TransactionGroupID] || (row.ExecuteStatus == "PENDING" && row.TransactionOutcome == "") {
				continue
			}
			if err := ensureLegacySQLStatementRecord(tx, file, *row); err != nil {
				return nil, 0, err
			}
			message := "所属事务组的多个 ADD COLUMN 已自动拆分，等待从 BEGIN 整组执行"
			before := *row
			row.ExecuteStatus, row.ExecuteMessage, row.SQLState, row.TransactionOutcome = "PENDING", message, "", ""
			row.ExecuteTime, row.ExecuteUser, row.SkipKind, row.DatabaseUser = nil, "", "", ""
			row.ManualFailure, row.AffectedRows, row.DurationMs = false, 0, 0
			if err := tx.Save(row).Error; err != nil {
				return nil, 0, err
			}
			record := newSQLStatementRecord(file, before, "AUTO_SPLIT", username)
			no, now := false, time.Now()
			record.Status, record.Message, record.TransactionOutcome = "PENDING", message, ""
			record.Submitted, record.FinishedAt = &no, &now
			if err := tx.Create(&record).Error; err != nil {
				return nil, 0, err
			}
		}
		file.ImportMessage = ""
		if issue := releaseSQLExecutionIssue(file, rows); issue != nil {
			file.ImportMessage = issue.Error()
		}
		if err := tx.Model(file).Updates(map[string]any{"file_content": file.FileContent, "import_message": file.ImportMessage}).Error; err != nil {
			return nil, 0, err
		}
	}
	annotateReleaseSQLTransactionGroups(rows)
	return rows, changed, nil
}

// Upgrade previously uploaded files as well. Historical batch snapshots and
// execution attempts remain immutable; only current mutable SQL is expanded.
func BackfillSQLAddColumnStatements() (int, error) {
	var cursor uint
	changed := 0
	var failures error
	for {
		var ids []uint
		if err := repository.DB.Model(&model.SQLChangeFile{}).
			Where("id > ? AND is_deleted = false AND execute_status IN ?", cursor, []string{"PENDING", "FAILED", "CANCELED", "PARTIAL_FAILED"}).
			Where("EXISTS (SELECT 1 FROM sql_change_statements s WHERE s.file_id = sql_change_files.id AND s.execute_status IN ('PENDING','FAILED','CANCELED') AND s.sql_content ILIKE '%ADD%')").
			Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
			return changed, err
		}
		if len(ids) == 0 {
			return changed, failures
		}
		for _, id := range ids {
			normalized := false
			if err := repository.DB.Transaction(func(tx *gorm.DB) error {
				file, err := lockSQLFile(tx, id)
				if err != nil {
					return err
				}
				var rows []model.SQLChangeStatement
				if err := tx.Where("file_id = ?", id).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
					return err
				}
				_, n, err := normalizeReleaseSQLAddColumns(tx, file, rows, "system:auto_add_column_split")
				normalized = err == nil && n > 0
				return err
			}); err != nil {
				failures = errors.Join(failures, fmt.Errorf("自动拆分 SQL 文件 %d 失败: %w", id, err))
			} else if normalized {
				changed++
			}
			cursor = id
		}
	}
}

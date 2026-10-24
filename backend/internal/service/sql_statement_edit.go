package service

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"gorm.io/gorm"
)

type EditReleaseSQLStatementRequest struct {
	Content         string `json:"content" binding:"required"`
	OriginalContent string `json:"originalContent" binding:"required"`
}

// Replace by statement position, not by SQL text: identical statements can occur
// several times. Keep comments, delimiters and all other original file bytes.
func replaceReleaseSQLStatement(content string, rows []model.SQLChangeStatement, target int, replacement string) (string, []model.SQLChangeStatement, error) {
	parts, err := pg_query.SplitWithScanner(content, true)
	if err != nil {
		return "", nil, fmt.Errorf("原文件无法拆分，请重新上传修正后的文件: %w", err)
	}
	cursor, index := 0, 0
	start, end := -1, -1
	for _, raw := range parts {
		offset := strings.Index(content[cursor:], raw)
		if offset < 0 {
			return "", nil, fmt.Errorf("无法定位原 SQL，请重新加载")
		}
		pos := cursor + offset
		cursor = pos + len(raw)
		leading := sqlLeadingTriviaLength(raw)
		text := strings.TrimSpace(strings.TrimSuffix(raw[leading:], ";"))
		if text == "" {
			continue
		}
		if index >= len(rows) || rows[index].SQLContent != text {
			return "", nil, fmt.Errorf("原文件与语句记录不一致，请重新上传修正后的文件")
		}
		if index == target {
			start, end = pos+leading, pos+leading+len(text)
		}
		index++
	}
	if start < 0 || index != len(rows) {
		return "", nil, fmt.Errorf("无法定位原 SQL，请重新加载")
	}
	updated := content[:start] + replacement + content[end:]
	replacements, err := splitReleaseSQL(replacement)
	if err != nil {
		return "", nil, err
	}
	parsed, err := splitReleaseSQL(updated)
	if err != nil || len(parsed) != len(rows)-1+len(replacements) {
		return "", nil, fmt.Errorf("修改影响了其他语句的分隔，请检查分号、注释或引号是否完整")
	}
	for i := range parsed {
		expected := ""
		switch {
		case i < target:
			expected = rows[i].SQLContent
		case i < target+len(replacements):
			expected = replacements[i-target].SQLContent
		default:
			expected = rows[i-len(replacements)+1].SQLContent
		}
		if parsed[i].SQLContent != expected {
			return "", nil, fmt.Errorf("修改影响了其他语句，请检查分号和注释")
		}
	}
	return updated, parsed, nil
}

// Edits affect the pending release script only. Never connect to the business
// database here, and never reset successful/skipped statements for a retry.
func (s *PostgreSQLService) EditReleaseSQLStatement(id uint, req EditReleaseSQLStatementRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	if len(req.Content) > 50*1024*1024 || strings.ContainsRune(req.Content, '\x00') {
		return nil, nil, fmt.Errorf("SQL 内容过大或包含非法字符")
	}
	normalized, err := expandReleaseSQLAddColumnContent(req.Content)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := splitReleaseSQL(normalized)
	if err != nil {
		return nil, nil, err
	}
	var file *model.SQLChangeFile
	var rows []model.SQLChangeStatement
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		var initial model.SQLChangeStatement
		if err := tx.First(&initial, id).Error; err != nil {
			return err
		}
		// Same lock order as execution, upload, skip and deletion.
		var err error
		file, err = lockSQLFile(tx, initial.FileID)
		if err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, true); err != nil {
			return err
		}
		if !slices.Contains([]string{"PENDING", "FAILED", "CANCELED", "PARTIAL_FAILED"}, file.ExecuteStatus) {
			return fmt.Errorf("当前文件状态不允许修改，请等待执行结束或核实执行结果")
		}
		if err := tx.Where("file_id = ?", file.ID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
		annotateReleaseSQLTransactionGroups(rows)
		target := -1
		for i := range rows {
			if rows[i].ExecuteStatus == "RUNNING" || rows[i].ExecuteStatus == "UNKNOWN" {
				return fmt.Errorf("文件存在执行中或结果待核实的语句，不能修改")
			}
			if rows[i].ID == id {
				target = i
			}
		}
		if target < 0 {
			return fmt.Errorf("SQL 语句不存在")
		}
		stmt := rows[target]
		if stmt.ExecuteStatus != "PENDING" && stmt.ExecuteStatus != "FAILED" {
			return fmt.Errorf("只允许修改未执行或失败的 SQL")
		}
		if stmt.SQLContent != req.OriginalContent {
			return fmt.Errorf("SQL 已被其他人修改，请重新加载后再编辑")
		}
		content := parsed[0].SQLContent
		if len(parsed) > 1 {
			parts := make([]string, len(parsed))
			for i := range parsed {
				parts[i] = parsed[i].SQLContent
			}
			// Newlines keep delimiters outside trailing line comments. The final
			// newline also keeps added rows ahead of following same-line SQL when
			// existing statement IDs are preserved and new IDs are allocated.
			content = strings.Join(parts, "\n;\n") + "\n"
		}
		if content == stmt.SQLContent {
			return fmt.Errorf("SQL 内容未发生变化")
		}
		updated, positions, err := replaceReleaseSQLStatement(file.FileContent, rows, target, content)
		if err != nil {
			return err
		}
		if len(updated) > 50*1024*1024 {
			return fmt.Errorf("修改后的文件超过 50MB")
		}
		if err := validateReleaseSQLTransactions(positions); err != nil {
			return err
		}
		group, err := releaseSQLTransactionGroupAt(rows, target)
		if err != nil {
			return err
		}
		if group != nil {
			for _, row := range rows[group.Start : group.End+1] {
				if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" || row.ExecuteStatus == "RUNNING" || row.ExecuteStatus == "UNKNOWN" {
					return fmt.Errorf("事务组存在已完成或待核实的语句，不能只修改其中一部分")
				}
			}
		}
		// Transaction boundaries may be repaired inside the selected unit, but
		// must not silently pull unrelated statements into a new transaction.
		allowedStart, allowedEnd := target, target+len(parsed)-1
		if group != nil {
			allowedStart, allowedEnd = group.Start, group.End+len(parsed)-1
		}
		newGroups, err := releaseSQLTransactionGroups(positions)
		if err != nil {
			return err
		}
		for _, next := range newGroups {
			if next.Start <= target+len(parsed)-1 && next.End >= target && (next.Start < allowedStart || next.End > allowedEnd) {
				return fmt.Errorf("修改不能把当前片段或原事务组之外的语句纳入新事务，请检查 BEGIN/COMMIT 的边界")
			}
		}
		audit := model.SQLStatementEdit{FileID: file.ID, StatementID: id, BeforeSQL: stmt.SQLContent, AfterSQL: content, BeforeStatus: stmt.ExecuteStatus, BeforeMessage: stmt.ExecuteMessage, BeforeSQLState: stmt.SQLState, Editor: username}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		if err := ensureLegacySQLStatementRecord(tx, file, stmt); err != nil {
			return err
		}
		now := time.Now()
		reset := map[int]bool{target: true}
		if group != nil {
			for i := group.Start; i <= group.End; i++ {
				if i != target {
					reset[i] = true
				}
			}
		} else if partner := releaseSQLViewPairIndex(rows, target, file.SchemaName); partner >= 0 && rows[partner].ExecuteStatus == "FAILED" {
			reset[partner] = true
		}
		for index := range reset {
			if index == target {
				continue
			}
			row := &rows[index]
			if err := ensureLegacySQLStatementRecord(tx, file, *row); err != nil {
				return err
			}
			message := "所属语句组的 SQL 已修改，等待整组事务重试"
			if err := appendSQLActionRecord(tx, file, *row, "EDIT", username, "PENDING", message); err != nil {
				return err
			}
			row.ExecuteStatus, row.ExecuteMessage, row.SQLState = "PENDING", message, ""
			row.TransactionOutcome = ""
			row.DatabaseUser = ""
			row.ManualFailure, row.SkipKind = false, ""
			row.ExecuteTime, row.AffectedRows, row.DurationMs = nil, 0, 0
		}
		nextRows := make([]model.SQLChangeStatement, 0, len(positions))
		nextRows = append(nextRows, rows[:target]...)
		for i := range parsed {
			row := parsed[i]
			if i == 0 {
				row = stmt
			}
			row.FileID, row.SQLContent, row.ExecuteStatus = file.ID, parsed[i].SQLContent, "PENDING"
			row.LineNumber = positions[target+i].LineNumber
			row.ExecuteMessage, row.ExecuteUser, row.SQLState, row.SkipKind, row.TransactionOutcome = "", "", "", "", ""
			row.DatabaseUser = ""
			row.ManualFailure, row.ExecuteTime, row.AffectedRows, row.DurationMs = false, nil, 0, 0
			row.UpdatedAt = now
			if i > 0 {
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
			edited := row
			if i == 0 {
				edited = stmt
				edited.SQLContent, edited.LineNumber = row.SQLContent, row.LineNumber
			}
			record := newSQLStatementRecord(file, edited, "EDIT", username)
			no := false
			record.StartedAt = now
			record.Status, record.TransactionOutcome, record.Submitted, record.FinishedAt = "PENDING", "", &no, &now
			record.Message = "SQL 已修改，等待执行"
			if len(parsed) > 1 {
				record.Message = fmt.Sprintf("原第 %d 行记录已修正拆分为 %d 条 SQL：本句为第 %d 条，等待执行", stmt.LineNumber, len(parsed), i+1)
				if i > 0 {
					record.SourceStatementID = stmt.ID
				}
			}
			if err := tx.Create(&record).Error; err != nil {
				return err
			}
			nextRows = append(nextRows, row)
		}
		nextRows = append(nextRows, rows[target+1:]...)
		for i := range nextRows {
			nextRows[i].LineNumber = positions[i].LineNumber
			if nextRows[i].ID == id || (i >= target && i < target+len(parsed)) {
				if err := tx.Save(&nextRows[i]).Error; err != nil {
					return err
				}
				continue
			}
			oldIndex := i
			if i >= target+len(parsed) {
				oldIndex = i - len(parsed) + 1
			}
			if reset[oldIndex] {
				if err := tx.Save(&nextRows[i]).Error; err != nil {
					return err
				}
			} else if err := tx.Model(&model.SQLChangeStatement{}).Where("id = ?", nextRows[i].ID).Update("line_number", nextRows[i].LineNumber).Error; err != nil {
				return err
			}
		}
		rows = nextRows
		file.FileContent, file.ExecuteStatus = updated, "PENDING"
		file.ExecuteMessage, file.ExecuteUser, file.ExecuteTime = "", "", nil
		file.ImportMessage = ""
		if issue := releaseSQLExecutionIssue(file, rows); issue != nil {
			file.ImportMessage = issue.Error()
		}
		return tx.Save(file).Error
	})
	annotateReleaseSQLTransactionGroups(rows)
	return file, rows, err
}

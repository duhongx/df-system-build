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
	parsed, err := splitReleaseSQL(updated)
	if err != nil || len(parsed) != len(rows) {
		return "", nil, fmt.Errorf("修改后必须保持一条完整语句，不能影响其他语句的分隔")
	}
	for i := range rows {
		expected := rows[i].SQLContent
		if i == target {
			expected = replacement
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
	parsed, err := splitReleaseSQL(req.Content)
	if err != nil {
		return nil, nil, err
	}
	if len(parsed) != 1 {
		return nil, nil, fmt.Errorf("每次只能修改一条 SQL；DO 块可作为一条完整语句编辑")
	}
	if err := validateReleaseSQLTransactions(parsed); err != nil {
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
		if !slices.Contains([]string{"PENDING", "FAILED", "CANCELED", "PARTIAL_FAILED"}, file.ExecuteStatus) {
			return fmt.Errorf("当前文件状态不允许修改，请等待执行结束或核实执行结果")
		}
		if err := tx.Where("file_id = ?", file.ID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
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
		stmt := &rows[target]
		if stmt.ExecuteStatus != "PENDING" && stmt.ExecuteStatus != "FAILED" {
			return fmt.Errorf("只允许修改未执行或失败的 SQL")
		}
		if stmt.SQLContent != req.OriginalContent {
			return fmt.Errorf("SQL 已被其他人修改，请重新加载后再编辑")
		}
		content := parsed[0].SQLContent
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
		audit := model.SQLStatementEdit{FileID: file.ID, StatementID: id, BeforeSQL: stmt.SQLContent, AfterSQL: content, BeforeStatus: stmt.ExecuteStatus, BeforeMessage: stmt.ExecuteMessage, BeforeSQLState: stmt.SQLState, Editor: username}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		if err := ensureLegacySQLStatementRecord(tx, file, *stmt); err != nil {
			return err
		}
		edited := *stmt
		edited.SQLContent = content
		if err := appendSQLActionRecord(tx, file, edited, "EDIT", username, "PENDING", "SQL 已修改，等待执行"); err != nil {
			return err
		}
		now := time.Now()
		stmt.SkipKind = ""
		stmt.SQLContent, stmt.ExecuteStatus = content, "PENDING"
		stmt.ExecuteMessage, stmt.ExecuteUser, stmt.SQLState = "", "", ""
		stmt.ExecuteTime, stmt.AffectedRows, stmt.DurationMs = nil, 0, 0
		stmt.UpdatedAt = now
		for i := range rows {
			rows[i].LineNumber = positions[i].LineNumber
			if i == target {
				if err := tx.Save(&rows[i]).Error; err != nil {
					return err
				}
			} else if err := tx.Model(&model.SQLChangeStatement{}).Where("id = ?", rows[i].ID).Update("line_number", rows[i].LineNumber).Error; err != nil {
				return err
			}
		}
		file.FileContent, file.ExecuteStatus = updated, "PENDING"
		file.ExecuteMessage, file.ExecuteUser, file.ExecuteTime = "", "", nil
		file.ImportMessage = ""
		if issue := releaseSQLExecutionIssue(file, rows); issue != nil {
			file.ImportMessage = issue.Error()
		}
		return tx.Save(file).Error
	})
	return file, rows, err
}

package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"gorm.io/gorm"
)

type EditReleaseSQLFileRequest struct {
	Content         string  `json:"content" binding:"required"`
	OriginalContent *string `json:"originalContent" binding:"required"`
}

type releaseSQLFileEditUnit struct {
	Start, End int
	Key        string
	Completed  bool
}

// Only introduce whitespace at already scanned statement boundaries. Preserve
// comments and all original SQL bytes, including dollar-quoted function bodies.
func releaseSQLFileEditSeparateLines(content string) (string, error) {
	parts, err := pg_query.SplitWithScanner(content, true)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	scan, copied, count := 0, 0, 0
	for _, raw := range parts {
		offset := strings.Index(content[scan:], raw)
		if offset < 0 {
			return "", fmt.Errorf("无法定位 SQL 文件中的语句边界")
		}
		start := scan + offset
		scan = start + len(raw)
		leading := sqlLeadingTriviaLength(raw)
		if strings.TrimSpace(strings.TrimSuffix(raw[leading:], ";")) == "" {
			continue
		}
		if count > 0 {
			position := start + leading
			result.WriteString(content[copied:position])
			result.WriteByte('\n')
			copied = position
		}
		count++
	}
	result.WriteString(content[copied:])
	if result.Len() > 50*1024*1024 {
		return "", fmt.Errorf("调整行号后的 SQL 文件超过 50MB")
	}
	return result.String(), nil
}

func releaseSQLFileEditUnits(rows []model.SQLChangeStatement, allowInvalid bool) ([]releaseSQLFileEditUnit, error) {
	groups, err := releaseSQLTransactionGroups(rows)
	if err != nil {
		if !allowInvalid {
			return nil, err
		}
		// A broken, wholly uncompleted script can be repaired as a full file.
		// Never guess the transaction membership of completed historical SQL.
		for _, row := range rows {
			if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" {
				return nil, fmt.Errorf("原文件的事务边界异常且包含已完成语句，无法安全合并执行进度：%w", err)
			}
		}
		groups = nil
	} else if allowInvalid {
		if err := validateReleaseSQLTransactionStates(rows); err != nil {
			return nil, err
		}
	}
	byStart := map[int]int{}
	for _, group := range groups {
		byStart[group.Start] = group.End
	}
	units := make([]releaseSQLFileEditUnit, 0, len(rows))
	for i := 0; i < len(rows); i++ {
		end := i
		if value, ok := byStart[i]; ok {
			end = value
		}
		parts := make([]string, 0, end-i+1)
		completed := true
		for _, row := range rows[i : end+1] {
			parts = append(parts, row.SQLContent)
			completed = completed && (row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED")
		}
		units = append(units, releaseSQLFileEditUnit{Start: i, End: end, Key: strings.Join(parts, "\x00"), Completed: completed})
		i = end
	}
	return units, nil
}

// Repeated SQL matches by occurrence. If some occurrences are removed, keep
// completed occurrences first so an earlier success can never become new work.
// Explicit transactions match as whole units, including BEGIN and COMMIT.
func matchReleaseSQLFileEditRows(previous, current []model.SQLChangeStatement) (map[int]int, error) {
	oldUnits, err := releaseSQLFileEditUnits(previous, true)
	if err != nil {
		return nil, err
	}
	newUnits, err := releaseSQLFileEditUnits(current, false)
	if err != nil {
		return nil, err
	}
	oldByKey := map[string][]int{}
	newCounts := map[string]int{}
	for i, unit := range oldUnits {
		oldByKey[unit.Key] = append(oldByKey[unit.Key], i)
	}
	for _, unit := range newUnits {
		newCounts[unit.Key]++
	}
	for key, candidates := range oldByKey {
		keep := newCounts[key]
		if keep >= len(candidates) {
			continue
		}
		chosen := make([]int, 0, keep)
		for _, index := range candidates {
			if oldUnits[index].Completed {
				chosen = append(chosen, index)
			}
		}
		if len(chosen) > keep {
			row := previous[oldUnits[chosen[keep]].Start]
			return nil, fmt.Errorf("原第 %d 行的成功／已跳过 SQL 或完整事务组被修改、删除或改变分组；请保留其内容，修改失败或未执行部分", row.LineNumber)
		}
		for _, index := range candidates {
			if len(chosen) == keep {
				break
			}
			if !oldUnits[index].Completed {
				chosen = append(chosen, index)
			}
		}
		slices.Sort(chosen)
		oldByKey[key] = chosen
	}
	matches := map[int]int{}
	lastCompleted := -1
	for _, unit := range newUnits {
		candidates := oldByKey[unit.Key]
		if len(candidates) == 0 {
			continue
		}
		oldIndex := candidates[0]
		oldByKey[unit.Key] = candidates[1:]
		old := oldUnits[oldIndex]
		if old.Completed {
			if oldIndex < lastCompleted {
				return nil, fmt.Errorf("原第 %d 行的已完成 SQL／事务组顺序发生变化；请保留已完成部分的顺序", previous[old.Start].LineNumber)
			}
			lastCompleted = oldIndex
		}
		for offset := 0; offset <= unit.End-unit.Start; offset++ {
			matches[unit.Start+offset] = old.Start + offset
		}
	}
	return matches, nil
}

// This updates platform metadata only. It never connects to a business DB,
// resets a completed SQL, deletes an execution attempt or rewrites a snapshot.
func (s *PostgreSQLService) EditReleaseSQLFile(id uint, req EditReleaseSQLFileRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, *model.SQLFileEdit, error) {
	if req.OriginalContent == nil {
		return nil, nil, nil, fmt.Errorf("缺少原始文件内容，请重新打开文件后编辑")
	}
	if len(req.Content) > 50*1024*1024 || strings.ContainsRune(req.Content, '\x00') {
		return nil, nil, nil, fmt.Errorf("SQL 文件超过 50MB 或包含非法字符")
	}
	parsed, err := splitReleaseSQL(req.Content)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := validateReleaseSQLTransactions(parsed); err != nil {
		return nil, nil, nil, err
	}
	var file *model.SQLChangeFile
	var rows []model.SQLChangeStatement
	var edit model.SQLFileEdit
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		file, err = lockSQLFile(tx, id)
		if err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, true); err != nil {
			return err
		}
		if !slices.Contains([]string{"PENDING", "FAILED", "CANCELED", "PARTIAL_FAILED"}, file.ExecuteStatus) {
			return fmt.Errorf("只允许编辑未完成的文件；执行中、排队中、已完成或结果待核实的文件不可保存修改")
		}
		if file.FileContent != *req.OriginalContent {
			return fmt.Errorf("文件内容已被其他操作修改，本次草稿未保存；请先复制草稿，再重新加载最新内容")
		}
		if file.FileContent == req.Content {
			return fmt.Errorf("文件内容未发生变化")
		}
		var previous []model.SQLChangeStatement
		if err := tx.Where("file_id = ?", id).Order("line_number ASC, id ASC").Find(&previous).Error; err != nil {
			return err
		}
		for _, row := range previous {
			if row.ExecuteStatus == "RUNNING" || row.ExecuteStatus == "UNKNOWN" {
				return fmt.Errorf("第 %d 行正在执行或结果待核实，不能修改整个文件", row.LineNumber)
			}
		}
		matches, err := matchReleaseSQLFileEditRows(previous, parsed)
		if err != nil {
			return err
		}
		annotateReleaseSQLTransactionGroups(previous)
		for _, row := range previous {
			if err := ensureLegacySQLStatementRecord(tx, file, row); err != nil {
				return err
			}
		}
		beforeJSON, err := json.Marshal(previous)
		if err != nil {
			return err
		}
		edit = model.SQLFileEdit{FileID: id, FileName: file.FileName, SchemaName: file.SchemaName, DatabaseTarget: file.DatabaseTarget,
			BeforeContent: file.FileContent, AfterContent: req.Content, BeforeStatementsJSON: string(beforeJSON), BeforeStatus: file.ExecuteStatus, Editor: username,
			PreservedStatements: len(matches), NewStatements: len(parsed) - len(matches), RemovedStatements: len(previous) - len(matches)}
		if err := tx.Create(&edit).Error; err != nil {
			return err
		}
		kept := map[uint]bool{}
		rows = make([]model.SQLChangeStatement, len(parsed))
		for i, next := range parsed {
			if oldIndex, ok := matches[i]; ok {
				row := previous[oldIndex]
				row.LineNumber = next.LineNumber
				if err := tx.Model(&model.SQLChangeStatement{}).Where("id = ?", row.ID).Update("line_number", row.LineNumber).Error; err != nil {
					return err
				}
				kept[row.ID], rows[i] = true, row
				continue
			}
			next.FileID = id
			next.ExecuteMessage = fmt.Sprintf("整文件修改 #%d：新增或修改的 SQL，等待执行", edit.ID)
			if err := tx.Create(&next).Error; err != nil {
				return err
			}
			rows[i] = next
			if err := appendSQLActionRecord(tx, file, next, "EDIT", username, "PENDING", next.ExecuteMessage); err != nil {
				return err
			}
		}
		// Reused IDs and newly allocated IDs may share an original source line.
		// Reject ambiguous ID ordering by adding a delimiter newline when needed.
		content := req.Content
		for i := 1; i < len(rows); i++ {
			if rows[i-1].LineNumber == rows[i].LineNumber && rows[i-1].ID > rows[i].ID {
				// Keep original comments: insert newlines only at scanner-delimited
				// statement boundaries, never rebuild the complete SQL from ASTs.
				content, err = releaseSQLFileEditSeparateLines(content)
				if err != nil {
					return err
				}
				positions, err := splitReleaseSQL(content)
				if err != nil || len(positions) != len(rows) {
					return fmt.Errorf("无法同步修改后的 SQL 行号")
				}
				for j := range rows {
					rows[j].LineNumber = positions[j].LineNumber
					if err := tx.Model(&model.SQLChangeStatement{}).Where("id = ?", rows[j].ID).Update("line_number", rows[j].LineNumber).Error; err != nil {
						return err
					}
				}
				break
			}
		}
		for _, row := range previous {
			if kept[row.ID] {
				continue
			}
			if err := appendSQLActionRecord(tx, file, row, "EDIT", username, "REMOVED", fmt.Sprintf("整文件修改 #%d：本条旧 SQL 已被修改或从当前文件移除；原执行历史保留", edit.ID)); err != nil {
				return err
			}
			if err := tx.Delete(&model.SQLChangeStatement{}, row.ID).Error; err != nil {
				return err
			}
		}
		beforeFile := *file
		file.FileContent, file.ExecuteStatus, file.ExecutionBatchID = content, "PENDING", 0
		rows, _, err = normalizeReleaseSQLAddColumns(tx, file, rows, username)
		if err != nil {
			return err
		}
		if err := validateReleaseSQLTransactionStates(rows); err != nil {
			return err
		}
		// ADD COLUMN expansion can add rows and reset a rolled-back group.
		// Report the final saved states, rather than the pre-normalization plan.
		oldByID := map[uint]model.SQLChangeStatement{}
		oldPositions := map[uint]int{}
		for index, row := range previous {
			oldByID[row.ID] = row
			oldPositions[row.ID] = index
		}
		annotateReleaseSQLTransactionGroups(rows)
		for i := range rows {
			row := &rows[i]
			oldIndex, existed := oldPositions[row.ID]
			if !existed || row.ExecuteStatus != "FAILED" || row.TransactionGroupID != 0 {
				continue
			}
			partner := releaseSQLViewPairIndex(rows, i, file.SchemaName)
			oldPartner := releaseSQLViewPairIndex(previous, oldIndex, file.SchemaName)
			if partner < 0 || oldPartner < 0 || rows[partner].ExecuteStatus != "PENDING" || kept[rows[partner].ID] || previous[oldPartner].ExecuteStatus != "FAILED" {
				continue
			}
			message := fmt.Sprintf("整文件修改 #%d：所属视图 DROP/CREATE 组合已修改，等待整组事务重试", edit.ID)
			if err := appendSQLActionRecord(tx, file, *row, "EDIT", username, "PENDING", message); err != nil {
				return err
			}
			row.ExecuteStatus, row.ExecuteMessage, row.SQLState, row.TransactionOutcome = "PENDING", message, "", ""
			row.ExecuteTime, row.ExecuteUser, row.SkipKind, row.DatabaseUser = nil, "", "", ""
			row.ManualFailure, row.AffectedRows, row.DurationMs = false, 0, 0
			if err := tx.Save(row).Error; err != nil {
				return err
			}
		}
		edit.PreservedStatements = 0
		for _, row := range rows {
			if old, ok := oldByID[row.ID]; ok && old.SQLContent == row.SQLContent && old.ExecuteStatus == row.ExecuteStatus && old.ManualFailure == row.ManualFailure && old.TransactionOutcome == row.TransactionOutcome {
				edit.PreservedStatements++
			}
		}
		edit.NewStatements, edit.RemovedStatements = len(rows)-edit.PreservedStatements, len(previous)-edit.PreservedStatements
		edit.AfterContent = file.FileContent
		if err := tx.Save(&edit).Error; err != nil {
			return err
		}
		file.ExecuteMessage = fmt.Sprintf("全文已保存（修改 #%d）：保留 %d 条原状态，%d 条调整为待执行，替换／移除 %d 条旧 SQL；保存未执行 SQL", edit.ID, edit.PreservedStatements, edit.NewStatements, edit.RemovedStatements)
		file.ExecuteUser, file.ExecuteTime, file.ImportMessage = "", nil, ""
		if !hasSQLContinuationWork(rows) {
			file.ExecuteStatus = "SUCCESS"
			allSkipped := len(rows) > 0
			for _, row := range rows {
				if row.ExecuteStatus != "SKIPPED" {
					allSkipped = false
				}
				if row.ExecuteStatus == "FAILED" {
					file.ExecuteStatus = "FAILED"
				}
				if row.ExecuteTime != nil && (file.ExecuteTime == nil || row.ExecuteTime.After(*file.ExecuteTime)) {
					file.ExecuteUser, file.ExecuteTime = row.ExecuteUser, row.ExecuteTime
				}
			}
			if allSkipped {
				file.ExecuteStatus = "SKIPPED"
			}
		}
		if issue := releaseSQLExecutionIssue(file, rows); issue != nil {
			file.ImportMessage = issue.Error()
		}
		if err := tx.Save(file).Error; err != nil {
			return err
		}
		no, now := false, time.Now()
		record := newSQLStatementRecord(file, model.SQLChangeStatement{ExecuteStatus: beforeFile.ExecuteStatus, ExecuteMessage: beforeFile.ExecuteMessage, ExecuteUser: beforeFile.ExecuteUser, ExecuteTime: beforeFile.ExecuteTime}, "FILE_EDIT", username)
		record.Status, record.Message, record.Submitted, record.FinishedAt = "PENDING", file.ExecuteMessage, &no, &now
		return tx.Create(&record).Error
	})
	if err != nil {
		return nil, nil, nil, err
	}
	annotateReleaseSQLTransactionGroups(rows)
	// Return summary only; the source is available from the explicit audit API.
	edit.BeforeContent, edit.AfterContent, edit.BeforeStatementsJSON = "", "", ""
	return file, rows, &edit, nil
}

func (s *PostgreSQLService) ListSQLFileEdits(id uint, page, size int) ([]model.SQLFileEdit, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	query := repository.DB.Model(&model.SQLFileEdit{}).Where("file_id = ?", id)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var edits []model.SQLFileEdit
	err := query.Omit("before_content", "after_content", "before_statements_json").Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&edits).Error
	return edits, total, err
}

func (s *PostgreSQLService) GetSQLFileEdit(fileID, editID uint) (*model.SQLFileEdit, []model.SQLChangeStatement, error) {
	var edit model.SQLFileEdit
	if err := repository.DB.Where("file_id = ?", fileID).First(&edit, editID).Error; err != nil {
		return nil, nil, err
	}
	var before []model.SQLChangeStatement
	if err := json.Unmarshal([]byte(edit.BeforeStatementsJSON), &before); err != nil {
		return nil, nil, fmt.Errorf("读取保存前的 SQL 状态失败: %w", err)
	}
	edit.BeforeStatementsJSON = ""
	return &edit, before, nil
}

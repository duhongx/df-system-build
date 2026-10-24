package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

type SQLFileViewTransferRequest struct {
	All          bool   `json:"all"`
	StatementIDs []uint `json:"statementIds"`
	SelectedIDs  []uint `json:"selectedIds"`
	PreviewHash  string `json:"previewHash"`
}

// A unit is either one statement or one complete original transaction. The UI
// cannot accidentally select only part of a BEGIN/COMMIT block.
type SQLViewTransferUnit struct {
	ID          uint                       `json:"id"`
	StartLine   int                        `json:"startLine"`
	EndLine     int                        `json:"endLine"`
	Transaction bool                       `json:"transaction"`
	ChangeCount int                        `json:"changeCount"`
	Tables      []string                   `json:"tables"`
	Targets     []string                   `json:"targets"`
	Statements  []model.SQLChangeStatement `json:"statements"`
	ContextKey  string                     `json:"contextKey"`
}

type SQLViewTransferExclusion struct {
	ID        uint   `json:"id"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Reason    string `json:"reason"`
}

type SQLFileViewTransferPreview struct {
	FileID      uint                       `json:"fileId"`
	FileName    string                     `json:"fileName"`
	Units       []SQLViewTransferUnit      `json:"units"`
	Excluded    []SQLViewTransferExclusion `json:"excluded"`
	PreviewHash string                     `json:"previewHash"`
}

func loadSQLFileViewTransfer(tx *gorm.DB, fileID uint, target string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	file, err := lockSQLFile(tx, fileID)
	if err != nil {
		return nil, nil, err
	}
	if err := requireReleaseSQLTarget(file, target); err != nil {
		return nil, nil, err
	}
	if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" || file.ExecuteStatus == "UNKNOWN" {
		return nil, nil, fmt.Errorf("文件正在执行或结果待核实，不能转入视图依赖")
	}
	if file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" {
		return nil, nil, fmt.Errorf("文件已完成或已标记不执行，不能重新转入视图依赖")
	}
	if err := guardSQLFileViewTask(tx, fileID, false); err != nil {
		return nil, nil, err
	}
	var rows []model.SQLChangeStatement
	if err := tx.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		if row.ExecuteStatus == "UNKNOWN" || row.ExecuteStatus == "RUNNING" || row.ExecuteStatus == "QUEUED" || row.ExecuteStatus == "APPLIED" {
			return nil, nil, fmt.Errorf("第 %d 行正在执行或结果待核实，请先处理原执行结果", row.LineNumber)
		}
	}
	return file, rows, nil
}

func buildSQLFileViewTransferPreview(file model.SQLChangeFile, rows []model.SQLChangeStatement, req SQLFileViewTransferRequest, target string) (*SQLFileViewTransferPreview, error) {
	if req.All == (len(req.StatementIDs) > 0) {
		return nil, fmt.Errorf("请选择整文件或指定语句，不能同时选择两种范围")
	}
	wanted := make(map[uint]bool, len(req.StatementIDs))
	for _, id := range req.StatementIDs {
		if id == 0 || wanted[id] {
			return nil, fmt.Errorf("语句 ID 无效或重复")
		}
		wanted[id] = true
	}
	known := make(map[uint]bool, len(rows))
	for _, row := range rows {
		known[row.ID] = true
	}
	for id := range wanted {
		if !known[id] {
			return nil, fmt.Errorf("语句 %d 不属于当前文件，不能跨文件合并", id)
		}
	}
	groups, err := releaseSQLTransactionGroups(rows)
	if err != nil {
		return nil, err
	}
	annotateReleaseSQLTransactionGroups(rows)
	contexts := collectSQLViewSourceContexts(file, rows)
	byStart := make(map[int]releaseSQLTransactionGroup, len(groups))
	for _, group := range groups {
		byStart[group.Start] = group
	}
	preview := &SQLFileViewTransferPreview{FileID: file.ID, FileName: file.FileName, Units: []SQLViewTransferUnit{}, Excluded: []SQLViewTransferExclusion{}}
	for start := 0; start < len(rows); {
		end := start
		group, transaction := byStart[start]
		if transaction {
			end = group.End
		}
		block := rows[start : end+1]
		include := req.All
		for _, row := range block {
			include = include || wanted[row.ID]
		}
		if include {
			unit, issue := buildSQLViewTransferUnit(block, transaction, group.Rollback)
			if issue == nil {
				var sourceContext sqlViewSourceContext
				sourceContext, issue = commonSQLViewSourceContext(block, contexts)
				if issue == nil {
					unit.ContextKey = sqlContentHash(sqlViewContextDescriptor(sourceContext))
				}
			}
			if issue != nil {
				preview.Excluded = append(preview.Excluded, SQLViewTransferExclusion{ID: block[0].ID, StartLine: block[0].LineNumber, EndLine: block[len(block)-1].LineNumber, Reason: issue.Error()})
			} else {
				preview.Units = append(preview.Units, unit)
			}
		}
		start = end + 1
	}
	ids := append([]uint(nil), req.StatementIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	// Bind confirmation to the reviewed source, scope, states and target. A
	// concurrent edit, retry or transfer forces a fresh preview before creation.
	snapshot, err := json.Marshal(struct {
		FileID    uint
		UpdatedAt time.Time
		Target    string
		All       bool
		IDs       []uint
		Rows      []model.SQLChangeStatement
	}{file.ID, file.UpdatedAt, target, req.All, ids, rows})
	if err != nil {
		return nil, err
	}
	preview.PreviewHash = sqlContentHash(string(snapshot))
	return preview, nil
}

func buildSQLViewTransferUnit(block []model.SQLChangeStatement, transaction, rollback bool) (SQLViewTransferUnit, error) {
	unit := SQLViewTransferUnit{ID: block[0].ID, StartLine: block[0].LineNumber, EndLine: block[len(block)-1].LineNumber, Transaction: transaction, Statements: block, Tables: []string{}, Targets: []string{}}
	if transaction {
		if rollback {
			return unit, fmt.Errorf("原事务以 ROLLBACK 结束，不能转为提交变更")
		}
		begin := releaseSQLNode(block[0].SQLContent).GetTransactionStmt()
		if begin == nil || len(begin.Options) > 0 {
			return unit, fmt.Errorf("原事务含特殊选项，需保留原事务人工处理")
		}
	}
	seenTables := map[string]bool{}
	for i, row := range block {
		if row.ViewDependencyTaskID > 0 {
			return unit, fmt.Errorf("第 %d 行已关联视图依赖任务 %d", row.LineNumber, row.ViewDependencyTaskID)
		}
		switch row.ExecuteStatus {
		case "", "PENDING", "FAILED", "PARTIAL_FAILED", "TIMEOUT", "CANCELED", "NOT_EXECUTABLE", "BLOCKED":
		default:
			return unit, fmt.Errorf("第 %d 行已有完成、跳过或待核实结果，保持原状", row.LineNumber)
		}
		if transaction && (i == 0 || i == len(block)-1) {
			continue
		}
		refs, err := validateViewDependencyAlterSQLs(SQLViewDependencyTaskRequest{AlterSQL: row.SQLContent})
		if err != nil {
			return unit, fmt.Errorf("第 %d 行不支持转入：%w；事务组须全部为字段类型变更", row.LineNumber, err)
		}
		for _, ref := range refs {
			table := quoteQualifiedName(ref.schema, ref.table)
			if !seenTables[table] {
				seenTables[table] = true
				unit.Tables = append(unit.Tables, table)
			}
			unit.Targets = append(unit.Targets, table+"."+quoteIdent(ref.column))
			unit.ChangeCount++
		}
	}
	if unit.ChangeCount == 0 {
		return unit, fmt.Errorf("没有字段类型变更")
	}
	return unit, nil
}

func (s *PostgreSQLService) PreviewSQLFileViewTransfer(fileID uint, req SQLFileViewTransferRequest) (*SQLFileViewTransferPreview, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, err
	}
	var preview *SQLFileViewTransferPreview
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		file, rows, err := loadSQLFileViewTransfer(tx, fileID, target)
		if err != nil {
			return err
		}
		preview, err = buildSQLFileViewTransferPreview(*file, rows, req, target)
		return err
	})
	return preview, err
}

func (s *PostgreSQLService) TransferSQLFileToViewTask(fileID uint, req SQLFileViewTransferRequest, username string) (*model.SQLViewDependencyTask, error) {
	if req.PreviewHash == "" || len(req.SelectedIDs) == 0 {
		return nil, fmt.Errorf("请先预览并选择要转入的字段变更")
	}
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, err
	}
	var task *model.SQLViewDependencyTask
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		file, rows, err := loadSQLFileViewTransfer(tx, fileID, target)
		if err != nil {
			return err
		}
		preview, err := buildSQLFileViewTransferPreview(*file, rows, req, target)
		if err != nil {
			return err
		}
		if preview.PreviewHash != req.PreviewHash {
			return fmt.Errorf("文件内容或处理状态已变化，请重新预览后转入")
		}
		selected := make(map[uint]bool, len(req.SelectedIDs))
		for _, id := range req.SelectedIDs {
			if id == 0 || selected[id] {
				return fmt.Errorf("选择的变更单元无效或重复")
			}
			selected[id] = true
		}
		var sources []model.SQLChangeStatement
		transactions, selectedUnits := 0, 0
		for _, unit := range preview.Units {
			if !selected[unit.ID] {
				continue
			}
			selectedUnits++
			if unit.Transaction {
				transactions++
			}
			sources = append(sources, unit.Statements...)
		}
		if selectedUnits != len(selected) {
			return fmt.Errorf("选择包含不可转入或不在预览范围内的语句，请重新预览")
		}
		if transactions > 0 && selectedUnits != 1 {
			return fmt.Errorf("为保留原 BEGIN/COMMIT 边界，一个原事务组须单独转入；不能与其他事务组或组外语句合并")
		}
		task, err = createSQLViewTransferTask(tx, file, sources, target, username)
		return err
	})
	return task, err
}

func createSQLViewTransferTask(tx *gorm.DB, file *model.SQLChangeFile, rows []model.SQLChangeStatement, target, username string) (*model.SQLViewDependencyTask, error) {
	if file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" || file.ExecuteStatus == "UNKNOWN" || file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
		return nil, fmt.Errorf("当前文件状态不允许转入视图依赖")
	}
	var all []model.SQLChangeStatement
	if err := tx.Where("file_id = ?", file.ID).Order("line_number,id").Find(&all).Error; err != nil {
		return nil, err
	}
	sourceContext, err := commonSQLViewSourceContext(rows, collectSQLViewSourceContexts(*file, all))
	if err != nil {
		return nil, err
	}
	contextJSON, err := json.Marshal(sourceContext)
	if err != nil {
		return nil, err
	}
	var sources []sqlViewSource
	var alters []string
	var refs []alterColumnRef
	for _, row := range rows {
		if row.FileID != file.ID || row.ViewDependencyTaskID > 0 {
			return nil, fmt.Errorf("第 %d 行不属于当前文件或已关联视图依赖任务", row.LineNumber)
		}
		switch row.ExecuteStatus {
		case "", "PENDING", "FAILED", "PARTIAL_FAILED", "TIMEOUT", "CANCELED", "NOT_EXECUTABLE", "BLOCKED":
		default:
			return nil, fmt.Errorf("第 %d 行已有完成、跳过或待核实结果，不能重新转入", row.LineNumber)
		}
		sources = append(sources, sqlViewSource{ID: row.ID, SQL: row.SQLContent, Line: row.LineNumber, GroupID: row.TransactionGroupID})
		if row.TransactionGroupID > 0 && (row.ID == row.TransactionGroupID || row.LineNumber == row.TransactionEndLine) && releaseSQLNode(row.SQLContent).GetTransactionStmt() != nil {
			continue
		}
		parsed, err := validateViewDependencyAlterSQLs(SQLViewDependencyTaskRequest{AlterSQL: row.SQLContent})
		if err != nil {
			return nil, fmt.Errorf("第 %d 行不能转入：%w", row.LineNumber, err)
		}
		for _, ref := range parsed {
			ref.line += row.LineNumber - 1
			ref.order = len(refs) + 1
			refs = append(refs, ref)
		}
		// A newline before the separator also handles a trailing -- comment.
		alters = append(alters, strings.TrimSpace(row.SQLContent))
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("没有可转入的字段类型变更")
	}
	alterSQL := strings.Join(alters, "\n;\n") + "\n;"
	if _, err := validateViewDependencyAlterSQLs(SQLViewDependencyTaskRequest{AlterSQL: alterSQL}); err != nil {
		return nil, err
	}
	snapshot, err := json.Marshal(sources)
	if err != nil {
		return nil, err
	}
	task := &model.SQLViewDependencyTask{SourceFileID: file.ID, SourceFileName: file.FileName, SourceStatementsJSON: string(snapshot), SourceContextJSON: string(contextJSON), SourceSyncStatus: "WAITING", DatabaseTarget: target, SchemaName: refs[0].schema, TableName: refs[0].table, ColumnName: refs[0].column, AlterSQL: alterSQL, Status: "CREATED", ExecutionMode: SQLViewDependencyExecutionModeTransaction, LockTimeout: "5s", StatementTimeout: "10min", Operator: username, RiskLevel: "WARN", RiskReason: "源 SQL 已暂缓；分析并确认后在事务中执行字段变更和依赖视图恢复"}
	if err := tx.Create(task).Error; err != nil {
		return nil, err
	}
	for _, ref := range refs {
		change := buildSQLViewDependencyChange(task.ID, ref, "CREATED")
		if err := tx.Create(&change).Error; err != nil {
			return nil, err
		}
	}
	now := time.Now()
	for _, row := range rows {
		if err := ensureLegacySQLStatementRecord(tx, file, row); err != nil {
			return nil, err
		}
		message := fmt.Sprintf("已转入视图依赖任务 %d，暂缓普通执行；本次未提交数据库", task.ID)
		if row.ExecuteMessage != "" {
			message += "；原记录：" + row.ExecuteMessage
		}
		if err := appendSQLActionRecord(tx, file, row, "VIEW_DEPENDENCY_TRANSFER", username, "FAILED", message); err != nil {
			return nil, err
		}
		outcome := ""
		if row.TransactionGroupID > 0 {
			outcome = "MANUAL_FAILED"
		}
		if err := tx.Model(&row).Updates(map[string]any{"view_dependency_task_id": task.ID, "execute_status": "FAILED", "manual_failure": true, "transaction_outcome": outcome, "execute_message": message, "execute_user": username, "execute_time": now}).Error; err != nil {
			return nil, err
		}
	}
	if err := tx.Model(file).Updates(map[string]any{"database_target": target, "execution_batch_id": 0}).Error; err != nil {
		return nil, err
	}
	if _, err := recomputeSQLFileStatusWithDB(tx, file.ID, username); err != nil {
		return nil, err
	}
	return task, nil
}

package service

import (
	"encoding/json"
	"fmt"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SQLCurrentFailure struct {
	model.SQLChangeStatement
	FileName    string `json:"fileName"`
	SchemaName  string `json:"schemaName"`
	Version     string `json:"version"`
	TaskStatus  string `json:"taskStatus,omitempty"`
	TaskMessage string `json:"taskMessage,omitempty"`
}
type SQLFailureFilter struct {
	Keyword, FileName, SchemaName, Handling string
	FileID                                  uint
}

func (s *PostgreSQLService) ListCurrentFailedSQL(page, size int, f SQLFailureFilter) ([]SQLCurrentFailure, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, 0, err
	}
	q := repository.DB.Table("sql_change_statements AS s").Joins("JOIN sql_change_files f ON f.id=s.file_id").Joins("LEFT JOIN sql_view_dependency_tasks t ON t.id=s.view_dependency_task_id").
		Where("f.is_deleted=false AND (f.database_target=? OR f.database_target='')", target).
		Where("s.execute_status IN ?", []string{"FAILED", "PARTIAL_FAILED", "TIMEOUT"}).
		Where("COALESCE(s.transaction_outcome,'') NOT IN ?", []string{"ROLLED_BACK", "NOT_EXECUTED"})
	if f.Keyword != "" {
		q = q.Where("s.sql_content ILIKE ?", "%"+f.Keyword+"%")
	}
	if f.FileName != "" {
		q = q.Where("f.file_name ILIKE ?", "%"+f.FileName+"%")
	}
	if f.FileID > 0 {
		q = q.Where("f.id = ?", f.FileID)
	}
	if f.SchemaName != "" {
		q = q.Where("f.schema_name=?", f.SchemaName)
	}
	if f.Handling == "view" {
		q = q.Where("s.view_dependency_task_id>0")
	} else if f.Handling == "retry" {
		q = q.Where("s.view_dependency_task_id=0")
	}
	var total int64
	if err = q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []SQLCurrentFailure
	err = q.Select("s.*, f.file_name, f.schema_name, f.version, t.status AS task_status, t.execute_message AS task_message").Order("f.version, f.group_sort_no, f.file_name, s.line_number, s.id").Offset((page - 1) * size).Limit(size).Scan(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	// Annotate groups from the whole source file, not only this page of failures.
	ids := map[uint]bool{}
	for _, row := range rows {
		ids[row.FileID] = true
	}
	for id := range ids {
		var all []model.SQLChangeStatement
		if err = repository.DB.Where("file_id=?", id).Order("line_number,id").Find(&all).Error; err != nil {
			return nil, 0, err
		}
		annotateReleaseSQLTransactionGroups(all)
		byID := map[uint]model.SQLChangeStatement{}
		for _, r := range all {
			byID[r.ID] = r
		}
		for i := range rows {
			if rows[i].FileID == id {
				rows[i].SQLChangeStatement = byID[rows[i].ID]
			}
		}
	}
	return rows, total, nil
}

type sqlViewSource struct {
	ID      uint   `json:"id"`
	SQL     string `json:"sql"`
	Line    int    `json:"line"`
	GroupID uint   `json:"groupId"`
}

// An attached source is immutable until the task is resolved. Ordinary
// continuation may skip deferred sources; it cannot race a live view task.
func guardSQLFileViewTask(tx *gorm.DB, id uint, mutating bool) error {
	q := tx.Table("sql_change_statements s").Joins("JOIN sql_view_dependency_tasks t ON t.id=s.view_dependency_task_id").Where("s.file_id=? AND s.execute_status <> 'SUCCESS'", id)
	if !mutating {
		q = q.Where("t.status IN ?", []string{"ANALYZING", "PRECHECKING", "EXECUTING", "RESTORING", "VERIFYING", "UNKNOWN"})
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("文件包含关联视图依赖任务，请先在视图依赖变更页面完成处理；当前操作不能改写关联语句或与执行中的任务并行")
	}
	return nil
}
func guardSQLStatementViewTask(stmt model.SQLChangeStatement) error {
	if stmt.ViewDependencyTaskID > 0 {
		return fmt.Errorf("该语句已转入视图依赖任务 %d，请在关联任务中处理", stmt.ViewDependencyTaskID)
	}
	return nil
}

// This creates a draft and defers the source. It never executes business SQL.
func (s *PostgreSQLService) TransferFailedSQLToViewTask(id uint, username string) (*model.SQLViewDependencyTask, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, err
	}
	var task model.SQLViewDependencyTask
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		var stmt model.SQLChangeStatement
		if err := tx.First(&stmt, id).Error; err != nil {
			return err
		}
		file, err := lockSQLFile(tx, stmt.FileID)
		if err != nil {
			return err
		}
		if err = tx.First(&stmt, id).Error; err != nil {
			return err
		}
		if err = requireReleaseSQLTarget(file, target); err != nil {
			return err
		}
		if file.DatabaseTarget == "" {
			if err = tx.Model(file).Update("database_target", target).Error; err != nil {
				return err
			}
			file.DatabaseTarget = target
		}
		if stmt.ViewDependencyTaskID > 0 {
			return tx.Where("is_deleted=false").First(&task, stmt.ViewDependencyTaskID).Error
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
			return fmt.Errorf("文件正在执行，请结束后再转入视图依赖变更")
		}
		if stmt.ExecuteStatus != "FAILED" {
			return fmt.Errorf("只有当前执行失败的 SQL 可以转入")
		}
		var all []model.SQLChangeStatement
		if err = tx.Where("file_id=?", file.ID).Order("line_number,id").Find(&all).Error; err != nil {
			return err
		}
		annotateReleaseSQLTransactionGroups(all)
		index := -1
		for i := range all {
			if all[i].ID == id {
				index = i
			}
		}
		if index < 0 {
			return fmt.Errorf("源语句不存在")
		}
		block := all[index : index+1]
		group, err := releaseSQLTransactionGroupAt(all, index)
		if err != nil {
			return err
		}
		if group != nil {
			if err = validateReleaseSQLTransactionStates(all); err != nil {
				return err
			}
			if group.Rollback {
				return fmt.Errorf("以 ROLLBACK 结束的事务不能转成已提交的视图变更")
			}
			begin := releaseSQLNode(all[group.Start].SQLContent).GetTransactionStmt()
			if len(begin.Options) > 0 {
				return fmt.Errorf("事务包含隔离级别等特殊选项，需整组人工审阅，不能单独转移")
			}
			block = all[group.Start : group.End+1]
		}
		for i, row := range block {
			if row.ViewDependencyTaskID > 0 {
				return fmt.Errorf("事务组已关联其他视图依赖任务")
			}
			if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" || row.ExecuteStatus == "RUNNING" || row.ExecuteStatus == "UNKNOWN" {
				return fmt.Errorf("第 %d 行已有完成或待核实结果，不能重新提交", row.LineNumber)
			}
			if group == nil || i > 0 && i < len(block)-1 {
				if _, err := validateViewDependencyAlterSQLs(SQLViewDependencyTaskRequest{AlterSQL: row.SQLContent}); err != nil {
					return fmt.Errorf("第 %d 行不能转入：%w；事务组必须全部为字段类型变更，不能只取其中一句", row.LineNumber, err)
				}
			}
		}
		created, err := createSQLViewTransferTask(tx, file, block, target, username)
		if err != nil {
			return err
		}
		task = *created
		return nil
	})
	return &task, err
}

// Called while claiming a view phase and again before publishing success.
func validateSQLViewTaskSource(tx *gorm.DB, task model.SQLViewDependencyTask, phases ...string) error {
	if task.SourceFileID == 0 {
		return nil
	}
	// Reuse the platform transaction's connection; do not request a second
	// connection while holding file/task locks (a pool may have size one).
	var settings []model.Settings
	if err := tx.Where("key IN ?", []string{"postgresql_host", "postgresql_port", "postgresql_database"}).Find(&settings).Error; err != nil {
		return err
	}
	cfg := PostgreSQLConfig{}
	for _, setting := range settings {
		switch setting.Key {
		case "postgresql_host":
			cfg.Host = setting.Value
		case "postgresql_port":
			cfg.Port = setting.Value
		case "postgresql_database":
			cfg.Database = setting.Value
		}
	}
	target := releaseSQLTarget(cfg)
	if target == "" || target != task.DatabaseTarget {
		return fmt.Errorf("关联任务属于其他数据库，不能在当前连接执行或回写")
	}
	file, err := lockSQLFile(tx, task.SourceFileID)
	if err != nil {
		return err
	}
	if file.DatabaseTarget != task.DatabaseTarget {
		return fmt.Errorf("源文件数据库目标已变化")
	}
	if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
		return fmt.Errorf("源文件正在执行，请等待结束后处理视图任务")
	}
	recovery := len(phases) > 0 && (phases[0] == "RESTORING" || phases[0] == "VERIFYING")
	blocking := []string{"ANALYZING", "PRECHECKING", "EXECUTING", "RESTORING", "VERIFYING"}
	if !recovery {
		blocking = append(blocking, "UNKNOWN")
	}
	var otherActive int64
	if err := tx.Model(&model.SQLViewDependencyTask{}).Where("source_file_id = ? AND id <> ? AND is_deleted = false AND status IN ?", file.ID, task.ID, blocking).Count(&otherActive).Error; err != nil {
		return err
	}
	if otherActive > 0 {
		return fmt.Errorf("同一源文件的其他视图任务正在处理或结果待核实，请先完成该任务")
	}
	var sources []sqlViewSource
	if err = json.Unmarshal([]byte(task.SourceStatementsJSON), &sources); err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("任务缺少源语句快照")
	}
	for _, source := range sources {
		var row model.SQLChangeStatement
		if err = tx.First(&row, source.ID).Error; err != nil {
			return err
		}
		validState := row.ExecuteStatus == "FAILED" && row.ManualFailure
		if task.SourceSyncStatus == "SUCCESS" {
			validState = row.ExecuteStatus == "SUCCESS"
		}
		if row.FileID != file.ID || row.ViewDependencyTaskID != task.ID || row.SQLContent != source.SQL || !validState {
			return fmt.Errorf("源 SQL 状态或内容已变化，不能执行或覆盖原结果")
		}
	}
	if !recovery && (len(phases) == 0 || phases[0] != "CANCELING") {
		return validateSQLViewSourceContext(tx, task, *file, sources)
	}
	return nil
}

// Return a draft or a confirmed rolled-back task to ordinary failure handling.
// Applied/uncertain database changes can never be detached to bypass recovery.
func (s *PostgreSQLService) CancelSQLViewTaskTransfer(id uint, username string) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		var task model.SQLViewDependencyTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, id).Error; err != nil {
			return err
		}
		if task.SourceFileID == 0 || task.SourceSyncStatus != "WAITING" || viewTaskBusy(task.Status) || task.OutcomeUncertain || task.Status == "UNKNOWN" || task.Status == "SUCCESS" {
			return fmt.Errorf("仅未执行或已确认回滚的关联任务可以撤回")
		}
		var applied int64
		if err := tx.Model(&model.SQLViewDependencyStep{}).Where("task_id=? AND step_name NOT IN ? AND status IN ?", id, []string{"BEGIN", "SET_LOCAL", "ROLLBACK"}, []string{"SUCCESS", "UNKNOWN", "RUNNING", "APPLIED"}).Count(&applied).Error; err != nil {
			return err
		}
		if applied > 0 {
			return fmt.Errorf("任务已有生效或待核实步骤，不能撤回，请先处理恢复和校验")
		}
		if err := validateSQLViewTaskSource(tx, task, "CANCELING"); err != nil {
			return err
		}
		var file model.SQLChangeFile
		if err := tx.First(&file, task.SourceFileID).Error; err != nil {
			return err
		}
		var rows []model.SQLChangeStatement
		if err := tx.Where("view_dependency_task_id=?", id).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			message := fmt.Sprintf("已撤回视图依赖任务 %d，保留失败状态，可编辑或单独重试；原记录：%s", id, row.ExecuteMessage)
			if err := appendSQLActionRecord(tx, &file, row, "VIEW_DEPENDENCY_CANCEL", username, "FAILED", message); err != nil {
				return err
			}
			if err := tx.Model(&row).Updates(map[string]any{"view_dependency_task_id": 0, "execute_message": message}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&task).Updates(map[string]any{"status": "CANCELED", "source_sync_status": "CANCELED", "execute_message": "关联已撤回，原 SQL 保留失败状态，未执行新的数据库变更"}).Error
	})
}

// Success is atomic with the task result and latest source rows. Historical
// failed attempts and execution-batch snapshots are never rewritten.
func completeSQLViewTaskSource(tx *gorm.DB, task *model.SQLViewDependencyTask, username string) error {
	if task.SourceFileID == 0 {
		return nil
	}
	if err := validateSQLViewTaskSource(tx, *task); err != nil {
		return err
	}
	var file model.SQLChangeFile
	if err := tx.First(&file, task.SourceFileID).Error; err != nil {
		return err
	}
	var sources []sqlViewSource
	if err := json.Unmarshal([]byte(task.SourceStatementsJSON), &sources); err != nil {
		return err
	}
	now := time.Now()
	cfg := PostgreSQLConfig{}
	if err := json.Unmarshal([]byte(task.DatabaseTarget), &cfg); err != nil {
		return err
	}
	// The target JSON uses Host/Port/Database; no credentials are persisted.
	for _, source := range sources {
		var row model.SQLChangeStatement
		if err := tx.First(&row, source.ID).Error; err != nil {
			return err
		}
		row.TransactionGroupID = source.GroupID
		message := fmt.Sprintf("已通过视图依赖任务 %d 执行，字段变更与视图恢复校验完成", task.ID)
		record := newSQLStatementRecord(&file, row, "VIEW_DEPENDENCY_EXECUTE", username)
		yes := true
		record.Submitted = &yes
		record.Status = "SUCCESS"
		record.Message = message
		record.FinishedAt = &now
		record.DatabaseHost = cfg.Host
		record.DatabasePort = cfg.Port
		record.DatabaseName = cfg.Database
		record.DatabaseUser = task.DatabaseUser
		if source.GroupID > 0 {
			record.TransactionOutcome = "COMMITTED"
		}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		fields := map[string]any{"execute_status": "SUCCESS", "manual_failure": false, "execute_message": message, "sql_state": "", "execute_user": username, "execute_time": now, "database_user": record.DatabaseUser}
		if source.GroupID > 0 {
			fields["transaction_outcome"] = "COMMITTED"
		}
		if err := tx.Model(&row).Updates(fields).Error; err != nil {
			return err
		}
	}
	task.SourceSyncStatus = "SUCCESS"
	if err := tx.Model(task).Update("source_sync_status", "SUCCESS").Error; err != nil {
		return err
	}
	// The source now has a result from this independent task. Keep its old
	// execution-batch snapshot, but do not reopen that old failure as live state.
	if err := tx.Model(&file).Update("execution_batch_id", 0).Error; err != nil {
		return err
	}
	_, err := recomputeSQLFileStatusWithDB(tx, file.ID, username)
	return err
}

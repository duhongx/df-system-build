package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

func sqlContentHash(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}
func sqlSkipKind(stmt model.SQLChangeStatement) string {
	if stmt.SkipKind != "" {
		return stmt.SkipKind
	}
	if stmt.ExecuteStatus != "SKIPPED" {
		return ""
	}
	if strings.HasPrefix(stmt.ExecuteMessage, "手工标记不执行:") {
		return "MANUAL"
	}
	switch stmt.ExecuteMessage {
	case "配置数据已存在", "表已存在", "索引已存在", "字段已存在", "视图不存在":
		return "AUTO"
	}
	return "UNKNOWN"
}
func newSQLStatementRecord(file *model.SQLChangeFile, stmt model.SQLChangeStatement, action, username string) model.SQLStatementRecord {
	return model.SQLStatementRecord{FileID: file.ID, StatementID: stmt.ID, FileName: file.FileName, SchemaName: file.SchemaName, DatabaseUser: file.SchemaName, SQLHash: sqlContentHash(stmt.SQLContent), SQLContent: stmt.SQLContent, LineNumber: stmt.LineNumber, Action: action, Operator: username, StartedAt: time.Now(), BeforeStatus: stmt.ExecuteStatus, BeforeMessage: stmt.ExecuteMessage, BeforeSQLState: stmt.SQLState, BeforeOperator: stmt.ExecuteUser, BeforeTime: stmt.ExecuteTime}
}
func appendSQLActionRecord(tx *gorm.DB, file *model.SQLChangeFile, stmt model.SQLChangeStatement, action, username, status, message string) error {
	record := newSQLStatementRecord(file, stmt, action, username)
	no := false
	now := time.Now()
	record.Submitted = &no
	record.FinishedAt = &now
	record.Status = status
	record.Message = message
	if action == "MANUAL_SKIP" || action == "FILE_SKIP" {
		record.SkipKind = "MANUAL"
	}
	return tx.Create(&record).Error
}

// Preserve the old latest state once, explicitly as a legacy snapshot, not as
// invented past attempts. Deleted/overwritten files remain searchable as well.
func ensureLegacySQLStatementRecord(tx *gorm.DB, file *model.SQLChangeFile, stmt model.SQLChangeStatement) error {
	if stmt.ExecuteStatus == "PENDING" || stmt.ExecuteStatus == "QUEUED" || stmt.ExecuteStatus == "" {
		return nil
	}
	var count int64
	if err := tx.Model(&model.SQLStatementRecord{}).Where("statement_id = ?", stmt.ID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	record := newSQLStatementRecord(file, stmt, "LEGACY_SNAPSHOT", stmt.ExecuteUser)
	record.Status, record.Message, record.SQLState = stmt.ExecuteStatus, stmt.ExecuteMessage, stmt.SQLState
	record.AffectedRows, record.DurationMs, record.SkipKind = stmt.AffectedRows, stmt.DurationMs, sqlSkipKind(stmt)
	record.FinishedAt = stmt.ExecuteTime
	if stmt.ExecuteTime != nil {
		record.StartedAt = *stmt.ExecuteTime
	} else {
		record.StartedAt = stmt.CreatedAt
	}
	return tx.Create(&record).Error
}
func BackfillSQLStatementHistory() error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", int64(681294031)).Error; err != nil {
			return err
		}
		var cursor uint
		for {
			var rows []model.SQLChangeStatement
			if err := tx.Where("id > ? AND execute_status NOT IN ?", cursor, []string{"PENDING", "QUEUED", ""}).Order("id").Limit(500).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, stmt := range rows {
				var file model.SQLChangeFile
				if err := tx.First(&file, stmt.FileID).Error; err != nil {
					return err
				}
				if err := ensureLegacySQLStatementRecord(tx, &file, stmt); err != nil {
					return err
				}
				cursor = stmt.ID
			}
		}
	})
}

type SQLRecordFilter struct {
	StatementID uint   `form:"statementId"`
	FileID      uint   `form:"fileId"`
	BatchID     uint   `form:"batchId"`
	SQLHash     string `form:"sqlHash"`
	Keyword     string `form:"keyword"`
	FileName    string `form:"fileName"`
	SchemaName  string `form:"schemaName"`
	Action      string `form:"action"`
	Status      string `form:"status"`
}

func (s *PostgreSQLService) ListSQLStatementRecords(page, size int, filter SQLRecordFilter) ([]model.SQLStatementRecord, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	q := repository.DB.Model(&model.SQLStatementRecord{})
	if filter.StatementID > 0 {
		// Re-upload preserves successful results by occurrence. Follow their
		// provenance instead of falsely presenting them as fresh executions.
		var lineage []uint
		if err := repository.DB.Raw(`WITH RECURSIVE lineage(id) AS (SELECT ?::bigint UNION SELECT r.source_statement_id FROM sql_statement_records r JOIN lineage l ON r.statement_id=l.id WHERE r.source_statement_id<>0) SELECT id FROM lineage`, filter.StatementID).Scan(&lineage).Error; err != nil {
			return nil, 0, err
		}
		q = q.Where("statement_id IN ?", lineage)
	}
	if filter.FileID > 0 {
		q = q.Where("file_id = ?", filter.FileID)
	}
	if filter.BatchID > 0 {
		q = q.Where("batch_id = ?", filter.BatchID)
	}
	if filter.SQLHash != "" {
		q = q.Where("sql_hash = ?", filter.SQLHash)
	}
	if filter.Keyword != "" {
		q = q.Where("sql_content ILIKE ?", "%"+filter.Keyword+"%")
	}
	if filter.FileName != "" {
		q = q.Where("file_name ILIKE ?", "%"+filter.FileName+"%")
	}
	if filter.SchemaName != "" {
		q = q.Where("schema_name = ?", filter.SchemaName)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	switch filter.Action {
	case "MANUAL_SKIP":
		q = q.Where("(action IN ? OR (action = ? AND skip_kind = ?))", []string{"MANUAL_SKIP", "FILE_SKIP"}, "LEGACY_SNAPSHOT", "MANUAL")
	case "AUTO_SKIP":
		q = q.Where("(action = ? OR (action = ? AND skip_kind = ?))", "AUTO_SKIP", "LEGACY_SNAPSHOT", "AUTO")
	default:
		if filter.Action != "" {
			q = q.Where("action = ?", filter.Action)
		}
	}
	var total int64
	var rows []model.SQLStatementRecord
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}
func createSQLExecutionSnapshot(tx *gorm.DB, batchID uint, file model.SQLChangeFile, rows []model.SQLChangeStatement) error {
	file.FileContent = ""
	base := append([]model.SQLChangeStatement(nil), rows...)
	for i := range base {
		base[i].ResultOrigin = "PREVIOUS"
		if base[i].ExecuteStatus != "SUCCESS" && base[i].ExecuteStatus != "SKIPPED" {
			base[i].ResultOrigin = "NOT_EXECUTED"
			base[i].ExecuteStatus = "PENDING"
			base[i].ExecuteMessage = ""
			base[i].ExecuteTime = nil
			base[i].ExecuteUser = ""
			base[i].SQLState = ""
			base[i].AffectedRows = 0
			base[i].DurationMs = 0
		}
	}
	rawFile, err := json.Marshal(file)
	if err != nil {
		return err
	}
	rawRows, err := json.Marshal(base)
	if err != nil {
		return err
	}
	return tx.Create(&model.SQLExecutionFileSnapshot{BatchID: batchID, FileID: file.ID, FileJSON: string(rawFile), StatementsJSON: string(rawRows)}).Error
}
func updateSQLExecutionSnapshot(tx *gorm.DB, file *model.SQLChangeFile) error {
	if file.ExecutionBatchID == 0 {
		return nil
	}
	copy := *file
	copy.FileContent = ""
	raw, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	return tx.Model(&model.SQLExecutionFileSnapshot{}).Where("batch_id = ? AND file_id = ?", file.ExecutionBatchID, file.ID).Update("file_json", string(raw)).Error
}
func (s *PostgreSQLService) GetSQLBatchFile(batchID, fileID uint) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	var snapshot model.SQLExecutionFileSnapshot
	if err := repository.DB.Where("batch_id = ? AND file_id = ?", batchID, fileID).First(&snapshot).Error; err != nil {
		return nil, nil, fmt.Errorf("此批次没有该文件的历史快照，旧版记录无法完整还原")
	}
	var file model.SQLChangeFile
	var rows []model.SQLChangeStatement
	if err := json.Unmarshal([]byte(snapshot.FileJSON), &file); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal([]byte(snapshot.StatementsJSON), &rows); err != nil {
		return nil, nil, err
	}
	var attempts []model.SQLStatementRecord
	if err := repository.DB.Where("batch_id = ? AND file_id = ? AND action IN ?", batchID, fileID, []string{"EXECUTE", "AUTO_SKIP"}).Order("id ASC").Find(&attempts).Error; err != nil {
		return nil, nil, err
	}
	byID := map[uint]model.SQLStatementRecord{}
	for _, attempt := range attempts {
		byID[attempt.StatementID] = attempt
	}
	for i := range rows {
		if r, ok := byID[rows[i].ID]; ok {
			rows[i].ResultOrigin = "CURRENT"
			rows[i].ExecuteStatus = r.Status
			rows[i].ExecuteMessage = r.Message
			rows[i].ExecuteUser = r.Operator
			rows[i].ExecuteTime = r.FinishedAt
			rows[i].SQLState = r.SQLState
			rows[i].SkipKind = r.SkipKind
			rows[i].AffectedRows = r.AffectedRows
			rows[i].DurationMs = r.DurationMs
		}
	}
	return &file, rows, nil
}

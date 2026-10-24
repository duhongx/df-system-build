package service

import (
	"fmt"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

// Summarize inherited statement results without recording the upload as a new
// execution. A partly inherited file still has pending SQL to execute.
func saveImportedSQLFileSummary(db *gorm.DB, file *model.SQLChangeFile, rows []model.SQLChangeStatement) error {
	success, skipped := 0, 0
	var latest *model.SQLChangeStatement
	for i := range rows {
		row := &rows[i]
		switch row.ExecuteStatus {
		case "SUCCESS":
			success++
		case "SKIPPED":
			skipped++
		default:
			continue
		}
		if row.ExecuteTime != nil && (latest == nil || row.ExecuteTime.After(*latest.ExecuteTime)) {
			latest = row
		}
	}
	if success+skipped == 0 {
		return nil
	}
	file.ExecuteStatus = "PENDING"
	file.ExecuteUser, file.ExecuteTime = "", nil
	file.ExecuteMessage = fmt.Sprintf("沿用历史结果：成功 %d，跳过 %d，待执行 %d", success, skipped, len(rows)-success-skipped)
	if success+skipped == len(rows) {
		file.ExecuteStatus = "SUCCESS"
		if skipped == len(rows) {
			file.ExecuteStatus = "SKIPPED"
		}
		if latest != nil {
			file.ExecuteUser, file.ExecuteTime = latest.ExecuteUser, latest.ExecuteTime
		}
	}
	return db.Model(file).Updates(map[string]any{
		"execute_status": file.ExecuteStatus, "execute_message": file.ExecuteMessage,
		"execute_user": file.ExecuteUser, "execute_time": file.ExecuteTime,
	}).Error
}

// Startup repair for previously imported revisions whose statements were all
// inherited, but whose file status was left pending. Historical attempts and
// execution-batch snapshots remain immutable.
func BackfillImportedSQLFileStatuses() error {
	var cursor uint
	for {
		var ids []uint
		if err := repository.DB.Model(&model.SQLChangeFile{}).
			Where("id > ? AND is_deleted = false AND execute_status = ? AND execution_batch_id = 0", cursor, "PENDING").
			Where("EXISTS (SELECT 1 FROM sql_statement_records r WHERE r.file_id = sql_change_files.id AND r.action = ?)", "INHERIT").
			Where("NOT EXISTS (SELECT 1 FROM sql_change_statements s WHERE s.file_id = sql_change_files.id AND s.execute_status NOT IN ?)", []string{"SUCCESS", "SKIPPED"}).
			Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := repository.DB.Transaction(func(tx *gorm.DB) error {
				file, err := lockSQLFile(tx, id)
				if err != nil {
					return err
				}
				if file.ExecuteStatus != "PENDING" || file.ExecutionBatchID != 0 {
					return nil
				}
				var rows []model.SQLChangeStatement
				if err := tx.Where("file_id = ?", id).Find(&rows).Error; err != nil {
					return err
				}
				for _, row := range rows {
					if row.ExecuteStatus != "SUCCESS" && row.ExecuteStatus != "SKIPPED" {
						return nil
					}
				}
				return saveImportedSQLFileSummary(tx, file, rows)
			}); err != nil {
				return err
			}
			cursor = id
		}
	}
}

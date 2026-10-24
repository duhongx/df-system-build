package service

import (
	"df-build-server/internal/model"
	"gorm.io/gorm"
)

// Editing may put a previously executed file back into PENDING. Classify its
// execution history separately from the state of its remaining work.
const sqlFileHasAttempt = `(execution_started_at IS NOT NULL OR execution_batch_id > 0
 OR execute_status NOT IN ('PENDING', 'NOT_EXECUTABLE', '')
 OR EXISTS (SELECT 1 FROM sql_change_statements s WHERE s.file_id=sql_change_files.id AND s.execute_status NOT IN ('PENDING','NOT_EXECUTABLE',''))
 OR EXISTS (SELECT 1 FROM sql_statement_records r WHERE r.file_id=sql_change_files.id AND r.action IN ('EXECUTE','INHERIT','VIEW_DEPENDENCY_EXECUTE')))`

func populateSQLFileCounts(db *gorm.DB, files []model.SQLChangeFile) error {
	if len(files) == 0 {
		return nil
	}
	ids := make([]uint, len(files))
	for i := range files {
		ids[i] = files[i].ID
	}
	var counts []struct {
		FileID                                            uint
		Total, Success, Failed, Pending, Skipped, Unknown int
	}
	if err := db.Model(&model.SQLChangeStatement{}).Select(`file_id, count(*) AS total,
 SUM(CASE WHEN execute_status='SUCCESS' THEN 1 ELSE 0 END) AS success,
 SUM(CASE WHEN execute_status IN ('FAILED','PARTIAL_FAILED','TIMEOUT') THEN 1 ELSE 0 END) AS failed,
 SUM(CASE WHEN execute_status NOT IN ('SUCCESS','SKIPPED','FAILED','PARTIAL_FAILED','TIMEOUT','UNKNOWN') THEN 1 ELSE 0 END) AS pending,
 SUM(CASE WHEN execute_status='SKIPPED' THEN 1 ELSE 0 END) AS skipped,
 SUM(CASE WHEN execute_status='UNKNOWN' THEN 1 ELSE 0 END) AS unknown`).Where("file_id IN ?", ids).Group("file_id").Scan(&counts).Error; err != nil {
		return err
	}
	byID := map[uint]int{}
	for i := range files {
		byID[files[i].ID] = i
		files[i].ResultStatus = files[i].ExecuteStatus
	}
	for _, c := range counts {
		f := &files[byID[c.FileID]]
		f.TotalStatements = c.Total
		f.SuccessStatements = c.Success
		f.FailedStatements = c.Failed
		f.PendingStatements = c.Pending
		f.SkippedStatements = c.Skipped
		f.UnknownStatements = c.Unknown
		switch {
		case f.ExecuteStatus == "RUNNING" || f.ExecuteStatus == "QUEUED":
		case c.Unknown > 0:
			f.ResultStatus = "UNKNOWN"
		case c.Skipped == c.Total:
			f.ResultStatus = "SKIPPED"
		case c.Success+c.Skipped == c.Total:
			f.ResultStatus = "SUCCESS"
		case c.Success > 0:
			f.ResultStatus = "PARTIAL_SUCCESS"
		case c.Failed > 0:
			f.ResultStatus = "FAILED"
		case f.ExecutionStartedAt != nil || f.ExecutionBatchID > 0:
			f.ResultStatus = "INCOMPLETE"
		}
	}
	return nil
}

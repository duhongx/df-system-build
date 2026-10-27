package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

// Claim every selected file in one transaction. Existing import batches are
// untouched; this batch records only the ordered execution selection.
func (s *PostgreSQLService) prepareSQLFileSequence(ids []uint, username string) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, nil, err
	}
	if len(ids) == 0 || len(ids) > 500 {
		return nil, nil, fmt.Errorf("请选择 1 至 500 个 SQL 文件")
	}
	unique := map[uint]bool{}
	for _, id := range ids {
		if id == 0 || unique[id] {
			return nil, nil, fmt.Errorf("文件 ID 无效或重复")
		}
		unique[id] = true
	}
	lockIDs := append([]uint(nil), ids...)
	sort.Slice(lockIDs, func(i, j int) bool { return lockIDs[i] < lockIDs[j] })
	batch := &model.SQLChangeBatch{BatchName: "SQL 文件执行", ExecuteStatus: "RUNNING", ExecuteUser: username, TotalFiles: len(ids)}
	var files []model.SQLChangeFile
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		for _, id := range lockIDs {
			file, err := lockSQLFile(tx, id)
			if err != nil {
				return err
			}
			if err := guardSQLFileViewTask(tx, file.ID, false); err != nil {
				return err
			}
			if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" || file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" {
				return fmt.Errorf("文件 %s 当前状态不允许执行", file.FileName)
			}
			var statements []model.SQLChangeStatement
			if err := tx.Where("file_id = ?", id).Order("line_number ASC, id ASC").Find(&statements).Error; err != nil {
				return err
			}
			for _, stmt := range statements {
				if stmt.ExecuteStatus == "RUNNING" || stmt.ExecuteStatus == "UNKNOWN" {
					return fmt.Errorf("文件 %s 第 %d 行执行结果待核实", file.FileName, stmt.LineNumber)
				}
			}
			if err := requireReleaseSQLTarget(file, target); err != nil {
				return err
			}
			if file.DatabaseTarget == "" {
				for _, stmt := range statements {
					if stmt.ExecuteStatus != "PENDING" {
						return fmt.Errorf("文件 %s 的数据库目标未确认，不能沿用旧进度", file.FileName)
					}
				}
				file.DatabaseTarget = target
			}
			statements, _, err = normalizeReleaseSQLAddColumns(tx, file, statements, username)
			if err != nil {
				return err
			}
			if issue := releaseSQLExecutionIssue(file, statements); issue != nil {
				code := "INVALID_SQL"
				if file.SchemaName == "" || validateSchemaName(file.SchemaName) != nil {
					code = "MISSING_SCHEMA"
				}
				batch.FilteredFiles = append(batch.FilteredFiles, model.SQLExecutionFileFilter{ID: file.ID, FileName: file.FileName, Code: code, Reason: issue.Error()})
				continue
			}
			if !hasSQLContinuationWork(statements) {
				batch.FilteredFiles = append(batch.FilteredFiles, model.SQLExecutionFileFilter{
					ID: file.ID, FileName: file.FileName,
					Code:   "NO_PENDING_SQL",
					Reason: "没有未执行语句；失败 SQL 请从失败清单单独重试",
				})
				continue
			}
			// Continuing a selection advances pending SQL. Previous failures
			// stay failed for explicit retry, with an audit of the operator's
			// choice to proceed past them. Newly encountered failures still stop.
			if err := deferFailedSQLForContinuation(tx, file, statements, username); err != nil {
				return err
			}
			files = append(files, *file)
		}
		if len(files) == 0 {
			if len(batch.FilteredFiles) > 0 {
				return fmt.Errorf("没有可继续执行的 SQL 文件。%s：%s", batch.FilteredFiles[0].FileName, batch.FilteredFiles[0].Reason)
			}
			return fmt.Errorf("没有可继续执行的 SQL 文件")
		}
		batch.TotalFiles = len(files)
		sort.SliceStable(files, func(i, j int) bool { return lessSQLFiles(files[i], files[j]) })
		ordered := make([]uint, len(files))
		for i := range files {
			ordered[i] = files[i].ID
		}
		raw, _ := json.Marshal(ordered)
		batch.FileIDsJSON = string(raw)
		filtered, err := json.Marshal(batch.FilteredFiles)
		if err != nil {
			return err
		}
		batch.FilteredFilesJSON = string(filtered)
		batch.ExecuteMessage = fmt.Sprintf("已加入 %d 个文件，按顺序执行；另有 %d 个文件未入队，原因见清单", len(files), len(batch.FilteredFiles))
		if err := tx.Create(batch).Error; err != nil {
			return err
		}
		for i := range files {
			files[i].ExecuteStatus = "QUEUED"
			files[i].ExecutionBatchID = batch.ID
			files[i].ExecuteMessage = "等待按顺序执行"
			files[i].ExecuteUser = username
			files[i].ImportMessage = ""
			if err := tx.Save(&files[i]).Error; err != nil {
				return err
			}
			var rows []model.SQLChangeStatement
			if err := tx.Where("file_id = ?", files[i].ID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
				return err
			}
			if err := createSQLExecutionSnapshot(tx, batch.ID, files[i], rows); err != nil {
				return err
			}
		}
		return nil
	})
	return batch, files, err
}

func deferFailedSQLForContinuation(tx *gorm.DB, file *model.SQLChangeFile, statements []model.SQLChangeStatement, username string) error {
	annotateReleaseSQLTransactionGroups(statements)
	for i := range statements {
		stmt := &statements[i]
		if stmt.ExecuteStatus != "FAILED" || stmt.ManualFailure {
			continue
		}
		if err := ensureLegacySQLStatementRecord(tx, file, *stmt); err != nil {
			return err
		}
		message := "继续执行：此句保留失败状态，暂缓重试；后续未执行语句继续，可在失败清单中单独重试"
		if stmt.ExecuteMessage != "" {
			message += "；最近一次错误：" + stmt.ExecuteMessage
		}
		if err := appendSQLActionRecord(tx, file, *stmt, "MANUAL_FAIL", username, "FAILED", message); err != nil {
			return err
		}
		stmt.ManualFailure = true
		stmt.ExecuteMessage = message
		if err := tx.Save(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

func hasSQLContinuationWork(statements []model.SQLChangeStatement) bool {
	for _, stmt := range statements {
		switch stmt.ExecuteStatus {
		case "PENDING", "CANCELED", "NOT_EXECUTABLE", "BLOCKED", "PARTIAL_FAILED":
			return true
		}
	}
	return false
}

func (s *PostgreSQLService) StartSQLFileSequence(ids []uint, username string, options SQLExecuteOptions) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	batch, files, err := s.prepareSQLFileSequence(ids, username)
	if err != nil {
		return nil, nil, err
	}
	responseBatch := *batch
	responseFiles := append([]model.SQLChangeFile(nil), files...)
	for i := range responseFiles {
		responseFiles[i].FileContent = ""
	}
	go s.runSQLFileSequence(batch, files, username, options)
	return &responseBatch, responseFiles, nil
}

func (s *PostgreSQLService) runSQLFileSequence(batch *model.SQLChangeBatch, files []model.SQLChangeFile, username string, options SQLExecuteOptions) {
	ctx, cancelBatch := context.WithCancel(context.Background())
	defer cancelBatch()
	if s.batchCancelReg != nil {
		if !s.batchCancelReg.register(batch.ID, cancelBatch) {
			return
		}
		defer s.batchCancelReg.unregister(batch.ID)
	}
	success, failed, skipped, deferred := 0, 0, 0, 0
	message := "执行完成"
	canceled := false
	completed, interrupted := false, false
	activeID := uint(0)
	// Also release files if a worker cannot persist an intermediate result.
	defer func() {
		if recovered := recover(); recovered != nil {
			interrupted = true
			failed++
			message = fmt.Sprintf("执行中断：%v", recovered)
			if activeID != 0 {
				_ = repository.DB.Model(&model.SQLChangeStatement{}).Where("file_id = ? AND execute_status = ?", activeID, "RUNNING").Updates(map[string]any{"execute_status": "UNKNOWN", "execute_message": "执行中断，结果待核实"}).Error
				s.markSQLFileExecutionFailed(activeID, username, fmt.Errorf("%s", message))
			}
		}
		if ctx.Err() != nil && !completed && !interrupted {
			canceled = true
			message = "执行已停止，后续文件未执行"
		}
		now := time.Now()
		stoppedFileName := ""
		if !completed {
			for _, file := range files {
				if file.ID == activeID {
					stoppedFileName = file.FileName
					break
				}
			}
		}
		waitingMessage := "前序执行未完成，本文件未执行"
		if canceled {
			waitingMessage = "已请求停止执行，本文件尚未执行"
		} else if stoppedFileName != "" {
			waitingMessage = "文件 " + stoppedFileName + " 执行失败，后续文件未执行"
		}
		releaseErr := repository.DB.Transaction(func(tx *gorm.DB) error {
			var waiting []model.SQLChangeFile
			if err := tx.Where("execution_batch_id = ? AND execute_status = ?", batch.ID, "QUEUED").Find(&waiting).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.SQLChangeFile{}).Where("execution_batch_id = ? AND execute_status = ?", batch.ID, "QUEUED").Updates(map[string]any{"execute_status": "CANCELED", "execute_message": waitingMessage, "execute_user": username, "execute_time": now}).Error; err != nil {
				return err
			}
			for _, file := range waiting {
				file.ExecuteStatus = "CANCELED"
				file.ExecuteMessage = waitingMessage
				file.ExecuteUser = username
				file.ExecuteTime = &now
				if err := updateSQLExecutionSnapshot(tx, &file); err != nil {
					return err
				}
			}
			return nil
		})
		if releaseErr != nil {
			completed = false
			failed++
			message += "；释放等待文件失败：" + releaseErr.Error()
		}
		status := "SUCCESS"
		if failed > 0 {
			status = "FAILED"
			if success > 0 {
				status = "PARTIAL_FAILED"
			}
		}
		if canceled {
			status = "CANCELED"
		}
		outcome := "ERROR"
		switch {
		case interrupted:
			outcome = "INTERRUPTED"
		case canceled:
			outcome = "CANCELED"
		case completed:
			outcome = "COMPLETED"
			message = fmt.Sprintf("本轮可执行 SQL 已全部处理：成功文件 %d，保留失败待重试文件 %d，跳过文件 %d。另有 %d 个文件未入队，原因见清单。", success, deferred, skipped, len(batch.FilteredFiles))
		}
		if err := repository.DB.Model(batch).Updates(map[string]any{
			"execute_status": status, "success_files": success, "failed_files": failed, "skipped_files": skipped,
			"deferred_files": deferred, "execute_message": message, "execute_time": now,
			"outcome": outcome, "finished_at": now, "stopped_file_id": activeID, "stopped_file_name": stoppedFileName,
		}).Error; err != nil {
			log.Printf("保存 SQL 队列 %d 结果失败: %v", batch.ID, err)
		}
	}()
	for _, item := range files {
		stop, err := releaseSQLBatchStopped(ctx, batch.ID)
		if err != nil {
			failed++
			message = "文件 " + item.FileName + " 执行失败：" + SQLExecutionErrorMessage(err)
			return
		}
		if stop {
			canceled = true
			message = "执行已停止，后续文件未执行"
			return
		}
		activeID = item.ID
		file, stopped, err := s.executeQueuedSQLFileWithContext(ctx, item.ID, batch.ID, username, options)
		if err != nil {
			s.markSQLFileExecutionFailed(item.ID, username, err)
			failed++
			message = SQLExecutionErrorMessage(err)
			return
		}
		if file.ExecuteStatus == "SUCCESS" {
			success++
		} else if file.ExecuteStatus == "SKIPPED" {
			skipped++
		} else if file.ExecuteStatus != "CANCELED" {
			failed++
			message = file.ExecuteMessage
			if file.ExecuteStatus == "FAILED" {
				isDeferred, checkErr := onlyManualDeferredSQLFailures(item.ID)
				if checkErr != nil {
					message += "；检查手动暂缓语句失败：" + checkErr.Error()
					return
				}
				if isDeferred {
					deferred++
					activeID = 0
					continue
				}
			}
			return
		}
		if stopped || file.ExecuteStatus == "CANCELED" {
			canceled = true
			message = "执行已停止，后续文件未执行"
			return
		}
		activeID = 0
	}
	completed = true
}

func onlyManualDeferredSQLFailures(fileID uint) (bool, error) {
	var statements []model.SQLChangeStatement
	if err := repository.DB.Select("execute_status", "manual_failure").Where("file_id = ?", fileID).Find(&statements).Error; err != nil {
		return false, err
	}
	deferred := 0
	for _, stmt := range statements {
		switch stmt.ExecuteStatus {
		case "FAILED":
			if !stmt.ManualFailure {
				return false, nil
			}
			deferred++
		case "SUCCESS", "SKIPPED":
		default:
			return false, nil
		}
	}
	return deferred > 0, nil
}

func (s *PostgreSQLService) executeQueuedSQLFile(id, batchID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, bool, error) {
	return s.executeQueuedSQLFileWithContext(context.Background(), id, batchID, username, options)
}
func (s *PostgreSQLService) executeQueuedSQLFileWithContext(parent context.Context, id, batchID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, bool, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if !s.cancelReg.register(id, cancel) {
		return nil, false, fmt.Errorf("SQL 文件正在执行中")
	}
	defer s.cancelReg.unregister(id)
	file, statements, opts, err := s.prepareQueuedSQLFileExecution(id, batchID, username, options)
	if err == nil {
		file, _, err = s.runReleaseSQLFile(ctx, file, statements, username, opts)
	}
	return file, ctx.Err() != nil, err
}

func (s *PostgreSQLService) getSelectedSQLBatchFiles(batch *model.SQLChangeBatch) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	if batch.FilteredFilesJSON != "" {
		if err := json.Unmarshal([]byte(batch.FilteredFilesJSON), &batch.FilteredFiles); err != nil {
			return nil, nil, fmt.Errorf("读取批次未入队文件清单失败: %w", err)
		}
	}
	var ids []uint
	if err := json.Unmarshal([]byte(batch.FileIDsJSON), &ids); err != nil {
		return nil, nil, err
	}
	var snapshots []model.SQLExecutionFileSnapshot
	if err := repository.DB.Select("batch_id", "file_id", "file_json").Where("batch_id = ?", batch.ID).Find(&snapshots).Error; err != nil {
		return nil, nil, err
	}
	byID := map[uint]model.SQLChangeFile{}
	for _, snapshot := range snapshots {
		var file model.SQLChangeFile
		if err := json.Unmarshal([]byte(snapshot.FileJSON), &file); err != nil {
			return nil, nil, err
		}
		byID[snapshot.FileID] = file
	}
	if len(byID) != len(ids) {
		batch.HistoryIncomplete = true
		var old []model.SQLChangeFile
		if err := repository.DB.Omit("file_content").Where("id IN ?", ids).Find(&old).Error; err != nil {
			return nil, nil, err
		}
		for _, file := range old {
			if _, ok := byID[file.ID]; !ok {
				file.ExecuteStatus = "UNKNOWN"
				file.ExecuteMessage = "旧版批次未保存当次文件快照，不能还原历史结果"
				file.ExecuteTime = nil
				file.ExecuteUser = ""
				byID[file.ID] = file
			}
		}
	}
	files := make([]model.SQLChangeFile, 0, len(ids))
	for _, id := range ids {
		if file, ok := byID[id]; ok {
			files = append(files, file)
		}
	}
	return batch, files, nil
}

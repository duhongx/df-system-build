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
			if issue := releaseSQLExecutionIssue(file, statements); issue != nil {
				batch.FilteredFiles = append(batch.FilteredFiles, model.SQLExecutionFileFilter{ID: file.ID, FileName: file.FileName, Reason: issue.Error()})
				continue
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
			files = append(files, *file)
		}
		if len(files) == 0 {
			return fmt.Errorf("所选文件均无法执行，可标记不执行或删除。%s：%s", batch.FilteredFiles[0].FileName, batch.FilteredFiles[0].Reason)
		}
		batch.TotalFiles = len(files)
		sort.SliceStable(files, func(i, j int) bool { return lessSQLFiles(files[i], files[j]) })
		ordered := make([]uint, len(files))
		for i := range files {
			ordered[i] = files[i].ID
		}
		raw, _ := json.Marshal(ordered)
		batch.FileIDsJSON = string(raw)
		if err := tx.Create(batch).Error; err != nil {
			return err
		}
		for i := range files {
			files[i].ExecuteStatus = "QUEUED"
			files[i].ExecutionBatchID = batch.ID
			files[i].ExecuteMessage = "等待按顺序执行"
			files[i].ExecuteUser = username
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
	success, failed, skipped := 0, 0, 0
	message := "执行完成"
	canceled := false
	activeID := uint(0)
	// Also release files if a worker cannot persist an intermediate result.
	defer func() {
		if recovered := recover(); recovered != nil {
			failed++
			message = fmt.Sprintf("执行中断：%v", recovered)
			if activeID != 0 {
				_ = repository.DB.Model(&model.SQLChangeStatement{}).Where("file_id = ? AND execute_status = ?", activeID, "RUNNING").Updates(map[string]any{"execute_status": "UNKNOWN", "execute_message": "执行中断，结果待核实"}).Error
				s.markSQLFileExecutionFailed(activeID, username, fmt.Errorf("%s", message))
			}
		}
		if ctx.Err() != nil && failed == 0 {
			canceled = true
			message = "执行已停止，后续文件未执行"
		}
		now := time.Now()
		releaseErr := repository.DB.Transaction(func(tx *gorm.DB) error {
			var waiting []model.SQLChangeFile
			if err := tx.Where("execution_batch_id = ? AND execute_status = ?", batch.ID, "QUEUED").Find(&waiting).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.SQLChangeFile{}).Where("execution_batch_id = ? AND execute_status = ?", batch.ID, "QUEUED").Updates(map[string]any{"execute_status": "CANCELED", "execute_message": "前序文件未完成，未执行本文件", "execute_user": username, "execute_time": now}).Error; err != nil {
				return err
			}
			for _, file := range waiting {
				file.ExecuteStatus = "CANCELED"
				file.ExecuteMessage = "前序文件未完成，未执行本文件"
				file.ExecuteUser = username
				file.ExecuteTime = &now
				if err := updateSQLExecutionSnapshot(tx, &file); err != nil {
					return err
				}
			}
			return nil
		})
		if releaseErr != nil {
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
		if err := repository.DB.Model(batch).Updates(map[string]any{"execute_status": status, "success_files": success, "failed_files": failed, "skipped_files": skipped, "execute_message": message, "execute_time": now}).Error; err != nil {
			log.Printf("保存 SQL 队列 %d 结果失败: %v", batch.ID, err)
		}
	}()
	for _, item := range files {
		stop, err := releaseSQLBatchStopped(ctx, batch.ID)
		if err != nil {
			failed++
			message = err.Error()
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
			message = err.Error()
			return
		}
		if file.ExecuteStatus == "SUCCESS" {
			success++
		} else if file.ExecuteStatus == "SKIPPED" {
			skipped++
		} else if file.ExecuteStatus != "CANCELED" {
			failed++
			message = file.ExecuteMessage
			return
		}
		if stopped || file.ExecuteStatus == "CANCELED" {
			canceled = true
			message = "执行已停止，后续文件未执行"
			return
		}
		activeID = 0
	}
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

package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
	"unicode"
)

// Numeric segments compare by value without integer overflow (V2 before V10).
func compareSQLVersions(a, b string) int {
	for len(a) > 0 && len(b) > 0 {
		da, db := a[0] >= '0' && a[0] <= '9', b[0] >= '0' && b[0] <= '9'
		if da && db {
			ia, ib := 0, 0
			for ia < len(a) && a[ia] >= '0' && a[ia] <= '9' {
				ia++
			}
			for ib < len(b) && b[ib] >= '0' && b[ib] <= '9' {
				ib++
			}
			na, nb := strings.TrimLeft(a[:ia], "0"), strings.TrimLeft(b[:ib], "0")
			if len(na) < len(nb) {
				return -1
			}
			if len(na) > len(nb) {
				return 1
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[ia:], b[ib:]
			continue
		}
		if a[0] < b[0] {
			return -1
		}
		if a[0] > b[0] {
			return 1
		}
		a, b = a[1:], b[1:]
	}
	return strings.Compare(a, b)
}
func lessSQLFiles(a, b model.SQLChangeFile) bool {
	if c := compareSQLVersions(a.Version, b.Version); c != 0 {
		return c < 0
	}
	if a.GroupSortNo != b.GroupSortNo {
		return a.GroupSortNo < b.GroupSortNo
	}
	if a.FileName != b.FileName {
		return a.FileName < b.FileName
	}
	return a.ID < b.ID
}
func releaseSQLBatchStopped(ctx context.Context, batchID uint) (bool, error) {
	if ctx.Err() != nil {
		return true, nil
	}
	if batchID == 0 {
		return false, nil
	}
	var row struct{ StopRequested bool }
	if err := repository.DB.Model(&model.SQLChangeBatch{}).Select("stop_requested").Where("id = ?", batchID).Take(&row).Error; err != nil {
		return false, fmt.Errorf("读取 SQL 队列停止状态失败: %w", err)
	}
	return row.StopRequested, nil
}
func (s *PostgreSQLService) CancelSQLFileSequence(id uint, username string) (*model.SQLChangeBatch, error) {
	var batch model.SQLChangeBatch
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("is_deleted = false AND file_ids_json <> ''").First(&batch, id).Error; err != nil {
			return err
		}
		if batch.ExecuteStatus != "RUNNING" {
			return fmt.Errorf("SQL 执行批次已结束")
		}
		if batch.StopRequested {
			return nil
		}
		now := time.Now()
		batch.StopRequested = true
		batch.StopUser = username
		batch.StopTime = &now
		return tx.Model(&batch).Updates(map[string]any{"stop_requested": true, "stop_user": username, "stop_time": now}).Error
	})
	if err != nil {
		return nil, err
	}
	// The database flag also covers startup and gaps between files; cancellation
	// accelerates the check, while current SQL still runs to its boundary.
	if s.batchCancelReg != nil {
		s.batchCancelReg.cancel(id)
	}
	return &batch, nil
}

type SQLFileExecutionConfigRequest struct {
	SchemaName         string  `json:"schemaName" binding:"required"`
	OriginalSchemaName *string `json:"originalSchemaName" binding:"required"`
}

func (s *PostgreSQLService) SetSQLFileExecutionConfig(ctx context.Context, id uint, req SQLFileExecutionConfigRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	schema := strings.TrimSpace(req.SchemaName)
	if schema == "" || len(schema) > 63 || strings.IndexFunc(schema, unicode.IsSpace) >= 0 || validateSchemaName(schema) != nil {
		return nil, nil, fmt.Errorf("请输入有效的执行账号/schema")
	}
	if req.OriginalSchemaName == nil {
		return nil, nil, fmt.Errorf("缺少原执行配置，请重新加载")
	}
	// Verify credentials, schema and USAGE without running any uploaded SQL.
	cfg, err := s.loadReleaseSQLConfig(schema)
	if err != nil {
		return nil, nil, err
	}
	db, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var exists bool
	if err := db.QueryRowContext(probe, "SELECT EXISTS (SELECT FROM pg_namespace WHERE nspname=$1)", schema).Scan(&exists); err != nil {
		return nil, nil, fmt.Errorf("账号连接校验失败: %w", err)
	}
	if !exists {
		return nil, nil, fmt.Errorf("schema %s 不存在", schema)
	}
	var allowed bool
	if err := db.QueryRowContext(probe, "SELECT has_schema_privilege(current_user,$1,'USAGE')", schema).Scan(&allowed); err != nil {
		return nil, nil, err
	}
	if !allowed {
		return nil, nil, fmt.Errorf("账号无权使用 schema %s", schema)
	}
	var file *model.SQLChangeFile
	var rows []model.SQLChangeStatement
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		file, err = lockSQLFile(tx, id)
		if err != nil {
			return err
		}
		if file.SchemaName != *req.OriginalSchemaName {
			return fmt.Errorf("执行配置已被修改，请重新加载")
		}
		if err := requireReleaseSQLTarget(file, releaseSQLTarget(cfg)); err != nil {
			return err
		}
		if file.ExecuteStatus != "PENDING" && file.ExecuteStatus != "FAILED" && file.ExecuteStatus != "CANCELED" {
			return fmt.Errorf("当前状态不允许修改执行账号/schema")
		}
		if err := tx.Where("file_id = ?", id).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.ExecuteStatus != "PENDING" {
				return fmt.Errorf("文件已有语句结果，不能切换执行账号/schema")
			}
		}
		var submitted int64
		if err := tx.Model(&model.SQLStatementRecord{}).Where("file_id = ? AND (submitted = true OR action IN ?)", id, []string{"EXECUTE", "AUTO_SKIP"}).Count(&submitted).Error; err != nil {
			return err
		}
		if submitted > 0 {
			return fmt.Errorf("文件已提交过数据库执行，不能切换执行账号/schema")
		}
		var duplicate int64
		if err := tx.Model(&model.SQLChangeFile{}).Where("id <> ? AND version = ? AND file_name = ? AND schema_name = ? AND database_target = ? AND is_deleted = false", id, file.Version, file.FileName, schema, releaseSQLTarget(cfg)).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return fmt.Errorf("目标账号/schema 下已有同名文件")
		}
		old := file.SchemaName
		file.SchemaName = schema
		file.DatabaseTarget = releaseSQLTarget(cfg)
		file.SchemaConfigured = true
		file.ExecuteStatus = "PENDING"
		file.ExecuteMessage = ""
		file.ExecuteTime = nil
		file.ExecuteUser = ""
		file.ImportMessage = ""
		if issue := releaseSQLExecutionIssue(file, rows); issue != nil {
			file.ImportMessage = issue.Error()
		}
		if err := tx.Save(file).Error; err != nil {
			return err
		}
		return appendSQLActionRecord(tx, file, model.SQLChangeStatement{SQLContent: "", LineNumber: 0}, "CONFIGURE", username, "PENDING", fmt.Sprintf("执行账号/schema：%s → %s（连接与 schema 权限已校验）", old, schema))
	})
	return file, rows, err
}

// Live workbench uses current states for retry decisions. History uses the
// default immutable snapshots, so manual skipping never rewrites old results.
func (s *PostgreSQLService) GetCurrentSQLBatchFiles(id uint) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	var batch model.SQLChangeBatch
	if err := repository.DB.Where("is_deleted = false").First(&batch, id).Error; err != nil {
		return nil, nil, err
	}
	if batch.FileIDsJSON == "" {
		return s.GetSQLBatch(id)
	}
	var ids []uint
	if err := json.Unmarshal([]byte(batch.FileIDsJSON), &ids); err != nil {
		return nil, nil, err
	}
	var rows []model.SQLChangeFile
	if err := repository.DB.Omit("file_content").Where("id IN ? AND is_deleted = false", ids).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	byID := map[uint]model.SQLChangeFile{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	files := make([]model.SQLChangeFile, 0, len(ids))
	for _, fileID := range ids {
		if row, ok := byID[fileID]; ok {
			files = append(files, row)
		}
	}
	return &batch, files, nil
}

package service

import (
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsremote"
	"df-build-server/internal/pglog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *OperationsService) scheduleLogSync(ctx context.Context) {
	var rows []model.OperationsDatabase
	if err := s.db.WithContext(ctx).Where("log_enabled=true AND (last_log_sync_at IS NULL OR last_log_sync_at < ?)", time.Now().Add(-30*time.Second)).Order("last_log_sync_at NULLS FIRST").Limit(5).Find(&rows).Error; err != nil {
		return
	}
	for _, row := range rows {
		if err := s.SyncLogsAsync(ctx, row.ID); err != nil {
			return
		}
	}
}
func (s *OperationsService) SyncLogsAsync(ctx context.Context, id uint) error {
	s.mu.Lock()
	if s.syncing[id] {
		s.mu.Unlock()
		return nil
	}
	select {
	case s.logSlots <- struct{}{}:
	default:
		s.mu.Unlock()
		return fmt.Errorf("日志采集队列繁忙，请稍后重试")
	}
	s.syncing[id] = true
	s.mu.Unlock()
	go func() {
		defer func() { <-s.logSlots; s.mu.Lock(); delete(s.syncing, id); s.mu.Unlock() }()
		qctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		var row model.OperationsDatabase
		if err := s.db.First(&row, id).Error; err != nil {
			return
		}
		err := s.syncDatabaseLogs(qctx, row)
		message := ""
		if err != nil {
			message = err.Error()
		}
		s.db.Model(&model.OperationsDatabase{}).Where("id=? AND revision=?", id, row.Revision).Updates(map[string]any{"last_log_sync_at": time.Now(), "log_error": message})
	}()
	return nil
}
func (s *OperationsService) syncDatabaseLogs(ctx context.Context, row model.OperationsDatabase) error {
	if row.ServerID == 0 || !path.IsAbs(row.LogDirectory) {
		return fmt.Errorf("请关联日志所在主机，并配置绝对日志目录")
	}
	if row.LogFormat != "csv" && row.LogFormat != "text" {
		return fmt.Errorf("日志格式仅支持 csv 或 text")
	}
	if _, err := time.LoadLocation(row.LogTimezone); err != nil {
		return fmt.Errorf("日志时区无效")
	}
	conn, err := s.connect(ctx, row.ServerID)
	if err != nil {
		return err
	}
	defer conn.Close()
	files, err := conn.SFTP.ReadDir(row.LogDirectory)
	if err != nil {
		return fmt.Errorf("读取日志目录失败: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().Before(files[j].ModTime()) })
	budget := int64(16 << 20)
	matched := 0
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		suffix := ".csv"
		if row.LogFormat == "text" {
			suffix = ".log"
		}
		if !strings.HasSuffix(strings.ToLower(file.Name()), suffix) {
			continue
		}
		matched++
		if row.LogStartAt != nil && file.ModTime().Before(*row.LogStartAt) {
			continue
		}
		name := path.Join(row.LogDirectory, file.Name())
		if path.Dir(name) != path.Clean(row.LogDirectory) {
			return fmt.Errorf("无效日志文件名")
		}
		// A stable device/inode identity survives renames. A changed prefix or file
		// truncation creates a new generation, including weekly reused filenames.
		raw, err := conn.Run(ctx, "stat -Lc '%d:%i %s %Y' -- "+opsremote.Quote(name), 2048)
		if err != nil {
			return err
		}
		parts := strings.Fields(string(raw))
		if len(parts) != 3 {
			return fmt.Errorf("无法识别日志文件属性")
		}
		size, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil {
			return e
		}
		modified, _ := strconv.ParseInt(parts[2], 10, 64)
		identity := fmt.Sprintf("%d:%s", row.ServerID, parts[0])
		var cursor model.DatabaseLogCursor
		err = s.db.Where("database_id=? AND file_identity=?", row.ID, identity).First(&cursor).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if err == gorm.ErrRecordNotFound {
			cursor = model.DatabaseLogCursor{DatabaseID: row.ID, FileIdentity: identity, Path: name}
		}
		f, err := conn.SFTP.Open(name)
		if err != nil {
			return err
		}
		head := make([]byte, min(int64(128), size))
		n, headErr := io.ReadFull(f, head)
		if headErr != nil && headErr != io.EOF && headErr != io.ErrUnexpectedEOF {
			f.Close()
			return headErr
		}
		head = head[:n]
		headLength := cursor.HeadLength
		if headLength == 0 {
			headLength = len(head)
		}
		changed := cursor.Offset > size || (cursor.HeadLength > 0 && (len(head) < cursor.HeadLength || opsremote.Digest(head[:min(len(head), cursor.HeadLength)]) != cursor.HeadHash))
		if changed {
			cursor.Generation++
			cursor.Offset = 0
			headLength = len(head)
		}
		headLength = min(headLength, len(head))
		headHash := opsremote.Digest(head[:headLength])
		stable := cursor.ID != 0 && cursor.LastSize == size && cursor.LastModified == modified
		if _, err := f.Seek(cursor.Offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, min(budget, int64(4<<20))))
		f.Close()
		if err != nil {
			return err
		}
		var batch pglog.Batch
		if row.LogFormat == "csv" {
			batch, err = pglog.ParseCSV(data, row.LogTimezone, row.SlowThresholdMs)
		} else {
			batch, err = pglog.ParseText(data, row.LogTimezone, row.SlowThresholdMs, stable && cursor.Offset+int64(len(data)) == size)
		}
		if err == nil && batch.Consumed == 0 && len(data) >= 4<<20 {
			err = fmt.Errorf("单条日志超过 4 MiB 或记录边界无法识别，偏移 %d 已保留，请核对日志格式", cursor.Offset)
		}
		parseErr := err
		startOffset := cursor.Offset
		cursor.Path = name
		cursor.HeadHash = headHash
		cursor.HeadLength = headLength
		cursor.LastSize = size
		cursor.LastModified = modified
		cursor.LastError = ""
		if parseErr != nil {
			cursor.LastError = parseErr.Error()
		}
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current model.OperationsDatabase
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, row.ID).Error; e != nil {
				return e
			}
			if current.Revision != row.Revision {
				return fmt.Errorf("日志连接配置已变化，本轮结果未写入")
			}
			if cursor.ID == 0 {
				if e := tx.Create(&cursor).Error; e != nil {
					return e
				}
			}
			for _, event := range batch.Events {
				if row.LogStartAt != nil && event.Time.Before(*row.LogStartAt) {
					continue
				}
				entry := model.DatabaseLogEvent{DatabaseID: row.ID, SourceKey: fmt.Sprintf("%d:%d:%d", cursor.ID, cursor.Generation, startOffset+event.Offset), Type: event.Type, EventTime: event.Time, DurationMs: event.DurationMs, Phase: event.Phase, DBName: event.DBName, Username: event.Username, PID: event.PID, Client: event.Client, Application: event.Application, SQLState: event.SQLState, SQL: event.SQL, Message: event.Message, Detail: event.Detail, Hint: event.Hint, Context: event.Context, Raw: event.Raw, SourcePath: name, SourceOffset: startOffset + event.Offset, Fingerprint: event.Fingerprint}
				if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; e != nil {
					return e
				}
			}
			cursor.Offset = startOffset + batch.Consumed
			return tx.Save(&cursor).Error
		})
		if err != nil {
			return err
		}
		if parseErr != nil {
			return fmt.Errorf("日志 %s: %w", file.Name(), parseErr)
		}
		budget -= int64(len(data))
		if budget <= 0 {
			return nil
		}
	}
	if matched == 0 {
		return fmt.Errorf("目录中未找到 %s 格式日志；请检查路径、日志格式和读取权限", row.LogFormat)
	}
	return nil
}

type OperationsEventFilter struct {
	Type, Keyword, From, To string
	MinDuration             float64
}

func (s *OperationsService) ListEvents(ctx context.Context, id uint, page, size int, filter OperationsEventFilter) ([]model.DatabaseLogEvent, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 30
	}
	q := s.db.WithContext(ctx).Model(&model.DatabaseLogEvent{}).Where("database_id=?", id)
	if filter.Type != "" {
		q = q.Where("type=?", filter.Type)
	}
	if filter.Keyword != "" {
		needle := "%" + filter.Keyword + "%"
		q = q.Where("sql ILIKE ? OR message ILIKE ? OR username ILIKE ? OR db_name ILIKE ?", needle, needle, needle, needle)
	}
	for _, v := range []struct{ raw, op string }{{filter.From, ">="}, {filter.To, "<="}} {
		if v.raw != "" {
			t, err := time.Parse(time.RFC3339, v.raw)
			if err != nil {
				return nil, 0, fmt.Errorf("无效时间范围")
			}
			q = q.Where("event_time "+v.op+" ?", t)
		}
	}
	if filter.MinDuration > 0 {
		q = q.Where("duration_ms>=?", filter.MinDuration)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.DatabaseLogEvent
	err := q.Select("id,database_id,type,event_time,duration_ms,phase,db_name,username,p_id,client,application,sql_state,left(sql,4000) AS sql,left(message,2000) AS message,source_path,source_offset,fingerprint,created_at").Order("event_time DESC,id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error
	return rows, total, err
}

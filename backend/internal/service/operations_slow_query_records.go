package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"gorm.io/gorm"
)

type SlowQueryFilter struct {
	Page, Size          int
	Keyword, Database   string
	TargetRunID, AsOfID uint
}
type SlowQueryRecord struct {
	TargetRunID uint      `json:"targetRunId,omitempty"`
	Instance    string    `json:"instance"`
	ID          uint      `json:"id"`
	Database    string    `json:"database"`
	EventTime   time.Time `json:"eventTime"`
	DurationMs  float64   `json:"durationMs"`
	Phase       string    `json:"phase"`
	Username    string    `json:"username"`
	Client      string    `json:"client"`
	SQL         string    `json:"sql"`
	CollectedAt time.Time `json:"collectedAt"`
}
type SlowQueryTarget struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}
type SlowQueryPage struct {
	List         []SlowQueryRecord `json:"list"`
	Total        int64             `json:"total"`
	Page         int               `json:"page"`
	Size         int               `json:"size"`
	AsOfID       uint              `json:"asOfId"`
	Targets      []SlowQueryTarget `json:"targets"`
	FrozenAt     *time.Time        `json:"frozenAt"`
	CollectedAt  *time.Time        `json:"collectedAt"`
	Availability string            `json:"availability"`
	Message      string            `json:"message"`
}

func slowQueryPagination(f SlowQueryFilter) SlowQueryFilter {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size < 1 || f.Size > 100 {
		f.Size = 20
	}
	return f
}

// The cutoff is the storage expiry rule, not a default business time filter.
// No log scan, SSH call or database diagnostic is started by these readers.
type reportFreezeTime struct{}

func (s *OperationsService) retainedSlowQueries(ctx context.Context, databaseID uint) *gorm.DB {
	now := time.Now()
	if at, ok := ctx.Value(reportFreezeTime{}).(time.Time); ok {
		now = at
	}
	cutoff := now.Add(-48 * time.Hour)
	return s.db.WithContext(ctx).Model(&model.DatabaseLogEvent{}).Where("database_id=? AND type='slow_sql' AND duration_ms>=5000 AND event_time>=? AND created_at>=?", databaseID, cutoff, cutoff)
}
func filterSlowQueries(q *gorm.DB, f SlowQueryFilter, databaseColumn string) *gorm.DB {
	if f.Database != "" {
		q = q.Where(databaseColumn+"=?", f.Database)
	}
	if f.Keyword != "" {
		needle := "%" + f.Keyword + "%"
		q = q.Where("sql ILIKE ? OR username ILIKE ? OR "+databaseColumn+" ILIKE ?", needle, needle, needle)
	}
	return q
}
func (s *OperationsService) MonitoringSlowQueries(ctx context.Context, id uint, filter SlowQueryFilter) (SlowQueryPage, error) {
	f := slowQueryPagination(filter)
	out := SlowQueryPage{List: []SlowQueryRecord{}, Targets: []SlowQueryTarget{}, Page: f.Page, Size: f.Size, Availability: "available"}
	var row model.OperationsDatabase
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return out, err
	}
	out.CollectedAt = row.LastLogSyncAt
	if !row.LogEnabled {
		out.Availability = "not_configured"
		out.Message = "日志采集未启用，以下仅展示已有记录"
	} else if row.LastLogSyncAt == nil || time.Since(*row.LastLogSyncAt) > 3*time.Minute || row.LogError != "" {
		out.Availability = "delayed"
		out.Message = "日志采集尚未完成或有延迟，以下仅展示已有记录"
	}
	q := filterSlowQueries(s.retainedSlowQueries(ctx, id), f, "db_name")
	if f.AsOfID == 0 {
		if err := q.Select("COALESCE(max(id),0)").Scan(&f.AsOfID).Error; err != nil {
			return out, err
		}
	}
	out.AsOfID = f.AsOfID
	q = filterSlowQueries(s.retainedSlowQueries(ctx, id), f, "db_name").Where("id<=?", f.AsOfID)
	if err := q.Count(&out.Total).Error; err != nil {
		return out, err
	}
	var events []model.DatabaseLogEvent
	if err := q.Order("event_time DESC,id DESC").Offset((f.Page - 1) * f.Size).Limit(f.Size).Find(&events).Error; err != nil {
		return out, err
	}
	for _, e := range events {
		out.List = append(out.List, SlowQueryRecord{ID: e.ID, Instance: row.Name, Database: e.DBName, EventTime: e.EventTime, DurationMs: e.DurationMs, Phase: e.Phase, Username: inspection.Redact(e.Username), Client: inspection.Redact(e.Client), SQL: inspection.Redact(e.SQL), CollectedAt: e.CreatedAt})
	}
	return out, nil
}

// Copies every retained source record in bounded batches within the report's
// repeatable-read transaction. The destination has its own archival lifecycle.
func (s *OperationsService) freezeInspectionSlowQueries(ctx context.Context, runID, targetID, databaseID uint) (int64, error) {
	var cursor uint
	var total int64
	for {
		var events []model.DatabaseLogEvent
		if err := s.retainedSlowQueries(ctx, databaseID).Where("id>?", cursor).Order("id").Limit(250).Find(&events).Error; err != nil {
			return total, err
		}
		if len(events) == 0 {
			return total, nil
		}
		rows := make([]model.InspectionSlowQuery, 0, len(events))
		for _, e := range events {
			rows = append(rows, model.InspectionSlowQuery{RunID: runID, TargetRunID: targetID, SourceEventID: e.ID, DatabaseName: e.DBName, EventTime: e.EventTime, DurationMs: e.DurationMs, Phase: e.Phase, Username: inspection.Redact(e.Username), Client: inspection.Redact(e.Client), SQL: inspection.Redact(e.SQL), CollectedAt: e.CreatedAt})
			cursor = e.ID
		}
		if err := s.db.WithContext(ctx).CreateInBatches(&rows, 250).Error; err != nil {
			return total, err
		}
		total += int64(len(rows))
	}
}

func (s *OperationsService) InspectionSlowQueries(ctx context.Context, runID uint, filter SlowQueryFilter) (SlowQueryPage, error) {
	f := slowQueryPagination(filter)
	out := SlowQueryPage{List: []SlowQueryRecord{}, Targets: []SlowQueryTarget{}, Page: f.Page, Size: f.Size, Availability: "archived"}
	var run model.InspectionRun
	if err := s.db.WithContext(ctx).Select("id,frozen_at,collection_mode").First(&run, runID).Error; err != nil {
		return out, err
	}
	out.FrozenAt = run.FrozenAt
	var targets []struct {
		ID             uint
		Name, SlowMeta string
	}
	const projection = `id,name,COALESCE((SELECT jsonb_build_object('availability',o->'attributes'->'availability','message',o->'reason','collectedAt',o->'collectedAt')::text FROM jsonb_array_elements(COALESCE(NULLIF(result_json->'reportChecks','null'::jsonb),'[]'::jsonb)) o WHERE o->>'code'='postgresql-slow-history' LIMIT 1),'') AS slow_meta`
	if err := s.db.WithContext(ctx).Model(&model.InspectionTargetRun{}).Select(projection).Where("run_id=? AND type='database'", runID).Order("id").Scan(&targets).Error; err != nil {
		return out, err
	}
	for _, target := range targets {
		out.Targets = append(out.Targets, SlowQueryTarget{target.ID, target.Name})
	}
	var messages []string
	selectedTargets := 0
	for _, target := range targets {
		if f.TargetRunID != 0 && f.TargetRunID != target.ID {
			continue
		}
		selectedTargets++
		var meta struct {
			Availability, Message string
			CollectedAt           time.Time
		}
		if target.SlowMeta != "" {
			if err := json.Unmarshal([]byte(target.SlowMeta), &meta); err != nil {
				return out, err
			}
		}
		if !meta.CollectedAt.IsZero() {
			at := meta.CollectedAt
			out.CollectedAt = &at
		}
		if meta.Availability == "not_configured" || meta.Availability == "delayed" {
			out.Availability = meta.Availability
			message := meta.Message
			if message == "" {
				if meta.Availability == "not_configured" {
					message = "日志采集未启用，仅保存当时已有记录"
				} else {
					message = "日志同步有延迟，仅保存当时已有记录"
				}
			}
			messages = append(messages, target.Name+"："+message)
		}
	}
	if selectedTargets > 1 {
		out.CollectedAt = nil
		if len(messages) > 0 {
			out.Availability = "partial"
		}
	}
	out.Message = strings.Join(messages, "；")
	if run.FrozenAt == nil {
		out.Message = "历史报告沿用当时的报告正文，未保存独立分页记录"
		out.Availability = "legacy"
		return out, nil
	}
	q := s.db.WithContext(ctx).Model(&model.InspectionSlowQuery{}).Where("run_id=?", runID)
	if f.TargetRunID > 0 {
		q = q.Where("target_run_id=?", f.TargetRunID)
	}
	q = filterSlowQueries(q, f, "database_name")
	if err := q.Count(&out.Total).Error; err != nil {
		return out, err
	}
	var rows []model.InspectionSlowQuery
	if err := q.Order("event_time DESC,source_event_id DESC,id DESC").Offset((f.Page - 1) * f.Size).Limit(f.Size).Find(&rows).Error; err != nil {
		return out, err
	}
	names := map[uint]string{}
	for _, target := range targets {
		names[target.ID] = target.Name
	}
	for _, r := range rows {
		out.List = append(out.List, SlowQueryRecord{ID: r.ID, TargetRunID: r.TargetRunID, Instance: names[r.TargetRunID], Database: r.DatabaseName, EventTime: r.EventTime, DurationMs: r.DurationMs, Phase: r.Phase, Username: r.Username, Client: r.Client, SQL: r.SQL, CollectedAt: r.CollectedAt})
	}
	return out, nil
}

func slowQueryReportObservation(page SlowQueryPage) inspection.Observation {
	o := inspection.Observation{Code: "postgresql-slow-history", Name: "已发生慢 SQL（CSV 日志）", Status: "RECORDED", Value: fmt.Sprintf("已保存 %d 条慢 SQL 日志记录", page.Total), Source: "PostgreSQL CSV 日志", Attributes: map[string]string{"pagination": "server", "total": strconv.FormatInt(page.Total, 10)}}
	if page.CollectedAt != nil {
		o.CollectedAt = *page.CollectedAt
	}
	if page.Availability != "available" && page.Availability != "archived" {
		o.Attributes["availability"] = page.Availability
		o.Reason = page.Message
		if page.Total == 0 {
			o.Status = "UNKNOWN"
		}
	}
	t := inspection.Table{Columns: []string{"发生时间", "数据库", "耗时 ms", "用户", "客户端", "SQL 摘要"}, Note: fmt.Sprintf("共 %d 条已有记录，正文列最近 %d 条；完整存档在在线报告中按发生时间倒序分页查看。", page.Total, len(page.List))}
	for _, r := range page.List {
		t.Rows = append(t.Rows, []string{r.EventTime.Format(time.RFC3339), r.Database, fmt.Sprintf("%.2f", r.DurationMs), r.Username, r.Client, inspection.Preview(r.SQL, 1000)})
	}
	o.Tables = []inspection.Table{t}
	return o
}

package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/componentprobe"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
)

func (s *OperationsService) monitoringPostgresSlowHistory(ctx context.Context, row model.OperationsDatabase) inspection.Observation {
	o := componentprobe.Observation("postgresql-slow-history", "慢 SQL 日志（48 小时）", "未接入慢 SQL 日志", "RECORDED")
	if !row.LogEnabled {
		o.Attributes = map[string]string{"availability": "not_configured"}
		return o
	}
	base := s.db.WithContext(ctx).Model(&model.DatabaseLogEvent{}).Where("database_id=? AND type='slow_sql' AND event_time>=?", row.ID, time.Now().Add(-48*time.Hour))
	var count int64
	if err := base.Count(&count).Error; err != nil {
		return componentprobe.Failed(o.Code, o.Name, fmt.Errorf("读取慢 SQL 日志统计失败"))
	}
	var events []model.DatabaseLogEvent
	if err := s.db.WithContext(ctx).Where("database_id=? AND type='slow_sql' AND event_time>=?", row.ID, time.Now().Add(-48*time.Hour)).Order("event_time DESC,id DESC").Limit(50).Find(&events).Error; err != nil {
		return componentprobe.Failed(o.Code, o.Name, fmt.Errorf("读取慢 SQL 日志失败"))
	}
	o.Value = fmt.Sprintf("最近 48 小时已采集 %d 条慢 SQL 日志", count)
	o.Measurements = map[string]float64{"historicalSlowLogs": float64(count)}
	t := inspection.Table{Columns: []string{"发生时间", "耗时 ms", "阶段", "用户", "客户端", "SQL 摘要"}, Note: "显示最近 50 条已采集日志；同一 SQL 的多个执行阶段分别计为日志记录。"}
	for _, e := range events {
		t.Rows = append(t.Rows, []string{e.EventTime.Format(time.RFC3339), fmt.Sprintf("%.2f", e.DurationMs), e.Phase, e.Username, e.Client, inspection.Preview(e.SQL, 1000)})
	}
	if row.LastLogSyncAt != nil {
		o.CollectedAt = *row.LastLogSyncAt
	}
	if row.LastLogSyncAt == nil || time.Since(*row.LastLogSyncAt) > 3*time.Minute || row.LogError != "" {
		o.Status = "UNKNOWN"
		o.Reason = "日志同步尚未完成或数据已延迟，以下仅为已采集记录"
		if row.LogError != "" {
			o.Reason += "；" + inspection.Preview(row.LogError, 200)
		}
	}
	o.Tables = []inspection.Table{t}
	return o
}

// Only transform the two existing guide result tables. Never send the complete
// deep SQL snapshot to the page or start a deep scan from a dashboard GET.
func (s *OperationsService) monitoringBloatSections(ctx context.Context, t monitoringTarget) []inspection.Observation {
	var state model.MonitoringState
	err := s.db.WithContext(ctx).Where("target_type=? AND target_id=? AND lane='deep'", t.Type, t.ID).First(&state).Error
	state.Lane = "deep"
	lane := monitoringLane(state, t)
	out := []inspection.Observation{}
	for _, sourceCode := range []string{"cloudhis-34", "cloudhis-35"} {
		code, name := "postgresql-table-bloat", "表膨胀估算"
		if sourceCode == "cloudhis-35" {
			code, name = "postgresql-index-bloat", "索引膨胀估算"
		}
		o := componentprobe.Failed(code, name, fmt.Errorf("等待后台膨胀统计"))
		o.CollectedAt = time.Time{}
		o.Attributes = map[string]string{"estimated": "true"}
		if err == nil && lane.Result != nil {
			for _, source := range lane.Result.ReportChecks {
				if source.Code == sourceCode {
					o = monitoringBloatObservation(source, code, name)
					break
				}
			}
			if lane.CollectedAt != nil {
				o.CollectedAt = *lane.CollectedAt
			}
			if lane.Stale || lane.LastError != "" {
				o.Attributes["stale"] = "true"
				o.Reason = "膨胀统计更新延迟，显示上次估算"
			}
		}
		out = append(out, o)
	}
	return out
}

func monitoringBloatObservation(source inspection.Observation, code, name string) inspection.Observation {
	o := inspection.Observation{Code: code, Name: name, Status: "RECORDED", CollectedAt: source.CollectedAt, Attributes: map[string]string{"estimated": "true"}}
	t := inspection.Table{Columns: []string{"表名", "膨胀倍数估算", "浪费空间估算 MiB"}, Note: "每 6 小时更新，展示最近 Top5 结果中的业务对象；估算受统计信息影响，不代表精确可回收空间。"}
	if code == "postgresql-index-bloat" {
		t.Columns = []string{"索引", "所属表", "膨胀倍数估算", "浪费空间估算 MiB"}
	}
	if source.Status == "UNKNOWN" {
		o.Status = "UNKNOWN"
		o.Reason = "膨胀估算未取得结果"
		return o
	}
	seen := map[string]bool{}
	for _, table := range source.Tables {
		indexes := map[string]int{}
		for i, c := range table.Columns {
			indexes[c] = i
		}
		get := func(row []string, key string) string {
			if i, ok := indexes[key]; ok && i < len(row) {
				return row[i]
			}
			return ""
		}
		for _, row := range table.Rows {
			schema, tableName := get(row, "schemaname"), get(row, "tablename")
			if schema == "" || tableName == "" || strings.HasPrefix(schema, "pg_") || schema == "information_schema" {
				continue
			}
			name := schema + "." + tableName
			ratioKey, wasteKey := "tbloat", "wastedbytes"
			if code == "postgresql-index-bloat" {
				index := get(row, "iname")
				if index == "" || index == "?" || index == "NULL" {
					continue
				}
				name = schema + "." + index
				ratioKey, wasteKey = "ibloat", "wastedibytes"
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			ratio, waste := "—", "—"
			if value, err := strconv.ParseFloat(get(row, ratioKey), 64); err == nil && value >= 1 {
				ratio = fmt.Sprintf("%.1f", value)
				if bytes, err := strconv.ParseFloat(get(row, wasteKey), 64); err == nil && bytes >= 0 {
					waste = fmt.Sprintf("%.2f", bytes/(1<<20))
				}
			}
			cells := []string{name, ratio, waste}
			if code == "postgresql-index-bloat" {
				cells = []string{name, schema + "." + tableName, ratio, waste}
			}
			t.Rows = append(t.Rows, cells)
		}
	}
	if len(t.Rows) == 0 {
		o.Status = "UNKNOWN"
		o.Reason = "最近的 Top5 结果未提供可判读的业务对象估算"
	}
	o.Tables = []inspection.Table{t}
	return o
}

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
	page, err := s.MonitoringSlowQueries(ctx, row.ID, SlowQueryFilter{Page: 1, Size: 1})
	if err != nil {
		return componentprobe.Failed("postgresql-slow-history", "已发生慢 SQL（CSV 日志）", fmt.Errorf("读取已保存慢 SQL 失败"))
	}
	page.List = nil
	o := slowQueryReportObservation(page)
	o.Tables = nil
	if page.Total > 0 || page.Availability == "available" {
		o.Measurements = map[string]float64{"slowSQLRecords": float64(page.Total)}
	}
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
	t := inspection.Table{Columns: []string{"数据库", "表名", "膨胀倍数估算", "浪费空间估算 MiB"}, Note: "每 6 小时更新，展示各数据库最近 Top5 结果中的业务对象；估算受统计信息影响，不代表精确可回收空间。"}
	if code == "postgresql-index-bloat" {
		t.Columns = []string{"数据库", "索引", "所属表", "膨胀倍数估算", "浪费空间估算 MiB"}
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
			database := get(row, "数据库")
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
			identity := database + "/" + name
			if seen[identity] {
				continue
			}
			seen[identity] = true
			ratio, waste := "—", "—"
			if value, err := strconv.ParseFloat(get(row, ratioKey), 64); err == nil && value >= 1 {
				ratio = fmt.Sprintf("%.1f", value)
				if bytes, err := strconv.ParseFloat(get(row, wasteKey), 64); err == nil && bytes >= 0 {
					waste = fmt.Sprintf("%.2f", bytes/(1<<20))
				}
			}
			cells := []string{database, name, ratio, waste}
			if code == "postgresql-index-bloat" {
				cells = []string{database, name, schema + "." + tableName, ratio, waste}
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

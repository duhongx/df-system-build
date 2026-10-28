package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
)

// Compare only actual archived results for the same target and SSH identity.
// No separately entered baseline or monitoring history is needed.
func (s *OperationsService) enrichInspectionHistory(target *model.InspectionTargetRun, result *opsprobe.JobResult) {
	if target.ManualJSON == "" {
		return
	}
	var timeline, counter *inspection.Observation
	for i := range result.ReportChecks {
		o := &result.ReportChecks[i]
		if o.Status == "UNKNOWN" || o.Status == "NOT_APPLICABLE" || o.ConfigurationState == "NOT_CONFIGURED" {
			continue
		}
		if o.Code == "cloudhis-17" {
			timeline = o
		}
		if o.Code == "cloudhis-39" {
			counter = o
		}
	}
	if timeline == nil && counter == nil {
		return
	}
	type archivedCheck struct {
		runID       uint
		observation inspection.Observation
	}
	previousChecks := map[string]archivedCheck{}
	before := result.StartedAt
	if before.IsZero() {
		before = time.Now()
	}
	rows, err := s.db.Model(&model.InspectionTargetRun{}).
		Select(`inspection_target_runs.id,inspection_target_runs.run_id,inspection_target_runs.finished_at,
		(SELECT item->'observations' FROM jsonb_array_elements(inspection_runs.report_snapshot_json->'objects') AS item WHERE item->>'id'=inspection_target_runs.id::text) AS result_json`).
		Joins("JOIN inspection_runs ON inspection_runs.id=inspection_target_runs.run_id").
		Where("inspection_target_runs.type=? AND target_id=? AND server_id=? AND server_endpoint=? AND run_id<>? AND inspection_target_runs.status='COMPLETED' AND inspection_target_runs.finished_at<=? AND inspection_runs.html_status='READY' AND inspection_runs.report_snapshot_json IS NOT NULL", target.Type, target.TargetID, target.ServerID, target.ServerEndpoint, target.RunID, before).
		Order("inspection_target_runs.finished_at DESC,inspection_target_runs.id DESC").Rows()
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var candidate model.InspectionTargetRun
			if s.db.ScanRows(rows, &candidate) != nil {
				continue
			}
			var archived []inspection.Observation
			if json.Unmarshal([]byte(candidate.ResultJSON), &archived) != nil {
				continue
			}
			for _, prior := range archived {
				current := timeline
				if prior.Code == "cloudhis-39" {
					current = counter
				}
				if (prior.Code != "cloudhis-17" && prior.Code != "cloudhis-39") || current == nil || len(prior.Measurements) == 0 {
					continue
				}
				if _, found := previousChecks[prior.Code]; found {
					continue
				}
				if prior.Code == "cloudhis-17" && prior.Status == "UNKNOWN" {
					continue
				}
				if len(current.Executions) > 0 && len(prior.Executions) > 0 && current.Executions[0].Command != prior.Executions[0].Command {
					continue
				}
				previousChecks[prior.Code] = archivedCheck{candidate.RunID, prior}
			}
			if (timeline == nil || previousChecks["cloudhis-17"].runID != 0) && (counter == nil || previousChecks["cloudhis-39"].runID != 0) {
				break
			}
		}
		if rows.Err() != nil {
			err = rows.Err()
		}
	}
	for _, code := range []string{"cloudhis-17", "cloudhis-39"} {
		previous, exists := previousChecks[code]
		if !exists {
			continue
		}
		prior := previous.observation
		if timeline != nil && prior.Code == "cloudhis-17" {
			var comparisons [][]string
			var grown []string
			keys := make([]string, 0, len(timeline.Measurements))
			for host := range timeline.Measurements {
				keys = append(keys, host)
			}
			sort.Strings(keys)
			for _, host := range keys {
				now := timeline.Measurements[host]
				before, ok := prior.Measurements[host]
				if !ok {
					continue
				}
				comparisons = append(comparisons, []string{host, fmt.Sprint(before), fmt.Sprint(now), fmt.Sprint(now - before)})
				if now > before {
					grown = append(grown, fmt.Sprintf("%s TL %.0f → %.0f", host, before, now))
				}
			}
			if len(comparisons) > 0 {
				timeline.Tables = append(timeline.Tables, inspection.Table{Title: fmt.Sprintf("与历史报告 #%d 的 TL 比较", previous.runID), Columns: []string{"成员地址", "前次 TL", "本次 TL", "变化"}, Rows: comparisons, Note: "前次采集：" + reportTime(prior.CollectedAt)})
				timeline.Value += fmt.Sprintf("；已与报告 #%d 比较", previous.runID)
				if len(grown) > 0 {
					timeline.Status = "WARNING"
					timeline.Reason = "指导书要求反馈 TL 增长：" + strings.Join(grown, "；")
				}
			}
		}
		if counter != nil && prior.Code == "cloudhis-39" {
			before, ok := prior.Measurements["inodeUsed"]
			now, nowOK := counter.Measurements["inodeUsed"]
			if ok && nowOK {
				counter.Tables = append(counter.Tables, inspection.Table{Title: "与上次巡检的采样比较", Columns: []string{"前次报告", "前次采样时间", "前次数量", "本次数量", "区间净变化"}, Rows: [][]string{{strconv.FormatUint(uint64(previous.runID), 10), reportTime(prior.CollectedAt), fmt.Sprint(before), fmt.Sprint(now), fmt.Sprint(now - before)}}, Note: "这是两次巡检之间的 inode 净变化，不能作为逐日新增或精确业务文件数量。"})
			}
		}
	}
	if timeline != nil && len(timeline.Tables) == 1 {
		timeline.Value += "；没有可比较的前次 TL，作为首次记录"
		if err != nil {
			timeline.Reason = "读取历史报告失败：" + err.Error()
		}
	}
	if counter != nil {
		enrichDailyRecords(counter)
	}
}

func enrichDailyRecords(o *inspection.Observation) {
	if len(o.Executions) < 2 {
		o.Status = "UNKNOWN"
		o.Gaps = append(o.Gaps, "没有取得客户已有的逐日记录文件，无法还原每日新增；本次 inode 采样已保留")
		return
	}
	pattern := regexp.MustCompile(`^\s*(\d{4}-\d{2}-\d{2})(?:[ T]00:00(?::00)?)?\s+([0-9]+)\s*$`)
	values := map[string]int64{}
	valid := true
	for _, line := range strings.Split(o.Executions[1].Stdout, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		m := pattern.FindStringSubmatch(line)
		if len(m) != 3 {
			valid = false
			continue
		}
		if _, err := time.Parse("2006-01-02", m[1]); err != nil {
			valid = false
			continue
		}
		count, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			valid = false
			continue
		}
		if old, ok := values[m[1]]; ok && old != count {
			valid = false
		}
		values[m[1]] = count
	}
	dates := make([]string, 0, len(values))
	for date := range values {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	rows := [][]string{}
	missing := false
	for i, date := range dates {
		delta := "缺少前一天记录"
		if i > 0 {
			previous, _ := time.Parse("2006-01-02", dates[i-1])
			current, _ := time.Parse("2006-01-02", date)
			if current.Sub(previous) == 24*time.Hour {
				delta = strconv.FormatInt(values[date]-values[dates[i-1]], 10)
			} else {
				missing = true
			}
		}
		rows = append(rows, []string{date, strconv.FormatInt(values[date], 10), delta})
	}
	o.Tables = append(o.Tables, inspection.Table{Title: "客户已有每日记录及净变化", Columns: []string{"日期（零点采样）", "inode 已用数", "较前一天净变化"}, Rows: rows, Note: "按指导书的 inode 统计口径计算；负数表示净减少。首次记录无前一天数据时不补算。"})
	if !valid || len(dates) < 2 || missing {
		o.Status = "UNKNOWN"
		o.Gaps = append(o.Gaps, "逐日记录不完整、日期不连续或格式不明确；仅对实际连续日期计算净变化，原文件完整保留")
	} else {
		o.Status = "RECORDED"
		o.Value += fmt.Sprintf("；已读取 %d 天已有零点记录", len(dates))
	}
}

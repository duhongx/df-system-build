package inspection

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func presentationText(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…（完整内容见任务检查详情）"
}

func presentationTable(t Table, code string) Table {
	out := t
	out.Columns = append([]string{}, t.Columns...)
	out.Rows = [][]string{}
	indices := []int{}
	preferred := map[string][]string{
		"cloudhis-25": {"application_name", "client_addr", "statename", "sync_statename", "slave_latency_mb"},
		"cloudhis-28": {"pid", "shemaname", "relname", "mode", "lock_duration", "query"},
		"cloudhis-29": {"pid", "datname", "username", "state_change", "state", "sql"},
		"cloudhis-30": {"pid", "datname", "username", "xact_start", "state", "sql"},
		"cloudhis-33": {"pid", "datname", "username", "query_start", "wait_event_type", "sql"},
		"cloudhis-34": {"schemaname", "tablename", "tbloat", "wastedsize", "wastedbytes", "pages"},
		"cloudhis-35": {"schemaname", "tablename", "iname", "ibloat", "wastedisize", "wastedibytes"},
	}[code]
	if len(t.Columns) > 6 {
		for _, key := range preferred {
			for i, column := range t.Columns {
				if strings.EqualFold(strings.Split(column, "(")[0], key) {
					indices = append(indices, i)
					break
				}
			}
		}
	}
	if len(indices) == 0 {
		for i := range t.Columns {
			if len(indices) == 6 {
				break
			}
			indices = append(indices, i)
		}
	}
	out.Columns = []string{}
	for _, i := range indices {
		out.Columns = append(out.Columns, t.Columns[i])
	}
	rows := append([][]string{}, t.Rows...)
	// Keep abnormal Pod/node rows ahead of ordinary entries before applying a
	// presentation limit. The original report and all rows remain unchanged.
	if code == "cloudhis-7" || code == "cloudhis-8" || code == "cloudhis-9" || code == "cloudhis-10" {
		rank := func(row []string) int {
			score := 0
			for i, column := range t.Columns {
				if i >= len(row) {
					continue
				}
				value := row[i]
				if column == "状态" && value != "Running" && !strings.HasPrefix(value, "Ready") {
					score += 10
				}
				if column == "就绪" {
					v := strings.Split(value, "/")
					if len(v) == 2 && v[0] != v[1] {
						score += 10
					}
				}
				if column == "重启次数" {
					n, _ := strconv.Atoi(value)
					if n > 0 {
						score++
					}
				}
			}
			return score
		}
		sort.SliceStable(rows, func(i, j int) bool { return rank(rows[i]) > rank(rows[j]) })
	}
	limit := 10
	if code == "cloudhis-14" {
		limit = 5
	}
	if len(rows) < limit {
		limit = len(rows)
	}
	for _, row := range rows[:limit] {
		values := []string{}
		for _, i := range indices {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			if code == "cloudhis-14" && i < len(t.Columns) && strings.Contains(t.Columns[i], "镜像仓库") {
				parts := strings.Split(value, "/")
				value = parts[len(parts)-1]
			}
			values = append(values, presentationText(value, 180))
		}
		out.Rows = append(out.Rows, values)
	}
	if len(rows) > limit {
		out.Note += fmt.Sprintf(" 共 %d 行，正文列出 %d 行，其余结果见任务检查详情。", len(rows), limit)
	}
	if len(indices) < len(t.Columns) {
		out.Note += " 正文展示关键列，完整列见任务检查详情。"
	}
	out.Note = presentationText(out.Note, 500)
	return out
}

func presentationObservation(o Observation, code string) Observation {
	if pod, ok := podPresentation(o); ok {
		o = pod
	}
	out := o
	out.Executions = nil
	out.Evidence = ""
	out.Value = presentationText(o.Value, 400)
	out.Reason = presentationText(o.Reason, 600)
	if code == "cloudhis-14" && o.Status == "WARNING" {
		out.Reason = "镜像留存数量超过指导书建议，需结合回滚需要与磁盘占用核对。"
	}
	out.Advice = presentationText(o.Advice, 400)
	for _, e := range o.Executions {
		if e.ExitCode != 0 || e.Error != "" || e.Truncated {
			message := strings.TrimSpace(e.Stderr)
			if message == "" {
				message = e.Error
			}
			if message != "" && !strings.Contains(out.Reason, message) {
				out.Reason = presentationText(strings.TrimSpace(out.Reason+" "+message), 600)
			}
		}
	}
	if out.Value == "" && (out.Reason != "" || len(o.Gaps) > 0) {
		out.Value = "未取得该项完整结果"
	}
	out.Gaps = append([]string{}, o.Gaps...)
	for i := range out.Gaps {
		out.Gaps[i] = presentationText(out.Gaps[i], 400)
	}
	out.Tables = []Table{}
	for _, t := range o.Tables {
		out.Tables = append(out.Tables, presentationTable(t, code))
	}
	if strings.HasPrefix(o.Value, "执行并保存") && len(o.Tables) > 0 {
		out.Value = fmt.Sprintf("本次查询返回 %d 行；关键结果如下", len(o.Tables[0].Rows))
	}
	if len(out.Tables) == 0 && len(o.Executions) > 0 && strings.HasPrefix(o.Value, "执行并保存") {
		out.Value = "检查命令已执行；实际返回保存在任务检查详情"
	}
	return out
}

// Presentation is a compact, immutable view. Full command/SQL evidence stays
// in the frozen snapshot and authenticated task detail, outside the report body.
func Presentation(source *Report) *Report {
	out := *source
	podEvidence := map[string]Observation{}
	out.Items = append([]Item{}, source.Items...)
	for i, item := range source.Items {
		out.Items[i].Reason = presentationText(item.Reason, 600)
		out.Items[i].Evidence = append([]Evidence{}, item.Evidence...)
		for j, e := range item.Evidence {
			if source.Mode == "configured" && (e.Status == "NORMAL" || e.Status == "RECORDED") {
				e.Value = keyResult(e.Observation)
			}
			out.Items[i].Evidence[j].Observation = presentationObservation(e.Observation, item.Code)
			if source.Mode == "configured" && (item.Code == "cloudhis-3" || item.Code == "cloudhis-38") {
				for k, t := range out.Items[i].Evidence[j].Tables {
					var rows [][]string
					for _, row := range t.Rows {
						if len(row) > 0 && (row[0] == "节点" || row[0] == "Version" || row[0] == "Drives" || row[0] == "容量与对象") {
							rows = append(rows, row)
						}
					}
					if t.Title == "mc admin info" {
						out.Items[i].Evidence[j].Tables[k].Rows = rows
					}
				}
			}
			if isPodCheck(item.Code) && e.ID != "" {
				podEvidence[e.ID] = out.Items[i].Evidence[j].Observation
			}
		}
	}
	out.Objects = append([]Object{}, source.Objects...)
	for i, object := range source.Objects {
		out.Objects[i].Observations = nil
		out.Objects[i].Supplement = nil
		out.Objects[i].Tables = []Table{}
		for _, t := range object.Tables {
			out.Objects[i].Tables = append(out.Objects[i].Tables, presentationTable(t, ""))
		}
	}
	out.Findings = append([]Finding{}, source.Findings...)
	for i, f := range source.Findings {
		if e, ok := podEvidence[f.EvidenceID]; ok {
			f.Value, f.Reason, f.Advice = e.Value, e.Reason, e.Advice
		}
		out.Findings[i].Value = presentationText(f.Value, 400)
		out.Findings[i].Reason = presentationText(f.Reason, 600)
		out.Findings[i].Advice = presentationText(f.Advice, 400)
	}
	out.Limitations = []string{"本报告列出运行情况、关键结果、异常与配置缺项。完整命令、SQL 和原始输出保存在任务检查详情。", "长表只展示关键列及部分记录，均注明原始数量；展示精简不改变实际检查覆盖统计。", "未配置、不适用与读取失败分别记录，不据缺少统计记录推断业务正常。"}
	return &out
}

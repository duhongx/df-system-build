package inspection

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func isPodCheck(code string) bool {
	return code == "cloudhis-8" || code == "cloudhis-9" || code == "cloudhis-10"
}

type reportPod struct {
	name, ready, status, lastRestart, age string
	restarts                              int
	abnormal                              bool
}

// Read complete structured evidence, including tables in older frozen reports.
// Do not infer health from a partial, failed or unrecognised collection.
func podPresentation(o Observation) (Observation, bool) {
	if !isPodCheck(o.Code) || len(o.Tables) != 1 || len(o.Tables[0].Rows) == 0 || len(o.Gaps) > 0 {
		return o, false
	}
	if o.Status != "NORMAL" && o.Status != "WARNING" && o.Status != "RECORDED" {
		return o, false
	}
	for _, e := range o.Executions {
		if e.ExitCode != 0 || e.Error != "" || e.Truncated {
			return o, false
		}
	}
	t := o.Tables[0]
	columns := map[string]int{}
	for i, column := range t.Columns {
		columns[column] = i
	}
	for _, column := range []string{"Pod", "就绪", "状态", "重启次数", "运行时间／补充"} {
		if _, ok := columns[column]; !ok {
			return o, false
		}
	}
	var current, history []reportPod
	restarted := 0
	for _, row := range t.Rows {
		cell := func(column string) string {
			i := columns[column]
			if i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		p := reportPod{name: cell("Pod"), ready: cell("就绪"), status: cell("状态"), lastRestart: "未提供", age: "未提供"}
		ready := strings.Split(p.ready, "/")
		if len(ready) != 2 || p.name == "" || p.status == "" {
			return o, false
		}
		n, e1 := strconv.Atoi(ready[0])
		total, e2 := strconv.Atoi(ready[1])
		var e3 error
		p.restarts, e3 = strconv.Atoi(cell("重启次数"))
		if e1 != nil || e2 != nil || e3 != nil || n < 0 || total <= 0 || n > total || p.restarts < 0 {
			return o, false
		}
		p.abnormal = n != total || p.status != "Running"
		fields := strings.Fields(cell("运行时间／补充"))
		if len(fields) > 0 {
			p.age = fields[len(fields)-1]
		}
		if len(fields) > 1 {
			p.lastRestart = strings.Trim(strings.Join(fields[:len(fields)-1], " "), "()")
		}
		if p.restarts > 0 {
			restarted++
		} else {
			p.lastRestart = "-"
		}
		if p.abnormal {
			current = append(current, p)
		} else if p.restarts > 0 {
			history = append(history, p)
		}
	}
	out := o
	out.Value = fmt.Sprintf("共 %d 个 Pod：当前异常 %d 个，当前运行且就绪 %d 个；%d 个有累计重启记录。", len(t.Rows), len(current), len(t.Rows)-len(current), restarted)
	out.Reason = ""
	out.Threshold = "非 Running 或未完全就绪为当前异常；累计重启单独记录。"
	out.Advice = ""
	if restarted > 0 {
		out.Reason = "累计重启是历史记录，不等同于当前故障。"
		out.Advice = "结合最近重启时间和事件核对；单次快照不能判断重启次数是否正在增加。"
	}
	out.Tables = []Table{}
	if len(current) > 0 {
		table := Table{Title: "当前异常 Pod", Columns: []string{"Pod", "就绪", "状态", "累计重启", "最近重启"}, Rows: [][]string{}}
		for _, p := range current {
			table.Rows = append(table.Rows, []string{p.name, p.ready, p.status, strconv.Itoa(p.restarts), p.lastRestart})
		}
		out.Tables = append(out.Tables, table)
	}
	if len(history) > 0 {
		sort.SliceStable(history, func(i, j int) bool { return history[i].restarts > history[j].restarts })
		table := Table{Title: "历史重启记录（当前均为 Running 且完全就绪）", Columns: []string{"Pod", "累计重启", "最近重启", "Pod 存续时间"}, Rows: [][]string{}}
		for _, p := range history {
			table.Rows = append(table.Rows, []string{p.name, strconv.Itoa(p.restarts), p.lastRestart, p.age})
		}
		out.Tables = append(out.Tables, table)
	}
	return out, true
}

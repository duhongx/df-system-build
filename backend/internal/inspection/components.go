package inspection

import (
	"strconv"
	"strings"
)

// Components is a compact report table, derived from the same frozen checks
// used by the detailed chapters and raw execution records.
func Components(r *Report) Table {
	if r.Mode == "configured" {
		t := Table{Title: "组件运行情况汇总", Columns: []string{"组件", "检查范围", "需关注/严重", "结果不完整"}}
		for _, g := range configuredSections(r) {
			total, warnings, unknown := 0, 0, 0
			for _, item := range g.Items {
				if strings.HasPrefix(item.Code, "summary-") {
					for _, e := range item.Evidence {
						for _, v := range e.Tables {
							total += len(v.Rows)
						}
					}
				} else {
					total += len(item.Evidence)
					if item.Status == "UNKNOWN" {
						unknown += len(item.Evidence)
					} else {
						warnings += len(item.Evidence)
					}
				}
			}
			scope := strconv.Itoa(total) + " 项"
			unit := " 项"
			if g.ID == "resources" {
				states := map[uint]string{}
				for _, o := range r.Objects {
					if o.Type == "host" {
						states[o.ID] = "NORMAL"
						if o.Status != "COMPLETED" {
							states[o.ID] = "UNKNOWN"
						}
					}
				}
				for _, item := range r.Items {
					if item.Code == "cloudhis-6" || item.Code == "cloudhis-15" || item.Code == "cloudhis-36" || strings.HasPrefix(item.Code, "host_") || strings.HasPrefix(item.Code, "disk:") || strings.HasPrefix(item.Code, "inode:") {
						for _, e := range item.Evidence {
							if before, ok := states[e.ObjectID]; ok && priority(e.Status) > priority(before) {
								states[e.ObjectID] = e.Status
							}
						}
					}
				}
				total, warnings, unknown = len(states), 0, 0
				for _, state := range states {
					if state == "UNKNOWN" {
						unknown++
					} else if state == "WARNING" || state == "CRITICAL" {
						warnings++
					}
				}
				scope = strconv.Itoa(total) + " 台主机"
				unit = " 台"
			}
			if total > 0 {
				t.Rows = append(t.Rows, []string{g.Name, scope, strconv.Itoa(warnings) + unit, strconv.Itoa(unknown) + unit})
			}
		}
		return t
	}
	names := []string{"Kubernetes 与应用", "PostgreSQL 与备份", "MinIO 与备份"}
	counts := make([]Summary, 3)
	for _, item := range r.Items {
		n, _ := strconv.Atoi(strings.TrimPrefix(item.Code, "cloudhis-"))
		i := 0
		if n == 2 || n == 4 || (n >= 15 && n <= 35) {
			i = 1
		}
		if n == 3 || n == 5 || n >= 36 {
			i = 2
		}
		c := &counts[i]
		c.Total++
		switch item.Status {
		case "NORMAL":
			c.Normal++
		case "WARNING", "CRITICAL":
			c.Warning++
		case "RECORDED":
			c.Recorded++
		case "UNKNOWN":
			c.Unknown++
		case "NOT_RUN":
			c.NotRun++
		case "NOT_APPLICABLE":
			c.NotApplicable++
		}
	}
	t := Table{Title: "组件运行情况汇总", Columns: []string{"组件", "正常", "需关注", "已记录待核对", "结果不完整", "未执行／不适用"}, Rows: [][]string{}}
	for i, name := range names {
		c := counts[i]
		t.Rows = append(t.Rows, []string{name, strconv.Itoa(c.Normal), strconv.Itoa(c.Warning), strconv.Itoa(c.Recorded), strconv.Itoa(c.Unknown), strconv.Itoa(c.NotRun) + " / " + strconv.Itoa(c.NotApplicable)})
	}
	return t
}

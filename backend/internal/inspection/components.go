package inspection

import (
	"strconv"
	"strings"
)

// Components is a compact report table, derived from the same frozen checks
// used by the detailed chapters and raw execution records.
func Components(r *Report) Table {
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

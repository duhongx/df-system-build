package inspection

import (
	"fmt"
	"strings"
)

// Section groups evidence into report chapters without duplicating stored data.
type Section struct {
	ID, Name, Intro string
	Items           []Item
}

func Sections(r *Report) []Section {
	if r.Mode == "configured" {
		return configuredSections(r)
	}
	out := []Section{
		{ID: "architecture", Name: "部署情况", Intro: "按实际部署记录 Kubernetes、原生 PostgreSQL／Patroni 和 MinIO 的关键情况。"},
		{ID: "resources", Name: "主机资源与容量", Intro: "汇总各主机 CPU、内存及实际文件系统容量，省略重复容器挂载；完整命令输出保存在任务检查详情。"},
		{ID: "applications", Name: "Kubernetes 与应用检查", Intro: "记录指导书要求的节点、Pod、日志目录、hprof 文件和镜像列表命令输出。"},
		{ID: "backup", Name: "备份文件检查", Intro: "登录对应备份服务器，记录实际备份目录中的文件、时间和大小。"},
		{ID: "database", Name: "PostgreSQL 检查", Intro: "记录实际部署、复制类型、备份、归档及数据库关键结果；不适用与读取失败分别说明。"},
		{ID: "storage", Name: "MinIO 检查", Intro: "记录 mc admin info 的节点、磁盘、容量与对象信息；缺少逐日采样时不推算每日新增。"},
	}
	for _, item := range r.Items {
		i := 4
		switch item.Code {
		case "cloudhis-1", "cloudhis-2", "cloudhis-3":
			i = 0
		case "cloudhis-6", "cloudhis-15", "cloudhis-36":
			i = 1
		case "cloudhis-7", "cloudhis-8", "cloudhis-9", "cloudhis-10", "cloudhis-11", "cloudhis-12", "cloudhis-13", "cloudhis-14":
			i = 2
		case "cloudhis-4", "cloudhis-5":
			i = 3
		case "cloudhis-37", "cloudhis-38", "cloudhis-39":
			i = 5
		}
		out[i].Items = append(out[i].Items, item)
	}
	return out
}

func configuredSections(r *Report) []Section {
	groups := []Section{{ID: "resources", Name: "Linux 主机检查", Intro: "主机基础资源与实际文件系统；省略容器运行时重复挂载。"}, {ID: "database", Name: "PostgreSQL 检查", Intro: "实例角色、连接、复制、归档、备份及指导书数据库检查。"}, {ID: "applications", Name: "Kubernetes 与应用检查", Intro: "集群节点、Pod 当前状态和应用检查；历史重启单列。"}, {ID: "storage", Name: "MinIO 检查", Intro: "节点、磁盘及容量；备份与逐日记录按实际配置说明。"}, {ID: "elasticsearch", Name: "Elasticsearch 检查", Intro: "集群健康、分片与节点资源快照。"}, {ID: "redis", Name: "Redis 检查", Intro: "连接、内存、复制与持久化情况。"}, {ID: "rabbitmq", Name: "RabbitMQ 检查", Intro: "运行概况、节点及资源告警。"}}
	known := map[string]int{}
	for i := 1; i <= 39; i++ {
		idx := 2
		if i == 2 || i == 4 || (i >= 15 && i <= 35) {
			idx = 1
		}
		if i == 3 || i == 5 || i >= 36 {
			idx = 3
		}
		if i == 6 || i == 15 || i == 36 {
			idx = 0
		}
		known[fmt.Sprintf("cloudhis-%d", i)] = idx
	}
	summaries := make([]Table, len(groups))
	for i := range summaries {
		summaries[i] = Table{Title: "关键检查结果", Columns: []string{"检查项目", "对象", "结论与关键结果"}}
	}
	for _, item := range r.Items {
		idx, ok := known[item.Code]
		if !ok {
			idx = 0
			for i, kind := range []string{"elasticsearch", "redis", "rabbitmq"} {
				if strings.HasPrefix(item.Code, kind+"-") {
					idx = i + 4
				}
			}
			if strings.HasPrefix(item.Code, "postgresql-") || strings.HasPrefix(item.Code, "database_") || strings.HasPrefix(item.Code, "lock:") || strings.HasPrefix(item.Code, "slow:") || strings.HasPrefix(item.Code, "transaction:") {
				idx = 1
			}
			if strings.HasPrefix(item.Code, "kubernetes-") {
				idx = 2
			}
		}
		if item.Code == "cloudhis-6" || item.Code == "cloudhis-15" || item.Code == "cloudhis-36" {
			continue
		}
		if item.Status == "NORMAL" || item.Status == "RECORDED" || item.Status == "NOT_APPLICABLE" {
			for _, e := range item.Evidence {
				value := keyResult(e.Observation)
				if e.Status == "NOT_APPLICABLE" {
					value = "不适用：" + e.Reason
				}
				summaries[idx].Rows = append(summaries[idx].Rows, []string{item.Name, e.ObjectName, presentationText(value, 180)})
			}
		} else {
			groups[idx].Items = append(groups[idx].Items, item)
		}
	}
	var out []Section
	for i, g := range groups {
		if len(summaries[i].Rows) > 0 {
			g.Items = append([]Item{{Definition: Definition{Code: "summary-" + g.ID, Name: "检查结果"}, Status: "RECORDED", Evidence: []Evidence{{Observation: Observation{Tables: []Table{summaries[i]}}}}}}, g.Items...)
		}
		if len(g.Items) > 0 || (g.ID == "resources" && len(ResourceTables(r)) > 0) {
			out = append(out, g)
		}
	}
	return out
}

func keyResult(o Observation) string {
	if !strings.HasPrefix(o.Value, "执行并保存") && o.Value != "" {
		return o.Value
	}
	if len(o.Tables) == 0 {
		return first(o.Reason, "已取得命令结果，明细见任务检查详情")
	}
	t := o.Tables[0]
	if len(t.Rows) == 0 {
		return "本次查询未返回行"
	}
	var values []string
	for _, row := range t.Rows[:min(len(t.Rows), 4)] {
		if len(t.Columns) >= 2 && len(row) >= 2 && strings.HasPrefix(strings.ToLower(t.Columns[0]), "name") && strings.HasPrefix(strings.ToLower(t.Columns[1]), "setting") {
			values = append(values, row[0]+"="+presentationText(row[1], 65))
			continue
		}
		var parts []string
		for i, v := range row {
			if i >= len(t.Columns) {
				break
			}
			col := t.Columns[i]
			if strings.Contains(strings.ToLower(col), "sql") || strings.EqualFold(col, "query") {
				continue
			}
			if len(parts) >= 4 {
				break
			}
			parts = append(parts, col+"="+presentationText(v, 65))
		}
		values = append(values, strings.Join(parts, "，"))
	}
	return strings.Join(values, "；") + fmt.Sprintf("（共 %d 行，完整结果见详情）", len(t.Rows))
}

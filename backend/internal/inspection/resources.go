package inspection

import "strings"

// ResourceTables combines host snapshots instead of repeating a metric/value
// table for every component installed on the same host.
func ResourceTables(r *Report) []Table {
	base := Table{Title: "主机资源快照", Columns: []string{"主机", "CPU / 核数", "内存使用 / 可用", "负载 1 / 5 / 15 分钟", "Swap 使用 / 总量"}}
	disks := Table{Title: "实际文件系统容量", Columns: []string{"主机", "挂载点", "总量 GiB", "可用 GiB", "使用率", "inode 使用"}}
	for _, o := range r.Objects {
		for _, t := range o.Tables {
			if t.Title == "主机资源快照" {
				values := map[string]string{}
				for _, row := range t.Rows {
					if len(row) > 1 {
						values[row[0]] = row[1]
					}
				}
				base.Rows = append(base.Rows, []string{o.Name, values["CPU / 核数"], values["内存使用 / 可用"], values["负载 1 / 5 / 15 分钟"], values["Swap 使用 / 总量"]})
			}
			if t.Title == "文件系统容量与 inode" {
				for _, row := range t.Rows {
					if len(row) < 5 {
						continue
					}
					if strings.Contains(row[0], "/var/lib/kubelet/") || strings.Contains(row[0], "/var/lib/docker/") {
						continue
					}
					mount := row[0]
					if v := strings.SplitN(mount, " / ", 2); len(v) == 2 {
						mount = v[1]
					}
					disks.Rows = append(disks.Rows, []string{o.Name, mount, row[1], row[2], row[3], row[4]})
				}
			}
		}
	}
	disks.Note = "仅列实际业务挂载与本地文件系统，容器运行时目录挂载明细保存在任务检查详情。"
	var out []Table
	if len(base.Rows) > 0 {
		out = append(out, base)
	}
	if len(disks.Rows) > 0 {
		out = append(out, disks)
	}
	return out
}

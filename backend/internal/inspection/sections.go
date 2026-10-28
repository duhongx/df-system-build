package inspection

// Section groups evidence into report chapters without duplicating stored data.
type Section struct {
	ID, Name, Intro string
	Items           []Item
}

func Sections(r *Report) []Section {
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

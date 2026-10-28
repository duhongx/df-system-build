package inspection

// Section groups evidence into report chapters without duplicating stored data.
type Section struct {
	ID, Name, Intro string
	Items           []Item
}

func Sections(r *Report) []Section {
	out := []Section{
		{ID: "architecture", Name: "集群信息", Intro: "记录对应服务器 kubectl get nodes、patronictl list 和 mc admin info 的原始返回。"},
		{ID: "resources", Name: "主机资源与容量", Intro: "登录对应主机执行 hostname、top、free、df 等命令，记录本次资源与文件系统输出。"},
		{ID: "applications", Name: "Kubernetes 与应用检查", Intro: "记录指导书要求的节点、Pod、日志目录、hprof 文件和镜像列表命令输出。"},
		{ID: "backup", Name: "备份文件检查", Intro: "登录对应备份服务器，记录实际备份目录中的文件、时间和大小。"},
		{ID: "database", Name: "PostgreSQL 检查", Intro: "记录 Patroni、归档目录及指导书第 20～35 项原 SQL 的实际返回；执行错误单独列出。"},
		{ID: "storage", Name: "MinIO 检查", Intro: "记录 df -i、已有 mc 别名下的集群信息以及手册规定的本次计数命令输出。"},
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

package inspection

import "fmt"

type Definition struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Section       string `json:"section"`
	Scope         string `json:"scope"`
	Owner         string `json:"owner"`
	Advice        string `json:"advice"`
	MissingReason string `json:"missingReason"`
}

func Catalogue() []Definition {
	names := []string{"Kubernetes 部署架构", "PostgreSQL 部署架构", "MinIO 部署架构", "PostgreSQL 备份", "MinIO 备份", "Kubernetes 主机资源", "Kubernetes 节点状态", "Kubernetes 系统插件", "Nacos 与 Seata", "Java 与 Vue 应用", "Java 服务日志大小", "Java 内存溢出线索", "Nacos access 日志", "历史镜像", "PostgreSQL 主机资源", "PostgreSQL 集群运行", "PostgreSQL 时间线", "PostgreSQL 复制延迟", "PostgreSQL 归档目录", "PostgreSQL 安装信息", "PostgreSQL 文件参数", "PostgreSQL 重要参数", "PostgreSQL 归档失败", "PostgreSQL 复制槽", "PostgreSQL 主从 WAL 复制", "PostgreSQL 表空间", "PostgreSQL 连接数", "PostgreSQL 锁等待与锁表", "空闲连接 Top5", "长事务 Top5", "PostgreSQL 对象数", "PostgreSQL 数据库年龄", "当前慢 SQL Top5", "表膨胀 Top5", "索引膨胀 Top5", "MinIO 主机资源", "MinIO inode", "MinIO 集群运行", "MinIO 每日新增"}
	sections := []string{"部署架构", "备份", "Kubernetes 与应用", "PostgreSQL", "MinIO"}
	out := make([]Definition, 0, 39)
	for i, name := range names {
		n := i + 1
		d := Definition{Code: fmt.Sprintf("cloudhis-%d", n), Name: name, Scope: "database", Section: sections[3], Owner: "DBA", Advice: "补充该项的数据源、权限与预期配置，取得证据后核实。", MissingReason: "未取得该项检查证据，需核对连接配置、采集能力及权限。"}
		switch {
		case n <= 3:
			d.Section = sections[0]
			d.Scope = "environment"
			d.Owner = "信息科与平台运维"
			d.MissingReason = "尚未登记或采集真实集群拓扑，不能仅凭主机数量判断高可用。"
			d.Advice = "登记集群成员、角色、入口和故障域，并核对实际拓扑。"
		case n <= 5:
			d.Section = sections[1]
			d.Scope = "environment"
			d.Owner = "备份运维"
			d.MissingReason = "未选择备份服务器或未取得备份文件目录清单。"
			d.Advice = "在对应备份服务器上查看实际备份目录，记录文件时间和大小。"
		case n <= 14:
			d.Section = sections[2]
			d.Scope = "kubernetes"
			d.Owner = "应用与平台运维"
			d.MissingReason = "未取得集群、服务或目录的专项检查证据。"
		case n >= 36:
			d.Section = sections[4]
			d.Scope = "minio"
			d.Owner = "存储运维"
			d.MissingReason = "未选择对应的 MinIO 服务器或未取得命令输出。"
			d.Advice = "选择对应 MinIO 服务器并填写该主机已有的 mc 别名，执行指导书命令。"
		}
		if n == 6 {
			d.Scope = "kubernetes"
		}
		if n == 15 {
			d.Scope = "postgresql"
		}
		if n == 17 || n == 18 {
			d.Advice = "查看 patronictl list 的 TL、Role、State 和 Lag 原始输出。"
		}
		if n == 34 || n == 35 {
			d.Advice = "使用适配版本和访问方法的估算，记录算法及统计新鲜度；先核实再安排维护。"
		}
		d.MissingReason = "本项没有取得对应服务器的命令或 SQL 执行结果。"
		d.Advice = "按指导书查看原始输出；失败时根据执行错误核实对应服务器、工具、路径或权限。"
		out = append(out, d)
	}
	return out
}

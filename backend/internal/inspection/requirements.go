package inspection

// RequiredCodes describes the report scope before collection starts. Optional
// deployment checks may return NOT_APPLICABLE with a factual reason; they must
// never disappear simply because a collector did not return an observation.
func RequiredCodes(kind string) []string {
	switch kind {
	case "host":
		return []string{"host_identity", "host_cpu", "host_memory", "host_filesystems", "host-discovery"}
	case "database":
		out := []string{"cloudhis-2", "cloudhis-4", "cloudhis-15", "cloudhis-16", "cloudhis-17", "cloudhis-18", "cloudhis-19", "postgresql-archive"}
		for _, d := range Catalogue() {
			for _, code := range []string{"cloudhis-20", "cloudhis-21", "cloudhis-22", "cloudhis-23", "cloudhis-24", "cloudhis-25", "cloudhis-26", "cloudhis-27", "cloudhis-28", "cloudhis-29", "cloudhis-30", "cloudhis-31", "cloudhis-32", "cloudhis-33", "cloudhis-34", "cloudhis-35"} {
				if d.Code == code {
					out = append(out, code)
				}
			}
		}
		return out
	case "kubernetes":
		return []string{"cloudhis-1", "cloudhis-6", "cloudhis-7", "cloudhis-8", "cloudhis-9", "cloudhis-10", "cloudhis-11", "cloudhis-12", "cloudhis-13", "cloudhis-14"}
	case "minio":
		return []string{"cloudhis-3", "cloudhis-5", "cloudhis-36", "cloudhis-37", "cloudhis-38", "cloudhis-39"}
	case "redis":
		return []string{"redis-info", "redis-persistence"}
	case "elasticsearch":
		return []string{"elasticsearch-health", "elasticsearch-resources"}
	case "rabbitmq":
		return []string{"rabbitmq-overview", "rabbitmq-nodes"}
	}
	return nil
}

func CheckDefinition(code string) Definition {
	for _, d := range Catalogue() {
		if d.Code == code {
			return d
		}
	}
	names := map[string]string{"host_identity": "Linux 系统信息", "host_cpu": "CPU 与负载", "host_memory": "内存与 Swap", "host_filesystems": "实际文件系统容量与 inode", "host-discovery": "主机部署识别", "postgresql-archive": "PostgreSQL 归档配置与执行统计", "redis-info": "Redis 运行情况", "redis-persistence": "Redis 持久化与复制", "elasticsearch-health": "Elasticsearch 集群状态", "elasticsearch-resources": "Elasticsearch 节点资源", "rabbitmq-overview": "RabbitMQ 运行概况", "rabbitmq-nodes": "RabbitMQ 节点与告警"}
	name := names[code]
	if name == "" {
		name = code
	}
	return Definition{Code: code, Name: name, Owner: "运维人员", Advice: "核对本项实际数据源、访问权限和部署方式。"}
}

func EnsureResults(codes []string, observations []Observation, reason string) []Observation {
	seen := map[string]bool{}
	for i := range observations {
		seen[observations[i].Code] = true
		if observations[i].Name == "" {
			observations[i].Name = CheckDefinition(observations[i].Code).Name
		}
	}
	if reason == "" {
		reason = "此项应纳入本次检查，但采集器没有返回结果；请核对数据源与执行阶段。"
	}
	for _, code := range codes {
		if !seen[code] {
			d := CheckDefinition(code)
			observations = append(observations, Observation{Code: code, Name: d.Name, Status: "UNKNOWN", Value: "未取得本项检查结果", Reason: reason, Advice: d.Advice})
			seen[code] = true
		}
	}
	return observations
}

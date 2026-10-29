package opsprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

func discoveredService(args []string) string {
	if len(args) == 0 {
		return ""
	}
	exe := filepath.Base(args[0])
	switch exe {
	case "postgres", "postmaster":
		return "postgresql"
	case "minio":
		return "minio"
	case "redis-server":
		return "redis"
	case "kubelet":
		return "kubernetes"
	case "etcd":
		return "etcd"
	case "dockerd":
		return "docker"
	case "beam.smp":
		for _, a := range args {
			if a == "rabbit" {
				return "rabbitmq"
			}
		}
	}
	if strings.HasPrefix(exe, "java") {
		for _, a := range args {
			if a == "org.elasticsearch.bootstrap.Elasticsearch" {
				return "elasticsearch"
			}
		}
	}
	return ""
}

func discoverInspection(ctx context.Context, m ManualOptions, bindings []InspectionDatabaseBinding, execute ManualExecutor) (ManualOptions, []inspection.Observation) {
	found := map[string]bool{}
	var etcdChecks []inspection.Observation
	for _, b := range bindings {
		if b.Config != nil {
			found["postgresql"] = true
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return m, []inspection.Observation{{Code: "host-discovery", Name: "主机服务发现", Status: "UNKNOWN", Reason: err.Error(), CollectedAt: time.Now()}}
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
		kind := discoveredService(args)
		if kind == "etcd" {
			etcdChecks = append(etcdChecks, inspectEtcd(ctx, args, filepath.Join("/proc", e.Name(), "root")))
		}
		if kind != "" {
			found[kind] = true
		}
	}
	for kind, unit := range map[string]string{"docker": "docker.service", "kubernetes": "kubelet.service", "minio": "minio.service", "redis": "redis.service", "rabbitmq": "rabbitmq-server.service", "elasticsearch": "elasticsearch.service", "etcd": "etcd.service"} {
		if !found[kind] {
			e := execute(ctx, "systemctl show "+shellQuote(unit)+" --property=LoadState --value")
			if e.ExitCode == 0 && strings.TrimSpace(e.Stdout) == "loaded" {
				found[kind] = true
			}
		}
	}
	known := map[string]bool{}
	for _, v := range m.ConfiguredComponents {
		known[v] = true
	}
	var out []inspection.Observation
	out = append(out, inspectJavaLogs(ctx)...)
	out = append(out, etcdChecks...)
	m.Profiles = nil
	if len(bindings) > 0 && !known["postgresql"] {
		m.Profiles = append(m.Profiles, "postgresql", "postgres_backup")
	} else if found["postgresql"] && !known["postgresql"] {
		out = append(out, inspection.Observation{Code: "postgresql-source", Name: "PostgreSQL 连接配置", Status: "UNKNOWN", Value: "发现本机 PostgreSQL 进程", Reason: "尚无关联本机的 PostgreSQL 连接，无法核对复制、归档及数据库指标；请在数据库工作台登记连接并关联此主机。", CollectedAt: time.Now()})
		m.Profiles = append(m.Profiles, "postgres_backup")
	}
	if found["kubernetes"] {
		m.Profiles = append(m.Profiles, "k8s_host")
		if !known["kubernetes"] {
			m.Profiles = append(m.Profiles, "kubernetes")
		}
	}
	if found["minio"] && !known["minio"] {
		m.Profiles = append(m.Profiles, "minio")
	}
	processes, _ := localInspectionProcesses(ctx)
	for _, p := range processes {
		if p.Home != "" {
			m.Profiles = append(m.Profiles, "nacos")
			break
		}
	}
	if found["kubernetes"] {
		m.Profiles = append(m.Profiles, "node")
	}
	if found["docker"] {
		out = append(out, inspectDocker(ctx, execute))
	}
	for _, kind := range []string{"redis", "rabbitmq", "elasticsearch", "etcd"} {
		if !found[kind] {
			continue
		}
		if kind == "etcd" && len(etcdChecks) > 0 {
			continue
		}
		if !known[kind] {
			out = append(out, inspection.Observation{Code: kind + "-source", Name: kind + " 检查数据源", Status: "UNKNOWN", Value: "已发现本机组件", Reason: "尚未登记此组件的访问配置，无法读取组件内部状态；进程存在不能代替组件巡检结果。", CollectedAt: time.Now()})
		}
	}
	roles := []string{}
	for _, kind := range []string{"docker", "kubernetes", "etcd", "postgresql", "minio", "redis", "rabbitmq", "elasticsearch"} {
		if found[kind] {
			roles = append(roles, kind)
		}
	}
	value := "未发现支持识别的组件进程或服务；已执行 Linux 基础检查"
	if len(roles) > 0 {
		value = "本机发现：" + strings.Join(roles, "、")
	}
	out = append(out, inspection.Observation{Code: "host-discovery", Name: "主机部署情况", Status: "RECORDED", Value: value, Source: "/proc 实际入口、关联组件与 systemd 服务单元", CollectedAt: time.Now()})
	set := map[string]bool{}
	profiles := []string{}
	for _, p := range m.Profiles {
		if !set[p] {
			profiles = append(profiles, p)
			set[p] = true
		}
	}
	m.Profiles = profiles
	return m, out
}

func inspectDocker(ctx context.Context, execute ManualExecutor) inspection.Observation {
	e := execute(ctx, `docker ps -a --format '{{json .}}'`)
	o := inspection.Observation{Code: "docker-containers", Name: "Docker 容器运行", Status: "NORMAL", Executions: []inspection.Execution{e}, CollectedAt: time.Now(), Source: "docker ps -a"}
	if e.ExitCode != 0 || e.Error != "" {
		o.Status = "UNKNOWN"
		o.Reason = "读取 Docker 容器状态失败"
		return o
	}
	t := inspection.Table{Title: "需核对的容器", Columns: []string{"容器", "镜像", "状态"}}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(e.Stdout), "\n") {
		if line == "" {
			continue
		}
		var v struct{ Names, Image, State, Status string }
		if json.Unmarshal([]byte(line), &v) != nil {
			o.Status = "UNKNOWN"
			o.Reason = "部分容器返回无法识别"
			continue
		}
		total++
		if v.State != "running" || strings.Contains(v.Status, "unhealthy") {
			t.Rows = append(t.Rows, []string{v.Names, v.Image, v.Status})
			if o.Status != "UNKNOWN" {
				o.Status = "WARNING"
			}
		}
	}
	o.Value = fmt.Sprintf("共 %d 个容器，%d 个未运行或健康检查异常，正常容器不逐一列出", total, len(t.Rows))
	if len(t.Rows) > 0 {
		o.Tables = []inspection.Table{t}
	}
	return o
}

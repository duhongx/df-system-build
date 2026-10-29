package opsprobe

import (
	"context"
	"embed"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

//go:embed manual_sql/*.sql
var manualSQL embed.FS

type ManualOptions struct {
	Auto                    bool     `json:"auto,omitempty"`
	ConfiguredComponents    []string `json:"configuredComponents,omitempty"`
	Profiles                []string `json:"profiles"`
	Namespace               string   `json:"namespace"`
	LogDirectory            string   `json:"logDirectory"`
	NacosPod                string   `json:"nacosPod"`
	NacosNamespace          string   `json:"nacosNamespace"`
	NacosLogDirectory       string   `json:"nacosLogDirectory"`
	ArchiveDirectory        string   `json:"archiveDirectory"`
	PostgresBackupDirectory string   `json:"postgresBackupDirectory"`
	MinIOBackupDirectory    string   `json:"minioBackupDirectory"`
	MinIOAlias              string   `json:"minioAlias"`
	MinIODailyFile          string   `json:"minioDailyFile"`
	PatroniConfig           string   `json:"patroniConfig"`
}

var manualProfiles = map[string][]int{
	"nacos":              {13},
	"kubernetes":         {1, 7, 8, 9, 10, 13},
	"k8s_host":           {6},
	"deploy":             {14},
	"node":               {6, 11, 12, 14},
	"postgresql":         {2, 15, 16, 17, 18, 19},
	"postgresql_archive": {19},
	"minio":              {3, 36, 37, 38, 39},
	"postgres_backup":    {4},
	"minio_backup":       {5},
	"postgresql_sql":     {20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35},
}

func (m ManualOptions) Codes() []string {
	set := map[int]bool{}
	for _, p := range m.Profiles {
		for _, n := range manualProfiles[p] {
			set[n] = true
		}
	}
	var nums []int
	for n := range set {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]string, 0, len(nums))
	for _, n := range nums {
		out = append(out, fmt.Sprintf("cloudhis-%d", n))
	}
	return out
}

func (m ManualOptions) Validate(kind string) error {
	if (len(m.Profiles) == 0 && !m.Auto) || len(m.Profiles) > len(manualProfiles) {
		return fmt.Errorf("请选择对应服务器的巡检范围")
	}
	seen := map[string]bool{}
	for _, p := range m.Profiles {
		if _, ok := manualProfiles[p]; !ok || seen[p] {
			return fmt.Errorf("巡检范围不合法或重复")
		}
		seen[p] = true
		if (kind == "database") != (p == "postgresql_sql") {
			return fmt.Errorf("数据库执行 SQL，主机执行对应服务器命令，请分别选择")
		}
	}
	for name, value := range map[string]string{"日志目录": m.LogDirectory, "Nacos 日志目录": m.NacosLogDirectory, "归档目录": m.ArchiveDirectory, "PostgreSQL 备份目录": m.PostgresBackupDirectory, "MinIO 备份目录": m.MinIOBackupDirectory, "Patroni 配置路径": m.PatroniConfig, "MinIO 每日记录文件": m.MinIODailyFile} {
		if value != "" && (!path.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") || len(value) > 1024) {
			return fmt.Errorf("%s必须是服务器上的绝对路径", name)
		}
	}
	for name, value := range map[string]string{"命名空间": m.Namespace, "Nacos 命名空间": m.NacosNamespace, "Nacos Pod": m.NacosPod, "MinIO 别名": m.MinIOAlias} {
		if value != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`).MatchString(value) {
			return fmt.Errorf("%s不合法", name)
		}
	}
	return nil
}

type ManualCommand struct{ Text, Missing string }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Commands are fixed read-only templates. Only server-specific names and paths
// are substituted, using shell quoting; arbitrary command input is not accepted.
func (m ManualOptions) Commands(code string) []ManualCommand {
	n, _ := strconv.Atoi(strings.TrimPrefix(code, "cloudhis-"))
	ns := m.Namespace
	if ns == "" {
		ns = "prod"
	}
	logDir := m.LogDirectory
	if logDir == "" {
		logDir = "/opt/logs/prod"
	}
	patroni := "patronictl "
	if m.PatroniConfig != "" {
		patroni += "-c " + shellQuote(m.PatroniConfig) + " "
	}
	patroni += "list"
	alias := m.MinIOAlias
	if alias == "" {
		alias = "prod"
	}
	minio := []ManualCommand{{Text: "mc admin info " + shellQuote(alias)}}
	switch n {
	case 1, 7:
		return []ManualCommand{{Text: "kubectl get nodes"}}
	case 2, 16, 17, 18:
		return []ManualCommand{{Text: patroni}}
	case 3, 38:
		return minio
	case 4, 5:
		dir := m.PostgresBackupDirectory
		if n == 5 {
			dir = m.MinIOBackupDirectory
		}
		if dir == "" {
			return []ManualCommand{{Missing: "请填写对应备份服务器上的实际备份目录。"}}
		}
		return []ManualCommand{{Text: "ls -lh -- " + shellQuote(dir)}, {Text: "find " + shellQuote(dir) + " -type f -printf '%TY-%Tm-%Td %TH:%TM:%TS %s %p\\n'"}}
	case 6, 15, 36:
		return []ManualCommand{{Text: "hostname"}, {Text: "uname -a"}, {Text: "uptime"}, {Text: "free -m"}, {Text: "df -h"}, {Text: "df -i"}, {Text: "top -b -n 1"}}
	case 8:
		return []ManualCommand{{Text: "kubectl get pods -n kube-system"}}
	case 9:
		return []ManualCommand{{Text: "kubectl get pods"}}
	case 10:
		return []ManualCommand{{Text: "kubectl get pods -n " + shellQuote(ns)}}
	case 11:
		return []ManualCommand{{Text: "cd " + shellQuote(logDir) + " && du -h ./ --max-depth=1"}}
	case 12:
		return []ManualCommand{{Text: "cd " + shellQuote(logDir) + " && find ./ -name '*hprof'"}}
	case 13:
		if m.NacosPod == "" {
			return []ManualCommand{{Missing: "请填写 Nacos Pod 名称，在对应 deploy 主机执行容器日志目录检查。"}}
		}
		dir := m.NacosLogDirectory
		if dir == "" {
			dir = "logs"
		}
		namespace := m.NacosNamespace
		if namespace == "" {
			namespace = "default"
		}
		inner := "cd " + shellQuote(dir) + " && ls -al access*"
		return []ManualCommand{{Text: "kubectl exec -n " + shellQuote(namespace) + " " + shellQuote(m.NacosPod) + " -- sh -c " + shellQuote(inner)}}
	case 14:
		var out []ManualCommand
		for _, p := range m.Profiles {
			if p == "deploy" {
				out = append(out, ManualCommand{Text: "docker images"})
			}
			if p == "node" {
				out = append(out, ManualCommand{Text: "crictl images"})
			}
		}
		return out
	case 19:
		if m.ArchiveDirectory == "" {
			return []ManualCommand{{Missing: "请填写当前 Leader 主机上的实际归档目录。"}}
		}
		inner := "du -h -- " + shellQuote(m.ArchiveDirectory) + " && find " + shellQuote(m.ArchiveDirectory) + " -type f -printf '%T@\\t%s\\t%p\\n'"
		// The guide runs this check as postgres on the current Leader. Do not
		// silently inspect a replica or treat a permission failure as normal.
		asPostgres := "if [ \"$(id -un)\" = postgres ]; then sh -c " + shellQuote(inner) + "; elif [ \"$(id -u)\" = 0 ]; then su -s /bin/sh postgres -c " + shellQuote(inner) + "; else sudo -n -u postgres sh -c " + shellQuote(inner) + "; fi"
		return []ManualCommand{{Text: patroni}, {Text: "hostname && hostname -I"}, {Text: asPostgres}}
	case 37:
		return []ManualCommand{{Text: "df -i"}}
	case 39:
		out := []ManualCommand{{Text: "df -i | grep openeuler-root | awk '{print $3}'"}}
		if m.MinIODailyFile != "" {
			out = append(out, ManualCommand{Text: "cat -- " + shellQuote(m.MinIODailyFile)})
		}
		return out
	}
	return nil
}

type ManualExecutor func(context.Context, string) inspection.Execution

func CollectManual(ctx context.Context, m ManualOptions, c *DatabaseConfig, execute ManualExecutor) ([]inspection.Observation, error) {
	return collectManual(ctx, m, c, execute, nil)
}

func collectManual(ctx context.Context, m ManualOptions, c *DatabaseConfig, execute ManualExecutor, overrides map[string][]inspection.Observation) ([]inspection.Observation, error) {
	kind := "host"
	if c != nil {
		kind = "database"
	}
	if err := m.Validate(kind); err != nil {
		return nil, err
	}
	definitions := map[string]inspection.Definition{}
	for _, d := range inspection.Catalogue() {
		definitions[d.Code] = d
	}
	cache := map[string]inspection.Execution{}
	out := []inspection.Observation{}
	var sqlExecutor *manualSQLExecutor
	if c != nil {
		sqlExecutor = newManualSQLExecutor(ctx, *c)
		defer sqlExecutor.Close()
	}
	for _, code := range m.Codes() {
		if ctx.Err() != nil {
			break
		}
		if observations, ok := overrides[code]; ok {
			for _, observation := range observations {
				observation.Code, observation.Name = code, definitions[code].Name
				out = append(out, observation)
			}
			continue
		}
		o := inspection.Observation{Code: code, Name: definitions[code].Name, Status: "RECORDED", Source: "对应服务器实际执行 / HIS 运维巡检操作指导书 V2.0", CollectedAt: time.Now(), Executions: []inspection.Execution{}, Tables: []inspection.Table{}}
		if c != nil {
			e, table := sqlExecutor.Execute(ctx, code)
			o.Executions = append(o.Executions, e)
			if len(table.Columns) > 0 {
				o.Tables = append(o.Tables, table)
			}
		} else {
			for _, cmd := range m.Commands(code) {
				if code == "cloudhis-19" && len(o.Executions) == 2 {
					leader, known := archiveLeaderStatus(o.Executions)
					if !known {
						manualUnknown(&o, "无法确认本机与 Patroni Leader 的对应关系，未在未知主机上检查归档")
						break
					}
					if !leader {
						o.Status = "NOT_APPLICABLE"
						o.Reason = "本机不是运行中的当前 Leader；归档目录应由本次选中的 Leader 主机检查"
						break
					}
				}
				if cmd.Missing != "" {
					o.Status = "UNKNOWN"
					o.Gaps = append(o.Gaps, cmd.Missing)
					continue
				}
				e, ok := cache[cmd.Text]
				if !ok {
					e = execute(ctx, cmd.Text)
					e.Kind = "command"
					cache[cmd.Text] = e
				}
				o.Executions = append(o.Executions, e)
			}
		}
		for _, e := range o.Executions {
			if e.ExitCode != 0 || e.Error != "" || e.Truncated {
				o.Status = "UNKNOWN"
				reason := e.Error
				if reason == "" {
					reason = fmt.Sprintf("命令退出码 %d", e.ExitCode)
				}
				if e.Truncated {
					reason += "；输出超过保存上限，已截断"
				}
				o.Gaps = append(o.Gaps, reason)
			}
		}
		if len(o.Executions) == 0 {
			o.Status = "UNKNOWN"
		}
		o.Value = fmt.Sprintf("执行并保存 %d 条命令或 SQL 的输出", len(o.Executions))
		if c == nil && o.Status == "RECORDED" {
			interpretManual(&o)
		}
		out = append(out, o)
	}
	return out, nil
}

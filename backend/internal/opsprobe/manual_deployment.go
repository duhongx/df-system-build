package opsprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

type inspectedProcess struct {
	PID, Home, Management string
	Patroni               bool
}

// Only the actual Nacos entrypoint identifies a server. A Java application's
// Nacos client configuration or dependency jars must not identify it as Nacos.
func inspectProcess(pid string, args []string, cgroup string) inspectedProcess {
	p := inspectedProcess{PID: pid, Management: "宿主机进程"}
	if len(args) == 0 {
		return p
	}
	entry := filepath.Base(args[0])
	p.Patroni = entry == "patroni"
	if (entry == "python" || strings.HasPrefix(entry, "python3")) && len(args) > 1 {
		p.Patroni = filepath.Base(args[1]) == "patroni"
	}
	home := ""
	jar := ""
	for i, arg := range args {
		if strings.HasPrefix(arg, "-Dnacos.home=") {
			home = strings.TrimPrefix(arg, "-Dnacos.home=")
		}
		if arg == "-jar" && i+1 < len(args) {
			jar = args[i+1]
		}
	}
	if !strings.HasPrefix(entry, "java") {
		return p
	}
	serverEntry := strings.HasPrefix(filepath.Base(jar), "nacos-server") && strings.HasSuffix(jar, ".jar")
	for _, arg := range args {
		if arg == "com.alibaba.nacos.Nacos" {
			serverEntry = true
		}
	}
	if !serverEntry {
		return p
	}
	if home == "" && strings.HasPrefix(filepath.Base(jar), "nacos-server") && strings.HasSuffix(jar, ".jar") {
		home = filepath.Dir(filepath.Dir(jar))
	}
	if !filepath.IsAbs(home) || strings.ContainsAny(home, "\x00\r\n") {
		return p
	}
	p.Home = filepath.Clean(home)
	if strings.Contains(cgroup, "kubepods") {
		p.Management = "Kubernetes"
	} else if strings.Contains(cgroup, "docker") {
		p.Management = "Docker"
	} else {
		for _, line := range strings.Split(cgroup, "\n") {
			for _, part := range strings.Split(line, "/") {
				if strings.HasSuffix(part, ".service") && part != "crond.service" && part != "cron.service" {
					p.Management = "systemd / " + part
				}
			}
		}
	}
	return p
}

func localInspectionProcesses(ctx context.Context) ([]inspectedProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := []inspectedProcess{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		base := filepath.Join("/proc", entry.Name())
		data, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil {
			continue
		}
		groups, _ := os.ReadFile(filepath.Join(base, "cgroup"))
		p := inspectProcess(entry.Name(), strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"), string(groups))
		if p.Patroni || p.Home != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

func deploymentObservation(value, status, source string) inspection.Observation {
	return inspection.Observation{Status: status, Value: value, Source: source, CollectedAt: time.Now(), Tables: []inspection.Table{}, Executions: []inspection.Execution{}}
}

// Adaptive collection changes the checks to match the actual deployment. The
// original collector remains available for historical command-only fixtures.
func CollectDeploymentAwareManual(ctx context.Context, m ManualOptions, c *DatabaseConfig, bindings []InspectionDatabaseBinding, execute ManualExecutor) ([]inspection.Observation, error) {
	if c != nil {
		return CollectManual(ctx, m, c, execute)
	}
	if err := m.Validate("host"); err != nil {
		return nil, err
	}
	processes, processErr := localInspectionProcesses(ctx)
	overrides := map[string][]inspection.Observation{}
	patroni := m.PatroniConfig != ""
	for _, name := range []string{"/etc/patroni.yml", "/etc/patroni.yaml", "/etc/patroni/patroni.yml", "/etc/patroni/config.yml"} {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			patroni = true
		}
	}
	for _, p := range processes {
		patroni = patroni || p.Patroni
	}
	// Preserve Patroni checks when a configured service exists, including a
	// failed/stopped service. A missing CLI alone never proves standalone mode.
	if !patroni && wantsCode(m, "cloudhis-2", "cloudhis-16", "cloudhis-17", "cloudhis-18", "cloudhis-19") {
		probe := execute(ctx, "systemctl show patroni.service --property=LoadState --value")
		if strings.TrimSpace(probe.Stdout) == "loaded" {
			patroni = true
		}
	}
	if processErr == nil && !patroni && len(bindings) > 0 && wantsCode(m, "cloudhis-2", "cloudhis-16", "cloudhis-17", "cloudhis-18", "cloudhis-19") {
		for _, binding := range bindings {
			facts, record, err := inspectNativePostgreSQL(ctx, binding)
			observations := nativePostgreSQLObservations(binding.Name, facts, record, err)
			for code, observation := range observations {
				overrides[code] = append(overrides[code], observation)
			}
		}
	}
	if m.NacosPod == "" && wantsCode(m, "cloudhis-13") && processErr == nil {
		if observation, found := inspectLocalNacos(processes, m.NacosLogDirectory); found {
			overrides["cloudhis-13"] = []inspection.Observation{observation}
		}
	}
	if m.PostgresBackupDirectory == "" && wantsCode(m, "cloudhis-4") {
		overrides["cloudhis-4"] = []inspection.Observation{inspectBackupConfiguration(ctx, execute)}
	}
	if m.MinIODailyFile == "" && wantsCode(m, "cloudhis-39") {
		o := deploymentObservation("未接入逐日采样记录；不计算每日新增数量", "RECORDED", "本次巡检配置与本机定时任务")
		tasks, complete := inspectCronTasks("daily")
		if complete && len(tasks) == 0 {
			o.Value = "常规 cron 中未发现 MinIO 逐日采样任务；没有逐日数据，不计算每日新增"
		}
		if len(tasks) > 0 {
			o.Value = "发现逐日采样任务，但未接入其记录文件；不计算每日新增"
		}
		o.ConfigurationState = "NOT_CONFIGURED"
		o.Reason = "记录当前配置情况，不把缺少每日统计当作采集失败，也不以 inode 数量代替对象新增量"
		o.Tables = []inspection.Table{{Title: "逐日采样任务", Columns: []string{"配置位置", "调度时间"}, Rows: tasks}}
		overrides["cloudhis-39"] = []inspection.Observation{o}
	}
	return collectManual(ctx, m, nil, execute, overrides)
}

func wantsCode(m ManualOptions, codes ...string) bool {
	for _, code := range m.Codes() {
		for _, want := range codes {
			if code == want {
				return true
			}
		}
	}
	return false
}

type nativePostgreSQLFacts struct {
	Endpoint          string     `json:"endpoint"`
	Version           string     `json:"version"`
	Recovery          bool       `json:"recovery"`
	StatsVisible      bool       `json:"statsVisible"`
	PhysicalPeers     int        `json:"physicalPeers"`
	LogicalPeers      int        `json:"logicalPeers"`
	PhysicalSlots     int        `json:"physicalSlots"`
	ArchiveMode       string     `json:"archiveMode"`
	ArchiveConfigured bool       `json:"archiveConfigured"`
	Archived          int64      `json:"archived"`
	Failed            int64      `json:"failed"`
	LastArchived      *time.Time `json:"lastArchived"`
	LastFailed        *time.Time `json:"lastFailed"`
}

const nativePostgreSQLSQL = `SELECT current_setting('server_version'),pg_is_in_recovery(),
 (SELECT rolsuper FROM pg_roles WHERE rolname=current_user) OR pg_has_role(current_user,'pg_read_all_stats','USAGE'),
 (SELECT count(*) FROM pg_stat_replication r LEFT JOIN pg_replication_slots s ON s.active_pid=r.pid WHERE COALESCE(s.slot_type,'physical')='physical'),
 (SELECT count(*) FROM pg_stat_replication r JOIN pg_replication_slots s ON s.active_pid=r.pid WHERE s.slot_type='logical'),
 (SELECT count(*) FROM pg_replication_slots WHERE slot_type='physical'),
 current_setting('archive_mode'),length(trim(current_setting('archive_command')))>0,
 archived_count,failed_count,last_archived_time,last_failed_time FROM pg_stat_archiver`

func inspectNativePostgreSQL(parent context.Context, binding InspectionDatabaseBinding) (nativePostgreSQLFacts, inspection.Execution, error) {
	f := nativePostgreSQLFacts{}
	e := inspection.Execution{Kind: "sql", Command: nativePostgreSQLSQL, StartedAt: time.Now(), ExitCode: -1}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	var err error
	if binding.Error != "" {
		err = fmt.Errorf("%s", binding.Error)
	} else if binding.Config == nil {
		err = fmt.Errorf("数据库连接不可用")
	} else {
		c := *binding.Config
		f.Endpoint = net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) + "/" + c.Database
		conn, openErr := OpenDatabase(ctx, c)
		if openErr != nil {
			err = openErr
		} else {
			err = conn.QueryRow(ctx, nativePostgreSQLSQL).Scan(&f.Version, &f.Recovery, &f.StatsVisible, &f.PhysicalPeers, &f.LogicalPeers, &f.PhysicalSlots, &f.ArchiveMode, &f.ArchiveConfigured, &f.Archived, &f.Failed, &f.LastArchived, &f.LastFailed)
			conn.Close(context.WithoutCancel(ctx))
		}
		if err != nil {
			err = fmt.Errorf("%s", redactSecret(err.Error(), c.Password))
		}
	}
	e.FinishedAt = time.Now()
	if err != nil {
		e.Error = err.Error()
		e.Stderr = e.Error
		return f, e, err
	}
	e.ExitCode = 0
	data, _ := json.Marshal(f)
	e.Stdout = string(data)
	return f, e, nil
}

func nativePostgreSQLObservations(name string, f nativePostgreSQLFacts, e inspection.Execution, err error) map[string]inspection.Observation {
	out := map[string]inspection.Observation{}
	for _, code := range []string{"cloudhis-2", "cloudhis-16", "cloudhis-17", "cloudhis-18", "cloudhis-19"} {
		o := deploymentObservation(name+" / "+f.Endpoint, "RECORDED", "对应服务器数据库只读查询")
		o.Executions = []inspection.Execution{e}
		if err != nil {
			o.Status = "UNKNOWN"
			o.Reason = err.Error()
			o.Gaps = []string{err.Error()}
			out[code] = o
			continue
		}
		standalone := f.StatsVisible && !f.Recovery && f.PhysicalPeers == 0 && f.PhysicalSlots == 0
		role := "读写实例"
		if f.Recovery {
			role = "物理备库"
		}
		switch code {
		case "cloudhis-2":
			o.Value = fmt.Sprintf("原生 PostgreSQL %s，%s；物理复制连接 %d，逻辑订阅连接 %d", f.Version, role, f.PhysicalPeers, f.LogicalPeers)
			if standalone {
				o.Value += "；当前未发现物理复制连接或物理复制槽，逻辑订阅不作为高可用副本"
			}
		case "cloudhis-16":
			o.Status = "NORMAL"
			o.Value = "原生 PostgreSQL 连接成功，当前角色：" + role
		case "cloudhis-17":
			o.Status = "NOT_APPLICABLE"
			o.Value = "未使用 Patroni 管理"
			o.Reason = "Patroni 时间线对比不适用；不执行 patronictl"
		case "cloudhis-18":
			if standalone {
				o.Status = "NOT_APPLICABLE"
				o.Value = "当前没有可测量延迟的物理复制连接"
				o.Reason = "当前未发现 Patroni 管理或物理复制连接；逻辑订阅单独记录，不作为物理备库"
			} else {
				o.Value = fmt.Sprintf("原生复制：物理发送连接 %d，逻辑订阅连接 %d，物理槽 %d；延迟明细见数据库 WAL 复制检查", f.PhysicalPeers, f.LogicalPeers, f.PhysicalSlots)
				if !f.StatsVisible {
					o.Status = "UNKNOWN"
					o.Reason = "缺少完整复制统计权限"
					o.Gaps = []string{o.Reason}
				}
			}
		case "cloudhis-19":
			if f.ArchiveMode == "off" || !f.ArchiveConfigured {
				o.Status = "WARNING"
				o.ConfigurationState = "NOT_CONFIGURED"
				o.Value = "WAL 归档未开启或归档命令未配置"
				o.Reason = "记录实际配置情况，属于数据保护需关注项"
			} else {
				o.Value = fmt.Sprintf("WAL 归档已开启；成功 %d 次，累计失败 %d 次", f.Archived, f.Failed)
				if f.LastArchived != nil {
					o.Value += "；最近成功 " + f.LastArchived.Format(time.RFC3339)
				}
				if f.LastFailed != nil && (f.LastArchived == nil || f.LastFailed.After(*f.LastArchived)) {
					o.Status = "WARNING"
					o.Reason = "最近归档失败发生在最近成功之后，请核对归档链"
				}
			}
			o.Threshold = "记录归档配置与执行统计；不据此宣称归档目录保留策略已核验"
		}
		out[code] = o
	}
	return out
}

func inspectLocalNacos(processes []inspectedProcess, logOverride string) (inspection.Observation, bool) {
	o := deploymentObservation("", "RECORDED", "对应服务器 /proc、cgroup 与 Nacos 日志目录元数据")
	t := inspection.Table{Title: "Nacos 部署及 access 日志", Columns: []string{"进程", "运行方式", "实际日志目录", "access 文件数"}, Rows: [][]string{}}
	bad := []string{}
	for _, p := range processes {
		if p.Home == "" {
			continue
		}
		dir := filepath.Join(p.Home, "logs")
		if logOverride != "" {
			if filepath.IsAbs(logOverride) {
				dir = logOverride
			} else {
				dir = filepath.Join(p.Home, logOverride)
			}
		}
		actual := filepath.Join("/proc", p.PID, "root", dir)
		entries, err := os.ReadDir(actual)
		if err != nil {
			o.Gaps = append(o.Gaps, fmt.Sprintf("Nacos 进程 %s 日志读取失败：%v", p.PID, err))
			continue
		}
		count := 0
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "access") {
				count++
			}
		}
		t.Rows = append(t.Rows, []string{p.PID, p.Management, dir, strconv.Itoa(count)})
		if count > 7 {
			bad = append(bad, fmt.Sprintf("进程 %s access 文件 %d 个，超过 7 个", p.PID, count))
		}
	}
	if len(t.Rows) == 0 && len(o.Gaps) == 0 {
		return o, false
	}
	o.Tables = []inspection.Table{t}
	o.Value = fmt.Sprintf("自动定位 %d 个 Nacos 服务进程；按每个实例分别检查 access 日志", len(t.Rows)+len(o.Gaps))
	o.Threshold = "每个实例的 access 历史文件超过 7 个需关注；不累加不同实例的文件数"
	if len(bad) > 0 {
		o.Status = "WARNING"
		o.Reason = strings.Join(bad, "；")
	}
	if len(o.Gaps) > 0 {
		o.Status = "UNKNOWN"
		o.Reason = strings.Join(o.Gaps, "；")
	}
	return o, true
}

// Task commands may contain credentials. Keep only configuration filenames and
// schedules; never copy the full crontab command into a report.
func inspectCronTasks(kind string) ([][]string, bool) {
	files := []string{"/etc/crontab"}
	complete := true
	for _, folder := range []string{"/etc/cron.d", "/var/spool/cron"} {
		entries, err := os.ReadDir(folder)
		if err != nil {
			if !os.IsNotExist(err) {
				complete = false
			}
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				files = append(files, filepath.Join(folder, entry.Name()))
			}
		}
	}
	rows := [][]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			if !os.IsNotExist(err) {
				complete = false
			}
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			f := strings.Fields(line)
			if len(f) < 2 || strings.HasPrefix(line, "#") || strings.Contains(f[0], "=") {
				continue
			}
			low := strings.ToLower(line)
			match := (kind == "backup" && ((strings.Contains(low, "pgbackrest") && strings.Contains(low, "backup")) || strings.Contains(low, "pg_dump") || strings.Contains(low, "pg_basebackup"))) || (kind == "daily" && strings.Contains(low, "inode") && (strings.Contains(low, "minio") || strings.Contains(low, "inspection")))
			if !match {
				continue
			}
			n := 5
			if strings.HasPrefix(f[0], "@") {
				n = 1
			}
			if n > len(f) {
				n = len(f)
			}
			rows = append(rows, []string{file, strings.Join(f[:n], " ")})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return strings.Join(rows[i], " ") < strings.Join(rows[j], " ") })
	return rows, complete
}

func inspectBackupConfiguration(ctx context.Context, execute ManualExecutor) inspection.Observation {
	o := deploymentObservation("", "RECORDED", "本机备份工具实际返回与常规 cron 配置")
	tasks, complete := inspectCronTasks("backup")
	o.Tables = []inspection.Table{{Title: "本机定时备份任务", Columns: []string{"配置位置", "调度时间"}, Rows: tasks, Note: "仅表示本机常规 cron 中识别到的任务，不推断外部备份调度是否存在"}}
	binary, err := exec.LookPath("pgbackrest")
	if err != nil {
		o.Value = "本机未发现 pgBackRest 工具"
		if len(tasks) == 0 && complete {
			o.Status = "WARNING"
			o.ConfigurationState = "NOT_CONFIGURED"
			o.Reason = "常规 cron 中未发现 PostgreSQL 定时备份任务，未取得备份文件证据"
		}
		return o
	}
	command := "cd /tmp && " + shellQuote(binary) + " info --output=json"
	if os.Geteuid() == 0 {
		command = "su -s /bin/sh postgres -c " + shellQuote(command)
	}
	e := execute(ctx, command)
	o.Executions = []inspection.Execution{e}
	if e.ExitCode != 0 || e.Error != "" {
		o.Status = "UNKNOWN"
		o.Reason = "pgBackRest 信息读取失败"
		o.Gaps = []string{o.Reason}
		return o
	}
	var stanzas []struct {
		Name   string `json:"name"`
		Status struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
		Backup []struct {
			Label     string `json:"label"`
			Type      string `json:"type"`
			Timestamp struct {
				Stop int64 `json:"stop"`
			} `json:"timestamp"`
		} `json:"backup"`
	}
	if err := json.Unmarshal([]byte(e.Stdout), &stanzas); err != nil {
		o.Status = "UNKNOWN"
		o.Reason = "pgBackRest 返回无法解析"
		o.Gaps = []string{o.Reason}
		return o
	}
	t := inspection.Table{Title: "pgBackRest 已有备份", Columns: []string{"stanza", "最近备份", "类型", "完成时间", "工具状态"}, Rows: [][]string{}}
	count := 0
	for _, stanza := range stanzas {
		label, kind, date := "无备份", "-", "-"
		latest := int64(0)
		for _, backup := range stanza.Backup {
			count++
			if backup.Timestamp.Stop > latest {
				latest = backup.Timestamp.Stop
				label, kind, date = backup.Label, backup.Type, time.Unix(latest, 0).Format(time.RFC3339)
			}
		}
		t.Rows = append(t.Rows, []string{stanza.Name, label, kind, date, stanza.Status.Message})
		if stanza.Status.Code != 0 {
			o.Status = "WARNING"
		}
	}
	o.Tables = append(o.Tables, t)
	o.Value = fmt.Sprintf("pgBackRest 中已有 %d 份历史备份", count)
	if len(tasks) == 0 && complete {
		o.Status = "WARNING"
		o.Reason = "本机常规 cron 中未发现定时备份任务；已有历史备份不代表当前在持续备份"
	}
	if !complete {
		o.Status = "UNKNOWN"
		o.Reason = "部分任务配置无法读取，未确认完整的本机备份调度范围"
		o.Gaps = append(o.Gaps, o.Reason)
	}
	if count == 0 {
		o.Status = "WARNING"
		o.Reason = "备份工具未返回已有备份"
	}
	return o
}

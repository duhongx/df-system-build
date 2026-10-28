package opsprobe

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

type patroniMember struct{ Name, Host, Role, State, TL, Lag string }

func patroniMembers(text string) []patroniMember {
	var out []patroniMember
	for _, line := range strings.Split(text, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 8 {
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if cells[1] == "" || strings.EqualFold(cells[1], "Member") {
			continue
		}
		out = append(out, patroniMember{cells[1], cells[2], cells[3], cells[4], cells[5], cells[6]})
	}
	return out
}

func archiveLeaderStatus(records []inspection.Execution) (bool, bool) {
	if len(records) < 2 {
		return false, false
	}
	for _, e := range records[:2] {
		if e.ExitCode != 0 || e.Error != "" {
			return false, false
		}
	}
	identities := strings.Fields(records[1].Stdout)
	for _, member := range patroniMembers(records[0].Stdout) {
		for _, local := range identities {
			if member.Host == local || member.Name == local {
				if strings.EqualFold(member.Role, "Leader") {
					return strings.EqualFold(member.State, "running"), strings.EqualFold(member.State, "running")
				}
				return false, true
			}
		}
	}
	return false, false
}

func resultText(o *inspection.Observation) string {
	var all []string
	for _, e := range o.Executions {
		all = append(all, e.Stdout)
	}
	return strings.Join(all, "\n")
}

func manualUnknown(o *inspection.Observation, reason string) {
	o.Status = "UNKNOWN"
	o.Reason = reason
	o.Gaps = append(o.Gaps, reason)
}

func manualOutcome(o *inspection.Observation, value, threshold string, problems []string) {
	o.Value, o.Threshold, o.Status = value, threshold, "NORMAL"
	if len(problems) > 0 {
		o.Status, o.Reason = "WARNING", strings.Join(problems, "；")
	}
}

// Only conditions explicitly stated in the supplied guide are classified.
// SQL checks and resource snapshots remain recorded observations.
func interpretManual(o *inspection.Observation) {
	text := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(resultText(o), "")
	switch o.Code {
	case "cloudhis-1", "cloudhis-7":
		var rows [][]string
		var bad []string
		masters, workers := 0, 0
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) < 5 || f[0] == "NAME" {
				continue
			}
			rows = append(rows, f[:5])
			if strings.Contains(f[2], "master") || strings.Contains(f[2], "control-plane") {
				masters++
			} else {
				workers++
			}
			if strings.Split(f[1], ",")[0] != "Ready" {
				bad = append(bad, f[0]+" 状态 "+f[1])
			}
		}
		if len(rows) == 0 {
			manualUnknown(o, "未能从 kubectl 输出取得节点列表")
			return
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "节点列表", Columns: []string{"节点", "状态", "角色", "运行时间", "版本"}, Rows: rows})
		if o.Code == "cloudhis-7" {
			manualOutcome(o, fmt.Sprintf("%d 个节点，%d 个非 Ready", len(rows), len(bad)), "指导书：各节点应为 Ready", bad)
		} else {
			o.Value = fmt.Sprintf("%d 个节点，控制节点 %d，其他节点 %d", len(rows), masters, workers)
			o.Threshold = "按指导书的 1／3／6 台服务器口径记录部署形式"
			switch {
			case len(rows) == 1:
				o.Status = "WARNING"
				o.Reason = "单节点部署，存在指导书所述单机风险"
			case len(rows) == 3:
				o.Status = "WARNING"
				o.Reason = "三节点部署；按指导书应关注 master／node 共用的资源竞争，实际混布情况需结合节点角色核对"
			case len(rows) == 6 && masters == 3 && workers == 3:
				o.Value += "；三控制节点、三工作节点分离部署"
			default:
				o.Value += "；部署形式见节点角色列表"
			}
		}
	case "cloudhis-8", "cloudhis-9", "cloudhis-10":
		var rows [][]string
		var bad []string
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) < 5 || f[0] == "NAME" {
				continue
			}
			ready := strings.Split(f[1], "/")
			if len(ready) != 2 {
				continue
			}
			rows = append(rows, []string{f[0], f[1], f[2], f[3], strings.Join(f[4:], " ")})
			restarts, err := strconv.Atoi(f[3])
			if err != nil {
				manualUnknown(o, "无法解析 Pod 重启次数")
				return
			}
			if ready[0] != ready[1] || f[2] != "Running" || restarts > 0 {
				bad = append(bad, fmt.Sprintf("%s：就绪 %s，状态 %s，重启 %d", f[0], f[1], f[2], restarts))
			}
		}
		if len(rows) == 0 {
			manualUnknown(o, "该命名空间未取得 Pod 运行记录")
			return
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "Pod 运行情况", Columns: []string{"Pod", "就绪", "状态", "重启次数", "运行时间／补充"}, Rows: rows})
		manualOutcome(o, fmt.Sprintf("%d 个 Pod，%d 个需关注", len(rows), len(bad)), "指导书：关注非 Running、未就绪及有重启记录的 Pod", bad)
	case "cloudhis-2", "cloudhis-16", "cloudhis-17", "cloudhis-18":
		members := patroniMembers(text)
		if len(members) == 0 {
			manualUnknown(o, "未能从 patronictl 输出取得成员列表")
			return
		}
		var rows [][]string
		var bad []string
		o.Measurements = map[string]float64{}
		for _, m := range members {
			rows = append(rows, []string{m.Name, m.Host, m.Role, m.State, m.TL, m.Lag})
			if o.Code == "cloudhis-16" && !strings.EqualFold(m.State, "running") {
				bad = append(bad, m.Name+" 状态 "+m.State)
			}
			if o.Code == "cloudhis-18" && !strings.EqualFold(m.Role, "Leader") {
				lag, e := strconv.ParseFloat(m.Lag, 64)
				if e != nil || math.IsNaN(lag) || math.IsInf(lag, 0) || lag < 0 {
					manualUnknown(o, "成员 "+m.Name+" 未取得有效 Lag 数值")
					return
				}
				if lag > 0 {
					bad = append(bad, m.Name+" Lag="+m.Lag+" MB")
				}
			}
			if o.Code == "cloudhis-17" {
				tl, e := strconv.ParseFloat(m.TL, 64)
				if e != nil || math.IsNaN(tl) || math.IsInf(tl, 0) || tl < 0 {
					manualUnknown(o, "成员 "+m.Name+" 未取得有效 TL")
					return
				}
				o.Measurements[m.Host] = tl
			}
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "Patroni 成员", Columns: []string{"成员", "地址", "角色", "状态", "TL", "Lag MB"}, Rows: rows})
		switch o.Code {
		case "cloudhis-2":
			o.Value = fmt.Sprintf("Patroni 成员 %d 个；读写入口未通过成员数量推定", len(members))
			if len(members) == 1 {
				o.Status = "WARNING"
				o.Reason = "单 PostgreSQL 成员，存在指导书所述单机风险"
			}
		case "cloudhis-16":
			manualOutcome(o, fmt.Sprintf("%d 个成员，%d 个状态异常", len(members), len(bad)), "指导书：State 应全部为 running", bad)
		case "cloudhis-17":
			o.Value = fmt.Sprintf("已记录 %d 个成员的当前 TL", len(members))
			o.Threshold = "指导书：TL 增长需反馈"
			o.Advice = "与同一服务器以前保存的巡检结果比较；首次记录不推断增长。"
		case "cloudhis-18":
			manualOutcome(o, fmt.Sprintf("%d 个成员，%d 个存在复制延迟", len(members), len(bad)), "指导书：Lag 大于 0 需反馈", bad)
		}
	case "cloudhis-11":
		var rows [][]string
		var bad []string
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			dir := strings.Join(f[1:], " ")
			if dir == "./" || dir == "." {
				continue
			}
			size, ok := humanSize(f[0])
			if !ok {
				manualUnknown(o, "无法解析目录大小："+line)
				return
			}
			rows = append(rows, []string{dir, f[0]})
			if size > 20*math.Pow(1024, 3) {
				bad = append(bad, dir+" 日志 "+f[0]+"，超过 20G")
			}
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "服务日志大小", Columns: []string{"目录", "大小"}, Rows: rows})
		manualOutcome(o, fmt.Sprintf("%d 个服务目录，%d 个超过 20G", len(rows), len(bad)), "指导书：单个服务目录日志超过 20G 需反馈；不将父目录合计当作单个服务", bad)
	case "cloudhis-12":
		var paths []string
		for _, l := range strings.Split(text, "\n") {
			if strings.TrimSpace(l) != "" {
				paths = append(paths, l)
			}
		}
		manualOutcome(o, fmt.Sprintf("发现 %d 个 hprof 文件", len(paths)), "指导书：发现 hprof 文件需反馈并人工分析", paths)
		o.Advice = "报告保留文件路径；堆转储的下载和转交通过现有文件管理进行。"
	case "cloudhis-13":
		count := 0
		for _, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "-") {
				count++
			}
		}
		if count == 0 && strings.TrimSpace(text) != "" {
			manualUnknown(o, "未取得可识别的 access 文件列表")
			return
		}
		var bad []string
		if count > 7 {
			bad = append(bad, fmt.Sprintf("access 历史文件 %d 个，超过 7 个", count))
		}
		manualOutcome(o, fmt.Sprintf("access 历史文件 %d 个", count), "指导书：超过 7 个历史文件需人工清理", bad)
	case "cloudhis-14":
		var rows [][]string
		var bad []string
		total := 0
		for _, e := range o.Executions {
			if !strings.Contains(e.Stdout, "TAG") || (!strings.Contains(e.Stdout, "REPOSITORY") && !strings.Contains(e.Stdout, "IMAGE")) {
				manualUnknown(o, "未取得可识别的镜像列表表头；原始返回已保留")
				return
			}
			counts := map[string]int{}
			order := []string{}
			source := e.Command
			for _, l := range strings.Split(e.Stdout, "\n") {
				f := strings.Fields(l)
				if len(f) < 3 || !strings.Contains(l, "prod") {
					continue
				}
				if counts[f[0]] == 0 {
					order = append(order, f[0])
				}
				counts[f[0]]++
				total++
			}
			for _, repo := range order {
				rows = append(rows, []string{source, repo, strconv.Itoa(counts[repo])})
				if counts[repo] > 2 {
					bad = append(bad, fmt.Sprintf("%s：%s 有 %d 个 prod 镜像", source, repo, counts[repo]))
				}
			}
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "prod 镜像数量", Columns: []string{"命令", "镜像仓库／服务", "数量"}, Rows: rows})
		manualOutcome(o, fmt.Sprintf("prod 镜像共 %d 个，%d 处服务镜像超过建议保留数量", total, len(bad)), "指导书：每个微服务建议保留最新 2 个镜像；本次只统计并提示人工核对", bad)
	case "cloudhis-19":
		var rows [][]string
		var bad []string
		for _, l := range strings.Split(o.Executions[len(o.Executions)-1].Stdout, "\n") {
			f := strings.SplitN(l, "\t", 3)
			if len(f) != 3 {
				continue
			}
			epoch, e := strconv.ParseFloat(f[0], 64)
			if e != nil {
				continue
			}
			modified := time.Unix(int64(epoch), 0)
			rows = append(rows, []string{f[2], modified.Format(time.RFC3339), f[1]})
			if o.CollectedAt.Sub(modified) > 7*24*time.Hour {
				bad = append(bad, f[2]+" 修改时间超过 7 天")
			}
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "归档文件时间", Columns: []string{"路径", "修改时间", "字节"}, Rows: rows})
		manualOutcome(o, fmt.Sprintf("已核实本机为当前 Leader，归档文件 %d 个，超过 7 天 %d 个", len(rows), len(bad)), "指导书：在当前 Leader 上记录归档目录；保留最近 7 天。此处按文件修改时间核对", bad)
	case "cloudhis-37":
		var rows [][]string
		var bad []string
		for _, l := range strings.Split(text, "\n") {
			f := strings.Fields(l)
			if len(f) < 6 || !strings.HasSuffix(f[4], "%") {
				continue
			}
			percent, e := strconv.ParseFloat(strings.TrimSuffix(f[4], "%"), 64)
			if e != nil {
				continue
			}
			rows = append(rows, f[:6])
			if percent > 80 {
				bad = append(bad, f[5]+" inode 已用 "+f[4])
			}
		}
		if len(rows) == 0 {
			manualUnknown(o, "未取得可解析的 inode 使用率")
			return
		}
		o.Tables = append(o.Tables, inspection.Table{Title: "inode 使用情况", Columns: []string{"文件系统", "总量", "已用", "可用", "使用率", "挂载点"}, Rows: rows})
		manualOutcome(o, fmt.Sprintf("%d 个文件系统，%d 个 inode 超过 80%%", len(rows), len(bad)), "指导书：inode 已用超过 80% 需反馈扩容", bad)
	case "cloudhis-3", "cloudhis-38":
		nodes := 0
		var bad []string
		for _, l := range strings.Split(text, "\n") {
			s := strings.TrimSpace(l)
			if strings.HasPrefix(s, "●") || strings.HasPrefix(s, "○") {
				nodes++
				if strings.HasPrefix(s, "○") {
					bad = append(bad, s)
				}
			}
			if regexp.MustCompile(`(?i)^\s*(uptime|status)\s*:\s*offline`).MatchString(l) {
				bad = append(bad, s)
			}
			if strings.HasPrefix(s, "Drives:") || strings.HasPrefix(s, "Network:") {
				parts := regexp.MustCompile(`(\d+)/(\d+)`).FindStringSubmatch(s)
				if len(parts) == 3 && parts[1] != parts[2] {
					bad = append(bad, s)
				}
			}
		}
		matches := regexp.MustCompile(`(?i)(\d+) drives? online,\s*(\d+) drives? offline`).FindStringSubmatch(text)
		if len(matches) == 3 && matches[2] != "0" {
			bad = append(bad, matches[2]+" 个磁盘离线")
		}
		if nodes == 0 {
			manualUnknown(o, "未能从 mc admin info 取得节点信息")
			return
		}
		if o.Code == "cloudhis-3" {
			o.Value = fmt.Sprintf("MinIO 节点 %d 个", nodes)
			if nodes == 1 {
				o.Status = "WARNING"
				o.Reason = "单节点 MinIO，存在指导书所述单机存储风险"
			}
			if nodes > 1 {
				o.Value += "；多节点 MinIO 集群，节点信息见原始输出"
			}
			return
		}
		if len(matches) != 3 {
			manualUnknown(o, "未取得 MinIO 磁盘在线／离线汇总；原始输出已保留")
			return
		}
		manualOutcome(o, fmt.Sprintf("节点 %d 个，磁盘在线 %s、离线 %s", nodes, matches[1], matches[2]), "指导书：节点或磁盘离线需反馈", bad)
	case "cloudhis-39":
		count, e := strconv.ParseFloat(strings.TrimSpace(o.Executions[0].Stdout), 64)
		if e != nil || count < 0 || math.IsNaN(count) || math.IsInf(count, 0) {
			manualUnknown(o, "未取得有效 inode 已用数量")
			return
		}
		o.Measurements = map[string]float64{"inodeUsed": count}
		o.Value = fmt.Sprintf("本次 inode 已用 %.0f；每日增量由已有逐日记录计算", count)
		o.Advice = "inode 净变化仅作指导书口径的参考，不等于病历、文书、签名的精确业务新增数量。"
	}
}

func humanSize(s string) (float64, bool) {
	parts := regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([KMGTPE]?)(?:I?B)?$`).FindStringSubmatch(strings.ToUpper(s))
	if len(parts) != 3 {
		return 0, false
	}
	v, e := strconv.ParseFloat(parts[1], 64)
	if e != nil {
		return 0, false
	}
	power := strings.Index(" KMGTPE", parts[2])
	if parts[2] == "" {
		power = 0
	}
	return v * math.Pow(1024, float64(power)), true
}

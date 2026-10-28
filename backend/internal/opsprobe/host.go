package opsprobe

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type cpuCounter struct{ total, idle, wait uint64 }
type ioCounter struct{ reads, writes, readSectors, writeSectors, readMs, writeMs, busyMs uint64 }
type hostCounters struct {
	cpu      map[string]cpuCounter
	networks map[string]Network
	disks    map[string]ioCounter
	at       time.Time
}

func num(s string) uint64              { v, _ := strconv.ParseUint(s, 10, 64); return v }
func proc(name string) (string, error) { b, err := os.ReadFile("/proc/" + name); return string(b), err }
func cpuCounters(text string) map[string]cpuCounter {
	out := map[string]cpuCounter{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var c cpuCounter
		for i := 1; i < len(f) && i <= 8; i++ {
			c.total += num(f[i])
		}
		c.idle = num(f[4])
		if len(f) > 5 {
			c.wait = num(f[5])
		}
		out[f[0]] = c
	}
	return out
}
func networkCounters(text string) map[string]Network {
	out := map[string]Network{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		f := strings.Fields(line[i+1:])
		if len(f) < 16 {
			continue
		}
		n := strings.TrimSpace(line[:i])
		if n == "lo" {
			continue
		}
		out[n] = Network{Name: n, ReceiveBytes: num(f[0]), TransmitBytes: num(f[8]), Errors: num(f[2]) + num(f[10]), Drops: num(f[3]) + num(f[11])}
	}
	return out
}
func diskCounters(text string) map[string]ioCounter {
	out := map[string]ioCounter{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 14 {
			continue
		}
		n := f[2]
		if strings.HasPrefix(n, "loop") || strings.HasPrefix(n, "ram") {
			continue
		}
		out[n] = ioCounter{num(f[3]), num(f[7]), num(f[5]), num(f[9]), num(f[6]), num(f[10]), num(f[12])}
	}
	return out
}
func readHostCounters() (hostCounters, error) {
	c := hostCounters{at: time.Now()}
	s, err := proc("stat")
	if err != nil {
		return c, err
	}
	c.cpu = cpuCounters(s)
	if len(c.cpu) == 0 {
		return c, fmt.Errorf("CPU 计数缺失")
	}
	s, err = proc("net/dev")
	if err != nil {
		return c, err
	}
	c.networks = networkCounters(s)
	s, err = proc("diskstats")
	if err != nil {
		return c, err
	}
	c.disks = diskCounters(s)
	return c, nil
}
func delta(a, b uint64) float64 {
	if b < a {
		return 0
	}
	return float64(b - a)
}
func cpuPercent(a, b cpuCounter) (float64, float64) {
	total := delta(a.total, b.total)
	if total <= 0 {
		return 0, 0
	}
	return max(0, min(100, (total-delta(a.idle, b.idle)-delta(a.wait, b.wait))/total*100)), max(0, min(100, delta(a.wait, b.wait)/total*100))
}
func CollectHost(ctx context.Context) (*HostMetrics, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("主机采集仅支持 Linux，当前系统 %s", runtime.GOOS)
	}
	first, err := readHostCounters()
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	second, err := readHostCounters()
	if err != nil {
		return nil, err
	}
	seconds := second.at.Sub(first.at).Seconds()
	m := &HostMetrics{CollectedAt: second.at, CPUCores: len(second.cpu) - 1, Disks: []Disk{}, Networks: []Network{}, DiskIO: []DiskIO{}, CPUPerCore: []float64{}, Warnings: []string{}}
	m.Hostname, _ = os.Hostname()
	m.CPUUsagePercent, m.CPUWaitPercent = cpuPercent(first.cpu["cpu"], second.cpu["cpu"])
	var cores []string
	for key := range second.cpu {
		if key != "cpu" {
			cores = append(cores, key)
		}
	}
	sort.Slice(cores, func(i, j int) bool {
		return num(strings.TrimPrefix(cores[i], "cpu")) < num(strings.TrimPrefix(cores[j], "cpu"))
	})
	for _, key := range cores {
		p, _ := cpuPercent(first.cpu[key], second.cpu[key])
		m.CPUPerCore = append(m.CPUPerCore, p)
	}
	if text, e := proc("meminfo"); e == nil {
		values := map[string]uint64{}
		for _, line := range strings.Split(text, "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				values[strings.TrimSuffix(f[0], ":")] = num(f[1]) * 1024
			}
		}
		m.MemTotalBytes = values["MemTotal"]
		m.MemAvailableBytes = values["MemAvailable"]
		m.MemCachedBytes = values["Cached"] + values["Buffers"]
		if m.MemAvailableBytes == 0 {
			m.MemAvailableBytes = min(m.MemTotalBytes, values["MemFree"]+m.MemCachedBytes)
			m.Warnings = append(m.Warnings, "内核无 MemAvailable，使用空闲与缓存估算")
		}
		m.MemUsedBytes = m.MemTotalBytes - min(m.MemAvailableBytes, m.MemTotalBytes)
		if m.MemTotalBytes > 0 {
			m.MemUsagePercent = float64(m.MemUsedBytes) / float64(m.MemTotalBytes) * 100
		}
		m.SwapTotalBytes = values["SwapTotal"]
		m.SwapUsedBytes = m.SwapTotalBytes - min(values["SwapFree"], m.SwapTotalBytes)
	} else {
		m.Warnings = append(m.Warnings, "读取内存信息失败: "+e.Error())
	}
	if text, e := proc("loadavg"); e == nil {
		f := strings.Fields(text)
		if len(f) >= 3 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
			m.Load5, _ = strconv.ParseFloat(f[1], 64)
			m.Load15, _ = strconv.ParseFloat(f[2], 64)
		}
	}
	if text, e := proc("uptime"); e == nil {
		f := strings.Fields(text)
		if len(f) > 0 {
			m.UptimeSeconds, _ = strconv.ParseFloat(f[0], 64)
		}
	}
	if text, e := proc("sys/kernel/osrelease"); e == nil {
		m.Kernel = strings.TrimSpace(text)
	}
	if text, e := os.ReadFile("/etc/os-release"); e == nil {
		for _, line := range strings.Split(string(text), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				m.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
			}
		}
	}
	for name, b := range second.networks {
		a, ok := first.networks[name]
		if !ok {
			continue
		}
		b.ReceivePerSecond = delta(a.ReceiveBytes, b.ReceiveBytes) / seconds
		b.TransmitPerSecond = delta(a.TransmitBytes, b.TransmitBytes) / seconds
		m.Networks = append(m.Networks, b)
	}
	sort.Slice(m.Networks, func(i, j int) bool { return m.Networks[i].Name < m.Networks[j].Name })
	for name, b := range second.disks {
		a, ok := first.disks[name]
		if !ok {
			continue
		}
		ops := delta(a.reads, b.reads) + delta(a.writes, b.writes)
		await := 0.0
		if ops > 0 {
			await = (delta(a.readMs, b.readMs) + delta(a.writeMs, b.writeMs)) / ops
		}
		m.DiskIO = append(m.DiskIO, DiskIO{Device: name, ReadPerSecond: delta(a.readSectors, b.readSectors) * 512 / seconds, WritePerSecond: delta(a.writeSectors, b.writeSectors) * 512 / seconds, ReadIOPS: delta(a.reads, b.reads) / seconds, WriteIOPS: delta(a.writes, b.writes) / seconds, Utilization: min(100, delta(a.busyMs, b.busyMs)/(seconds*10)), AwaitMs: await})
	}
	sort.Slice(m.DiskIO, func(i, j int) bool { return m.DiskIO[i].Device < m.DiskIO[j].Device })
	mounts, e := proc("self/mounts")
	if e != nil {
		m.Warnings = append(m.Warnings, "读取挂载点失败: "+e.Error())
	} else {
		seen := map[string]bool{}
		sc := bufio.NewScanner(strings.NewReader(mounts))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 3 {
				continue
			}
			device, mp, fs := unescapeMount(f[0]), unescapeMount(f[1]), f[2]
			if seen[mp] || !strings.HasPrefix(device, "/") || fs == "overlay" || fs == "squashfs" || fs == "tmpfs" {
				continue
			}
			seen[mp] = true
			var st syscall.Statfs_t
			if err := syscall.Statfs(mp, &st); err != nil {
				m.Warnings = append(m.Warnings, "读取磁盘 "+mp+" 失败: "+err.Error())
				continue
			}
			total, avail := st.Blocks*uint64(st.Bsize), st.Bavail*uint64(st.Bsize)
			if total == 0 {
				continue
			}
			d := Disk{Device: device, Mountpoint: mp, FSType: fs, TotalBytes: total, AvailableBytes: avail, UsedPercent: float64(total-min(avail, total)) / float64(total) * 100, Inodes: st.Files}
			if st.Files > 0 {
				d.InodeUsedPercent = float64(st.Files-min(st.Ffree, st.Files)) / float64(st.Files) * 100
			}
			m.Disks = append(m.Disks, d)
		}
	}
	sort.Slice(m.Disks, func(i, j int) bool { return m.Disks[i].Mountpoint < m.Disks[j].Mountpoint })
	return m, nil
}
func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\134`, `\`).Replace(s)
}
func HostChecks(m *HostMetrics) []Check {
	checks := []Check{{Code: "host_identity", Name: "系统信息", Status: "NORMAL", Value: m.OS + " / " + m.Kernel, Evidence: m.Hostname}, {Code: "host_cpu", Name: "CPU 本次采样", Status: level(m.CPUUsagePercent, 80, 95), Value: fmt.Sprintf("%.1f%%，I/O 等待 %.1f%%", m.CPUUsagePercent, m.CPUWaitPercent), Evidence: "两次 /proc/stat 计数差值；采样间隔约 1 秒", Advice: "持续偏高时结合进程和历史趋势排查"}, {Code: "host_memory", Name: "可用内存", Status: level(m.MemUsagePercent, 85, 95), Value: fmt.Sprintf("使用 %.1f%%，可用 %.2f GiB", m.MemUsagePercent, float64(m.MemAvailableBytes)/(1<<30)), Advice: "核对进程内存与 Swap 使用"}}
	if m.Source == "node_exporter" {
		checks[1].Evidence = "两次 node_exporter HTTP 计数差值；采样间隔约 1 秒"
	}
	for _, d := range m.Disks {
		checks = append(checks, Check{Code: "disk:" + d.Mountpoint, Name: "磁盘 " + d.Mountpoint, Status: level(d.UsedPercent, 80, 90), Value: fmt.Sprintf("使用 %.1f%%，可用 %.2f GiB", d.UsedPercent, float64(d.AvailableBytes)/(1<<30)), Evidence: d.Device, Advice: "核对增长来源并按运维流程清理或扩容"})
		if d.Inodes > 0 {
			checks = append(checks, Check{Code: "inode:" + d.Mountpoint, Name: "inode " + d.Mountpoint, Status: level(d.InodeUsedPercent, 80, 90), Value: fmt.Sprintf("使用 %.1f%%", d.InodeUsedPercent)})
		}
	}
	if len(m.Disks) == 0 {
		checks = append(checks, Check{Code: "host_disks", Name: "磁盘信息", Status: "UNKNOWN", Error: "未取得可用的磁盘挂载点"})
	}
	for _, w := range m.Warnings {
		checks = append(checks, Check{Code: "collection_warning", Name: "采集完整性", Status: "UNKNOWN", Error: w})
	}
	return checks
}
func level(v, w, c float64) string {
	if v >= c {
		return "CRITICAL"
	}
	if v >= w {
		return "WARNING"
	}
	return "NORMAL"
}
func ServiceCheck(ctx context.Context, name string) Check {
	out, err := exec.CommandContext(ctx, "systemctl", "is-active", "--", name).CombinedOutput()
	c := Check{Code: "service:" + name, Name: "服务 " + name, Status: "NORMAL", Value: strings.TrimSpace(string(out))}
	if err != nil {
		c.Status = "WARNING"
		c.Error = err.Error()
		c.Advice = "核对该主机是否要求此服务运行"
	}
	return c
}

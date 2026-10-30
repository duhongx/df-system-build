// Package nodeexporter collects host monitoring exclusively over HTTP.
package nodeexporter

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/opsprobe"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

type snapshot struct {
	at       time.Time
	families map[string]*dto.MetricFamily
}

func label(m *dto.Metric, key string) string {
	for _, p := range m.Label {
		if p.GetName() == key {
			return p.GetValue()
		}
	}
	return ""
}
func value(m *dto.Metric) float64 {
	if m.Counter != nil {
		return m.Counter.GetValue()
	}
	if m.Gauge != nil {
		return m.Gauge.GetValue()
	}
	return m.Untyped.GetValue()
}
func finite(v float64) bool                      { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (s snapshot) all(name string) []*dto.Metric { return s.families[name].GetMetric() }
func (s snapshot) scalar(name string) (float64, bool) {
	list := s.all(name)
	if len(list) == 0 {
		return 0, false
	}
	v := value(list[0])
	return v, finite(v)
}
func (s snapshot) by(name, key string) map[string]float64 {
	out := map[string]float64{}
	for _, m := range s.all(name) {
		v := value(m)
		if finite(v) {
			out[label(m, key)] = v
		}
	}
	return out
}
func (s snapshot) n(name string) float64 { v, _ := s.scalar(name); return v }
func read(ctx context.Context, client *http.Client, url string) (snapshot, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if e != nil {
		return snapshot{}, e
	}
	req.Header.Set("Accept", "text/plain; version=0.0.4")
	resp, e := client.Do(req)
	if e != nil {
		return snapshot{}, fmt.Errorf("node_exporter HTTP 请求失败: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot{}, fmt.Errorf("node_exporter 返回 HTTP %d", resp.StatusCode)
	}
	const limit = 16 << 20
	data, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil {
		return snapshot{}, e
	}
	if len(data) > limit {
		return snapshot{}, fmt.Errorf("node_exporter 响应超过 16 MiB 限制")
	}
	parser := expfmt.NewTextParser(prommodel.UTF8Validation)
	families, e := parser.TextToMetricFamilies(strings.NewReader(string(data)))
	if e != nil {
		return snapshot{}, fmt.Errorf("node_exporter 指标格式错误: %w", e)
	}
	return snapshot{time.Now(), families}, nil
}
func Collect(ctx context.Context, url string) (*opsprobe.HostMetrics, error) {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("指标地址发生重定向，请填写最终 HTTP 地址")
	}}
	a, e := read(ctx, client, url)
	if e != nil {
		return nil, e
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	b, e := read(ctx, client, url)
	if e != nil {
		return nil, e
	}
	return calculate(a, b, url)
}
func difference(a, b float64) (float64, bool) {
	if !finite(a) || !finite(b) || a < 0 || b < a {
		return 0, false
	}
	return b - a, true
}
func calculate(a, b snapshot, url string) (*opsprobe.HostMetrics, error) {
	secs := b.at.Sub(a.at).Seconds()
	if secs <= 0 {
		return nil, fmt.Errorf("采样间隔无效")
	}
	m := &opsprobe.HostMetrics{Source: "node_exporter", SourceURL: url, CollectorVersion: "http-1.1", SampleSeconds: secs, CollectedAt: b.at, Disks: []opsprobe.Disk{}, Networks: []opsprobe.Network{}, DiskIO: []opsprobe.DiskIO{}, CPUPerCore: []float64{}, Warnings: []string{}}
	m.MemTotalBytes = uint64(max(0, b.n("node_memory_MemTotal_bytes")))
	m.MemAvailableBytes = uint64(max(0, b.n("node_memory_MemAvailable_bytes")))
	if m.MemTotalBytes == 0 {
		return nil, fmt.Errorf("node_exporter 未提供有效内存总量，请检查 meminfo collector")
	}
	m.MemCachedBytes = uint64(max(0, b.n("node_memory_Cached_bytes")+b.n("node_memory_Buffers_bytes")))
	if _, ok := b.scalar("node_memory_MemAvailable_bytes"); !ok {
		m.MemAvailableBytes = min(m.MemTotalBytes, uint64(max(0, b.n("node_memory_MemFree_bytes")))+m.MemCachedBytes)
		m.Warnings = append(m.Warnings, "exporter 未提供 MemAvailable，使用空闲内存与缓存估算")
	}
	m.MemUsedBytes = m.MemTotalBytes - min(m.MemTotalBytes, m.MemAvailableBytes)
	m.MemUsagePercent = float64(m.MemUsedBytes) / float64(m.MemTotalBytes) * 100
	m.SwapTotalBytes = uint64(max(0, b.n("node_memory_SwapTotal_bytes")))
	m.SwapUsedBytes = m.SwapTotalBytes - min(m.SwapTotalBytes, uint64(max(0, b.n("node_memory_SwapFree_bytes"))))
	m.Load1 = b.n("node_load1")
	m.Load5 = b.n("node_load5")
	m.Load15 = b.n("node_load15")
	if v, ok := b.scalar("node_boot_time_seconds"); ok {
		now, ok := b.scalar("node_time_seconds")
		if !ok {
			now = float64(b.at.Unix())
		}
		m.UptimeSeconds = max(0, now-v)
	}
	for _, i := range b.all("node_uname_info") {
		m.Hostname = label(i, "nodename")
		m.Kernel = label(i, "release")
		m.OS = label(i, "sysname")
	}
	for _, i := range b.all("node_os_info") {
		m.OS = label(i, "pretty_name")
	}
	for _, i := range b.all("node_exporter_build_info") {
		m.ExporterVersion = label(i, "version")
	}
	if v, ok := b.scalar("node_procs_running"); ok && v >= 0 {
		n := uint64(v)
		m.RunnableTasks = &n
	}
	if v, ok := b.scalar("node_procs_blocked"); ok && v >= 0 {
		n := uint64(v)
		m.BlockedTasks = &n
	}
	cpuA, cpuB := cpuModes(a), cpuModes(b)
	var cores []string
	for core := range cpuB {
		cores = append(cores, core)
	}
	sort.Slice(cores, func(i, j int) bool {
		left, le := strconv.Atoi(cores[i])
		right, re := strconv.Atoi(cores[j])
		if le == nil && re == nil {
			return left < right
		}
		return cores[i] < cores[j]
	})
	if len(cores) == 0 {
		return nil, fmt.Errorf("node_exporter 未提供 CPU 计数，请检查 cpu collector")
	}
	m.CPUCores = len(cores)
	m.CPUCoreIDs = cores
	var total, idle, wait, steal, user, system float64
	for _, core := range cores {
		current, previous := cpuB[core], cpuA[core]
		dt, di, dw, ds, du, dk := 0.0, 0.0, 0.0, 0.0, 0.0, 0.0
		if len(previous) == 0 {
			return nil, fmt.Errorf("CPU 核数在采样期间变化，请重试")
		}
		for _, mode := range []string{"idle", "iowait", "steal", "user", "nice", "system", "irq", "softirq"} {
			v, exists := current[mode]
			old, present := previous[mode]
			if !exists || !present {
				continue
			}
			dv, ok := difference(old, v)
			if !ok {
				return nil, fmt.Errorf("CPU 计数已重置，请重试采样")
			}
			dt += dv
			switch mode {
			case "idle":
				di += dv
			case "iowait":
				dw += dv
			case "steal":
				ds += dv
			case "user", "nice":
				du += dv
			default:
				dk += dv
			}
		}
		if dt <= 0 {
			return nil, fmt.Errorf("CPU 计数未更新，请检查指标缓存或 exporter 状态")
		}
		m.CPUPerCore = append(m.CPUPerCore, max(0, (dt-di-dw-ds)/dt*100))
		total += dt
		idle += di
		wait += dw
		steal += ds
		user += du
		system += dk
	}
	m.CPUUsagePercent = max(0, (total-idle-wait-steal)/total*100)
	m.CPUWaitPercent = wait / total * 100
	m.CPUStealPercent = steal / total * 100
	m.CPUUserPercent = user / total * 100
	m.CPUSystemPercent = system / total * 100
	// Scheduler waiting time per completed timeslice is a latency, not a CPU utilization percentage.
	waitingA, waitingB := a.by("node_schedstat_waiting_seconds_total", "cpu"), b.by("node_schedstat_waiting_seconds_total", "cpu")
	slicesA, slicesB := a.by("node_schedstat_timeslices_total", "cpu"), b.by("node_schedstat_timeslices_total", "cpu")
	if len(waitingA) > 0 && len(waitingB) > 0 {
		waitSum, sliceSum := 0.0, 0.0
		valid := true
		for core, cur := range waitingB {
			old, has := waitingA[core]
			sa, sok := slicesA[core]
			sb, bok := slicesB[core]
			dw, wok := difference(old, cur)
			ds, ok := difference(sa, sb)
			if !has || !sok || !bok || !wok || !ok {
				valid = false
				break
			}
			waitSum += dw
			sliceSum += ds
		}
		if valid && sliceSum > 0 {
			v := waitSum / sliceSum * 1000
			m.CPUSchedulingWaitMs = &v
		}
	}
	// Two HTTP scrapes only cover the sample interval, not the kernel 10/60/300 second PSI windows.
	if old, ok := a.scalar("node_pressure_cpu_waiting_seconds_total"); ok {
		if cur, ok := b.scalar("node_pressure_cpu_waiting_seconds_total"); ok {
			if dv, valid := difference(old, cur); valid {
				v := min(100, dv/secs*100)
				m.CPUIntervalPressurePercent = &v
			}
		}
	}
	if m.CPUIntervalPressurePercent == nil {
		m.CPUPressureError = "node_exporter 未提供 CPU PSI 等待计数，该项不可用"
	}
	collectDisks(a, b, m, secs)
	collectNetworks(a, b, m, secs)
	collectFilesystems(b, m)
	enrichFromExporter(b, m)
	return m, nil
}
func cpuModes(s snapshot) map[string]map[string]float64 {
	out := map[string]map[string]float64{}
	for _, m := range s.all("node_cpu_seconds_total") {
		core := label(m, "cpu")
		if out[core] == nil {
			out[core] = map[string]float64{}
		}
		out[core][label(m, "mode")] = value(m)
	}
	return out
}
func collectDisks(a, b snapshot, m *opsprobe.HostMetrics, secs float64) {
	names := b.by("node_disk_reads_completed_total", "device")
	as := map[string]map[string]float64{}
	bs := map[string]map[string]float64{}
	metrics := []string{"reads_completed_total", "writes_completed_total", "read_bytes_total", "written_bytes_total", "read_time_seconds_total", "write_time_seconds_total", "io_time_seconds_total", "io_time_weighted_seconds_total", "io_now"}
	for _, n := range metrics {
		as[n] = a.by("node_disk_"+n, "device")
		bs[n] = b.by("node_disk_"+n, "device")
	}
	for name := range names {
		d := opsprobe.DiskIO{Device: name, Kind: "disk", BackingDevices: []string{}, Mountpoints: []string{}}
		if strings.HasPrefix(name, "dm-") {
			d.Kind = "mapper"
		}
		if strings.HasPrefix(name, "sr") {
			d.Kind = "optical"
		}
		if strings.HasPrefix(name, "md") {
			d.Kind = "raid"
		}
		get := func(n string) (float64, bool) {
			prev, ok := as[n][name]
			cur, has := bs[n][name]
			if !ok || !has {
				return 0, false
			}
			return difference(prev, cur)
		}
		reads, rok := get("reads_completed_total")
		writes, wok := get("writes_completed_total")
		rb, _ := get("read_bytes_total")
		wb, _ := get("written_bytes_total")
		rt, rtok := get("read_time_seconds_total")
		wt, wtok := get("write_time_seconds_total")
		busy, _ := get("io_time_seconds_total")
		queue, _ := get("io_time_weighted_seconds_total")
		if rok && wok {
			d.ReadIOPS = reads / secs
			d.WriteIOPS = writes / secs
			d.ReadPerSecond = rb / secs
			d.WritePerSecond = wb / secs
			d.Utilization = min(100, busy/secs*100)
			d.AverageQueue = queue / secs
			d.InFlight = uint64(max(0, bs["io_now"][name]))
			if reads > 0 && rtok {
				v := rt / reads * 1000
				d.ReadAwaitMs = &v
			}
			if writes > 0 && wtok {
				v := wt / writes * 1000
				d.WriteAwaitMs = &v
			}
			if reads+writes > 0 && rtok && wtok {
				d.AwaitMs = (rt + wt) / (reads + writes) * 1000
			}
		}
		m.DiskIO = append(m.DiskIO, d)
	}
	sort.Slice(m.DiskIO, func(i, j int) bool { return m.DiskIO[i].Device < m.DiskIO[j].Device })
}
func collectNetworks(a, b snapshot, m *opsprobe.HostMetrics, secs float64) {
	ar, br := a.by("node_network_receive_bytes_total", "device"), b.by("node_network_receive_bytes_total", "device")
	at, bt := a.by("node_network_transmit_bytes_total", "device"), b.by("node_network_transmit_bytes_total", "device")
	re, te := b.by("node_network_receive_errs_total", "device"), b.by("node_network_transmit_errs_total", "device")
	rd, td := b.by("node_network_receive_drop_total", "device"), b.by("node_network_transmit_drop_total", "device")
	for name, v := range br {
		if name == "lo" {
			continue
		}
		old, ok := ar[name]
		oldTx, okTx := at[name]
		if !ok || !okTx {
			continue
		}
		r, valid := difference(old, v)
		w, validW := difference(oldTx, bt[name])
		if !valid || !validW {
			continue
		}
		m.Networks = append(m.Networks, opsprobe.Network{Name: name, ReceiveBytes: uint64(max(0, v)), TransmitBytes: uint64(max(0, bt[name])), ReceivePerSecond: r / secs, TransmitPerSecond: w / secs, Errors: uint64(max(0, re[name]+te[name])), Drops: uint64(max(0, rd[name]+td[name]))})
	}
	sort.Slice(m.Networks, func(i, j int) bool { return m.Networks[i].Name < m.Networks[j].Name })
}
func collectFilesystems(s snapshot, m *opsprobe.HostMetrics) {
	key := func(row *dto.Metric) string {
		return label(row, "device") + "\x00" + label(row, "mountpoint") + "\x00" + label(row, "fstype")
	}
	maps := map[string]map[string]float64{}
	for _, name := range []string{"avail_bytes", "files", "files_free", "device_error"} {
		maps[name] = map[string]float64{}
		for _, row := range s.all("node_filesystem_" + name) {
			maps[name][key(row)] = value(row)
		}
	}
	for _, row := range s.all("node_filesystem_size_bytes") {
		device, mp, fs := label(row, "device"), label(row, "mountpoint"), label(row, "fstype")
		if !strings.HasPrefix(device, "/") || fs == "overlay" || fs == "squashfs" || fs == "tmpfs" {
			continue
		}
		k := key(row)
		total := value(row)
		if total <= 0 || !finite(total) {
			continue
		}
		if maps["device_error"][k] != 0 {
			m.Warnings = append(m.Warnings, "exporter 无法读取文件系统 "+mp)
			continue
		}
		available, hasAvail := maps["avail_bytes"][k]
		if !hasAvail {
			m.Warnings = append(m.Warnings, "文件系统缺少可用容量指标: "+mp)
			continue
		}
		d := opsprobe.Disk{Device: device, Mountpoint: mp, FSType: fs, TotalBytes: uint64(total), AvailableBytes: uint64(max(0, available)), UsedPercent: max(0, min(100, (total-available)/total*100)), Inodes: uint64(max(0, maps["files"][k]))}
		if d.Inodes > 0 {
			d.InodeUsedPercent = max(0, min(100, (float64(d.Inodes)-maps["files_free"][k])/float64(d.Inodes)*100))
		}
		m.Disks = append(m.Disks, d)
	}
	sort.Slice(m.Disks, func(i, j int) bool { return m.Disks[i].Mountpoint < m.Disks[j].Mountpoint })
}
func enrichFromExporter(s snapshot, m *opsprobe.HostMetrics) {
	byName := map[string]*opsprobe.DiskIO{}
	for i := range m.DiskIO {
		byName[m.DiskIO[i].Device] = &m.DiskIO[i]
	}
	for _, row := range s.all("node_disk_device_mapper_info") {
		if d := byName[label(row, "device")]; d != nil {
			d.MappedName = label(row, "name")
			if d.MappedName == "" {
				d.MappedName = label(row, "dm_name")
			}
			if label(row, "lv_name") != "" || strings.HasPrefix(label(row, "uuid"), "LVM-") {
				d.Kind = "lvm"
			}
		}
	}
	for _, row := range s.all("node_disk_device_mapper_backing_info") {
		if d := byName[label(row, "device")]; d != nil {
			d.BackingDevices = append(d.BackingDevices, label(row, "backing_device"))
		}
	}
	for _, disk := range m.Disks {
		for i := range m.DiskIO {
			d := &m.DiskIO[i]
			if disk.Device == "/dev/"+d.Device || d.MappedName != "" && disk.Device == "/dev/mapper/"+d.MappedName {
				d.Mountpoints = append(d.Mountpoints, disk.Mountpoint)
			}
		}
	}
	for _, d := range m.DiskIO {
		if d.Kind == "mapper" && d.MappedName == "" {
			m.CapabilityNotes = append(m.CapabilityNotes, "exporter 未导出 "+d.Device+" 的映射名称与上下游关系，此处无法确认挂载用途")
		}
	}
}

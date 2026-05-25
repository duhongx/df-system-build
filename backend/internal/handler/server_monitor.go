package handler

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/middleware"
	"df-build-server/internal/repository"
	"df-build-server/pkg/response"

	"github.com/gin-gonic/gin"
)

type ServerMonitorHandler struct {
	repo *repository.ServerMgmtRepo
}

func NewServerMonitorHandler() *ServerMonitorHandler {
	return &ServerMonitorHandler{repo: repository.NewServerMgmtRepo()}
}

func (h *ServerMonitorHandler) RegisterRoutes(r *gin.RouterGroup) {
	g := r.Group("/server-mgmt")
	g.Use(middleware.AuthRequired())
	{
		g.GET("/:id/metrics", h.GetMetrics)
	}
}

type ServerMetrics struct {
	// CPU
	CPUUsagePercent float64 `json:"cpuUsagePercent"`
	CPUCores        int     `json:"cpuCores"`

	// Memory
	MemTotalBytes     uint64  `json:"memTotalBytes"`
	MemAvailableBytes uint64  `json:"memAvailableBytes"`
	MemUsedBytes      uint64  `json:"memUsedBytes"`
	MemUsagePercent   float64 `json:"memUsagePercent"`

	// Load
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	// Uptime
	UptimeSeconds float64 `json:"uptimeSeconds"`
	BootTime      string  `json:"bootTime"`

	// Disk
	Disks []DiskInfo `json:"disks"`

	// Network
	Networks []NetworkInfo `json:"networks"`
}

type DiskInfo struct {
	Mountpoint   string  `json:"mountpoint"`
	Device       string  `json:"device"`
	TotalBytes   uint64  `json:"totalBytes"`
	AvailBytes   uint64  `json:"availBytes"`
	UsedBytes    uint64  `json:"usedBytes"`
	UsagePercent float64 `json:"usagePercent"`
}

type NetworkInfo struct {
	Interface    string `json:"interface"`
	ReceiveBytes  uint64 `json:"receiveBytes"`
	TransmitBytes uint64 `json:"transmitBytes"`
}

func (h *ServerMonitorHandler) GetMetrics(c *gin.Context) {
	id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	server, err := h.repo.FindByID(uint(id))
	if err != nil {
		response.Fail(c, 12201, "服务器不存在")
		return
	}

	// Fetch metrics from node_exporter
	nodeExporterURL := fmt.Sprintf("http://%s:9100/metrics", server.Host)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(nodeExporterURL)
	if err != nil {
		response.Fail(c, 12202, fmt.Sprintf("无法连接 node_exporter (%s:9100): %v", server.Host, err))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		response.Fail(c, 12202, "读取 metrics 失败")
		return
	}

	metrics := parseNodeExporterMetrics(string(body))
	response.OK(c, metrics)
}

func parseNodeExporterMetrics(raw string) *ServerMetrics {
	m := &ServerMetrics{}
	lines := strings.Split(raw, "\n")

	// Parse into a map for easier access
	values := make(map[string]float64)
	labelValues := make(map[string]map[string]float64) // metric{labels} -> value

	for _, line := range lines {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}

		metricName := parts[0]
		// Store simple metrics
		if !strings.Contains(metricName, "{") {
			values[metricName] = val
		} else {
			// Parse labeled metrics
			idx := strings.Index(metricName, "{")
			name := metricName[:idx]
			labelsStr := metricName[idx+1 : len(metricName)-1]
			if labelValues[name] == nil {
				labelValues[name] = make(map[string]float64)
			}
			labelValues[name][labelsStr] = val
		}
	}

	// CPU - calculate from node_cpu_seconds_total
	var totalIdle, totalAll float64
	cpuCores := 0
	if cpuMetrics, ok := labelValues["node_cpu_seconds_total"]; ok {
		coreSet := make(map[string]bool)
		for labels, val := range cpuMetrics {
			// labels: cpu="0",mode="idle"
			labelMap := parseLabels(labels)
			cpu := labelMap["cpu"]
			mode := labelMap["mode"]
			if !coreSet[cpu] {
				coreSet[cpu] = true
			}
			totalAll += val
			if mode == "idle" {
				totalIdle += val
			}
		}
		cpuCores = len(coreSet)
		if totalAll > 0 {
			m.CPUUsagePercent = round((1 - totalIdle/totalAll) * 100)
		}
	}
	m.CPUCores = cpuCores

	// Memory
	memTotal := uint64(values["node_memory_MemTotal_bytes"])
	memAvail := uint64(values["node_memory_MemAvailable_bytes"])
	m.MemTotalBytes = memTotal
	m.MemAvailableBytes = memAvail
	m.MemUsedBytes = memTotal - memAvail
	if memTotal > 0 {
		m.MemUsagePercent = round(float64(memTotal-memAvail) / float64(memTotal) * 100)
	}

	// Load
	m.Load1 = round(values["node_load1"])
	m.Load5 = round(values["node_load5"])
	m.Load15 = round(values["node_load15"])

	// Uptime
	bootTime := values["node_boot_time_seconds"]
	if bootTime > 0 {
		m.UptimeSeconds = float64(time.Now().Unix()) - bootTime
		m.BootTime = time.Unix(int64(bootTime), 0).Format("2006-01-02 15:04:05")
	}

	// Disk - from node_filesystem_size_bytes and node_filesystem_avail_bytes
	diskSizes := labelValues["node_filesystem_size_bytes"]
	diskAvails := labelValues["node_filesystem_avail_bytes"]
	if diskSizes != nil {
		mountpoints := make(map[string]*DiskInfo)
		for labels, size := range diskSizes {
			labelMap := parseLabels(labels)
			mp := labelMap["mountpoint"]
			fstype := labelMap["fstype"]
			// Skip virtual filesystems
			if fstype == "tmpfs" || fstype == "devtmpfs" || fstype == "overlay" || mp == "" {
				continue
			}
			if size == 0 {
				continue
			}
			mountpoints[mp] = &DiskInfo{
				Mountpoint: mp,
				Device:     labelMap["device"],
				TotalBytes: uint64(size),
			}
		}
		for labels, avail := range diskAvails {
			labelMap := parseLabels(labels)
			mp := labelMap["mountpoint"]
			if d, ok := mountpoints[mp]; ok {
				d.AvailBytes = uint64(avail)
				d.UsedBytes = d.TotalBytes - d.AvailBytes
				if d.TotalBytes > 0 {
					d.UsagePercent = round(float64(d.UsedBytes) / float64(d.TotalBytes) * 100)
				}
			}
		}
		for _, d := range mountpoints {
			m.Disks = append(m.Disks, *d)
		}
	}

	// Network - from node_network_receive_bytes_total / node_network_transmit_bytes_total
	rxMetrics := labelValues["node_network_receive_bytes_total"]
	txMetrics := labelValues["node_network_transmit_bytes_total"]
	if rxMetrics != nil {
		for labels, rx := range rxMetrics {
			labelMap := parseLabels(labels)
			iface := labelMap["device"]
			// Skip loopback and virtual interfaces
			if iface == "lo" || strings.HasPrefix(iface, "veth") || strings.HasPrefix(iface, "docker") || strings.HasPrefix(iface, "br-") {
				continue
			}
			ni := NetworkInfo{
				Interface:     iface,
				ReceiveBytes:  uint64(rx),
			}
			if txMetrics != nil {
				if tx, ok := txMetrics[labels]; ok {
					ni.TransmitBytes = uint64(tx)
				}
			}
			m.Networks = append(m.Networks, ni)
		}
	}

	return m
}

func parseLabels(labelsStr string) map[string]string {
	result := make(map[string]string)
	pairs := strings.Split(labelsStr, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			val := strings.Trim(strings.TrimSpace(kv[1]), "\"")
			result[key] = val
		}
	}
	return result
}

func round(f float64) float64 {
	return math.Round(f*100) / 100
}

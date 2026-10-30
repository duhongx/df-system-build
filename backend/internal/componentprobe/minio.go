package componentprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
	"github.com/minio/minio-go/v7/pkg/signer"
)

// MinIOEndpoint accepts a service address, never a console URL or S3 bucket
// path. Bare host:port uses HTTP; TLS must be requested explicitly with https.
func MinIOEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n\t") {
		return "", fmt.Errorf("请填写 MinIO API 地址，例如 http://192.168.199.102:9000")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("MinIO 请填写 HTTP(S) API 地址，不包含账号、路径或查询参数，例如 http://192.168.199.102:9000")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("MinIO API 端口需在 1～65535 之间")
		}
	}
	u.Path = ""
	return u.String(), nil
}

// Only decode report facts. Admin info can also contain environment variables;
// never store its complete response or authentication headers in snapshots.
type minioInfo struct {
	Mode    string `json:"mode"`
	Backend struct {
		Type         string `json:"backendType"`
		OnlineDisks  *int   `json:"onlineDisks"`
		OfflineDisks *int   `json:"offlineDisks"`
	} `json:"backend"`
	Buckets *struct {
		Count *uint64
		Error string
	} `json:"buckets"`
	Objects *struct {
		Count *uint64
		Error string
	} `json:"objects"`
	Usage *struct {
		Size  *uint64
		Error string
	} `json:"usage"`
	Servers []struct {
		State    string            `json:"state"`
		Endpoint string            `json:"endpoint"`
		Version  string            `json:"version"`
		Uptime   int64             `json:"uptime"`
		Network  map[string]string `json:"network"`
		Drives   []struct {
			Path       string  `json:"path"`
			State      string  `json:"state"`
			TotalSpace uint64  `json:"totalspace"`
			UsedSpace  *uint64 `json:"usedspace"`
			FreeSpace  *uint64 `json:"availspace"`
			Healing    bool    `json:"healing"`
		} `json:"drives"`
	} `json:"servers"`
}

func minio(ctx context.Context, c Config) []inspection.Observation {
	fail := func(err error) []inspection.Observation {
		return []inspection.Observation{Failed("cloudhis-3", "MinIO 部署情况", err), Failed("cloudhis-38", "MinIO 集群运行", err)}
	}
	endpoint, err := MinIOEndpoint(c.Endpoint)
	if err != nil {
		return fail(err)
	}
	if c.Username == "" || c.Secret == "" {
		return fail(fmt.Errorf("MinIO API 需要 Access Key 和 Secret Key"))
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	const apiPath = "/minio/admin/v3/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+apiPath+"?metrics=false", nil)
	if err != nil {
		return fail(fmt.Errorf("MinIO API 地址无效"))
	}
	req.Header.Set("Accept", "application/json")
	sum := sha256.Sum256(nil)
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	// Same SigV4 authentication and admin endpoint as mc/madmin ServerInfo.
	req = signer.SignV4(*req, c.Username, c.Secret, "", "")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fail(fmt.Errorf("MinIO API 连接失败，请核对平台到 API 地址的网络、HTTP/HTTPS 及 TLS 证书"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message := fmt.Sprintf("MinIO 管理 API 返回 HTTP %d", resp.StatusCode)
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			message += "，请核对 Access Key、Secret Key 及 admin:ServerInfo 权限"
		} else if resp.StatusCode == http.StatusNotFound {
			message += "，请确认填写的是 MinIO API 地址，而非控制台地址"
		}
		return fail(fmt.Errorf("%s", message))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return fail(fmt.Errorf("MinIO API 响应读取失败或超过 4 MiB，未将不完整数据作为正常结果"))
	}
	var info minioInfo
	if json.Unmarshal(data, &info) != nil || len(info.Servers) == 0 {
		return fail(fmt.Errorf("MinIO 管理 API 未返回有效节点信息，请确认 API 地址及服务版本"))
	}
	return minioObservations(info)
}

func minioObservations(info minioInfo) []inspection.Observation {
	a := Observation("cloudhis-3", "MinIO 部署情况", fmt.Sprintf("MinIO 节点 %d 个，部署模式 %s，存储后端 %s", len(info.Servers), info.Mode, info.Backend.Type), "RECORDED")
	a.Source = "MinIO 管理 API /minio/admin/v3/info"
	a.Attributes = map[string]string{"architecture": "集群"}
	if len(info.Servers) == 1 {
		a.Attributes["architecture"] = "单机"
		a.Status, a.Reason = "WARNING", "单节点 MinIO，存在指导书所述单机存储风险"
	}
	nodes := inspection.Table{Columns: []string{"节点地址", "状态", "版本", "运行时间", "磁盘在线/总数"}}
	disks := inspection.Table{Columns: []string{"节点地址", "磁盘", "状态", "使用率", "可用 GiB"}}
	o := Observation("cloudhis-38", "MinIO 集群运行", "", "NORMAL")
	o.Source = a.Source
	o.Threshold = "节点、磁盘或节点网络离线需反馈；磁盘使用率达到 85% 需关注。"
	o.Measurements = map[string]float64{"nodes": float64(len(info.Servers))}
	var online, offline, unknown, offlineNodes int
	var reasons []string
	for _, server := range info.Servers {
		switch strings.ToLower(server.State) {
		case "online":
		case "offline":
			offlineNodes++
		default:
			unknown++
		}
		if server.Endpoint == "" || len(server.Drives) == 0 {
			unknown++
		}
		nodeOnline := 0
		for _, state := range server.Network {
			if state == "offline" {
				reasons = append(reasons, "存在节点网络离线")
				break
			}
		}
		for _, disk := range server.Drives {
			switch strings.ToLower(disk.State) {
			case "ok", "online":
				online++
				nodeOnline++
			case "offline", "corrupt", "faulty", "unformatted":
				offline++
			default:
				unknown++
			}
			usage, free := "未取得", "未取得"
			if disk.TotalSpace > 0 && disk.UsedSpace != nil && disk.FreeSpace != nil && *disk.UsedSpace <= disk.TotalSpace && *disk.FreeSpace <= disk.TotalSpace {
				used := 100 * float64(*disk.UsedSpace) / float64(disk.TotalSpace)
				usage, free = fmt.Sprintf("%.1f%%", used), fmt.Sprintf("%.2f", float64(*disk.FreeSpace)/(1<<30))
				o.Measurements["maxDiskPercent"] = max(o.Measurements["maxDiskPercent"], used)
				if used >= 85 {
					reasons = append(reasons, "存在磁盘使用率达到 85%")
				}
			}
			if disk.Healing {
				reasons = append(reasons, "存在正在修复的磁盘")
			}
			disks.Rows = append(disks.Rows, []string{server.Endpoint, disk.Path, disk.State, usage, free})
		}
		nodes.Rows = append(nodes.Rows, []string{server.Endpoint, server.State, server.Version, (time.Duration(max(server.Uptime, 0)) * time.Second).String(), fmt.Sprintf("%d/%d", nodeOnline, len(server.Drives))})
	}
	// Summary counts include unavailable nodes whose individual drives may be
	// absent from the response. Never replace those counts with visible nodes only.
	if info.Backend.OnlineDisks != nil && info.Backend.OfflineDisks != nil {
		if *info.Backend.OnlineDisks < 0 || *info.Backend.OfflineDisks < 0 {
			unknown++
		} else {
			online = max(online, *info.Backend.OnlineDisks)
			offline = max(offline, *info.Backend.OfflineDisks)
		}
	}
	o.Value = fmt.Sprintf("节点 %d 个（离线 %d），磁盘在线 %d、离线 %d", len(info.Servers), offlineNodes, online, offline)
	if offlineNodes > 0 || offline > 0 {
		reasons = append(reasons, "存在节点或磁盘离线")
	}
	if unknown > 0 || online+offline == 0 {
		o.Status, o.Reason = "UNKNOWN", "部分节点或磁盘状态未完整返回，不能认定全部正常"
	} else {
		o.Measurements["onlineDisks"], o.Measurements["offlineDisks"] = float64(online), float64(offline)
	}
	if len(reasons) > 0 {
		o.Status, o.Reason = "WARNING", strings.Join(uniqueStrings(reasons), "；")
		if unknown > 0 {
			o.Reason += "；另有节点或磁盘信息未完整取得"
		}
	}
	o.Measurements["offlineNodes"] = float64(offlineNodes)
	if info.Buckets != nil && info.Buckets.Count != nil && info.Buckets.Error == "" {
		o.Measurements["buckets"] = float64(*info.Buckets.Count)
		o.Value += fmt.Sprintf("；Bucket %d 个", *info.Buckets.Count)
	}
	if info.Objects != nil && info.Objects.Count != nil && info.Objects.Error == "" {
		o.Measurements["objects"] = float64(*info.Objects.Count)
		o.Value += fmt.Sprintf("；对象 %d 个", *info.Objects.Count)
	}
	if info.Usage != nil && info.Usage.Size != nil && info.Usage.Error == "" {
		o.Measurements["usedBytes"] = float64(*info.Usage.Size)
		o.Value += fmt.Sprintf("；对象容量 %.2f GiB", float64(*info.Usage.Size)/(1<<30))
	}
	a.Tables, o.Tables = []inspection.Table{nodes}, []inspection.Table{disks}
	return []inspection.Observation{a, o}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

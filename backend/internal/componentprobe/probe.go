// Package componentprobe collects bounded, read-only inspection facts on demand.
package componentprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

type Config struct{ Kind, Endpoint, Username, Secret string }

func Observation(code, name, value, status string) inspection.Observation {
	return inspection.Observation{Code: code, Name: name, Value: value, Status: status, CollectedAt: time.Now(), Source: "组件只读接口"}
}

func Failed(code, name string, err error) inspection.Observation {
	o := Observation(code, name, "未取得结果", "UNKNOWN")
	o.Reason = inspection.Redact(err.Error())
	o.Advice = "核对连接、访问权限及服务状态后重新采集。"
	return o
}

// HTTP only follows the explicitly configured endpoint. Redirects must not
// forward internal credentials to another destination.
func getJSON(ctx context.Context, c Config, suffix string, out any) error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("组件 HTTP 地址无效")
	}
	u.Path = strings.TrimRight(u.Path, "/") + strings.SplitN(suffix, "?", 2)[0]
	if parts := strings.SplitN(suffix, "?", 2); len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	r, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	if c.Username != "" || c.Secret != "" {
		r.SetBasicAuth(c.Username, c.Secret)
	}
	r.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return fmt.Errorf("组件接口连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("接口 %s 返回 HTTP %d", suffix, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("接口结果超过 4 MiB，无法确认完整性")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("接口 %s 未返回有效 JSON", suffix)
	}
	return nil
}

func Collect(ctx context.Context, c Config) []inspection.Observation {
	switch c.Kind {
	case "elasticsearch":
		return elastic(ctx, c)
	case "rabbitmq":
		return rabbit(ctx, c)
	case "redis":
		return redis(ctx, c)
	}
	return []inspection.Observation{Failed(c.Kind+"-collection", "组件采集", fmt.Errorf("不支持该组件类型"))}
}

func elastic(ctx context.Context, c Config) []inspection.Observation {
	var health struct {
		ClusterName string `json:"cluster_name"`
		Status      string `json:"status"`
		Nodes       int    `json:"number_of_nodes"`
		DataNodes   int    `json:"number_of_data_nodes"`
		Active      int    `json:"active_shards"`
		Unassigned  int    `json:"unassigned_shards"`
		TimedOut    bool   `json:"timed_out"`
	}
	if err := getJSON(ctx, c, "/_cluster/health?timeout=5s", &health); err != nil {
		return []inspection.Observation{Failed("elasticsearch-health", "Elasticsearch 集群状态", err)}
	}
	status := "NORMAL"
	if health.Status == "yellow" {
		status = "WARNING"
	}
	if health.Status == "red" {
		status = "CRITICAL"
	}
	if health.TimedOut || health.Nodes < 1 || health.ClusterName == "" || (health.Status != "green" && health.Status != "yellow" && health.Status != "red") {
		status = "UNKNOWN"
	}
	o := Observation("elasticsearch-health", "Elasticsearch 集群状态", fmt.Sprintf("集群 %s，状态 %s，节点 %d（数据节点 %d），活跃分片 %d，未分配 %d", health.ClusterName, health.Status, health.Nodes, health.DataNodes, health.Active, health.Unassigned), status)
	o.Measurements = map[string]float64{"nodes": float64(health.Nodes), "activeShards": float64(health.Active), "unassignedShards": float64(health.Unassigned)}
	o.Threshold = "green 正常；yellow 存在未分配副本，需核对；red 存在不可用主分片。"
	if status == "UNKNOWN" {
		o.Reason = "健康请求超时或返回了无法识别的集群状态"
	}
	out := []inspection.Observation{o}
	var nodes struct {
		Nodes map[string]struct {
			Name string `json:"name"`
			FS   struct {
				Total struct {
					Total     int64 `json:"total_in_bytes"`
					Available int64 `json:"available_in_bytes"`
				} `json:"total"`
			} `json:"fs"`
			JVM struct {
				Mem struct {
					Used *int `json:"heap_used_percent"`
				} `json:"mem"`
			} `json:"jvm"`
		} `json:"nodes"`
	}
	if err := getJSON(ctx, c, "/_nodes/stats/fs,jvm?filter_path=nodes.*.name,nodes.*.fs.total,nodes.*.jvm.mem.heap_used_percent", &nodes); err != nil {
		return append(out, Failed("elasticsearch-resources", "Elasticsearch 节点资源", err))
	}
	r := Observation("elasticsearch-resources", "Elasticsearch 节点资源", fmt.Sprintf("取得 %d 个节点的磁盘与 JVM 快照", len(nodes.Nodes)), "NORMAL")
	r.Measurements = map[string]float64{}
	t := inspection.Table{Columns: []string{"节点", "磁盘使用率", "可用 GiB", "JVM 堆使用率"}}
	for _, n := range nodes.Nodes {
		if n.FS.Total.Total <= 0 || n.JVM.Mem.Used == nil {
			r.Status = "UNKNOWN"
			r.Reason = "部分节点未返回文件系统容量"
			continue
		}
		used := 100 * (1 - float64(n.FS.Total.Available)/float64(n.FS.Total.Total))
		r.Measurements["maxDiskPercent"] = max(r.Measurements["maxDiskPercent"], used)
		r.Measurements["maxHeapPercent"] = max(r.Measurements["maxHeapPercent"], float64(*n.JVM.Mem.Used))
		if (used >= 85 || *n.JVM.Mem.Used >= 85) && r.Status == "NORMAL" {
			r.Status = "WARNING"
		}
		t.Rows = append(t.Rows, []string{n.Name, fmt.Sprintf("%.1f%%", used), fmt.Sprintf("%.1f", float64(n.FS.Total.Available)/(1<<30)), fmt.Sprintf("%d%%", *n.JVM.Mem.Used)})
	}
	if len(nodes.Nodes) == 0 {
		r.Status = "UNKNOWN"
		r.Reason = "未返回节点资源"
	}
	if len(nodes.Nodes) != health.Nodes {
		r.Status = "UNKNOWN"
		r.Reason = "节点资源返回数量与集群节点数不一致，未取得完整结果"
	}
	r.Tables = []inspection.Table{t}
	return append(out, r)
}

func rabbit(ctx context.Context, c Config) []inspection.Observation {
	var overview struct {
		Version string `json:"rabbitmq_version"`
		Cluster string `json:"cluster_name"`
		Object  struct {
			Connections int `json:"connections"`
			Queues      int `json:"queues"`
			Consumers   int `json:"consumers"`
		} `json:"object_totals"`
		Queue struct {
			Ready   int64 `json:"messages_ready"`
			Unacked int64 `json:"messages_unacknowledged"`
		} `json:"queue_totals"`
	}
	if err := getJSON(ctx, c, "/api/overview", &overview); err != nil {
		return []inspection.Observation{Failed("rabbitmq-overview", "RabbitMQ 运行概况", err)}
	}
	o := Observation("rabbitmq-overview", "RabbitMQ 运行概况", fmt.Sprintf("集群 %s，版本 %s；连接 %d，队列 %d，消费者 %d；待消费 %d，未确认 %d", overview.Cluster, overview.Version, overview.Object.Connections, overview.Object.Queues, overview.Object.Consumers, overview.Queue.Ready, overview.Queue.Unacked), "RECORDED")
	o.Measurements = map[string]float64{"connections": float64(overview.Object.Connections), "queues": float64(overview.Object.Queues), "consumers": float64(overview.Object.Consumers), "readyMessages": float64(overview.Queue.Ready), "unackedMessages": float64(overview.Queue.Unacked)}
	o.Reason = "消息数量为当前快照，不能单凭积压数量认定业务异常。"
	var nodes []struct {
		Name       string   `json:"name"`
		Running    *bool    `json:"running"`
		MemAlarm   *bool    `json:"mem_alarm"`
		DiskAlarm  *bool    `json:"disk_free_alarm"`
		Partitions []string `json:"partitions"`
	}
	if err := getJSON(ctx, c, "/api/nodes?columns=name,running,mem_alarm,disk_free_alarm,partitions", &nodes); err != nil {
		return []inspection.Observation{o, Failed("rabbitmq-nodes", "RabbitMQ 节点与告警", err)}
	}
	n := Observation("rabbitmq-nodes", "RabbitMQ 节点与告警", fmt.Sprintf("检查 %d 个节点", len(nodes)), "NORMAL")
	t := inspection.Table{Columns: []string{"节点", "运行", "内存告警", "磁盘告警", "网络分区"}}
	for _, v := range nodes {
		if v.Running == nil || v.MemAlarm == nil || v.DiskAlarm == nil {
			n.Status = "UNKNOWN"
			n.Reason = "缺少节点状态或告警字段"
		} else if !*v.Running || *v.MemAlarm || *v.DiskAlarm || len(v.Partitions) > 0 {
			if n.Status != "UNKNOWN" {
				n.Status = "WARNING"
			}
		}
		t.Rows = append(t.Rows, []string{v.Name, boolText(v.Running), boolText(v.MemAlarm), boolText(v.DiskAlarm), fmt.Sprint(len(v.Partitions))})
	}
	if len(nodes) == 0 {
		n.Status = "UNKNOWN"
		n.Reason = "未返回节点，核对管理接口权限"
	}
	n.Tables = []inspection.Table{t}
	return []inspection.Observation{o, n}
}
func boolText(v *bool) string {
	if v == nil {
		return "未取得"
	}
	if *v {
		return "是"
	}
	return "否"
}

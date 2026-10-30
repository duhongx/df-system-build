package componentprobe

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/inspection"
)

// The collector only sends AUTH, PING, INFO and CLUSTER INFO. It never reads keys.
func redis(ctx context.Context, c Config) []inspection.Observation {
	fail := func(err error) []inspection.Observation {
		return []inspection.Observation{Failed("redis-info", "Redis 运行情况", err)}
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Host == "" {
		return fail(fmt.Errorf("Redis 地址无效"))
	}
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "6379")
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	var conn net.Conn
	if u.Scheme == "rediss" {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = d.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fail(fmt.Errorf("Redis 连接失败"))
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(12 * time.Second)
	if v, ok := ctx.Deadline(); ok && v.Before(deadline) {
		deadline = v
	}
	conn.SetDeadline(deadline)
	r := bufio.NewReader(conn)
	command := func(args ...string) (string, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "*%d\r\n", len(args))
		for _, a := range args {
			fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
		}
		if _, err := io.WriteString(conn, b.String()); err != nil {
			return "", err
		}
		return redisReply(r)
	}
	if c.Secret != "" {
		args := []string{"AUTH"}
		if c.Username != "" {
			args = append(args, c.Username)
		}
		args = append(args, c.Secret)
		if _, err := command(args...); err != nil {
			return fail(fmt.Errorf("Redis 认证失败，请核对账号与凭据"))
		}
	}
	if pong, err := command("PING"); err != nil || pong != "PONG" {
		return fail(fmt.Errorf("Redis PING 失败"))
	}
	info, err := command("INFO")
	if err != nil {
		return fail(fmt.Errorf("Redis INFO 读取失败，请核对权限"))
	}
	m := redisInfo(info)
	if m["redis_version"] == "" || m["role"] == "" || m["used_memory"] == "" {
		return fail(fmt.Errorf("Redis INFO 缺少必要字段"))
	}
	o := Observation("redis-info", "Redis 运行情况", fmt.Sprintf("版本 %s，角色 %s，连接 %s，已用内存 %s，上限 %s，淘汰键累计 %s", m["redis_version"], m["role"], m["connected_clients"], m["used_memory_human"], m["maxmemory_human"], m["evicted_keys"]), "NORMAL")
	o.Measurements = map[string]float64{}
	o.Attributes = map[string]string{"version": m["redis_version"], "role": m["role"]}
	for key, field := range map[string]string{"connections": "connected_clients", "memoryBytes": "used_memory", "maxMemoryBytes": "maxmemory", "operationsPerSecond": "instantaneous_ops_per_sec", "evictedKeys": "evicted_keys"} {
		if value, err := strconv.ParseFloat(m[field], 64); err == nil {
			o.Measurements[key] = value
		}
	}
	if m["role"] == "slave" && m["master_link_status"] != "up" {
		o.Status = "WARNING"
		o.Reason = "副本与主节点连接未处于 up 状态"
	}
	maxMemory, _ := strconv.ParseFloat(m["maxmemory"], 64)
	used, _ := strconv.ParseFloat(m["used_memory"], 64)
	if maxMemory > 0 && used/maxMemory >= 0.85 {
		o.Status = "WARNING"
		o.Reason += " 内存使用达到配置上限的 85%"
	}
	p := Observation("redis-persistence", "Redis 持久化与复制", fmt.Sprintf("RDB 最近状态 %s；AOF 启用 %s，写入状态 %s；连接副本 %s，主从连接 %s", m["rdb_last_bgsave_status"], m["aof_enabled"], m["aof_last_write_status"], m["connected_slaves"], m["master_link_status"]), "RECORDED")
	p.Attributes = map[string]string{"rdbStatus": m["rdb_last_bgsave_status"], "aofEnabled": m["aof_enabled"]}
	if m["rdb_last_bgsave_status"] == "err" || (m["aof_enabled"] == "1" && m["aof_last_write_status"] == "err") {
		p.Status = "WARNING"
		p.Reason = "持久化最近一次执行/写入失败"
	}
	out := []inspection.Observation{o, p}
	if m["cluster_enabled"] == "1" {
		text, err := command("CLUSTER", "INFO")
		if err != nil {
			return append(out, Failed("redis-cluster", "Redis 集群状态", fmt.Errorf("CLUSTER INFO 读取失败")))
		}
		v := redisInfo(text)
		status := "NORMAL"
		if v["cluster_state"] != "ok" {
			status = "WARNING"
		}
		out = append(out, Observation("redis-cluster", "Redis 集群状态", fmt.Sprintf("状态 %s，已知节点 %s，分片槽正常 %s，失败 %s", v["cluster_state"], v["cluster_known_nodes"], v["cluster_slots_ok"], v["cluster_slots_fail"]), status))
	}
	return out
}
func redisInfo(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		p := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(p) == 2 {
			m[p[0]] = p[1]
		}
	}
	return m
}
func redisReply(r *bufio.Reader) (string, error) {
	data, err := r.ReadSlice('\n')
	line := string(data)
	if err != nil {
		return "", err
	}
	if len(line) > 4096 {
		return "", fmt.Errorf("响应头过长")
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if len(line) == 0 {
		return "", fmt.Errorf("空响应")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return "", fmt.Errorf("Redis 命令被拒绝")
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 || n > 4<<20 {
			return "", fmt.Errorf("Redis 响应长度异常")
		}
		b := make([]byte, n+2)
		_, err = io.ReadFull(r, b)
		if err != nil {
			return "", err
		}
		if string(b[n:]) != "\r\n" {
			return "", fmt.Errorf("Redis 响应结尾无效")
		}
		return string(b[:n]), nil
	}
	return "", fmt.Errorf("Redis 响应类型不支持")
}

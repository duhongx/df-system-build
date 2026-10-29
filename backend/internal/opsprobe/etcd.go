package opsprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"df-build-server/internal/inspection"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func processFlag(args []string, name string) string {
	for i, a := range args {
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Locate endpoints and mounted certificates from the actual local etcd process.
// /health performs the component's health read, without changing configuration.
func inspectEtcd(ctx context.Context, args []string, root string) inspection.Observation {
	o := inspection.Observation{Code: "kubernetes-etcd", Name: "etcd 健康状态", Status: "UNKNOWN", CollectedAt: time.Now(), Source: "实际 etcd 进程配置及 /health 接口"}
	fail := func(message string) inspection.Observation {
		o.Reason = message
		o.Value = "未取得 etcd 健康结果"
		return o
	}
	endpoint := processFlag(args, "--advertise-client-urls")
	if endpoint == "" {
		endpoint = processFlag(args, "--listen-client-urls")
	}
	endpoint = strings.Split(endpoint, ",")[0]
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fail("未能从 etcd 实际启动参数定位客户端地址")
	}
	if u.Hostname() == "0.0.0.0" {
		u.Host = "127.0.0.1:" + u.Port()
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	defer tr.CloseIdleConnections()
	if u.Scheme == "https" {
		ca := processFlag(args, "--trusted-ca-file")
		cert := processFlag(args, "--cert-file")
		key := processFlag(args, "--key-file")
		if ca != "" {
			data, err := os.ReadFile(filepath.Join(root, ca))
			if err != nil {
				return fail("无法读取实际 etcd CA 文件")
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(data) {
				return fail("etcd CA 文件无效")
			}
			tr.TLSClientConfig.RootCAs = pool
		}
		if processFlag(args, "--client-cert-auth") == "true" {
			// kubeadm provides a dedicated client certificate next to server.crt.
			candidate := filepath.Join(filepath.Dir(cert), "healthcheck-client.crt")
			candidateKey := filepath.Join(filepath.Dir(cert), "healthcheck-client.key")
			if _, err := os.Stat(filepath.Join(root, candidate)); err == nil {
				cert, key = candidate, candidateKey
			}
			pair, err := tls.LoadX509KeyPair(filepath.Join(root, cert), filepath.Join(root, key))
			if err != nil {
				return fail("无法读取 etcd 客户端证书与私钥")
			}
			tr.TLSClientConfig.Certificates = []tls.Certificate{pair}
		}
	}
	u.Path = "/health"
	u.RawQuery = "serializable=false"
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(qctx, "GET", u.String(), nil)
	if err != nil {
		return fail("etcd 健康地址无效")
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fail("etcd 健康接口访问失败，请核对本机网络及实际证书配置")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(data) > 8192 {
		return fail("etcd 健康响应不完整")
	}
	var body struct {
		Health json.RawMessage `json:"health"`
		Reason string          `json:"reason"`
	}
	if json.Unmarshal(data, &body) != nil {
		return fail("etcd 健康接口未返回可识别结果")
	}
	healthy := string(body.Health) == "true" || string(body.Health) == `"true"`
	if !healthy && string(body.Health) != "false" && string(body.Health) != `"false"` {
		return fail("etcd 健康接口未提供有效健康状态")
	}
	o.Value = fmt.Sprintf("本机 etcd 客户端地址 %s，健康检查 %t", endpoint, healthy)
	if response.StatusCode == 200 && healthy {
		o.Status = "NORMAL"
	} else {
		o.Status = "WARNING"
		o.Reason = fmt.Sprintf("健康接口 HTTP %d；%s", response.StatusCode, body.Reason)
	}
	return o
}

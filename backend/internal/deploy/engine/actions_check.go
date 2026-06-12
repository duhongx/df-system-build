package engine

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

func (e *ActionExecutor) httpCheck(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.URL == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "HTTP URL",
			Reason:     "URL 为空",
			Detail:     "http_check.url 不能为空",
			Suggestion: "补充明确的健康检查 URL。",
		}
	}
	timeout, err := parseActionTimeout(spec.Timeout)
	if err != nil {
		return timeoutError(ctx, actionName, spec.Timeout, err)
	}
	client := http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	attempts := spec.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	interval, err := parseActionInterval(spec.Interval)
	if err != nil {
		return timeoutError(ctx, actionName, spec.Interval, err)
	}
	var lastErr error
	var lastStatus int
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := client.Get(spec.URL)
		if err != nil {
			lastErr = err
			if attempt < attempts {
				time.Sleep(interval)
				continue
			}
			return &DeployError{
				Context:    ctx,
				Action:     actionName,
				Position:   "HTTP URL " + spec.URL,
				Reason:     "HTTP 请求失败",
				Detail:     err.Error(),
				Suggestion: "检查服务是否启动、端口是否监听、网络和防火墙是否正常。",
			}
		}
		expected := spec.ExpectedStatus
		if expected == 0 {
			expected = http.StatusOK
		}
		lastStatus = resp.StatusCode
		_ = resp.Body.Close()
		if resp.StatusCode == expected {
			return nil
		}
		if attempt < attempts {
			time.Sleep(interval)
		}
	}
	if lastErr != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "HTTP URL " + spec.URL,
			Reason:     "HTTP 请求失败",
			Detail:     lastErr.Error(),
			Suggestion: "检查服务是否启动、端口是否监听、网络和防火墙是否正常。",
		}
	}
	expected := spec.ExpectedStatus
	if expected == 0 {
		expected = http.StatusOK
	}
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "HTTP URL " + spec.URL,
		Reason:     "HTTP 状态码不符合预期",
		Detail:     fmt.Sprintf("expected=%d actual=%d attempts=%d", expected, lastStatus, attempts),
		Suggestion: "查看服务日志，确认健康检查路径和端口配置是否正确。",
	}
}

func (e *ActionExecutor) tcpCheck(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Address == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "TCP 地址",
			Reason:     "地址为空",
			Detail:     "tcp_check.address 不能为空",
			Suggestion: "补充 host:port 格式的 TCP 地址。",
		}
	}
	timeout, err := parseActionTimeout(spec.Timeout)
	if err != nil {
		return timeoutError(ctx, actionName, spec.Timeout, err)
	}
	attempts := spec.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	interval, err := parseActionInterval(spec.Interval)
	if err != nil {
		return timeoutError(ctx, actionName, spec.Interval, err)
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		conn, err := net.DialTimeout("tcp", spec.Address, timeout)
		if err == nil {
			return conn.Close()
		}
		lastErr = err
		if attempt < attempts {
			time.Sleep(interval)
		}
	}
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "TCP 地址 " + spec.Address,
		Reason:     "TCP 连接失败",
		Detail:     fmt.Sprintf("%v attempts=%d", lastErr, attempts),
		Suggestion: "检查服务监听地址、端口、防火墙和主机连通性。",
	}
}

func (e *ActionExecutor) resourcePreflight(ctx TaskContext, spec ActionSpec, actionName string) error {
	if spec.Manifest == "" {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "资源清单 manifest",
			Reason:     "资源清单路径为空",
			Detail:     "resource_preflight.manifest 不能为空",
			Suggestion: "配置资源清单路径，用于部署前检查离线资源完整性。",
		}
	}
	manifest, err := LoadResourceManifestFile(spec.Manifest)
	if err != nil {
		return &DeployError{
			Context:    ctx,
			Action:     actionName,
			Position:   "资源清单 " + spec.Manifest,
			Reason:     "读取资源清单失败",
			Detail:     err.Error(),
			Suggestion: "检查资源清单文件是否存在以及 YAML 格式是否正确。",
		}
	}
	mismatches := manifest.MissingRequiredWithReasons(ctx.Component, e.resourceDir)
	if len(mismatches) == 0 {
		return nil
	}
	// Group reasons by class so the operator sees a tidy summary
	// rather than 30 lines of "missing".
	missingPaths := []string{}
	hashMismatches := []string{}
	other := []string{}
	for _, m := range mismatches {
		switch {
		case strings.HasPrefix(m.Reason, "missing"):
			missingPaths = append(missingPaths, m.Item.RelativePath)
		case strings.HasPrefix(m.Reason, "sha256"):
			hashMismatches = append(hashMismatches, m.Item.RelativePath+" ("+m.Reason+")")
		default:
			other = append(other, m.Item.RelativePath+" ("+m.Reason+")")
		}
	}
	parts := []string{}
	if len(missingPaths) > 0 {
		parts = append(parts, "缺失文件: "+strings.Join(missingPaths, ", "))
	}
	if len(hashMismatches) > 0 {
		parts = append(parts, "hash 不一致: "+strings.Join(hashMismatches, ", "))
	}
	if len(other) > 0 {
		parts = append(parts, "其他: "+strings.Join(other, ", "))
	}
	return &DeployError{
		Context:    ctx,
		Action:     actionName,
		Position:   "离线资源目录 " + e.resourceDir,
		Reason:     "离线资源校验失败",
		Detail:     strings.Join(parts, "; "),
		Suggestion: "重新解压正确版本的离线包,或核对 manifest.yml 的 sha256 / relative_path。",
	}
}

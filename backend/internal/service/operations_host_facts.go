package service

import (
	"context"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/opsremote"
	"encoding/json"
	"fmt"
	"net"
	"path"
	"strings"
	"time"
)

// A component's host-side facts use the same private, read-only runner as a
// system report. The persisted result is then shared by its page and report.
func (s *OperationsService) collectHostFacts(parent context.Context, serverID uint, opt opsprobe.ManualOptions, bindings []opsprobe.InspectionDatabaseBinding) (*opsprobe.JobResult, error) {
	ctx, cancel := context.WithTimeout(parent, 125*time.Second)
	defer cancel()
	var server model.Server
	if err := s.db.First(&server, serverID).Error; err != nil {
		return nil, fmt.Errorf("关联主机不存在")
	}
	conn, err := s.connectServer(ctx, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	exe, base, err := conn.EnsureRunner(ctx)
	if err != nil {
		return nil, err
	}
	dir := path.Join(base, fmt.Sprintf("facts-%d-%d", serverID, time.Now().UnixNano()))
	if err := conn.SFTP.MkdirAll(dir); err != nil {
		return nil, err
	}
	if err := conn.SFTP.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	defer func() {
		for _, name := range []string{"config.json", "status.json", "result.json", "launcher.log", "job.lock", "cancel"} {
			_ = conn.SFTP.Remove(path.Join(dir, name))
		}
		_ = conn.SFTP.RemoveDirectory(dir)
	}()
	cfg := opsprobe.JobConfig{Type: "host", Name: server.Remark, Manual: &opt, HostDatabases: bindings, TimeoutSeconds: 120}
	if err := conn.JSONFile(path.Join(dir, "config.json"), cfg); err != nil {
		return nil, err
	}
	if _, err := conn.Run(ctx, "nohup "+opsremote.Quote(exe)+" --mode job --dir "+opsremote.Quote(dir)+" </dev/null >"+opsremote.Quote(path.Join(dir, "launcher.log"))+" 2>&1 &", 4096); err != nil {
		return nil, err
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		if data, err := conn.ReadFile(path.Join(dir, "result.json"), 0); err == nil {
			var r opsprobe.JobResult
			if err := json.Unmarshal(data, &r); err != nil {
				return nil, fmt.Errorf("主机结果格式无效")
			}
			redactCollection(&r)
			return &r, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("主机专项检查未完整结束：%w", ctx.Err())
		case <-tick.C:
		}
	}
}

func (s *OperationsService) matchingServers(ctx context.Context, addresses []string, explicit uint) ([]model.Server, error) {
	var rows []model.Server
	if err := s.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	addressesSet := map[string]bool{}
	for _, v := range addresses {
		addressesSet[strings.TrimSuffix(strings.ToLower(v), ".")] = true
	}
	var out []model.Server
	for _, row := range rows {
		if explicit > 0 {
			if row.ID == explicit {
				out = append(out, row)
			}
			continue
		}
		matched := addressesSet[strings.TrimSuffix(strings.ToLower(row.Host), ".")]
		if !matched && net.ParseIP(row.Host) == nil {
			lookupCtx, cancel := context.WithTimeout(ctx, time.Second)
			ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, row.Host)
			cancel()
			if err == nil {
				for _, ip := range ips {
					matched = matched || addressesSet[ip.IP.String()]
				}
			}
		}
		if matched {
			out = append(out, row)
		}
	}
	return out, nil
}

func relatedFailure(codes []string, reason string) []inspection.Observation {
	return inspection.EnsureResults(codes, nil, reason)
}

func appendRelatedHost(result *opsprobe.JobResult, host model.Server, facts *opsprobe.JobResult, codes []string) {
	wanted := map[string]bool{}
	for _, code := range codes {
		wanted[code] = true
	}
	filtered := []inspection.Observation{}
	synthesized := map[string]bool{}
	for _, code := range codes {
		if (code == "cloudhis-6" || code == "cloudhis-15" || code == "cloudhis-36" || code == "cloudhis-37") && facts.Metrics != nil {
			m := facts.Metrics
			o := inspection.Observation{Code: code, Name: inspection.CheckDefinition(code).Name, Status: "NORMAL", Source: "关联主机实际资源采样", CollectedAt: m.CollectedAt, Tables: hostReportTables(m)}
			o.Value = fmt.Sprintf("CPU %.1f%%，内存 %.1f%%；文件系统明细见本项结构化结果", m.CPUUsagePercent, m.MemUsagePercent)
			for _, c := range facts.Checks {
				if c.Status == "CRITICAL" {
					o.Status = "CRITICAL"
				} else if c.Status == "WARNING" && o.Status != "CRITICAL" {
					o.Status = "WARNING"
				}
				if c.Status == "WARNING" || c.Status == "CRITICAL" {
					o.Reason += c.Name + "：" + c.Value + "；"
				}
			}
			if code == "cloudhis-37" {
				count, bad := 0, 0
				for _, d := range m.Disks {
					if opsprobe.ReportableMountpoint(d.Mountpoint) && d.Inodes > 0 {
						count++
						if d.InodeUsedPercent >= 80 {
							bad++
						}
					}
				}
				o.Value = fmt.Sprintf("已取得 %d 个文件系统的 inode 状态，%d 个达到 80%% 提醒阈值", count, bad)
				if count == 0 {
					o.Status = "UNKNOWN"
					o.Reason = "文件系统未提供 inode 统计"
				}
			}
			if m.CPUCores == 0 || m.MemTotalBytes == 0 || len(m.Disks) == 0 || len(m.Warnings) > 0 {
				o.Status = "UNKNOWN"
				o.Reason = "基础资源数据不完整"
			}
			filtered = append(filtered, o)
			synthesized[code] = true
		}
	}
	for _, o := range facts.ReportChecks {
		if wanted[o.Code] && !synthesized[o.Code] {
			filtered = append(filtered, o)
		}
	}
	filtered = inspection.EnsureResults(codes, filtered, facts.Error)
	hostName := host.Remark
	if hostName == "" {
		hostName = host.Host
	}
	copy := *facts
	copy.ReportChecks = filtered
	copy.ExpectedChecks = codes
	copy.Checks = nil
	copy.RelatedHosts = nil
	result.RelatedHosts = append(result.RelatedHosts, opsprobe.RelatedHostResult{ServerID: host.ID, Name: hostName, Endpoint: serverEndpoint(host), Result: &copy})
	for _, o := range filtered {
		o.Origin = &inspection.Origin{Type: "host", ID: host.ID, Name: hostName, Endpoint: serverEndpoint(host)}
		result.ReportChecks = append(result.ReportChecks, o)
	}
}

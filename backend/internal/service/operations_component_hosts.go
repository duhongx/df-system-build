package service

import (
	"context"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// mc may run on a management machine. Its executor is not automatically a
// storage node; use actual admin-info members instead.
func minioMemberAddresses(observations []inspection.Observation) []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range observations {
		if o.Code != "cloudhis-3" {
			continue
		}
		for _, table := range o.Tables {
			for _, row := range table.Rows {
				if len(row) < 2 || row[0] != "节点" {
					continue
				}
				fields := strings.Fields(row[1])
				if len(fields) < 2 {
					continue
				}
				v := fields[1]
				if !strings.Contains(v, "://") {
					v = "http://" + v
				}
				u, err := url.Parse(v)
				if err != nil || u.Hostname() == "" {
					continue
				}
				address := u.Hostname()
				if !seen[address] {
					out = append(out, address)
					seen[address] = true
				}
			}
		}
	}
	return out
}

func (s *OperationsService) collectMinIOHosts(ctx context.Context, row model.ComponentConnection, r *opsprobe.JobResult) {
	codes := []string{"cloudhis-36", "cloudhis-37"}
	addresses := minioMemberAddresses(r.ReportChecks)
	if len(addresses) == 0 {
		r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "mc 未返回可定位的 MinIO 成员地址，不能将 mc 执行主机当作存储节点")...)
		return
	}
	seen := map[uint]bool{}
	for _, address := range addresses {
		local := address == "localhost" || (net.ParseIP(address) != nil && net.ParseIP(address).IsLoopback())
		explicit := uint(0)
		verifyLocal := local
		if local {
			if len(addresses) > 1 {
				r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "多个 MinIO 成员中包含回环地址，无法唯一确认该节点所在主机")...)
				continue
			}
			if len(addresses) == 1 && row.Endpoint != "" {
				u, err := url.Parse(row.Endpoint)
				if err != nil || u.Hostname() == "" {
					continue
				}
				address = u.Hostname()
				local = address == "localhost" || (net.ParseIP(address) != nil && net.ParseIP(address).IsLoopback())
			}
			if local {
				explicit = row.ServerID
				verifyLocal = true
			}
		}
		hosts, e := s.matchingServers(ctx, []string{address}, explicit)
		if e != nil || len(hosts) != 1 {
			r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "MinIO 成员 "+address+" 未唯一匹配到纳管主机，未使用 mc 客户端主机代替")...)
			continue
		}
		host := hosts[0]
		if seen[host.ID] {
			continue
		}
		seen[host.ID] = true
		facts, e := s.collectHostFacts(ctx, host.ID, opsprobe.ManualOptions{Auto: true, ConfiguredComponents: []string{"kubernetes", "minio", "redis", "rabbitmq", "elasticsearch"}}, nil)
		if e != nil {
			r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "MinIO 节点 "+address+" 专项采集失败："+e.Error())...)
			continue
		}
		if verifyLocal {
			found := false
			for _, o := range facts.ReportChecks {
				if o.Code == "host-discovery" {
					for _, kind := range strings.Split(strings.TrimPrefix(o.Value, "本机发现："), "、") {
						found = found || kind == "minio"
					}
				}
			}
			if !found {
				r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "mc 返回回环地址，但执行主机未发现本地 MinIO 服务，尚不能确认存储节点位置")...)
				continue
			}
		}
		appendRelatedHost(r, host, facts, codes)
	}
}

func (s *OperationsService) collectDatabaseHosts(ctx context.Context, row model.OperationsDatabase, cfg opsprobe.DatabaseConfig, r *opsprobe.JobResult) {
	codes := []string{"cloudhis-2", "cloudhis-15", "cloudhis-16", "cloudhis-17", "cloudhis-18", "cloudhis-19", "cloudhis-4"}
	missing := []string{"cloudhis-15", "cloudhis-4", "cloudhis-17", "cloudhis-18", "cloudhis-19"}
	hosts, err := s.matchingServers(ctx, []string{row.Host}, row.ServerID)
	if err != nil || len(hosts) == 0 {
		reason := "未匹配到数据库所在纳管主机，无法核对备份、归档文件与本机部署方式"
		if err != nil {
			reason = err.Error()
		}
		r.ReportChecks = append(r.ReportChecks, relatedFailure(missing, reason)...)
		return
	}
	if len(hosts) > 1 && row.ServerID == 0 {
		r.ReportChecks = append(r.ReportChecks, relatedFailure(missing, "地址匹配多个主机，无法唯一定位执行位置；请关联实例所在主机")...)
		return
	}
	host := hosts[0]
	for _, h := range hosts {
		if h.ID == row.ServerID {
			host = h
		}
	}
	opt := opsprobe.ManualOptions{Auto: true, ArchiveDirectory: row.ArchiveDirectory, PostgresBackupDirectory: row.BackupDirectory, ConfiguredComponents: []string{"kubernetes", "minio", "redis", "rabbitmq", "elasticsearch"}}
	facts, err := s.collectHostFacts(ctx, host.ID, opt, []opsprobe.InspectionDatabaseBinding{{Name: row.Name, Config: &cfg}})
	if err != nil {
		r.ReportChecks = append(r.ReportChecks, relatedFailure(missing, fmt.Sprintf("主机 %s 专项采集失败：%v", host.Host, err))...)
		return
	}
	// pgBackRest info is host-wide. Without an explicit directory it cannot
	// establish which of several local PostgreSQL instances owns a backup.
	{
		var peers []model.OperationsDatabase
		if s.db.Where("host=? OR server_id=?", row.Host, host.ID).Find(&peers).Error == nil {
			instances := map[string]bool{}
			for _, p := range peers {
				instances[fmt.Sprintf("%s:%d", p.Host, p.Port)] = true
			}
			if len(instances) > 1 {
				for i := range facts.ReportChecks {
					if facts.ReportChecks[i].Code == "cloudhis-4" && row.BackupDirectory == "" && (row.BackupServerID == 0 || row.BackupServerID == host.ID) {
						facts.ReportChecks[i].Status = "UNKNOWN"
						facts.ReportChecks[i].Reason = "主机上登记了多个 PostgreSQL 实例；当前工具返回主机级备份，尚未确认与本实例的对应关系，请关联实际备份目录"
					}
					for _, execution := range facts.ReportChecks[i].Executions {
						if strings.Contains(execution.Command, "patronictl") {
							facts.ReportChecks[i].Status = "UNKNOWN"
							facts.ReportChecks[i].Reason = "主机上登记了多个 PostgreSQL 实例；尚未确认 Patroni 配置与本连接的对应关系，不能用主机级集群结果代替实例结果"
							break
						}
					}
				}
			}
		}
	}
	replace := map[string]bool{}
	for _, c := range codes {
		replace[c] = true
	}
	prior := r.ReportChecks
	r.ReportChecks = nil
	for _, o := range prior {
		if !replace[o.Code] {
			r.ReportChecks = append(r.ReportChecks, o)
		}
	}
	if row.BackupServerID > 0 && row.BackupServerID != host.ID {
		codes = codes[:len(codes)-1]
		var backup model.Server
		if err := s.db.First(&backup, row.BackupServerID).Error; err != nil {
			r.ReportChecks = append(r.ReportChecks, relatedFailure([]string{"cloudhis-4"}, "登记的备份主机不存在")...)
		} else {
			back, e := s.collectHostFacts(ctx, backup.ID, opsprobe.ManualOptions{Profiles: []string{"postgres_backup"}, PostgresBackupDirectory: row.BackupDirectory}, nil)
			if e != nil {
				r.ReportChecks = append(r.ReportChecks, relatedFailure([]string{"cloudhis-4"}, "备份主机采集失败："+e.Error())...)
			} else {
				appendRelatedHost(r, backup, back, []string{"cloudhis-4"})
			}
		}
	}
	appendRelatedHost(r, host, facts, codes)
}

func (s *OperationsService) collectKubernetesHosts(ctx context.Context, row model.ComponentConnection, r *opsprobe.JobResult) {
	codes := []string{"cloudhis-6", "cloudhis-11", "cloudhis-12", "cloudhis-13", "cloudhis-14"}
	if len(r.MemberAddresses) == 0 {
		r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "未能取得集群成员地址，无法定位该集群主机专项")...)
		return
	}
	hosts, err := s.matchingServers(ctx, r.MemberAddresses, 0)
	if err != nil {
		r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, err.Error())...)
		return
	}
	if len(hosts) == 0 {
		r.ReportChecks = append(r.ReportChecks, relatedFailure(codes, "集群节点没有匹配到纳管主机，未使用其他集群主机代替")...)
		return
	}
	for _, h := range hosts {
		facts, e := s.collectHostFacts(ctx, h.ID, opsprobe.ManualOptions{Auto: true, ConfiguredComponents: []string{"kubernetes", "minio", "redis", "rabbitmq", "elasticsearch"}}, nil)
		if e != nil {
			for _, o := range relatedFailure(codes, "节点 "+h.Host+" 主机专项失败："+e.Error()) {
				o.Origin = &inspection.Origin{Type: "host", ID: h.ID, Name: h.Remark, Endpoint: serverEndpoint(h)}
				r.ReportChecks = append(r.ReportChecks, o)
			}
			continue
		}
		found := false
		for _, o := range facts.ReportChecks {
			if o.Code == "cloudhis-13" {
				found = true
			}
		}
		if !found {
			facts.ReportChecks = append(facts.ReportChecks, inspection.Observation{Code: "cloudhis-13", Name: "Nacos access 日志", Status: "NOT_APPLICABLE", Reason: "本节点未发现本地 Nacos 服务进程；其他节点分别检查"})
		}
		appendRelatedHost(r, h, facts, codes)
	}
	for _, addresses := range r.MemberGroups {
		covered := false
		matched, e := s.matchingServers(ctx, addresses, 0)
		covered = e == nil && len(matched) > 0
		if !covered {
			for _, o := range relatedFailure(codes, "集群成员 "+strings.Join(addresses, ",")+" 尚未匹配到纳管主机") {
				r.ReportChecks = append(r.ReportChecks, o)
			}
		}
	}
}

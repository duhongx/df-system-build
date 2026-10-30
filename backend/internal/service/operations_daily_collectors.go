package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"df-build-server/internal/componentprobe"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/pkg/crypto"
)

type dailyMonitoringContext struct{}
type dailyHostOwner struct{}

func (s *OperationsService) collectMonitoring(ctx context.Context, t monitoringTarget, lane string) (*opsprobe.JobResult, error) {
	ctx = context.WithValue(ctx, dailyMonitoringContext{}, true)
	r := &opsprobe.JobResult{Type: t.Kind, Name: t.Name, Version: opsprobe.Version, StartedAt: time.Now(), Indicators: map[string]float64{}}
	defer func() { r.FinishedAt = time.Now() }()
	if t.Type == "host" {
		var bindings []opsprobe.InspectionDatabaseBinding
		var databases []model.OperationsDatabase
		if err := s.db.WithContext(ctx).Where("server_id=?", t.ID).Find(&databases).Error; err != nil {
			return nil, err
		}
		for _, d := range databases {
			cfg, err := s.databaseConfig(d)
			binding := opsprobe.InspectionDatabaseBinding{Name: d.Name}
			if err != nil {
				binding.Error = err.Error()
			} else {
				binding.Config = &cfg
			}
			bindings = append(bindings, binding)
		}
		opt := opsprobe.ManualOptions{Auto: true}
		var components []model.ComponentConnection
		if err := s.db.WithContext(ctx).Where("enabled=true").Find(&components).Error; err != nil {
			return nil, err
		}
		for _, c := range components {
			if c.ServerID == t.ID && (c.Kind == "redis" || c.Kind == "rabbitmq" || c.Kind == "elasticsearch") {
				opt.ConfiguredComponents = append(opt.ConfiguredComponents, c.Kind)
			}
			if c.Kind == "kubernetes" || c.Kind == "minio" {
				var state model.MonitoringState
				if s.db.Where("target_type='component' AND target_id=? AND lane='live' AND last_success_at>?", c.ID, time.Now().Add(-3*time.Minute)).First(&state).Error == nil {
					var current opsprobe.JobResult
					if json.Unmarshal([]byte(state.ResultJSON), &current) != nil {
						continue
					}
					addresses := current.MemberAddresses
					if c.Kind == "minio" {
						addresses = minioMemberAddresses(current.ReportChecks)
					}
					if hosts, err := s.matchingServers(ctx, addresses, 0); err == nil {
						for _, h := range hosts {
							if h.ID == t.ID {
								opt.ConfiguredComponents = append(opt.ConfiguredComponents, c.Kind)
							}
						}
					}
				}
			}
		}
		return s.collectHostFacts(context.WithValue(ctx, dailyHostOwner{}, t.ID), t.ID, opt, bindings)
	}
	if t.Type == "database" {
		var row model.OperationsDatabase
		if err := s.db.WithContext(ctx).First(&row, t.ID).Error; err != nil {
			return nil, err
		}
		cfg, err := s.databaseConfig(row)
		if err != nil {
			return nil, err
		}
		switch lane {
		case "facts":
			s.collectDatabaseHosts(ctx, row, cfg, r)
		case "deep":
			r.ReportChecks = opsprobe.InspectionDatabase(ctx, cfg)
		default:
			d, err := opsprobe.Diagnose(ctx, cfg)
			if err != nil {
				return nil, fmt.Errorf("数据库监控采集失败：%s", SQLExecutionErrorMessage(err))
			}
			r.Database = d
			r.Checks = opsprobe.DatabaseChecks(d)
			r.Indicators["connections"] = float64(d.Connections)
			r.Indicators["maxConnections"] = float64(d.MaxConnections)
			r.Indicators["deadlocks"] = float64(d.Deadlocks)
			for _, spec := range []struct{ code, name, key string }{{"cloudhis-28", "当前锁等待", "lockWaits"}, {"cloudhis-30", "长事务", "longTransactions"}, {"cloudhis-33", "当前慢 SQL", "slowQueries"}} {
				count := 0
				for _, a := range d.Activities {
					if (spec.key == "lockWaits" && a.WaitEventType == "Lock") || (spec.key == "longTransactions" && a.TransactionSeconds >= 300) || (spec.key == "slowQueries" && a.State == "active" && a.ElapsedSeconds >= 5) {
						count++
					}
				}
				status := "NORMAL"
				if count > 0 {
					status = "WARNING"
				}
				o := componentprobe.Observation(spec.code, spec.name, fmt.Sprintf("%d 个", count), status)
				if !d.StatsVisible || d.Truncated {
					o.Status = "UNKNOWN"
					o.Reason = "会话可见范围不足或返回达到上限，不能认定无异常"
				} else {
					r.Indicators[spec.key] = float64(count)
				}
				r.ReportChecks = append(r.ReportChecks, o)
			}
			o := componentprobe.Observation("cloudhis-27", "连接使用情况", fmt.Sprintf("%d / %d", d.Connections, d.MaxConnections), "NORMAL")
			if d.MaxConnections > 0 && float64(d.Connections)/float64(d.MaxConnections) >= 0.8 {
				o.Status = "WARNING"
			}
			r.ReportChecks = append(r.ReportChecks, o)
			r.ReportChecks = append(r.ReportChecks, opsprobe.InspectionPostgreSQLDeployment(ctx, row.Name, cfg)...)
		}
	} else {
		var row model.ComponentConnection
		if err := s.db.WithContext(ctx).First(&row, t.ID).Error; err != nil {
			return nil, err
		}
		secret := ""
		if row.SecretEncrypted != "" {
			v, err := crypto.Decrypt(row.SecretEncrypted)
			if err != nil {
				return nil, fmt.Errorf("解密组件凭据失败")
			}
			secret = v
		}
		switch row.Kind {
		case "kubernetes":
			if lane == "live" {
				r.ReportChecks = s.collectKubernetesInto(ctx, row, secret, r)
			} else {
				view, err := s.MonitoringDetail(ctx, t.Type, t.ID)
				if err == nil && view.Result != nil && !view.Stale && view.LastError == "" {
					r.MemberAddresses = view.Result.MemberAddresses
					r.MemberGroups = view.Result.MemberGroups
				} else {
					r.ReportChecks = s.collectKubernetesInto(ctx, row, secret, r)
					r.ReportChecks = nil
				}
				s.collectKubernetesHosts(ctx, row, r)
			}
		case "minio":
			r.ReportChecks = s.collectMinIOPhase(ctx, row, secret, lane != "live")
			if lane != "live" {
				s.collectMinIOHosts(ctx, row, r)
				filtered := r.ReportChecks[:0]
				for _, o := range r.ReportChecks {
					if o.Code != "cloudhis-3" && o.Code != "cloudhis-38" {
						filtered = append(filtered, o)
					}
				}
				r.ReportChecks = filtered
			}
		default:
			r.ReportChecks = componentprobe.Collect(ctx, componentprobe.Config{Kind: row.Kind, Endpoint: row.Endpoint, Username: row.Username, Secret: secret})
		}
	}
	r.ExpectedChecks = monitoringLaneCodes(t, lane)
	r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, r.ReportChecks, "本轮后台采集没有完整返回此项数据")
	for _, o := range r.ReportChecks {
		if o.Status != "UNKNOWN" {
			for key, value := range o.Measurements {
				r.Indicators[key] = value
			}
		}
	}
	if t.Kind == "kubernetes" && lane == "live" {
		var previous model.MonitoringState
		if s.db.Where("target_type=? AND target_id=? AND lane='live' AND source_digest=?", t.Type, t.ID, t.Digest).First(&previous).Error == nil {
			var old opsprobe.JobResult
			if json.Unmarshal([]byte(previous.ResultJSON), &old) == nil {
				matched := 0
				delta := 0.0
				for uid, value := range r.Counters {
					if prior, ok := old.Counters[uid]; ok {
						matched++
						delta += max(0, value-prior)
					}
				}
				if matched > 0 {
					r.Indicators["restartIncrease"] = delta
				}
			}
		}
	}
	return r, nil
}

func monitoringResultError(r *opsprobe.JobResult) string {
	if r == nil {
		return "尚未取得数据"
	}
	if r.Error != "" {
		return r.Error
	}
	var errors []string
	for _, o := range r.ReportChecks {
		if o.Status == "UNKNOWN" {
			errors = append(errors, o.Name+"："+o.Reason)
		}
	}
	return strings.Join(errors, "；")
}

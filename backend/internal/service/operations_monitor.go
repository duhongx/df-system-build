package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/nodeexporter"
	"df-build-server/internal/opsprobe"
	"gorm.io/gorm/clause"
)

func (s *OperationsService) HostMonitor(id uint) (model.HostMonitorConfig, *opsprobe.HostMetrics, error) {
	var server model.Server
	if err := s.db.First(&server, id).Error; err != nil {
		return model.HostMonitorConfig{}, nil, err
	}
	cfg := model.HostMonitorConfig{ServerID: id, IntervalSeconds: 60}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&cfg).Error; err != nil {
		return cfg, nil, err
	}
	if err := s.db.First(&cfg, id).Error; err != nil {
		return cfg, nil, err
	}
	var sample model.HostMetricSample
	var metrics *opsprobe.HostMetrics
	if err := s.db.Where("server_id=? AND collected_at>=?", id, time.Now().Add(-48*time.Hour)).Order("collected_at DESC").First(&sample).Error; err == nil {
		var m opsprobe.HostMetrics
		if err := json.Unmarshal([]byte(sample.MetricsJSON), &m); err != nil {
			return cfg, nil, err
		}
		metrics = &m
	}
	return cfg, metrics, nil
}
func (s *OperationsService) ConfigureMonitor(id uint, enabled bool, interval int, metricsURL ...string) (model.HostMonitorConfig, error) {
	if interval < 30 || interval > 3600 {
		return model.HostMonitorConfig{}, fmt.Errorf("采集间隔应为 30～3600 秒")
	}
	cfg, _, err := s.HostMonitor(id)
	if err != nil {
		return cfg, err
	}
	cfg.Enabled, cfg.IntervalSeconds = enabled, interval
	if len(metricsURL) > 0 {
		raw := strings.TrimSpace(metricsURL[0])
		if raw != "" {
			u, e := url.Parse(raw)
			if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" {
				return cfg, fmt.Errorf("指标地址必须是 HTTP(S) URL，不允许内嵌账号密码")
			}
		}
		cfg.MetricsURL = raw
	}
	return cfg, s.db.Model(&cfg).Updates(map[string]any{"enabled": enabled, "interval_seconds": interval, "metrics_url": cfg.MetricsURL}).Error
}
func (s *OperationsService) CollectHostAsync(ctx context.Context, id uint) error {
	s.mu.Lock()
	if s.collecting[id] {
		s.mu.Unlock()
		return nil
	}
	select {
	case s.hostSlots <- struct{}{}:
	default:
		s.mu.Unlock()
		return fmt.Errorf("主机采集队列繁忙，请稍后重试")
	}
	s.collecting[id] = true
	s.mu.Unlock()
	go func() {
		defer func() { <-s.hostSlots; s.mu.Lock(); delete(s.collecting, id); s.mu.Unlock() }()
		qctx, cancel := context.WithTimeout(ctx, 35*time.Second)
		defer cancel()
		now := time.Now()
		cfg := model.HostMonitorConfig{ServerID: id, IntervalSeconds: 60}
		if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&cfg).Error; err != nil {
			return
		}
		if err := s.db.Model(&cfg).Updates(map[string]any{"last_attempt_at": now, "last_error": ""}).Error; err != nil {
			return
		}
		var server model.Server
		err := s.db.First(&server, id).Error
		if err == nil {
			if err = s.db.First(&cfg, id).Error; err == nil {
				endpoint := cfg.MetricsURL
				if endpoint == "" {
					endpoint = "http://" + net.JoinHostPort(server.Host, "9100") + "/metrics"
				}
				var metrics *opsprobe.HostMetrics
				metrics, err = nodeexporter.Collect(qctx, endpoint)
				if err == nil {
					var data []byte
					data, err = json.Marshal(metrics)
					if err == nil {
						err = s.db.Create(&model.HostMetricSample{ServerID: id, CollectedAt: time.Now(), MetricsJSON: string(data)}).Error
					}
				}
			}
		}
		updates := map[string]any{"last_error": ""}
		if err != nil {
			updates["last_error"] = err.Error()
		} else {
			updates["last_sample_at"] = time.Now()
		}
		s.db.Model(&model.HostMonitorConfig{}).Where("server_id=?", id).Updates(updates)
	}()
	return nil
}
func (s *OperationsService) scheduleHosts(ctx context.Context) {
	var rows []model.HostMonitorConfig
	if err := s.db.WithContext(ctx).Where("enabled=true AND (last_attempt_at IS NULL OR last_attempt_at < now() - interval_seconds * interval '1 second')").Order("last_attempt_at NULLS FIRST").Limit(10).Find(&rows).Error; err != nil {
		return
	}
	for _, row := range rows {
		if err := s.CollectHostAsync(ctx, row.ServerID); err != nil {
			return
		}
	}
}
func (s *OperationsService) HostHistory(ctx context.Context, id uint, hours int) ([]opsprobe.HostMetrics, int, error) {
	if hours < 1 || hours > 48 {
		return nil, 0, fmt.Errorf("历史范围应为 1～48 小时")
	}
	step := max(30, (hours*3600+599)/600)
	step = ((step + 29) / 30) * 30
	var samples []model.HostMetricSample
	// Compute the bucket once: PostgreSQL treats separate bind parameters as
	// different expressions even when they hold the same interval value.
	err := s.db.WithContext(ctx).Raw(`SELECT * FROM (SELECT DISTINCT ON (bucket) id,server_id,collected_at,metrics_json FROM (SELECT *,floor(extract(epoch FROM collected_at)/?) AS bucket FROM host_metric_samples WHERE server_id=? AND collected_at>=?) raw ORDER BY bucket,collected_at DESC,id DESC) s ORDER BY collected_at ASC`, step, id, time.Now().Add(-time.Duration(hours)*time.Hour)).Scan(&samples).Error
	if err != nil {
		return nil, step, err
	}
	out := make([]opsprobe.HostMetrics, 0, len(samples))
	for _, row := range samples {
		var m opsprobe.HostMetrics
		if err := json.Unmarshal([]byte(row.MetricsJSON), &m); err != nil {
			return nil, step, err
		}
		m.CollectedAt = row.CollectedAt
		out = append(out, m)
	}
	return out, step, nil
}

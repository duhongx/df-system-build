package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/opsremote"
	"df-build-server/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type OperationsService struct {
	db                  *gorm.DB
	workspace           string
	owner               string
	mu                  sync.Mutex
	collecting          map[uint]bool
	syncing             map[uint]bool
	active              map[uint]bool
	diagnosing          map[uint]bool
	diagnostics         map[uint]diagnosticCache
	componentCollecting map[string]bool
	hostSlots           chan struct{}
	logSlots            chan struct{}
	runSlots            chan struct{}
	reportSlots         chan struct{}
}
type diagnosticCache struct {
	at       time.Time
	revision uint
	result   *opsprobe.Diagnostic
	err      error
}

func NewOperationsService(db *gorm.DB, workspace string) *OperationsService {
	base, _ := filepath.Abs(filepath.Join(workspace, "operations"))
	return &OperationsService{db: db, workspace: base, owner: fmt.Sprintf("worker-%d", time.Now().UnixNano()), collecting: map[uint]bool{}, syncing: map[uint]bool{}, active: map[uint]bool{}, diagnosing: map[uint]bool{}, diagnostics: map[uint]diagnosticCache{}, componentCollecting: map[string]bool{}, hostSlots: make(chan struct{}, 2), logSlots: make(chan struct{}, 1), runSlots: make(chan struct{}, 2), reportSlots: make(chan struct{}, 2)}
}
func (s *OperationsService) Start(ctx context.Context) {
	s.cleanupOperations(ctx)
	s.db.Model(&model.ServiceOperation{}).Where("status IN ?", []string{"QUEUED", "RUNNING"}).Updates(map[string]any{"status": "UNKNOWN", "message": "平台重启，操作结果待核实；请先刷新服务状态", "finished_at": time.Now()})
	go func() {
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		cleanup := time.NewTicker(time.Hour)
		defer cleanup.Stop()
		var lastAccessAuditCheck time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if time.Since(lastAccessAuditCheck) > time.Minute {
					s.db.WithContext(ctx).Model(&model.DatabaseQueryRecord{}).Where("kind='access' AND status='RUNNING' AND started_at<?", time.Now().Add(-2*time.Minute)).Updates(map[string]any{"status": "UNKNOWN", "error": "操作未记录最终状态，请核对实际用户与权限后再操作"})
					lastAccessAuditCheck = time.Now()
				}
				s.scheduleHosts(ctx)
				s.scheduleLogSync(ctx)
				s.scheduleInspections(ctx)
			case <-cleanup.C:
				s.cleanupOperations(ctx)
			}
		}
	}()
}
func (s *OperationsService) cleanupOperations(ctx context.Context) {
	s.db.WithContext(ctx).Where("collected_at < ?", time.Now().Add(-48*time.Hour)).Delete(&model.CollectionSample{})
	s.db.WithContext(ctx).Where("collected_at < ?", time.Now().Add(-48*time.Hour)).Delete(&model.HostMetricSample{})
	s.db.WithContext(ctx).Where("event_time < ? OR created_at < ?", time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)).Delete(&model.DatabaseLogEvent{})
}
func (s *OperationsService) connect(ctx context.Context, id uint) (*opsremote.Conn, error) {
	var server model.Server
	if err := s.db.WithContext(ctx).First(&server, id).Error; err != nil {
		return nil, fmt.Errorf("关联主机不存在: %w", err)
	}
	return s.connectServer(ctx, server)
}
func (s *OperationsService) connectServer(ctx context.Context, server model.Server) (*opsremote.Conn, error) {
	id := server.ID
	config := model.HostMonitorConfig{ServerID: id, IntervalSeconds: 60}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&config).Error; err != nil {
		return nil, err
	}
	if err := s.db.First(&config, id).Error; err != nil {
		return nil, err
	}
	c, err := opsremote.Connect(ctx, server, config.SSHFingerprint)
	if err != nil {
		return nil, err
	}
	if config.SSHFingerprint == "" {
		result := s.db.Model(&model.HostMonitorConfig{}).Where("server_id=? AND ssh_fingerprint=''", id).Update("ssh_fingerprint", c.Fingerprint)
		if result.Error != nil {
			c.Close()
			return nil, result.Error
		}
		var saved model.HostMonitorConfig
		if err := s.db.First(&saved, id).Error; err != nil {
			c.Close()
			return nil, err
		}
		if saved.SSHFingerprint != c.Fingerprint {
			c.Close()
			return nil, fmt.Errorf("SSH 主机指纹在连接期间发生变化")
		}
	}
	return c, nil
}
func (s *OperationsService) databaseConfig(row model.OperationsDatabase) (opsprobe.DatabaseConfig, error) {
	if row.Source == "environment" {
		c, err := NewPostgreSQLService().loadConfig()
		if err != nil {
			return opsprobe.DatabaseConfig{}, err
		}
		if c.Host == "" || c.Database == "" || c.Password == "" || environmentDatabaseDigest(c) != row.EnvironmentDigest {
			return opsprobe.DatabaseConfig{}, fmt.Errorf("环境连接已变化，请刷新连接列表后重试")
		}
		return opsprobe.DatabaseConfig{Host: row.Host, Port: row.Port, Database: row.Database, Username: row.Username, Password: c.Password, SSLMode: row.SSLMode}, nil
	}

	password, err := crypto.Decrypt(row.PasswordEncrypted)
	if err != nil {
		return opsprobe.DatabaseConfig{}, fmt.Errorf("解密数据库巡检凭据失败")
	}
	return opsprobe.DatabaseConfig{Host: row.Host, Port: row.Port, Database: row.Database, Username: row.Username, Password: password, SSLMode: row.SSLMode}, nil
}
func (s *OperationsService) Diagnose(ctx context.Context, id uint) (*opsprobe.Diagnostic, error) {
	var row model.OperationsDatabase
	if err := s.db.First(&row, id).Error; err != nil {
		return nil, err
	}
	s.mu.Lock()
	cached, ok := s.diagnostics[id]
	if ok && cached.revision == row.Revision && time.Since(cached.at) < 5*time.Second {
		s.mu.Unlock()
		return cached.result, cached.err
	}
	if s.diagnosing[id] {
		s.mu.Unlock()
		return nil, fmt.Errorf("本实例正在刷新，请稍后重试")
	}
	s.diagnosing[id] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.diagnosing, id); s.mu.Unlock() }()
	cfg, err := s.databaseConfig(row)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	result, err := opsprobe.Diagnose(queryCtx, cfg)
	if err != nil {
		err = fmt.Errorf("数据库诊断失败: %s", SQLExecutionErrorMessage(err))
	}
	s.mu.Lock()
	s.diagnostics[id] = diagnosticCache{time.Now(), row.Revision, result, err}
	s.mu.Unlock()
	return result, err
}
func (s *OperationsService) DetectLogs(ctx context.Context, id uint) (map[string]string, error) {
	var row model.OperationsDatabase
	if err := s.db.First(&row, id).Error; err != nil {
		return nil, err
	}
	cfg, err := s.databaseConfig(row)
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	settings, err := opsprobe.LogSettings(qctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("日志设置检测失败: %s", SQLExecutionErrorMessage(err))
	}
	directory := settings["log_directory"]
	if directory != "" && !filepath.IsAbs(directory) {
		if settings["data_directory"] != "" {
			directory = filepath.Join(settings["data_directory"], directory)
		} else {
			directory = ""
		}
	}
	settings["resolved_directory"] = directory
	if row.ServerID == 0 {
		return settings, fmt.Errorf("数据库未关联日志所在主机，请先关联主机")
	}
	conn, err := s.connect(qctx, row.ServerID)
	if err != nil {
		return settings, err
	}
	defer conn.Close()
	if directory == "" {
		return settings, fmt.Errorf("无法读取日志目录配置，请填写绝对日志路径")
	}
	files, err := conn.SFTP.ReadDir(directory)
	if err != nil {
		return settings, fmt.Errorf("日志目录不可读: %w", err)
	}
	settings["readable_files"] = strconv.Itoa(len(files))
	settings["format"] = "text"
	if strings.Contains(settings["log_destination"], "csvlog") {
		settings["format"] = "csv"
	}
	settings["source_identity"] = fmt.Sprintf("%s:%d/%s@%d", row.Host, row.Port, row.Database, row.ServerID)
	settings["detected_at"] = time.Now().Format(time.RFC3339)
	if err := s.db.Model(&model.OperationsDatabase{}).Where("id=? AND revision=?", id, row.Revision).Update("log_capability", mustJSON(settings)).Error; err != nil {
		return settings, err
	}
	return settings, nil
}
func serverEndpoint(server model.Server) string {
	return server.Username + "@" + net.JoinHostPort(server.Host, strconv.Itoa(server.Port))
}
func (s *OperationsService) ImportCurrentDatabase(name string) (*model.OperationsDatabase, error) {
	return s.SyncEnvironmentDatabase(context.Background(), true, name)
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

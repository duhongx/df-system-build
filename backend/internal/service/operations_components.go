package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"df-build-server/internal/componentprobe"
	"df-build-server/internal/inspection"
	"df-build-server/internal/model"
	"df-build-server/internal/opsprobe"
	"df-build-server/internal/opsremote"
	"df-build-server/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ComponentOptions struct {
	Namespace       string `json:"namespace"`
	Alias           string `json:"alias"`
	DailyFile       string `json:"dailyFile"`
	BackupDirectory string `json:"backupDirectory"`
}
type ComponentRequest struct {
	ID          uint             `json:"id"`
	Kind        string           `json:"kind"`
	Name        string           `json:"name"`
	Endpoint    string           `json:"endpoint"`
	Username    string           `json:"username"`
	Secret      string           `json:"secret"`
	ClearSecret bool             `json:"clearSecret"`
	ServerID    uint             `json:"serverId"`
	Enabled     bool             `json:"enabled"`
	Options     ComponentOptions `json:"options"`
}

func validateComponent(req ComponentRequest) error {
	if req.ClearSecret && req.Secret != "" {
		return fmt.Errorf("不能同时填写新凭据与清除凭据")
	}
	if strings.TrimSpace(req.Name) == "" || len([]rune(req.Name)) > 128 {
		return fmt.Errorf("请填写 1～128 字的组件名称")
	}
	if len(req.Username) > 128 || len(req.Secret) > 1<<20 {
		return fmt.Errorf("账号或凭据过长")
	}
	switch req.Kind {
	case "kubernetes":
	case "minio":
		if req.ServerID == 0 {
			return fmt.Errorf("MinIO 请关联运行 mc 的已纳管主机")
		}
		if req.Options.Alias != "" && !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(req.Options.Alias) {
			return fmt.Errorf("mc 别名不合法")
		}
	case "redis", "rabbitmq", "elasticsearch":
	default:
		return fmt.Errorf("组件类型不支持")
	}
	if req.Kind != "kubernetes" && (req.Kind != "minio" || req.Endpoint != "") {
		u, err := url.Parse(req.Endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("请填写无内嵌凭据的组件地址")
		}
		if req.Kind == "redis" {
			if (u.Scheme != "redis" && u.Scheme != "rediss") || (u.Path != "" && u.Path != "/") {
				return fmt.Errorf("Redis 地址格式为 redis://主机:6379 或 rediss://主机:6379")
			}
		} else if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("地址需以 http:// 或 https:// 开头")
		}
	}
	for _, v := range []string{req.Options.DailyFile, req.Options.BackupDirectory} {
		if v != "" && (!path.IsAbs(v) || strings.ContainsAny(v, "\x00\r\n") || len(v) > 1024) {
			return fmt.Errorf("文件和目录需填写主机上的绝对路径")
		}
	}
	return nil
}

func (s *OperationsService) SaveComponent(ctx context.Context, req ComponentRequest) (*model.ComponentConnection, error) {
	if err := validateComponent(req); err != nil {
		return nil, err
	}
	var result model.ComponentConnection
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row := model.ComponentConnection{Revision: 1}
		if req.ID > 0 {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, req.ID).Error; err != nil {
				return err
			}
			if row.Source == "environment" {
				return fmt.Errorf("此连接复用环境配置，请到环境配置维护")
			}
			if row.Kind != req.Kind {
				return fmt.Errorf("不能修改已有组件的类型")
			}
			row.Revision++
		}
		if req.ServerID > 0 {
			var host model.Server
			if err := tx.First(&host, req.ServerID).Error; err != nil {
				return fmt.Errorf("关联主机不存在")
			}
		}
		if req.Secret != "" {
			encrypted, err := crypto.Encrypt(req.Secret)
			if err != nil {
				return fmt.Errorf("凭据加密失败")
			}
			row.SecretEncrypted = encrypted
		} else if req.ClearSecret {
			row.SecretEncrypted = ""
		}
		if req.Kind == "kubernetes" && row.SecretEncrypted == "" {
			return fmt.Errorf("请填写此集群的 kubeconfig；环境集群直接复用环境配置")
		}
		if req.Kind == "minio" && req.Endpoint != "" && (row.SecretEncrypted == "" || req.Username == "") {
			return fmt.Errorf("使用独立 MinIO 地址时请填写 Access Key 和 Secret Key；也可留空地址复用主机已有 mc 别名")
		}
		if req.Kind == "kubernetes" {
			req.Endpoint = ""
			req.Username = ""
		}
		row.Kind, row.Name, row.Endpoint, row.Username, row.ServerID, row.Enabled, row.OptionsJSON = req.Kind, strings.TrimSpace(req.Name), req.Endpoint, req.Username, req.ServerID, req.Enabled, mustJSON(req.Options)
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		result = row
		return nil
	})
	return &result, err
}

// Register the environment cluster by reference. Secrets are not copied into a
// second settings form. Existing manually registered sources remain untouched.
func (s *OperationsService) EnsureInspectionSources(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(70291029)").Error; err != nil {
			return err
		}
		var settings []model.Settings
		if err := tx.Where("key IN ?", []string{"k8s_kubeconfig_path", "k8s_kubeconfig_content"}).Order("key").Find(&settings).Error; err != nil {
			return err
		}
		configured := false
		for _, v := range settings {
			configured = configured || strings.TrimSpace(v.Value) != ""
		}
		if !configured {
			return tx.Model(&model.ComponentConnection{}).Where("kind='kubernetes' AND source='environment' AND enabled=true").Updates(map[string]any{"enabled": false, "environment_digest": "", "revision": gorm.Expr("revision+1")}).Error
		}
		var count int64
		if err := tx.Model(&model.ComponentConnection{}).Where("kind='kubernetes' AND source='environment'").Count(&count).Error; err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(func() string {
			m := map[string]string{}
			for _, v := range settings {
				m[v.Key] = v.Value
			}
			return mustJSON(m)
		}())))
		if count == 0 {
			return tx.Create(&model.ComponentConnection{Kind: "kubernetes", Name: "环境 Kubernetes", Source: "environment", EnvironmentDigest: digest, Enabled: true, OptionsJSON: "{}", Revision: 1}).Error
		}
		return tx.Model(&model.ComponentConnection{}).Where("kind='kubernetes' AND source='environment' AND (environment_digest IS DISTINCT FROM ? OR enabled=false)", digest).Updates(map[string]any{"environment_digest": digest, "revision": gorm.Expr("revision+1"), "enabled": true}).Error
	})
}

func (s *OperationsService) Components(ctx context.Context) ([]model.ComponentConnection, error) {
	if err := s.EnsureInspectionSources(ctx); err != nil {
		return nil, err
	}
	var rows []model.ComponentConnection
	err := s.db.WithContext(ctx).Order("kind,id").Find(&rows).Error
	return rows, err
}

func (s *OperationsService) LatestCollection(ctx context.Context, kind string, id uint) (*model.CollectionSample, error) {
	if kind != "component" && kind != "database" && kind != "host" {
		return nil, fmt.Errorf("采集对象类型无效")
	}
	var row model.CollectionSample
	err := s.db.WithContext(ctx).Where("target_type=? AND target_id=? AND collected_at>=?", kind, id, time.Now().Add(-48*time.Hour)).Order("id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

func (s *OperationsService) rememberCollection(kind string, id, revision uint, result *opsprobe.JobResult) error {
	redactCollection(result)
	setCollectionCoverage(result)
	return s.db.Create(&model.CollectionSample{TargetType: kind, TargetID: id, Revision: revision, CollectedAt: result.FinishedAt, ResultJSON: mustJSON(result)}).Error
}

func setCollectionCoverage(r *opsprobe.JobResult) {
	o := inspection.Object{ID: 1, Type: r.Type, Name: r.Name, Status: "COMPLETED", Message: r.Error, Observations: r.ReportChecks}
	configuredObject(&o, *r)
	doc := inspection.Build(inspection.Report{Mode: "configured", Objects: []inspection.Object{o}})
	r.Coverage = &doc.Summary
}

func (s *OperationsService) CollectComponent(ctx context.Context, id uint) (*opsprobe.JobResult, error) {
	var row model.ComponentConnection
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, err
	}
	key := fmt.Sprintf("component:%d", id)
	if !s.claimCollection(key) {
		return nil, fmt.Errorf("该组件正在采集，请稍后刷新")
	}
	defer s.releaseCollection(key)
	result := s.collectComponent(ctx, row)
	if err := s.rememberCollection("component", id, row.Revision, result); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *OperationsService) claimCollection(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.componentCollecting[key] {
		return false
	}
	s.componentCollecting[key] = true
	return true
}
func (s *OperationsService) releaseCollection(key string) {
	s.mu.Lock()
	delete(s.componentCollecting, key)
	s.mu.Unlock()
}

func (s *OperationsService) collectComponent(parent context.Context, row model.ComponentConnection) *opsprobe.JobResult {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	r := &opsprobe.JobResult{Version: opsprobe.Version, Type: row.Kind, Name: row.Name, StartedAt: time.Now()}
	secret := ""
	var err error
	if row.SecretEncrypted != "" {
		secret, err = crypto.Decrypt(row.SecretEncrypted)
	}
	if err != nil {
		r.ReportChecks = []inspection.Observation{componentprobe.Failed(row.Kind+"-collection", "组件凭据", fmt.Errorf("解密凭据失败"))}
	} else {
		switch row.Kind {
		case "kubernetes":
			r.ReportChecks = s.collectKubernetesInto(ctx, row, secret, r)
			s.collectKubernetesHosts(ctx, row, r)
		case "minio":
			r.ReportChecks = s.collectMinIO(ctx, row, secret)
			s.collectMinIOHosts(ctx, row, r)
		default:
			r.ReportChecks = componentprobe.Collect(ctx, componentprobe.Config{Kind: row.Kind, Endpoint: row.Endpoint, Username: row.Username, Secret: secret})
		}
	}
	r.ExpectedChecks = inspection.RequiredCodes(row.Kind)
	r.ReportChecks = inspection.EnsureResults(r.ExpectedChecks, r.ReportChecks, r.Error)
	r.FinishedAt = time.Now()
	redactCollection(r)
	return r
}

func redactCollection(r *opsprobe.JobResult) {
	r.Error = inspection.Redact(r.Error)
	for i := range r.RelatedHosts {
		if r.RelatedHosts[i].Result != nil {
			redactCollection(r.RelatedHosts[i].Result)
		}
	}
	for i := range r.ReportChecks {
		inspection.SanitizeObservation(&r.ReportChecks[i])
	}
	for i := range r.Checks {
		c := &r.Checks[i]
		c.Value = inspection.Redact(c.Value)
		c.Evidence = inspection.Redact(c.Evidence)
		c.Error = inspection.Redact(c.Error)
	}
}

func (s *OperationsService) collectMinIO(ctx context.Context, row model.ComponentConnection, secret string) []inspection.Observation {
	fail := func(err error) []inspection.Observation {
		return []inspection.Observation{componentprobe.Failed("cloudhis-38", "MinIO 集群运行", err)}
	}
	conn, err := s.connect(ctx, row.ServerID)
	if err != nil {
		return fail(err)
	}
	defer conn.Close()
	var opt ComponentOptions
	if json.Unmarshal([]byte(row.OptionsJSON), &opt) != nil {
		return fail(fmt.Errorf("MinIO 采集选项无效"))
	}
	alias := opt.Alias
	if alias == "" {
		alias = "prod"
	}
	prefix := "mc "
	if row.Endpoint != "" {
		home, err := conn.SFTP.Getwd()
		if err != nil {
			return fail(err)
		}
		dir := path.Join(home, ".dfops", fmt.Sprintf("mc-inspection-%d", time.Now().UnixNano()))
		if err := conn.SFTP.MkdirAll(dir); err != nil {
			return fail(err)
		}
		if err := conn.SFTP.Chmod(dir, 0700); err != nil {
			return fail(err)
		}
		defer conn.SFTP.RemoveDirectory(dir)
		defer conn.SFTP.Remove(path.Join(dir, "config.json"))
		alias = "inspection"
		config := map[string]any{"version": "10", "aliases": map[string]any{alias: map[string]string{"url": row.Endpoint, "accessKey": row.Username, "secretKey": secret, "api": "S3v4", "path": "auto"}}}
		if err := conn.JSONFile(path.Join(dir, "config.json"), config); err != nil {
			return fail(fmt.Errorf("写入临时 mc 配置失败"))
		}
		prefix += "--config-dir " + opsremote.Quote(dir) + " "
	}
	cmd := prefix + "admin info " + opsremote.Quote(alias)
	start := time.Now()
	data, err := conn.Run(ctx, cmd, 2<<20)
	if err != nil {
		return fail(err)
	}
	o := opsprobe.InspectMinIOText(string(data))
	o.Source = "纳管主机 mc admin info"
	o.Executions = []inspection.Execution{{Kind: "command", Command: "mc admin info [巡检连接]", Stdout: string(data), StartedAt: start, FinishedAt: time.Now()}}
	architecture := opsprobe.InspectMinIOArchitectureText(string(data))
	out := []inspection.Observation{architecture, o}
	if opt.DailyFile != "" {
		data, err := conn.ReadFile(opt.DailyFile, 1<<20)
		if err != nil {
			out = append(out, componentprobe.Failed("cloudhis-39", "MinIO 每日记录", err))
		} else {
			v := minioDailyObservation(string(data))
			out = append(out, v)
		}
	} else {
		v := componentprobe.Observation("cloudhis-39", "MinIO 每日记录", "未配置逐日采样记录，不推算每日新增", "RECORDED")
		v.ConfigurationState = "NOT_CONFIGURED"
		out = append(out, v)
	}
	if opt.BackupDirectory != "" {
		out = append(out, remoteBackupInventory(ctx, conn, opt.BackupDirectory))
	} else {
		out = append(out, componentprobe.Observation("cloudhis-5", "MinIO 备份", "未配置备份目录，当前没有可核对的备份文件证据", "RECORDED"))
	}
	return out
}

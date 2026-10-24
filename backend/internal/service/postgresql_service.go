package service

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PostgreSQLService struct {
	settingsRepo   *repository.SettingsRepo
	cancelReg      *sqlExecutionCancelRegistry
	batchCancelReg *sqlExecutionCancelRegistry
	dbPool         *sql.DB
	dbPoolMu       sync.Mutex
	dbPoolCfgHash  string
}

func NewPostgreSQLService() *PostgreSQLService {
	return &PostgreSQLService{
		settingsRepo:   repository.NewSettingsRepo(),
		cancelReg:      globalSQLExecutionCancelRegistry,
		batchCancelReg: globalSQLBatchCancelRegistry,
	}
}

var validSchemaNameRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const sqlForceWhitelistSettingKey = "postgresql_sql_force_whitelist"

const (
	sqlExecuteLockTimeoutSettingKey      = "sql_execute_lock_timeout"
	sqlExecuteStatementTimeoutSettingKey = "sql_execute_statement_timeout"
	defaultSQLExecuteLockTimeout         = "5s"
	defaultSQLExecuteStatementTimeout    = "10min"
)

var forceableBlockedSQLTypes = map[string]string{
	"UPDATE_WITHOUT_WHERE":        "UPDATE 缺少 WHERE",
	"DELETE_WITHOUT_WHERE":        "DELETE 缺少 WHERE",
	"UPDATE_TRIVIAL_WHERE":        "UPDATE 条件无有效过滤",
	"DELETE_TRIVIAL_WHERE":        "DELETE 条件无有效过滤",
	"UPDATE_WEAK_WHERE":           "UPDATE 条件过弱",
	"DELETE_WEAK_WHERE":           "DELETE 条件过弱",
	"UPDATE":                      "UPDATE 影响行数过大",
	"DELETE":                      "DELETE 影响行数过大",
	"DROP_TABLE":                  "删除表",
	"TRUNCATE":                    "清空表",
	"ALTER_COLUMN_TYPE":           "字段类型变更",
	"ALTER_SET_NOT_NULL":          "设置 NOT NULL",
	"ADD_COLUMN_DEFAULT_VOLATILE": "新增 volatile 默认值字段",
	"ADD_CHECK":                   "新增 CHECK 约束",
	"ADD_FOREIGN_KEY":             "新增外键",
	"ADD_PRIMARY_KEY":             "新增主键",
	"ADD_UNIQUE":                  "新增唯一约束",
	"CREATE_INDEX":                "创建索引",
	"CREATE_UNIQUE_INDEX":         "创建唯一索引",
	"CREATE_INDEX_EXPRESSION":     "创建表达式索引",
	"CREATE_INDEX_PARTIAL":        "创建部分索引",
}

var defaultSQLForceWhitelist = []string{
	"UPDATE_WITHOUT_WHERE",
	"DELETE_WITHOUT_WHERE",
	"UPDATE_TRIVIAL_WHERE",
	"DELETE_TRIVIAL_WHERE",
	"UPDATE_WEAK_WHERE",
	"DELETE_WEAK_WHERE",
	"UPDATE",
	"DELETE",
	"DROP_TABLE",
	"TRUNCATE",
	"ALTER_COLUMN_TYPE",
	"ALTER_SET_NOT_NULL",
	"ADD_COLUMN_DEFAULT_VOLATILE",
	"ADD_CHECK",
	"ADD_FOREIGN_KEY",
	"ADD_PRIMARY_KEY",
	"ADD_UNIQUE",
	"CREATE_INDEX",
	"CREATE_UNIQUE_INDEX",
	"CREATE_INDEX_EXPRESSION",
	"CREATE_INDEX_PARTIAL",
}

func validateSchemaName(name string) error {
	if name == "" {
		return nil
	}
	if !validSchemaNameRegex.MatchString(name) {
		return fmt.Errorf("schema 名称包含非法字符: %s", name)
	}
	return nil
}

func (s *PostgreSQLService) getDB(expectedTargets ...string) (*sql.DB, error) {
	s.dbPoolMu.Lock()
	defer s.dbPoolMu.Unlock()

	cfg, _, err := s.loadSQLExecutionConfig()
	if err != nil {
		return nil, err
	}
	if len(expectedTargets) > 0 && expectedTargets[0] != "" && releaseSQLTarget(cfg) != expectedTargets[0] {
		return nil, fmt.Errorf("数据库连接目标已变化，不能执行原数据库的视图任务")
	}
	cfgHash := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join([]string{cfg.Host, cfg.Port, cfg.Database, cfg.User, cfg.Password}, "\x00"))))

	if s.dbPool != nil && s.dbPoolCfgHash == cfgHash {
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		pingErr := s.dbPool.PingContext(pingCtx)
		cancel()
		if pingErr == nil {
			return s.dbPool, nil
		}
		s.dbPool.Close()
	}

	if s.dbPool != nil {
		_ = s.dbPool.Close()
		s.dbPool = nil
	}
	db, err := openPostgreSQL(cfg)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(10 * time.Minute)
	s.dbPool = db
	s.dbPoolCfgHash = cfgHash
	return db, nil
}

type PostgreSQLConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
}

type PostgreSQLInstanceInfo struct {
	Config       PostgreSQLConfig  `json:"config"`
	Status       string            `json:"status"`
	Message      string            `json:"message"`
	Version      string            `json:"version"`
	CurrentDB    string            `json:"currentDb"`
	CurrentUser  string            `json:"currentUser"`
	Role         string            `json:"role"`
	ServerAddr   string            `json:"serverAddr"`
	ServerPort   int               `json:"serverPort"`
	Settings     map[string]string `json:"settings"`
	Replications []ReplicationInfo `json:"replications"`
	CheckedAt    time.Time         `json:"checkedAt"`
}

type ReplicationInfo struct {
	ClientAddr string `json:"clientAddr"`
	State      string `json:"state"`
	SyncState  string `json:"syncState"`
	WriteLag   string `json:"writeLag"`
	FlushLag   string `json:"flushLag"`
	ReplayLag  string `json:"replayLag"`
}

type ParseSQLRequest struct {
	CreatedUser string `json:"-"`
	SystemCode  string `json:"systemCode"`
	Environment string `json:"environment"`
	SchemaName  string `json:"schemaName"`
	Version     string `json:"version"`
	GroupSortNo int    `json:"groupSortNo"`
	FileName    string `json:"fileName"`
	Content     string `json:"content"`
	Overwrite   bool   `json:"overwrite"`
}

type UpdateSQLFileContentRequest struct {
	SystemCode  string `json:"systemCode"`
	Environment string `json:"environment"`
	SchemaName  string `json:"schemaName"`
	Version     string `json:"version"`
	GroupSortNo int    `json:"groupSortNo"`
	FileName    string `json:"fileName"`
	Content     string `json:"content" binding:"required"`
}

type UpdateSQLStatementContentRequest struct {
	Content string `json:"content" binding:"required"`
}

type RiskAnalysis struct {
	SQLType       string
	RiskLevel     string
	RiskReason    string
	EstimatedRows int64
}

type SQLExecuteOptions struct {
	SkipExistsColumn        bool   `json:"skipExistsColumn"`
	SkipExistsTable         bool   `json:"skipExistsTable"`
	SkipUniqueConstraint    bool   `json:"skipUniqueConstraint"`
	RequireRiskConfirmation bool   `json:"requireRiskConfirmation"`
	ConfirmWarnRisk         bool   `json:"confirmWarnRisk"`
	ForceBlockedSQL         bool   `json:"forceBlockedSql"`
	LockTimeout             string `json:"lockTimeout"`
	StatementTimeout        string `json:"statementTimeout"`
}

type ExecuteSQLContentRequest struct {
	ParseSQLRequest
	Options SQLExecuteOptions `json:"options"`
}

type ImportServerSQLRequest struct {
	CreatedUser string `json:"-"`
	FilePath    string `json:"filePath" binding:"required"`
	Overwrite   bool   `json:"overwrite"`
}

type SQLForceWhitelistOption struct {
	SQLType string `json:"sqlType"`
	Label   string `json:"label"`
}

type SQLForceWhitelistConfig struct {
	Available []SQLForceWhitelistOption `json:"available"`
	Enabled   []string                  `json:"enabled"`
}

type SQLExportStatement struct {
	LineNumber     int
	SQLContent     string
	ExecuteStatus  string
	ExecuteMessage string
	RiskReason     string
}

type ParseSQLBatchFile struct {
	FileName string `json:"fileName" binding:"required"`
	Content  string `json:"content"`
}

type ParseSQLBatchRequest struct {
	CreatedUser string              `json:"-"`
	BatchName   string              `json:"batchName"`
	Overwrite   bool                `json:"overwrite"`
	Files       []ParseSQLBatchFile `json:"files" binding:"required"`
}

type sqlExecutionCancelRegistry struct {
	mu      sync.Mutex
	cancels map[uint]context.CancelFunc
}

var globalSQLExecutionCancelRegistry = newSQLExecutionCancelRegistry()
var globalSQLBatchCancelRegistry = newSQLExecutionCancelRegistry()

func newSQLExecutionCancelRegistry() *sqlExecutionCancelRegistry {
	return &sqlExecutionCancelRegistry{cancels: map[uint]context.CancelFunc{}}
}

func (r *sqlExecutionCancelRegistry) register(fileID uint, cancel context.CancelFunc) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cancels[fileID]; exists {
		return false
	}
	r.cancels[fileID] = cancel
	return true
}

func (r *sqlExecutionCancelRegistry) unregister(fileID uint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, fileID)
}

func (r *sqlExecutionCancelRegistry) cancel(fileID uint) bool {
	r.mu.Lock()
	cancel, exists := r.cancels[fileID]
	r.mu.Unlock()
	if !exists {
		return false
	}
	cancel()
	return true
}

func (s *PostgreSQLService) GetInstanceInfo(ctx context.Context) (*PostgreSQLInstanceInfo, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	info := &PostgreSQLInstanceInfo{
		Config: PostgreSQLConfig{
			Host: cfg.Host, Port: cfg.Port, Database: cfg.Database,
			User: cfg.User, Password: maskPassword(cfg.Password),
		},
		Status:    "UNKNOWN",
		Settings:  map[string]string{},
		CheckedAt: time.Now(),
	}
	if cfg.Host == "" {
		info.Status = "UNCONFIGURED"
		info.Message = "PostgreSQL 主机地址未配置"
		return info, nil
	}

	db, err := openPostgreSQL(cfg)
	if err != nil {
		info.Status = "DOWN"
		info.Message = err.Error()
		return info, nil
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		info.Status = "DOWN"
		info.Message = err.Error()
		return info, nil
	}

	info.Status = "UP"
	info.Message = "连接正常"
	_ = db.QueryRowContext(ctx, `SELECT version(), current_database(), current_user, COALESCE(inet_server_addr()::text,''), COALESCE(inet_server_port(),0), CASE WHEN pg_is_in_recovery() THEN 'standby' ELSE 'primary' END`).Scan(
		&info.Version, &info.CurrentDB, &info.CurrentUser, &info.ServerAddr, &info.ServerPort, &info.Role,
	)
	info.Settings = readPGSettings(ctx, db)
	if info.Role == "primary" {
		info.Replications = readReplications(ctx, db)
	}
	return info, nil
}

func (s *PostgreSQLService) GetSQLForceWhitelist() (SQLForceWhitelistConfig, error) {
	enabled, err := s.loadSQLForceWhitelist()
	if err != nil {
		return SQLForceWhitelistConfig{}, err
	}
	return SQLForceWhitelistConfig{
		Available: availableSQLForceWhitelistOptions(),
		Enabled:   enabled,
	}, nil
}

func (s *PostgreSQLService) SaveSQLForceWhitelist(enabled []string) (SQLForceWhitelistConfig, error) {
	normalized := mergeDefaultSQLForceWhitelist(enabled)
	payload, err := json.Marshal(normalized)
	if err != nil {
		return SQLForceWhitelistConfig{}, err
	}
	if err := s.settingsRepo.Set(sqlForceWhitelistSettingKey, string(payload)); err != nil {
		return SQLForceWhitelistConfig{}, err
	}
	return SQLForceWhitelistConfig{
		Available: availableSQLForceWhitelistOptions(),
		Enabled:   normalized,
	}, nil
}

func (s *PostgreSQLService) loadSQLForceWhitelist() ([]string, error) {
	raw, err := s.settingsRepo.GetByKey(sqlForceWhitelistSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return append([]string(nil), defaultSQLForceWhitelist...), nil
	}
	var enabled []string
	if err := json.Unmarshal([]byte(raw), &enabled); err != nil {
		return nil, fmt.Errorf("SQL 强制执行白名单配置格式错误")
	}
	return mergeDefaultSQLForceWhitelist(enabled), nil
}

func (s *PostgreSQLService) applySQLExecuteSettings(options SQLExecuteOptions) SQLExecuteOptions {
	if strings.TrimSpace(options.LockTimeout) == "" {
		if value, _ := s.settingsRepo.GetByKey(sqlExecuteLockTimeoutSettingKey); strings.TrimSpace(value) != "" {
			options.LockTimeout = value
		}
	}
	if strings.TrimSpace(options.StatementTimeout) == "" {
		if value, _ := s.settingsRepo.GetByKey(sqlExecuteStatementTimeoutSettingKey); strings.TrimSpace(value) != "" {
			options.StatementTimeout = value
		}
	}
	options.LockTimeout = normalizeSQLTimeoutLiteral(options.LockTimeout, defaultSQLExecuteLockTimeout)
	options.StatementTimeout = normalizeSQLTimeoutLiteral(options.StatementTimeout, defaultSQLExecuteStatementTimeout)
	return options
}

func availableSQLForceWhitelistOptions() []SQLForceWhitelistOption {
	types := make([]string, 0, len(forceableBlockedSQLTypes))
	for sqlType := range forceableBlockedSQLTypes {
		types = append(types, sqlType)
	}
	sort.Strings(types)
	options := make([]SQLForceWhitelistOption, 0, len(types))
	for _, sqlType := range types {
		options = append(options, SQLForceWhitelistOption{SQLType: sqlType, Label: forceableBlockedSQLTypes[sqlType]})
	}
	return options
}

func normalizeSQLForceWhitelist(enabled []string) []string {
	seen := make(map[string]bool, len(enabled))
	normalized := make([]string, 0, len(enabled))
	for _, sqlType := range enabled {
		sqlType = strings.ToUpper(strings.TrimSpace(sqlType))
		if sqlType == "" || seen[sqlType] || !isForceableBlockedSQLType(sqlType) {
			continue
		}
		seen[sqlType] = true
		normalized = append(normalized, sqlType)
	}
	sort.Strings(normalized)
	return normalized
}

func mergeDefaultSQLForceWhitelist(enabled []string) []string {
	merged := make([]string, 0, len(defaultSQLForceWhitelist)+len(enabled))
	merged = append(merged, defaultSQLForceWhitelist...)
	merged = append(merged, enabled...)
	return normalizeSQLForceWhitelist(merged)
}

func isForceableBlockedSQLType(sqlType string) bool {
	_, ok := forceableBlockedSQLTypes[strings.ToUpper(strings.TrimSpace(sqlType))]
	return ok
}

func forceWhitelistContains(whitelist []string, sqlType string) bool {
	sqlType = strings.ToUpper(strings.TrimSpace(sqlType))
	if !isForceableBlockedSQLType(sqlType) {
		return false
	}
	for _, item := range whitelist {
		if strings.EqualFold(strings.TrimSpace(item), sqlType) {
			return true
		}
	}
	return false
}

func (s *PostgreSQLService) CreateSQLViewDependencyTask(req SQLViewDependencyTaskRequest, username string) (*model.SQLViewDependencyTask, error) {
	refs, err := validateViewDependencyAlterSQLs(req)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("ALTER SQL 不能为空")
	}
	lockTimeout := strings.TrimSpace(req.LockTimeout)
	if lockTimeout == "" {
		lockTimeout = "3s"
	}
	statementTimeout := strings.TrimSpace(req.StatementTimeout)
	if statementTimeout == "" {
		statementTimeout = "10min"
	}
	if _, _, _, err := viewTimeouts(lockTimeout, statementTimeout); err != nil {
		return nil, err
	}
	executionMode, err := normalizeSQLViewDependencyExecutionMode(req.ExecutionMode)
	if err != nil {
		return nil, err
	}
	firstRef := refs[0]
	task := &model.SQLViewDependencyTask{
		SchemaName:       firstRef.schema,
		TableName:        firstRef.table,
		ColumnName:       firstRef.column,
		AlterSQL:         strings.TrimSpace(req.AlterSQL),
		Status:           "CREATED",
		ExecutionMode:    executionMode,
		RiskLevel:        "WARN",
		RiskReason:       "字段属性变更涉及视图依赖时会短暂删除并恢复视图，默认仅生成执行计划",
		LockTimeout:      lockTimeout,
		StatementTimeout: statementTimeout,
		Operator:         strings.TrimSpace(username),
	}
	if err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		for _, ref := range refs {
			change := buildSQLViewDependencyChange(task.ID, ref, "CREATED")
			if err := tx.Create(&change).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *PostgreSQLService) GetSQLViewDependencyTask(id uint) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	var task model.SQLViewDependencyTask
	if err := repository.DB.Where("is_deleted = ?", false).First(&task, id).Error; err != nil {
		return nil, nil, err
	}
	var items []model.SQLViewDependencyItem
	if err := repository.DB.Where("task_id = ?", id).Order("drop_order ASC, id ASC").Find(&items).Error; err != nil {
		return nil, nil, err
	}
	return &task, items, nil
}

func (s *PostgreSQLService) GetSQLViewDependencyChanges(taskID uint) ([]model.SQLViewDependencyChange, error) {
	var changes []model.SQLViewDependencyChange
	if err := repository.DB.Where("task_id = ?", taskID).Order("change_order ASC, id ASC").Find(&changes).Error; err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		return changes, nil
	}
	var task model.SQLViewDependencyTask
	if err := repository.DB.Where("is_deleted = ?", false).First(&task, taskID).Error; err != nil {
		return nil, err
	}
	return ensureSQLViewDependencyChanges(task)
}

func (s *PostgreSQLService) GetSQLViewDependencySteps(taskID uint) ([]model.SQLViewDependencyStep, error) {
	var steps []model.SQLViewDependencyStep
	if err := repository.DB.Where("task_id = ?", taskID).Order("step_order ASC, id ASC").Find(&steps).Error; err != nil {
		return nil, err
	}
	return steps, nil
}

func buildSQLViewDependencyChange(taskID uint, ref alterColumnRef, status string) model.SQLViewDependencyChange {
	line := ref.line
	if line <= 0 {
		line = 1
	}
	order := ref.order
	if order <= 0 {
		order = 1
	}
	return model.SQLViewDependencyChange{
		TaskID:      taskID,
		ChangeOrder: order,
		LineNumber:  line,
		SchemaName:  ref.schema,
		TableName:   ref.table,
		ColumnName:  ref.column,
		AlterSQL:    strings.TrimSpace(ref.sql),
		Status:      status,
	}
}

func ensureSQLViewDependencyChanges(task model.SQLViewDependencyTask) ([]model.SQLViewDependencyChange, error) {
	var changes []model.SQLViewDependencyChange
	if err := repository.DB.Where("task_id = ?", task.ID).Order("change_order ASC, id ASC").Find(&changes).Error; err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		return changes, nil
	}
	refs, err := validateViewDependencyAlterSQLs(SQLViewDependencyTaskRequest{
		AlterSQL: task.AlterSQL,
	})
	if err != nil {
		return nil, err
	}
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		for _, ref := range refs {
			change := buildSQLViewDependencyChange(task.ID, ref, task.Status)
			if err := tx.Create(&change).Error; err != nil {
				return err
			}
			changes = append(changes, change)
		}
		return nil
	})
	return changes, err
}

func (s *PostgreSQLService) ListSQLViewDependencyTasks(page, pageSize int) ([]model.SQLViewDependencyTask, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var total int64
	var tasks []model.SQLViewDependencyTask
	query := repository.DB.Model(&model.SQLViewDependencyTask{}).Where("is_deleted = ?", false)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error
	return tasks, total, err
}

func (s *PostgreSQLService) AnalyzeSQLViewDependencyTask(ctx context.Context, id uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	claimed, _, err := s.markSQLViewDependencyTaskRunning(id, "ANALYZING", "分析依赖中", username, nil)
	if err != nil {
		return nil, nil, err
	}
	defer settleViewTaskPhase(id, "ANALYZING", username)
	task := *claimed
	if err := s.ensureSQLViewSourceContext(ctx, &task); err != nil {
		_, saveErr := markSQLViewDependencyTaskStatus(id, "FAILED", err.Error(), username)
		return nil, nil, errors.Join(err, saveErr)
	}
	changes, err := ensureSQLViewDependencyChanges(task)
	if err != nil {
		return nil, nil, err
	}
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		return nil, nil, err
	}
	snapshots, err := s.loadViewDependencySnapshotsForChanges(ctx, db, changes)
	if err != nil {
		return nil, nil, err
	}
	items := make([]model.SQLViewDependencyItem, 0, len(snapshots))
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ?", task.ID).Delete(&model.SQLViewDependencyItem{}).Error; err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			item := buildSQLViewDependencyItemFromSnapshot(task.ID, snapshot)
			if err := tx.Create(&item).Error; err != nil {
				return err
			}
			items = append(items, item)
		}
		if err := tx.Model(&model.SQLViewDependencyChange{}).
			Where("task_id = ?", task.ID).
			Updates(map[string]interface{}{
				"status":        "ANALYZED",
				"error_message": "",
			}).Error; err != nil {
			return err
		}
		now := time.Now()
		task.Status = "ANALYZED"
		task.Operator = strings.TrimSpace(username)
		task.AnalyzedAt = &now
		task.ExecuteMessage = fmt.Sprintf("已分析字段变更 %d 条，依赖视图 %d 个", len(changes), len(items))
		result := tx.Model(&model.SQLViewDependencyTask{}).Where("id = ? AND status = ? AND is_deleted = false", task.ID, "ANALYZING").Updates(map[string]any{"status": task.Status, "operator": task.Operator, "analyzed_at": task.AnalyzedAt, "execute_message": task.ExecuteMessage})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("视图任务分析权已失效")
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &task, items, nil
}

func (s *PostgreSQLService) ExportSQLViewDependencyPlan(id uint) (string, error) {
	task, items, err := s.GetSQLViewDependencyTask(id)
	if err != nil {
		return "", err
	}
	if err := ensureSQLViewDependencyTaskHasPlan(*task, items); err != nil {
		return "", err
	}
	return BuildSQLViewDependencyManualPlan(*task, items), nil
}

func (s *PostgreSQLService) ExportSQLViewDependencyRestorePlan(id uint) (string, error) {
	task, items, err := s.GetSQLViewDependencyTask(id)
	if err != nil {
		return "", err
	}
	if err := ensureSQLViewDependencyTaskHasPlan(*task, items); err != nil {
		return "", err
	}
	return BuildSQLViewDependencyRestorePlan(*task, items), nil
}

func (s *PostgreSQLService) PrecheckSQLViewDependencyTask(ctx context.Context, id uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	task, items, err := s.markSQLViewDependencyTaskRunning(id, "PRECHECKING", "预检中", username, ensureSQLViewDependencyTaskHasPlan)
	if err != nil {
		return nil, nil, err
	}
	defer settleViewTaskPhase(id, "PRECHECKING", username)
	changes, err := ensureSQLViewDependencyChanges(*task)
	if err != nil {
		_, saveErr := markSQLViewDependencyTaskStatus(id, "FAILED", err.Error(), username)
		return nil, nil, errors.Join(err, saveErr)
	}
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		return nil, nil, err
	}
	precheckCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	inspector := NewSQLMetadataInspector(db)
	tableSeen := make(map[string]alterColumnRef)
	for _, change := range changes {
		if _, err := inspector.GetColumnInfo(precheckCtx, change.SchemaName, change.TableName, change.ColumnName); err != nil {
			msg := fmt.Sprintf("目标字段 %s.%s.%s 不存在或无法读取: %s", change.SchemaName, change.TableName, change.ColumnName, err.Error())
			_, _ = markSQLViewDependencyTaskStatus(task.ID, "PRECHECK_FAILED", msg, username)
			return nil, nil, err
		}
		key := strings.ToLower(strings.TrimSpace(change.SchemaName)) + "." + strings.ToLower(strings.TrimSpace(change.TableName))
		tableSeen[key] = alterColumnRef{schema: change.SchemaName, table: change.TableName}
	}
	for _, table := range tableSeen {
		waitingLocks, err := countWaitingLocksForRelation(precheckCtx, db, table.schema, table.table)
		if err != nil {
			_, _ = markSQLViewDependencyTaskStatus(task.ID, "PRECHECK_FAILED", "锁检查失败: "+err.Error(), username)
			return nil, nil, err
		}
		if waitingLocks > 0 {
			msg := fmt.Sprintf("目标表 %s.%s 当前存在 %d 个等待锁，建议稍后再执行", table.schema, table.table, waitingLocks)
			_, _ = markSQLViewDependencyTaskStatus(task.ID, "PRECHECK_FAILED", msg, username)
			return nil, nil, errors.New(msg)
		}
	}
	if err := s.validateCurrentViewDependencyBackup(precheckCtx, db, *task, changes, items); err != nil {
		_, _ = markSQLViewDependencyTaskStatus(task.ID, "PRECHECK_FAILED", err.Error(), username)
		return nil, nil, err
	}
	msg := fmt.Sprintf("预检通过，字段变更 %d 条，依赖视图 %d 个；执行变更将使用 lock_timeout=%s", len(changes), len(items), task.LockTimeout)
	updated, err := markSQLViewDependencyTaskStatus(task.ID, "PRECHECK_PASSED", msg, username)
	if err != nil {
		return nil, nil, err
	}
	return updated, items, nil
}

func (s *PostgreSQLService) prepareSQLViewDependencyExecution(ctx context.Context, id uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	taskForPrecheck, _, err := s.GetSQLViewDependencyTask(id)
	if err != nil {
		return nil, nil, err
	}
	if viewTaskBusy(taskForPrecheck.Status) || (taskForPrecheck.Status == "UNKNOWN" || taskForPrecheck.OutcomeUncertain) {
		return nil, nil, fmt.Errorf("任务正在处理或执行结果待核实，不能重复执行")
	}
	if taskForPrecheck.Status != "PRECHECK_PASSED" {
		if _, _, err := s.PrecheckSQLViewDependencyTask(ctx, id, username); err != nil {
			return nil, nil, err
		}
	}
	task, items, err := s.markSQLViewDependencyTaskRunning(id, "EXECUTING", "开始执行字段变更", username, ensureSQLViewDependencyTaskCanExecute)
	if err != nil {
		return nil, nil, err
	}
	return task, items, nil
}

func (s *PostgreSQLService) ExecuteSQLViewDependencyTask(ctx context.Context, id uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	task, items, err := s.prepareSQLViewDependencyExecution(ctx, id, username)
	if err != nil {
		return nil, nil, err
	}
	return s.executeClaimedSQLViewDependencyTask(ctx, task, items, username)
}

func (s *PostgreSQLService) executeClaimedSQLViewDependencyTask(ctx context.Context, task *model.SQLViewDependencyTask, items []model.SQLViewDependencyItem, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	id := task.ID
	defer settleViewTaskPhase(id, "EXECUTING", username)
	if err := s.ensureSQLViewSourceContext(ctx, task); err != nil {
		updated, saveErr := markSQLViewDependencyTaskStatus(id, "FAILED", err.Error(), username)
		return updated, items, errors.Join(err, saveErr)
	}
	changes, err := ensureSQLViewDependencyChanges(*task)
	if err != nil {
		_, saveErr := markSQLViewDependencyTaskStatus(id, "FAILED", err.Error(), username)
		return nil, nil, errors.Join(err, saveErr)
	}
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		_, saveErr := markSQLViewDependencyTaskStatus(id, "FAILED", err.Error(), username)
		return nil, nil, errors.Join(err, saveErr)
	}
	if err := s.validateCurrentViewDependencyBackup(ctx, db, *task, changes, items); err != nil {
		updated, saveErr := markSQLViewDependencyTaskStatus(task.ID, "PRECHECK_FAILED", err.Error(), username)
		if saveErr != nil {
			return nil, nil, saveErr
		}
		return updated, items, err
	}
	executionMode := task.ExecutionMode
	if strings.TrimSpace(executionMode) == "" {
		executionMode = SQLViewDependencyExecutionModeStep
	}
	if executionMode == SQLViewDependencyExecutionModeTransaction {
		err = s.runSQLViewDependencyStepsOnDB(ctx, db, task.ID, BuildSQLViewDependencyTransactionalSteps(*task, items))
	} else {
		err = s.runSQLViewDependencyStepExecutionOnDB(ctx, db, task.ID, BuildSQLViewDependencyStepExecutionPlan(*task, items))
	}
	if err != nil {
		status := "FAILED"
		msg := "执行变更失败: " + err.Error()
		var stepErr SQLViewDependencyStepExecutionError
		if errors.As(err, &stepErr) {
			if stepErr.RestoreAttempted && stepErr.RestoreSucceeded {
				status = "RESTORED"
				msg = "字段变更失败，已按备份恢复视图: " + stepErr.Err.Error()
			} else if stepErr.RestoreAttempted && !stepErr.RestoreSucceeded {
				status = "RESTORE_FAILED"
				msg = "字段变更失败，且自动恢复视图失败，请查看备份并恢复视图: " + err.Error()
			}
		} else if executionMode == SQLViewDependencyExecutionModeTransaction {
			msg = "事务执行失败，请核实回滚及目标对象状态: " + err.Error()
		}
		var unknown sqlViewOutcomeUnknownError
		if errors.As(err, &unknown) {
			status = "UNKNOWN"
			msg = "数据库结果待核实，不能直接重放变更: " + err.Error()
		}
		updated, saveErr := markSQLViewDependencyTaskStatus(task.ID, status, msg, username)
		if saveErr != nil {
			return nil, nil, saveErr
		}
		return updated, items, err
	}
	if err := db.QueryRowContext(ctx, "SELECT current_user").Scan(&task.DatabaseUser); err != nil {
		return nil, nil, err
	}
	now := time.Now()
	task.Status = "SUCCESS"
	task.Operator = strings.TrimSpace(username)
	task.ExecutedAt = &now
	if executionMode == SQLViewDependencyExecutionModeTransaction {
		task.ExecuteMessage = fmt.Sprintf("事务执行完成，字段变更 %d 条，恢复视图 %d 个", len(changes), len(items))
	} else {
		task.ExecuteMessage = fmt.Sprintf("分步执行完成，字段变更 %d 条，恢复视图 %d 个", len(changes), len(items))
	}
	if err := repository.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.SQLViewDependencyTask{}).Where("id = ? AND status = ? AND is_deleted = false", task.ID, "EXECUTING").Updates(map[string]any{"status": task.Status, "operator": task.Operator, "executed_at": task.ExecutedAt, "execute_message": task.ExecuteMessage, "database_user": task.DatabaseUser})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("视图任务执行权已失效")
		}
		if err := tx.Model(&model.SQLViewDependencyChange{}).
			Where("task_id = ?", task.ID).
			Updates(map[string]interface{}{
				"status":        "SUCCESS",
				"error_message": "",
			}).Error; err != nil {
			return err
		}
		return completeSQLViewTaskSource(tx, task, username)
	}); err != nil {
		return nil, nil, err
	}
	return task, items, nil
}

func (s *PostgreSQLService) RestoreSQLViewDependencyTask(ctx context.Context, id uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	task, items, err := s.markSQLViewDependencyTaskRunning(id, "RESTORING", "开始恢复视图", username, ensureSQLViewDependencyTaskHasPlan)
	if err != nil {
		return nil, nil, err
	}
	defer settleViewTaskPhase(id, "RESTORING", username)
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		_, saveErr := markSQLViewDependencyTaskStatus(id, "FAILED", err.Error(), username)
		return nil, nil, errors.Join(err, saveErr)
	}
	steps := BuildSQLViewDependencyRestoreTransactionalSteps(*task, items)
	err = s.runSQLViewDependencyAdhocStepsOnDB(ctx, db, task.ID, steps)
	if err != nil {
		msg := "恢复视图失败，请查看备份并恢复视图: " + err.Error()
		updated, saveErr := markSQLViewDependencyTaskStatus(task.ID, "RESTORE_FAILED", msg, username)
		if saveErr != nil {
			return nil, nil, saveErr
		}
		return updated, items, err
	}
	updated, err := markSQLViewDependencyTaskStatus(task.ID, "RESTORED", fmt.Sprintf("已恢复视图 %d 个", len(items)), username)
	if err != nil {
		return nil, nil, err
	}
	return updated, items, nil
}

func (s *PostgreSQLService) RestoreSQLViewDependencyItem(ctx context.Context, taskID, itemID uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	task, item, previousStatus, err := s.claimSQLViewDependencyItemAction(taskID, itemID, "RESTORING", username)
	if err != nil {
		return nil, nil, err
	}
	defer settleViewTaskPhase(taskID, "RESTORING", username)
	steps := BuildSQLViewDependencyItemRestoreSteps(item)
	if len(steps) == 0 {
		return nil, nil, fmt.Errorf("依赖对象 %s.%s 没有可恢复的备份 SQL", item.ObjectSchema, item.ObjectName)
	}
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		return nil, nil, err
	}
	err = s.runSQLViewDependencyAdhocStepsOnDB(ctx, db, task.ID, steps)
	if saveErr := updateSQLViewDependencyItemActionResult(task.ID, item.ID, username, previousStatus, "RESTORED", "已恢复视图 "+viewDependencyQualifiedName(item), err, "RESTORE_FAILED"); saveErr != nil {
		return nil, nil, saveErr
	}
	updatedTask, items, loadErr := s.GetSQLViewDependencyTask(task.ID)
	if loadErr != nil {
		return nil, nil, loadErr
	}
	if err != nil {
		return updatedTask, items, err
	}
	return updatedTask, items, nil
}

func (s *PostgreSQLService) VerifySQLViewDependencyItem(ctx context.Context, taskID, itemID uint, username string) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	task, item, previousStatus, err := s.claimSQLViewDependencyItemAction(taskID, itemID, "VERIFYING", username)
	if err != nil {
		return nil, nil, err
	}
	defer settleViewTaskPhase(taskID, "VERIFYING", username)
	steps := BuildSQLViewDependencyItemVerifySteps(item)
	if len(steps) == 0 {
		return nil, nil, fmt.Errorf("依赖对象 %s.%s 没有可校验的 SQL", item.ObjectSchema, item.ObjectName)
	}
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		return nil, nil, err
	}
	err = s.runSQLViewDependencyAdhocStepsOnDB(ctx, db, task.ID, steps)
	if saveErr := updateSQLViewDependencyItemActionResult(task.ID, item.ID, username, previousStatus, "VERIFIED", "视图校验通过 "+viewDependencyQualifiedName(item), err, "VERIFY_FAILED"); saveErr != nil {
		return nil, nil, saveErr
	}
	updatedTask, items, loadErr := s.GetSQLViewDependencyTask(task.ID)
	if loadErr != nil {
		return nil, nil, loadErr
	}
	if err != nil {
		return updatedTask, items, err
	}
	return updatedTask, items, nil
}

func (s *PostgreSQLService) claimSQLViewDependencyItemAction(taskID, itemID uint, phase, username string) (*model.SQLViewDependencyTask, model.SQLViewDependencyItem, string, error) {
	var item model.SQLViewDependencyItem
	var previousStatus string
	task, _, err := s.markSQLViewDependencyTaskRunning(taskID, phase, "处理依赖对象中", username, func(task model.SQLViewDependencyTask, items []model.SQLViewDependencyItem) error {
		previousStatus = task.Status
		for _, candidate := range items {
			if candidate.ID == itemID {
				item = candidate
				return nil
			}
		}
		return gorm.ErrRecordNotFound
	})
	return task, item, previousStatus, err
}

func updateSQLViewDependencyItemActionResult(taskID, itemID uint, username, previousStatus, successStatus, successMessage string, actionErr error, failureStatus string) error {
	status := successStatus
	message := successMessage
	itemError := ""
	if actionErr != nil {
		status = failureStatus
		message = actionErr.Error()
		itemError = actionErr.Error()
	}
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.SQLViewDependencyItem{}).Where("task_id = ? AND id = ?", taskID, itemID).Updates(map[string]interface{}{
			"status":        status,
			"error_message": itemError,
		}).Error; err != nil {
			return err
		}
		taskStatus := previousStatus
		if actionErr != nil {
			taskStatus = failureStatus
			var unknown sqlViewOutcomeUnknownError
			if errors.As(actionErr, &unknown) {
				taskStatus = "UNKNOWN"
			}
		}
		if taskStatus == "UNKNOWN" {
			if err := tx.Model(&model.SQLViewDependencyTask{}).Where("id = ?", taskID).Update("outcome_uncertain", true).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.SQLViewDependencyTask{}).Where("id = ? AND status IN ?", taskID, []string{"RESTORING", "VERIFYING"}).Updates(map[string]interface{}{
			"status":          taskStatus,
			"execute_message": message,
			"operator":        strings.TrimSpace(username),
		}).Error
	})
}

func viewDependencyQualifiedName(item model.SQLViewDependencyItem) string {
	return strings.Trim(strings.TrimSpace(item.ObjectSchema)+"."+strings.TrimSpace(item.ObjectName), ".")
}

type sqlConnViewDependencyExecutor struct {
	ctx         context.Context
	conn        *sql.Conn
	timeout     time.Duration
	nameContext *sqlViewConnectionContext
}

func (e sqlConnViewDependencyExecutor) Exec(sqlText string) error {
	ctx := e.ctx
	cancel := func() {}
	if e.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, e.timeout)
	}
	defer cancel()
	if err := e.nameContext.apply(ctx, e.conn, sqlText); err != nil {
		return err
	}
	_, err := e.conn.ExecContext(ctx, sqlText)
	return err
}

func (s *PostgreSQLService) runSQLViewDependencyStepsOnDB(ctx context.Context, db *sql.DB, taskID uint, steps []string) error {
	models, err := replaceSQLViewDependencySteps(taskID, steps)
	if err != nil {
		return err
	}
	return runLoggedViewSQL(ctx, db, taskID, steps, models)
}
func (s *PostgreSQLService) runSQLViewDependencyAdhocStepsOnDB(ctx context.Context, db *sql.DB, taskID uint, steps []string) error {
	models, err := appendSQLViewDependencySteps(taskID, steps)
	if err != nil {
		return err
	}
	return runLoggedViewSQL(ctx, db, taskID, steps, models)
}

func runLoggedViewSQL(ctx context.Context, db *sql.DB, taskID uint, steps []string, models []model.SQLViewDependencyStep) (resultErr error) {
	var task model.SQLViewDependencyTask
	if err := repository.DB.First(&task, taskID).Error; err != nil {
		return err
	}
	conn, cleanup, timeout, err := guardedViewConnection(ctx, db, task.LockTimeout, task.StatementTimeout)
	if err != nil {
		return err
	}
	defer cleanup()
	var nameContext *sqlViewConnectionContext
	for _, step := range steps {
		if releaseSQLNode(step).GetAlterTableStmt() != nil {
			setupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			nameContext, err = newSQLViewConnectionContext(setupCtx, conn, task)
			cancel()
			if err != nil {
				return err
			}
			break
		}
	}
	executor := sqlConnViewDependencyExecutor{ctx: ctx, conn: conn, timeout: timeout, nameContext: nameContext}
	inTx := false
	var txSteps []uint
	defer func() {
		if !inTx {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(rollbackCtx, "ROLLBACK;"); err != nil {
			resultErr = errors.Join(resultErr, sqlViewOutcomeUnknownError{err})
			return
		}
		if err := appendSQLViewDependencyRollbackStep(taskID); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
		for _, id := range txSteps {
			resultErr = errors.Join(resultErr, markSQLViewDependencyStepFinished(id, "ROLLED_BACK", "事务已回滚，本步骤未提交"))
		}
	}()
	for idx, text := range steps {
		if err := markSQLViewDependencyStepRunning(models[idx].ID); err != nil {
			return err
		}
		execErr := executor.Exec(text)
		if execErr != nil {
			status := "FAILED"
			if pgState(execErr) == "" {
				status = "UNKNOWN"
				execErr = sqlViewOutcomeUnknownError{execErr}
			}
			if err := markSQLViewDependencyStepFinished(models[idx].ID, status, execErr.Error()); err != nil {
				execErr = errors.Join(execErr, sqlViewOutcomeUnknownError{err})
			}
			for pending := idx + 1; pending < len(models); pending++ {
				execErr = errors.Join(execErr, markSQLViewDependencyStepFinished(models[pending].ID, "SKIPPED", "前序步骤失败，未执行"))
			}
			return execErr
		}
		switch strings.ToUpper(strings.TrimSpace(text)) {
		case "BEGIN;":
			inTx = true
		case "COMMIT;", "ROLLBACK;":
			inTx = false
		default:
			if inTx {
				txSteps = append(txSteps, models[idx].ID)
			}
		}
		if err := markSQLViewDependencyStepFinished(models[idx].ID, "SUCCESS", ""); err != nil {
			return sqlViewOutcomeUnknownError{err}
		}
	}
	return nil
}

func (s *PostgreSQLService) runSQLViewDependencyStepExecutionOnDB(ctx context.Context, db *sql.DB, taskID uint, plan SQLViewDependencyStepExecutionPlan) error {
	models, err := replaceSQLViewDependencySteps(taskID, plan.AllSteps())
	if err != nil {
		return err
	}
	conn, cleanup, timeout, err := guardedViewConnection(ctx, db, plan.LockTimeout, plan.StatementTimeout)
	if err != nil {
		return err
	}
	defer cleanup()
	setupCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	nameContext, err := newSQLViewConnectionContext(setupCtx, conn, plan.sourceTask)
	cancel()
	if err != nil {
		return err
	}
	var recoveryConn *sql.Conn
	var recoveryCleanup func()
	defer func() {
		if recoveryCleanup != nil {
			recoveryCleanup()
		}
	}()
	run := func(index int, stage, text string, recovering bool) error {
		if err := markSQLViewDependencyStepRunning(models[index].ID); err != nil {
			return fmt.Errorf("保存步骤执行前状态失败，未提交本步骤: %w", err)
		}
		stepCtx, stepConn := ctx, conn
		if recovering {
			stepCtx = context.WithoutCancel(ctx)
			if recoveryConn == nil {
				var err error
				recoveryConn, recoveryCleanup, _, err = guardedViewConnection(stepCtx, db, plan.LockTimeout, plan.StatementTimeout)
				if err != nil {
					saveErr := markSQLViewDependencyStepFinished(models[index].ID, "FAILED", err.Error())
					return errors.Join(err, saveErr)
				}
			}
			stepConn = recoveryConn
		}
		stepContext := nameContext
		if recovering {
			stepContext = nil // Recovery runs only backup SQL in the original admin context.
		}
		err := (sqlConnViewDependencyExecutor{ctx: stepCtx, conn: stepConn, timeout: timeout, nameContext: stepContext}).Exec(text)
		status, message := "SUCCESS", ""
		if err != nil {
			status, message = "FAILED", err.Error()
			if pgState(err) == "" {
				status = "UNKNOWN"
				err = sqlViewOutcomeUnknownError{err}
			}
		}
		if saveErr := markSQLViewDependencyStepFinished(models[index].ID, status, message); saveErr != nil {
			resultErr := errors.Join(err, sqlViewOutcomeUnknownError{fmt.Errorf("步骤已提交，但保存结果失败: %w", saveErr)})
			if err == nil {
				return sqlViewStepCommittedError{resultErr}
			}
			return resultErr
		}
		return err
	}
	return runViewStepPlan(plan, run, func(index int) error {
		return markSQLViewDependencyStepFinished(models[index].ID, "SKIPPED", "前序步骤失败或对象未删除，本步骤未执行")
	})
}

func (s *PostgreSQLService) validateCurrentViewDependencyBackup(ctx context.Context, db *sql.DB, task model.SQLViewDependencyTask, changes []model.SQLViewDependencyChange, items []model.SQLViewDependencyItem) error {
	if len(changes) == 0 {
		var err error
		changes, err = ensureSQLViewDependencyChanges(task)
		if err != nil {
			return err
		}
	}
	current, err := s.loadViewDependencySnapshotsForChanges(ctx, db, changes)
	if err != nil {
		return err
	}
	return validateViewDependencyBackupFresh(items, current)
}

func (s *PostgreSQLService) loadViewDependencySnapshotsForChanges(ctx context.Context, db *sql.DB, changes []model.SQLViewDependencyChange) ([]viewDependencySnapshot, error) {
	all := make([]viewDependencySnapshot, 0)
	for _, change := range changes {
		snapshots, err := s.loadViewDependencySnapshots(ctx, db, change.SchemaName, change.TableName, change.ColumnName)
		if err != nil {
			return nil, fmt.Errorf("分析 %s.%s.%s 依赖失败: %w", change.SchemaName, change.TableName, change.ColumnName, err)
		}
		all = append(all, snapshots...)
	}
	return mergeViewDependencySnapshots(all), nil
}

func mergeViewDependencySnapshots(snapshots []viewDependencySnapshot) []viewDependencySnapshot {
	if len(snapshots) == 0 {
		return nil
	}
	mergedByKey := make(map[string]viewDependencySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		key := viewDependencyObjectKey(snapshot.Schema, snapshot.Name, snapshot.Kind)
		existing, ok := mergedByKey[key]
		if !ok || snapshot.Depth > existing.Depth {
			mergedByKey[key] = snapshot
		}
	}
	merged := make([]viewDependencySnapshot, 0, len(mergedByKey))
	for _, snapshot := range mergedByKey {
		merged = append(merged, snapshot)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Depth != merged[j].Depth {
			return merged[i].Depth > merged[j].Depth
		}
		if !strings.EqualFold(merged[i].Schema, merged[j].Schema) {
			return strings.ToLower(merged[i].Schema) < strings.ToLower(merged[j].Schema)
		}
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	total := len(merged)
	for idx := range merged {
		merged[idx].DropOrder = idx + 1
		merged[idx].RestoreOrder = total - idx
	}
	return merged
}

func (s *PostgreSQLService) markSQLViewDependencyTaskRunning(id uint, status, message, username string, validate func(model.SQLViewDependencyTask, []model.SQLViewDependencyItem) error) (*model.SQLViewDependencyTask, []model.SQLViewDependencyItem, error) {
	var task model.SQLViewDependencyTask
	var items []model.SQLViewDependencyItem
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("is_deleted = ?", false).First(&task, id).Error; err != nil {
			return err
		}
		if err := tx.Where("task_id = ?", id).Order("drop_order ASC, id ASC").Find(&items).Error; err != nil {
			return err
		}
		if viewTaskBusy(task.Status) {
			return fmt.Errorf("任务正在执行中，请勿重复提交")
		}
		if status == "RESTORING" {
			if err := ensureSQLViewDependencyTaskCanRestore(task, items); err != nil {
				return err
			}
		}
		if task.Status == "UNKNOWN" {
			task.OutcomeUncertain = true
		}
		if (task.Status == "UNKNOWN" || task.OutcomeUncertain) && status != "RESTORING" && status != "VERIFYING" {
			return fmt.Errorf("执行结果待核实，请先核实并恢复依赖对象，不能直接重放变更")
		}
		if status == "ANALYZING" || status == "PRECHECKING" || status == "EXECUTING" {
			if task.Status == "SUCCESS" {
				return fmt.Errorf("任务已执行成功，不能重复变更；请创建新的变更任务")
			}
			var applied int64
			if err := tx.Model(&model.SQLViewDependencyStep{}).Where("task_id = ? AND step_name = ? AND status IN ?", id, "ALTER_TABLE", []string{"SUCCESS", "UNKNOWN", "RUNNING"}).Count(&applied).Error; err != nil {
				return err
			}
			if applied > 0 {
				return fmt.Errorf("任务已有字段变更生效或结果待核实，不能重放；请核实并创建新的变更任务")
			}
		}
		if err := validateSQLViewTaskSource(tx, task, status); err != nil {
			return err
		}
		if validate != nil {
			if err := validate(task, items); err != nil {
				return err
			}
		}
		task.Status = status
		task.ExecuteMessage = message
		task.Operator = strings.TrimSpace(username)
		return tx.Save(&task).Error
	})
	if err != nil {
		return nil, nil, err
	}
	return &task, items, nil
}

func replaceSQLViewDependencySteps(taskID uint, steps []string) ([]model.SQLViewDependencyStep, error) {
	stepModels := make([]model.SQLViewDependencyStep, 0, len(steps))
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("task_id = ?", taskID).Delete(&model.SQLViewDependencyStep{}).Error; err != nil {
			return err
		}
		for idx, step := range steps {
			stepModel := model.SQLViewDependencyStep{
				TaskID:     taskID,
				StepName:   inferSQLViewDependencyStepName(step),
				StepOrder:  idx + 1,
				SQLContent: step,
				Status:     "PENDING",
			}
			if err := tx.Create(&stepModel).Error; err != nil {
				return err
			}
			stepModels = append(stepModels, stepModel)
		}
		return bindSQLViewChangesToSteps(tx, taskID, stepModels)
	})
	return stepModels, err
}

func appendSQLViewDependencySteps(taskID uint, steps []string) ([]model.SQLViewDependencyStep, error) {
	stepModels := make([]model.SQLViewDependencyStep, 0, len(steps))
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		var maxOrder int
		if err := tx.Model(&model.SQLViewDependencyStep{}).
			Where("task_id = ?", taskID).
			Select("COALESCE(MAX(step_order), 0)").
			Scan(&maxOrder).Error; err != nil {
			return err
		}
		for idx, step := range steps {
			stepModel := model.SQLViewDependencyStep{
				TaskID:     taskID,
				StepName:   inferSQLViewDependencyStepName(step),
				StepOrder:  maxOrder + idx + 1,
				SQLContent: step,
				Status:     "PENDING",
			}
			if err := tx.Create(&stepModel).Error; err != nil {
				return err
			}
			stepModels = append(stepModels, stepModel)
		}
		return nil
	})
	return stepModels, err
}

func inferSQLViewDependencyStepName(step string) string {
	upper := strings.ToUpper(strings.TrimSpace(step))
	switch {
	case upper == "BEGIN;":
		return "BEGIN"
	case strings.HasPrefix(upper, "SET LOCAL "):
		return "SET_LOCAL"
	case strings.HasPrefix(upper, "DROP "):
		return "DROP_DEPENDENCY"
	case strings.HasPrefix(upper, "ALTER TABLE "):
		return "ALTER_TABLE"
	case strings.HasPrefix(upper, "CREATE "):
		return "RESTORE_OBJECT"
	case strings.HasPrefix(upper, "REFRESH MATERIALIZED VIEW "):
		return "REFRESH_MATERIALIZED_VIEW"
	case strings.HasPrefix(upper, "COMMENT ON "):
		return "RESTORE_COMMENT"
	case strings.HasPrefix(upper, "GRANT "):
		return "RESTORE_GRANT"
	case strings.HasPrefix(upper, "DO $$"):
		return "VERIFY"
	case upper == "COMMIT;":
		return "COMMIT"
	case upper == "ROLLBACK;":
		return "ROLLBACK"
	default:
		return "SQL"
	}
}

func markSQLViewDependencyStepRunning(id uint) error {
	now := time.Now()
	return updateSQLViewStepAndChanges(id, map[string]any{"status": "RUNNING", "started_at": &now, "error_message": ""}, "RUNNING", "")
}

func markSQLViewDependencyStepFinished(id uint, status, message string) error {
	now := time.Now()
	return updateSQLViewStepAndChanges(id, map[string]any{"status": status, "finished_at": &now, "error_message": message}, status, message)
}

func appendSQLViewDependencyRollbackStep(taskID uint) error {
	now := time.Now()
	var maxOrder int
	if err := repository.DB.Model(&model.SQLViewDependencyStep{}).Where("task_id = ?", taskID).Select("COALESCE(MAX(step_order), 0)").Scan(&maxOrder).Error; err != nil {
		return err
	}
	return repository.DB.Create(&model.SQLViewDependencyStep{
		TaskID:       taskID,
		StepName:     "ROLLBACK",
		StepOrder:    maxOrder + 1,
		SQLContent:   "ROLLBACK;",
		Status:       "SUCCESS",
		StartedAt:    &now,
		FinishedAt:   &now,
		ErrorMessage: "",
	}).Error
}

func markSQLViewDependencyTaskStatus(id uint, status, message, username string) (*model.SQLViewDependencyTask, error) {
	fields := map[string]any{"status": status, "execute_message": message, "operator": strings.TrimSpace(username)}
	if status == "UNKNOWN" {
		fields["outcome_uncertain"] = true
	}
	result := repository.DB.Model(&model.SQLViewDependencyTask{}).Where("id = ? AND is_deleted = false AND status IN ?", id, []string{"ANALYZING", "PRECHECKING", "EXECUTING", "RESTORING", "VERIFYING"}).Updates(fields)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("视图任务执行权已失效")
	}
	if status == "UNKNOWN" {
		if err := repository.DB.Model(&model.SQLViewDependencyChange{}).Where("task_id = ? AND status IN ?", id, []string{"RUNNING", "APPLIED"}).Updates(map[string]any{"status": "UNKNOWN", "error_message": message}).Error; err != nil {
			return nil, err
		}
	}
	var task model.SQLViewDependencyTask
	if err := repository.DB.First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func countWaitingLocksForRelation(ctx context.Context, db *sql.DB, schemaName, tableName string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
SELECT count(*)::int
FROM pg_locks l
JOIN pg_class c ON c.oid = l.relation
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relname = $2
  AND NOT l.granted`, schemaName, tableName).Scan(&count)
	return count, err
}

func (s *PostgreSQLService) loadViewDependencySnapshots(ctx context.Context, db *sql.DB, schemaName, tableName, columnName string) ([]viewDependencySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
WITH RECURSIVE dep_tree AS (
  SELECT dependent_view.oid,
         dependent_ns.nspname AS object_schema,
         dependent_view.relname AS object_name,
         dependent_view.relkind::text AS object_kind,
         1 AS depth
  FROM pg_depend d
  JOIN pg_rewrite r ON r.oid = d.objid
  JOIN pg_class dependent_view ON dependent_view.oid = r.ev_class
  JOIN pg_namespace dependent_ns ON dependent_ns.oid = dependent_view.relnamespace
  JOIN pg_class source_table ON source_table.oid = d.refobjid
  JOIN pg_namespace source_ns ON source_ns.oid = source_table.relnamespace
  JOIN pg_attribute a ON a.attrelid = source_table.oid AND a.attnum = d.refobjsubid
  WHERE d.deptype = 'n'
    AND d.classid = 'pg_rewrite'::regclass
    AND source_ns.nspname = $1
    AND source_table.relname = $2
    AND a.attname = $3
    AND dependent_view.relkind IN ('v', 'm')
  UNION
  SELECT dependent_view.oid,
         dependent_ns.nspname AS object_schema,
         dependent_view.relname AS object_name,
         dependent_view.relkind::text AS object_kind,
         dep_tree.depth + 1 AS depth
  FROM pg_depend d
  JOIN pg_rewrite r ON r.oid = d.objid
  JOIN pg_class dependent_view ON dependent_view.oid = r.ev_class
  JOIN pg_namespace dependent_ns ON dependent_ns.oid = dependent_view.relnamespace
  JOIN dep_tree ON dep_tree.oid = d.refobjid
  WHERE d.deptype = 'n'
    AND d.classid = 'pg_rewrite'::regclass
    AND dependent_view.relkind IN ('v', 'm')
    AND dependent_view.oid <> dep_tree.oid
)
SELECT t.oid::bigint,
       t.object_schema,
       t.object_name,
       t.object_kind,
       max(t.depth) AS depth,
       pg_get_viewdef(t.oid, true) AS definition,
       pg_get_userbyid(c.relowner) AS owner_name,
       COALESCE(array_to_string(c.reloptions, ','), '') AS reloptions
FROM dep_tree t
JOIN pg_class c ON c.oid = t.oid
GROUP BY t.oid, t.object_schema, t.object_name, t.object_kind, c.relowner, c.reloptions
ORDER BY max(t.depth) DESC, t.object_schema, t.object_name`, schemaName, tableName, columnName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type depRow struct {
		oid        int64
		schema     string
		name       string
		kind       string
		depth      int
		definition string
		owner      string
		reloptions string
	}
	var depRows []depRow
	for rows.Next() {
		var row depRow
		if err := rows.Scan(&row.oid, &row.schema, &row.name, &row.kind, &row.depth, &row.definition, &row.owner, &row.reloptions); err != nil {
			return nil, err
		}
		depRows = append(depRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	snapshots := make([]viewDependencySnapshot, 0, len(depRows))
	total := len(depRows)
	for idx, row := range depRows {
		grantSQL, err := loadViewGrantSQL(ctx, db, row.schema, row.name)
		if err != nil {
			return nil, err
		}
		commentSQL, err := loadViewCommentSQL(ctx, db, row.oid, row.schema, row.name, row.kind)
		if err != nil {
			return nil, err
		}
		indexSQL, err := loadViewIndexSQL(ctx, db, row.schema, row.name, row.kind)
		if err != nil {
			return nil, err
		}
		ruleSQL, err := loadViewRuleSQL(ctx, db, row.schema, row.name)
		if err != nil {
			return nil, err
		}
		triggerSQL, err := loadViewTriggerSQL(ctx, db, row.schema, row.name)
		if err != nil {
			return nil, err
		}
		additionalSQL, optionsJSON := buildViewOptionsBackupSQL(row.schema, row.name, row.kind, row.reloptions)
		snapshots = append(snapshots, viewDependencySnapshot{
			Schema:        row.schema,
			Name:          row.name,
			Kind:          row.kind,
			Depth:         row.depth,
			Definition:    row.definition,
			Owner:         row.owner,
			GrantSQL:      grantSQL,
			CommentSQL:    commentSQL,
			IndexSQL:      indexSQL,
			RuleSQL:       ruleSQL,
			TriggerSQL:    triggerSQL,
			OptionsJSON:   optionsJSON,
			DropOrder:     idx + 1,
			RestoreOrder:  total - idx,
			Materialized:  row.kind == "m",
			AdditionalSQL: additionalSQL,
		})
	}
	return snapshots, nil
}

func loadViewGrantSQL(ctx context.Context, db *sql.DB, schemaName, viewName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT privilege_type, grantee, is_grantable
FROM information_schema.role_table_grants
WHERE table_schema = $1
  AND table_name = $2
ORDER BY grantee, privilege_type`, schemaName, viewName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	qualifiedName := quoteQualifiedName(schemaName, viewName)
	var grants []string
	for rows.Next() {
		var privilege, grantee, grantable string
		if err := rows.Scan(&privilege, &grantee, &grantable); err != nil {
			return nil, err
		}
		sqlText := fmt.Sprintf("GRANT %s ON %s TO %s", strings.ToUpper(privilege), qualifiedName, quoteRoleIdent(grantee))
		if strings.EqualFold(grantable, "YES") {
			sqlText += " WITH GRANT OPTION"
		}
		grants = append(grants, sqlText+";")
	}
	return grants, rows.Err()
}

func loadViewCommentSQL(ctx context.Context, db *sql.DB, oid int64, schemaName, viewName, kind string) ([]string, error) {
	objectType := "VIEW"
	if kind == "m" {
		objectType = "MATERIALIZED VIEW"
	}
	qualifiedName := quoteQualifiedName(schemaName, viewName)
	var comments []string
	var objectComment sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT obj_description($1::oid, 'pg_class')`, oid).Scan(&objectComment); err != nil {
		return nil, err
	}
	if objectComment.Valid {
		comments = append(comments, fmt.Sprintf("COMMENT ON %s %s IS '%s';", objectType, qualifiedName, escapeSQLString(objectComment.String)))
	}
	rows, err := db.QueryContext(ctx, `
SELECT a.attname, col_description(a.attrelid, a.attnum)
FROM pg_attribute a
WHERE a.attrelid = $1::oid
  AND a.attnum > 0
  AND NOT a.attisdropped
  AND col_description(a.attrelid, a.attnum) IS NOT NULL
ORDER BY a.attnum`, oid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var columnName string
		var columnComment string
		if err := rows.Scan(&columnName, &columnComment); err != nil {
			return nil, err
		}
		comments = append(comments, fmt.Sprintf("COMMENT ON COLUMN %s.%s IS '%s';", qualifiedName, quoteIdent(columnName), escapeSQLString(columnComment)))
	}
	return comments, rows.Err()
}

func loadViewIndexSQL(ctx context.Context, db *sql.DB, schemaName, viewName, kind string) ([]string, error) {
	if kind != "m" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
SELECT indexdef
FROM pg_indexes
WHERE schemaname = $1
  AND tablename = $2
ORDER BY indexname`, schemaName, viewName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []string
	for rows.Next() {
		var indexSQL string
		if err := rows.Scan(&indexSQL); err != nil {
			return nil, err
		}
		indexes = append(indexes, ensureSQLSemicolon(indexSQL))
	}
	return indexes, rows.Err()
}

func loadViewRuleSQL(ctx context.Context, db *sql.DB, schemaName, viewName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT definition
FROM pg_rules
WHERE schemaname = $1
  AND tablename = $2
  AND rulename <> '_RETURN'
ORDER BY rulename`, schemaName, viewName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []string
	for rows.Next() {
		var ruleSQL string
		if err := rows.Scan(&ruleSQL); err != nil {
			return nil, err
		}
		rules = append(rules, ensureSQLSemicolon(ruleSQL))
	}
	return rules, rows.Err()
}

func loadViewTriggerSQL(ctx context.Context, db *sql.DB, schemaName, viewName string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT pg_get_triggerdef(t.oid, true)
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relname = $2
  AND NOT t.tgisinternal
ORDER BY t.tgname`, schemaName, viewName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var triggers []string
	for rows.Next() {
		var triggerSQL string
		if err := rows.Scan(&triggerSQL); err != nil {
			return nil, err
		}
		triggers = append(triggers, ensureSQLSemicolon(triggerSQL))
	}
	return triggers, rows.Err()
}

func buildViewOptionsBackupSQL(schemaName, viewName, kind, reloptions string) ([]string, string) {
	reloptions = strings.TrimSpace(reloptions)
	if reloptions == "" {
		return nil, "{}"
	}
	objectType := "VIEW"
	if kind == "m" {
		objectType = "MATERIALIZED VIEW"
	}
	qualifiedName := quoteQualifiedName(schemaName, viewName)
	options := strings.Split(reloptions, ",")
	sort.Strings(options)
	return []string{fmt.Sprintf("ALTER %s %s SET (%s);", objectType, qualifiedName, strings.Join(options, ", "))}, mustMarshalStringSlice(options)
}

func escapeSQLString(input string) string {
	return strings.ReplaceAll(input, "'", "''")
}

func ensureSQLSemicolon(sqlText string) string {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" || strings.HasSuffix(sqlText, ";") {
		return sqlText
	}
	return sqlText + ";"
}

func (s *PostgreSQLService) ParseSQL(req ParseSQLRequest) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	var file *model.SQLChangeFile
	var statements []model.SQLChangeStatement
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		createdFile, createdStatements, err := s.parseSQLWithDB(tx, req)
		if err != nil {
			return err
		}
		file = createdFile
		statements = createdStatements
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return file, statements, nil
}

func (s *PostgreSQLService) parseSQLWithDB(db *gorm.DB, req ParseSQLRequest) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, nil, err
	}
	version, order, schema, err := releaseSQLUploadMetadata(req.FileName)
	if err != nil {
		return nil, nil, err
	}
	parsed, splitErr := splitReleaseSQL(req.Content)
	importMessage := ""
	if splitErr != nil {
		parsed = nil
		importMessage = splitErr.Error()
	} else if issue := releaseSQLExecutionIssue(&model.SQLChangeFile{SchemaName: schema, FileContent: req.Content}, parsed); issue != nil {
		importMessage = issue.Error()
	}
	var previous model.SQLChangeFile
	var previousStatements []model.SQLChangeStatement
	if err := lockSQLFileIdentity(db, version, req.FileName, schema); err != nil {
		return nil, nil, err
	}
	var configured model.SQLChangeFile
	configErr := db.Where("version = ? AND file_name = ? AND schema_configured = true AND database_target = ?", version, req.FileName, target).Order("id DESC").First(&configured).Error
	if configErr != nil && !errors.Is(configErr, gorm.ErrRecordNotFound) {
		return nil, nil, configErr
	}
	if configErr == nil {
		schema = configured.SchemaName
	}
	err = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("version = ? AND file_name = ? AND schema_name = ? AND database_target = ?", version, req.FileName, schema, target).Order("id DESC").First(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, err
	}
	if err == nil {
		if !previous.IsDeleted && !req.Overwrite {
			return nil, nil, fmt.Errorf("SQL 文件已存在")
		}
		if err := guardSQLFileViewTask(db, previous.ID, true); err != nil {
			return nil, nil, err
		}
		if previous.ExecuteStatus == "RUNNING" || previous.ExecuteStatus == "QUEUED" {
			return nil, nil, fmt.Errorf("SQL 文件正在执行中，不能覆盖")
		}
		if err := db.Where("file_id = ?", previous.ID).Order("line_number ASC, id ASC").Find(&previousStatements).Error; err != nil {
			return nil, nil, err
		}
		for _, stmt := range previousStatements {
			if stmt.ExecuteStatus == "RUNNING" || stmt.ExecuteStatus == "UNKNOWN" {
				return nil, nil, fmt.Errorf("原文件第 %d 行执行结果待核实，请核实并处理后再覆盖", stmt.LineNumber)
			}
		}
		if err := db.Model(&previous).Update("is_deleted", true).Error; err != nil {
			return nil, nil, err
		}
	}
	file := &model.SQLChangeFile{ExecutionStartedAt: previous.ExecutionStartedAt, DatabaseTarget: target, FileName: req.FileName, FileContent: req.Content, Version: version, GroupSortNo: order, SchemaName: schema, SchemaConfigured: configErr == nil, CreatedUser: req.CreatedUser, ImportMessage: importMessage, ExecuteStatus: "PENDING"}
	file.ImportMessage = ""
	if splitErr != nil {
		file.ImportMessage = splitErr.Error()
	} else if issue := releaseSQLExecutionIssue(file, parsed); issue != nil {
		file.ImportMessage = issue.Error()
	}
	if err := db.Create(file).Error; err != nil {
		return nil, nil, err
	}
	// Match repeated statements by occurrence, so one old success never marks several new statements as successful.
	inheritance := releaseSQLInheritanceCandidates(previousStatements, parsed)
	for i := range parsed {
		stmt := &parsed[i]
		var inherited *model.SQLChangeStatement
		stmt.FileID = file.ID
		if prior, ok := inheritance[i]; ok {
			if prior.ExecuteStatus == "SUCCESS" || prior.ExecuteStatus == "SKIPPED" {
				inherited = &prior
				if err := ensureLegacySQLStatementRecord(db, &previous, prior); err != nil {
					return nil, nil, err
				}
				stmt.ExecuteStatus = prior.ExecuteStatus
				stmt.SkipKind = sqlSkipKind(prior)
				stmt.ExecuteMessage = prior.ExecuteMessage
				stmt.ExecuteTime = prior.ExecuteTime
				stmt.ExecuteUser = prior.ExecuteUser
				stmt.SQLState = prior.SQLState
				stmt.DurationMs = prior.DurationMs
				stmt.AffectedRows = prior.AffectedRows
				stmt.TransactionOutcome = prior.TransactionOutcome
				stmt.DatabaseUser = prior.DatabaseUser
			}
		}
		if err := db.Create(stmt).Error; err != nil {
			return nil, nil, err
		}
		if inherited != nil {
			record := newSQLStatementRecord(file, *stmt, "INHERIT", req.CreatedUser)
			no := false
			now := time.Now()
			record.Submitted = &no
			record.FinishedAt = &now
			record.Status = stmt.ExecuteStatus
			record.SkipKind = stmt.SkipKind
			record.SourceStatementID = inherited.ID
			record.Message = fmt.Sprintf("沿用前一文件版本的已完成结果（来源语句 %d）", inherited.ID)
			if err := db.Create(&record).Error; err != nil {
				return nil, nil, err
			}
		}
	}
	// Normalize after the first inheritance pass: completed old compound SQL
	// stays completed. A second pass matches already normalized prior uploads,
	// including whole committed BEGIN/COMMIT groups.
	parsed, _, err = normalizeReleaseSQLAddColumns(db, file, parsed, req.CreatedUser)
	if err != nil {
		return nil, nil, err
	}
	for i, prior := range releaseSQLInheritanceCandidates(previousStatements, parsed) {
		if parsed[i].ExecuteStatus != "PENDING" || (prior.ExecuteStatus != "SUCCESS" && prior.ExecuteStatus != "SKIPPED") {
			continue
		}
		if err := ensureLegacySQLStatementRecord(db, &previous, prior); err != nil {
			return nil, nil, err
		}
		stmt := &parsed[i]
		stmt.ExecuteStatus, stmt.SkipKind, stmt.ExecuteMessage = prior.ExecuteStatus, sqlSkipKind(prior), prior.ExecuteMessage
		stmt.ExecuteTime, stmt.ExecuteUser, stmt.SQLState = prior.ExecuteTime, prior.ExecuteUser, prior.SQLState
		stmt.DurationMs, stmt.AffectedRows, stmt.TransactionOutcome = prior.DurationMs, prior.AffectedRows, prior.TransactionOutcome
		stmt.DatabaseUser = prior.DatabaseUser
		if err := db.Save(stmt).Error; err != nil {
			return nil, nil, err
		}
		record := newSQLStatementRecord(file, *stmt, "INHERIT", req.CreatedUser)
		no, now := false, time.Now()
		record.Submitted, record.FinishedAt, record.Status = &no, &now, stmt.ExecuteStatus
		record.SkipKind, record.SourceStatementID = stmt.SkipKind, prior.ID
		record.Message = fmt.Sprintf("沿用前一文件版本的已完成结果（来源语句 %d）", prior.ID)
		if err := db.Create(&record).Error; err != nil {
			return nil, nil, err
		}
	}
	if err := saveImportedSQLFileSummary(db, file, parsed); err != nil {
		return nil, nil, err
	}
	annotateReleaseSQLTransactionGroups(parsed)
	return file, parsed, nil
}

func (s *PostgreSQLService) createSQLStatementsForFile(db *gorm.DB, fileID uint, content, schemaName string, statements []string) ([]model.SQLChangeStatement, error) {
	items, err := splitReleaseSQL(content)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].FileID = fileID
		if err := db.Create(&items[i]).Error; err != nil {
			return nil, err
		}
	}
	var file model.SQLChangeFile
	if err := db.First(&file, fileID).Error; err != nil {
		return nil, err
	}
	items, _, err = normalizeReleaseSQLAddColumns(db, &file, items, file.CreatedUser)
	return items, err
}

func (s *PostgreSQLService) analyzeSQLForChangeStatement(schemaName, sqlText string) (RiskAnalysis, []ViewDependency, SQLExecutionStrategy) {
	analysis := AnalyzeSQLRisk(sqlText)
	var viewDeps []ViewDependency
	if analysis.SQLType == "ALTER_COLUMN_TYPE" {
		analysis = s.enrichColumnTypeRiskWithMetadata(schemaName, sqlText, analysis)
		if deps, err := s.findViewDependenciesWithDefinitions(context.Background(), schemaName, sqlText); err == nil && len(deps) > 0 {
			viewDeps = deps
			analysis = applyColumnTypeViewDependencyWarning(analysis, deps)
		}
	}
	if analysis.SQLType == "DROP_TABLE" || analysis.SQLType == "TRUNCATE" {
		analysis = s.enrichDestructiveTableRiskWithMetadata(schemaName, sqlText, analysis)
	}
	if isDMLRiskWithAffectedRows(analysis.SQLType) {
		analysis = s.enrichDMLRiskWithExplainEstimate(sqlText, analysis)
	}
	if isLargeTableSensitiveSQLType(analysis.SQLType) {
		analysis = s.enrichLargeTableSensitiveRiskWithMetadata(schemaName, sqlText, analysis)
	}
	if analysis.SQLType == "CREATE_VIEW" {
		analysis = s.enrichReplaceViewRiskWithMetadata(schemaName, sqlText, analysis)
	}
	return analysis, viewDeps, DetermineExecutionStrategy(analysis)
}

func (s *PostgreSQLService) UpdateSQLFileContent(fileID uint, req UpdateSQLFileContentRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	if strings.TrimSpace(req.Content) == "" {
		return nil, nil, fmt.Errorf("SQL 内容为空")
	}

	var file *model.SQLChangeFile
	var statements []model.SQLChangeStatement
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.SQLChangeFile
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("is_deleted = ?", false).First(&existing, fileID).Error; err != nil {
			return err
		}
		switch existing.ExecuteStatus {
		case "RUNNING":
			return fmt.Errorf("SQL 文件正在执行中，不能编辑")
		case "SUCCESS":
			return fmt.Errorf("已执行成功的 SQL 文件不能原地编辑，请重新上传为新文件")
		}

		parsedStatements := SplitSQLStatements(req.Content)
		if len(parsedStatements) == 0 {
			return fmt.Errorf("SQL 内容为空")
		}

		fileName := existing.FileName
		if strings.TrimSpace(req.FileName) != "" {
			fileName = defaultSQLFileName(req.FileName)
		}
		version, groupSortNo, schemaName := parseSQLFileMeta(fileName)
		if version == "" {
			version = existing.Version
		}
		if groupSortNo == 0 {
			groupSortNo = existing.GroupSortNo
		}
		if schemaName == "" {
			schemaName = existing.SchemaName
		}
		if strings.TrimSpace(req.Version) != "" {
			version = strings.TrimSpace(req.Version)
		}
		if req.GroupSortNo > 0 {
			groupSortNo = req.GroupSortNo
		}
		if strings.TrimSpace(req.SchemaName) != "" {
			schemaName = strings.TrimSpace(req.SchemaName)
		}
		if schemaName == "" {
			schemaName = inferSQLSchema(parsedStatements)
		}
		if err := validateSchemaName(schemaName); err != nil {
			return err
		}

		if strings.TrimSpace(req.SystemCode) != "" {
			existing.SystemCode = strings.TrimSpace(req.SystemCode)
		}
		if strings.TrimSpace(req.Environment) != "" {
			existing.Environment = strings.TrimSpace(req.Environment)
		}
		existing.FileName = fileName
		existing.FileContent = req.Content
		existing.SchemaName = schemaName
		existing.Version = version
		existing.GroupSortNo = groupSortNo
		existing.ExecuteStatus = "PENDING"
		existing.ExecuteMessage = ""
		existing.ExecuteUser = ""
		existing.ExecuteTime = nil
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}

		if err := tx.Where("file_id = ?", existing.ID).Delete(&model.SQLViewBackup{}).Error; err != nil {
			return err
		}
		if err := tx.Where("file_id = ?", existing.ID).Delete(&model.SQLChangeStatement{}).Error; err != nil {
			return err
		}

		createdStatements, err := s.createSQLStatementsForFile(tx, existing.ID, req.Content, schemaName, parsedStatements)
		if err != nil {
			return err
		}
		if err := tx.First(&existing, existing.ID).Error; err != nil {
			return err
		}
		if existing.BatchID > 0 {
			if err := markSQLBatchPendingAfterFileEdit(tx, existing.BatchID, username); err != nil {
				return err
			}
		}
		file = &existing
		statements = createdStatements
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return file, statements, nil
}

func (s *PostgreSQLService) UpdateSQLStatementContent(statementID uint, req UpdateSQLStatementContentRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, nil, fmt.Errorf("SQL 内容为空")
	}

	var fileID uint
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		var stmt model.SQLChangeStatement
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&stmt, statementID).Error; err != nil {
			return err
		}
		switch stmt.ExecuteStatus {
		case "RUNNING":
			return fmt.Errorf("SQL 正在执行中，不能修改")
		case "SUCCESS":
			return fmt.Errorf("已执行成功的 SQL 不能修改")
		}

		var file model.SQLChangeFile
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("is_deleted = ?", false).First(&file, stmt.FileID).Error; err != nil {
			return err
		}
		switch file.ExecuteStatus {
		case "RUNNING":
			return fmt.Errorf("SQL 文件正在执行中，不能修改")
		case "SUCCESS":
			return fmt.Errorf("已执行成功的 SQL 文件不能原地修改，请重新上传为新文件")
		}

		analysis, viewDeps, strategy := s.analyzeSQLForChangeStatement(file.SchemaName, content)
		if err := tx.Model(&model.SQLChangeStatement{}).Where("id = ?", stmt.ID).Updates(map[string]any{
			"sql_content":            content,
			"sql_type":               analysis.SQLType,
			"risk_level":             analysis.RiskLevel,
			"risk_reason":            analysis.RiskReason,
			"estimated_rows":         analysis.EstimatedRows,
			"can_run_in_transaction": strategy.CanRunInTransaction,
			"execution_strategy":     strategy.Name,
			"execute_status":         defaultStatementStatus(analysis),
			"execute_message":        "",
			"sql_state":              "",
			"affected_rows":          int64(0),
			"duration_ms":            int64(0),
			"execute_time":           nil,
		}).Error; err != nil {
			return err
		}

		if err := tx.Where("statement_id = ?", stmt.ID).Delete(&model.SQLViewBackup{}).Error; err != nil {
			return err
		}
		if len(viewDeps) > 0 {
			if err := saveViewDependencyBackupsWithDB(tx, file.ID, stmt.ID, viewDeps); err != nil {
				return err
			}
		}

		file.ExecuteStatus = "PENDING"
		file.ExecuteMessage = "SQL 语句已修改，请重新执行"
		file.ExecuteUser = strings.TrimSpace(username)
		file.ExecuteTime = nil
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		if file.BatchID > 0 {
			if err := markSQLBatchPendingAfterFileEdit(tx, file.BatchID, username); err != nil {
				return err
			}
		}
		fileID = file.ID
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	file, err := recomputeSQLFileStatus(fileID, username)
	if err != nil {
		return nil, nil, err
	}
	var statements []model.SQLChangeStatement
	if err := repository.DB.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&statements).Error; err != nil {
		return nil, nil, err
	}
	return file, statements, nil
}

func (s *PostgreSQLService) AutocompleteSQLStatementSchema(ctx context.Context, statementID uint, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	var stmt model.SQLChangeStatement
	if err := repository.DB.First(&stmt, statementID).Error; err != nil {
		return nil, nil, err
	}
	if stmt.ExecuteStatus == "SUCCESS" {
		return nil, nil, fmt.Errorf("已执行成功的 SQL 不能补全 schema")
	}
	if stmt.ExecuteStatus == "RUNNING" {
		return nil, nil, fmt.Errorf("SQL 正在执行中，不能补全 schema")
	}

	analysis := AnalyzeSQLRisk(stmt.SQLContent)
	if stmt.SQLType != "MISSING_SCHEMA" && analysis.SQLType != "MISSING_SCHEMA" {
		return nil, nil, fmt.Errorf("当前 SQL 不需要 schema 补全")
	}

	cfg, _, err := s.loadSQLExecutionConfig()
	if err != nil {
		return nil, nil, err
	}
	db, err := openPostgreSQL(cfg)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	completed, err := autocompleteMissingSchemaSQL(ctx, db, stmt.SQLContent)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(completed) == strings.TrimSpace(stmt.SQLContent) {
		return nil, nil, fmt.Errorf("未找到可补全的 schema")
	}
	return s.UpdateSQLStatementContent(statementID, UpdateSQLStatementContentRequest{Content: completed}, username)
}

type schemaCompletionRef struct {
	relationName string
	location     int
}

const schemaCompletionAmbiguousSchema = "\x00AMBIGUOUS_SCHEMA"

var (
	schemaCompletionSchemaNameRegexp = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

func autocompleteMissingSchemaSQL(ctx context.Context, db *sql.DB, sqlText string) (string, error) {
	refs, err := collectSchemaCompletionRefsFromAST(sqlText)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("未找到可补全 schema 的对象")
	}

	targets := make(map[string]struct{})
	for _, ref := range refs {
		if strings.TrimSpace(ref.relationName) != "" {
			targets[ref.relationName] = struct{}{}
		}
	}

	schemas := make(map[string]string, len(targets))
	for target := range targets {
		schema, err := lookupUniqueRelationSchema(ctx, db, target)
		if err != nil {
			return "", err
		}
		schemas[target] = schema
	}
	if len(schemas) == 0 {
		return "", fmt.Errorf("未找到可补全 schema 的对象")
	}

	return rewriteSchemaCompletionRefs(sqlText, refs, schemas), nil
}

func collectSchemaCompletionTargetsFromAST(sqlText string) (map[string]struct{}, error) {
	refs, err := collectSchemaCompletionRefsFromAST(sqlText)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.relationName) != "" {
			targets[ref.relationName] = struct{}{}
		}
	}
	return targets, nil
}

func collectSchemaCompletionRefsFromAST(sqlText string) ([]schemaCompletionRef, error) {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return nil, err
	}
	refs := make([]schemaCompletionRef, 0)
	seenLocations := make(map[int]struct{})
	for _, rawStmt := range tree.GetStmts() {
		if rawStmt == nil || rawStmt.GetStmt() == nil {
			continue
		}
		cteNames := collectCTENames(rawStmt.GetStmt().ProtoReflect(), nil)
		collectSchemaCompletionRefs(rawStmt.GetStmt().ProtoReflect(), cteNames, seenLocations, &refs)
		if rawStmt.GetStmt().GetCommentStmt() != nil {
			collectSchemaCompletionRefsFromCommentSQL(sqlText, seenLocations, &refs)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].location > refs[j].location
	})
	return refs, nil
}

func completeMissingSchemasFromFileContext(statements []string) []string {
	relationSchemas := collectExplicitRelationSchemasFromStatements(statements)
	if len(relationSchemas) == 0 {
		return statements
	}
	completed := make([]string, len(statements))
	for idx, sqlText := range statements {
		completed[idx] = sqlText
		refs, err := collectSchemaCompletionRefsFromAST(sqlText)
		if err != nil || len(refs) == 0 {
			continue
		}
		completed[idx] = rewriteSchemaCompletionRefs(sqlText, refs, relationSchemas)
	}
	return completed
}

func collectExplicitRelationSchemasFromStatements(statements []string) map[string]string {
	schemas := make(map[string]string)
	for _, sqlText := range statements {
		tree, err := pg_query.Parse(sqlText)
		if err != nil {
			continue
		}
		for _, rawStmt := range tree.GetStmts() {
			if rawStmt == nil || rawStmt.GetStmt() == nil {
				continue
			}
			for _, target := range schemaRequiredTargets(rawStmt.GetStmt()) {
				storeExplicitRelationSchema(schemas, target.rel)
			}
			collectExplicitRangeVarSchemas(rawStmt.GetStmt().ProtoReflect(), schemas)
		}
	}
	return schemas
}

func collectExplicitRangeVarSchemas(message protoreflect.Message, schemas map[string]string) {
	if !message.IsValid() {
		return
	}
	if rel, ok := message.Interface().(*pg_query.RangeVar); ok {
		storeExplicitRelationSchema(schemas, rel)
	}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			continue
		}
		value := message.Get(field)
		if field.IsList() {
			list := value.List()
			for j := 0; j < list.Len(); j++ {
				collectExplicitRangeVarSchemas(list.Get(j).Message(), schemas)
			}
			continue
		}
		if value.Message().IsValid() {
			collectExplicitRangeVarSchemas(value.Message(), schemas)
		}
	}
}

func storeExplicitRelationSchema(schemas map[string]string, rel *pg_query.RangeVar) {
	if rel == nil {
		return
	}
	schemaName := strings.TrimSpace(rel.GetSchemaname())
	relName := strings.TrimSpace(rel.GetRelname())
	if schemaName == "" || relName == "" {
		return
	}
	store := func(key string) {
		existing, exists := schemas[key]
		if exists && existing == schemaCompletionAmbiguousSchema {
			return
		}
		if exists && existing != "" && !strings.EqualFold(existing, schemaName) {
			schemas[key] = schemaCompletionAmbiguousSchema
			return
		}
		schemas[key] = schemaName
	}
	store(relName)
	store(strings.ToLower(relName))
}

func collectSchemaCompletionRefsFromCommentSQL(sqlText string, seenLocations map[int]struct{}, refs *[]schemaCompletionRef) {
	if match := reCommentOnColumn.FindStringSubmatchIndex(sqlText); len(match) >= 4 {
		objectText := sqlText[match[2]:match[3]]
		parts := strings.Split(objectText, ".")
		if len(parts) == 2 {
			addSchemaCompletionRef(parts[0], match[2], seenLocations, refs)
		}
		return
	}
	if match := reCommentOnRelation.FindStringSubmatchIndex(sqlText); len(match) >= 4 {
		objectText := sqlText[match[2]:match[3]]
		parts := strings.Split(objectText, ".")
		if len(parts) == 1 {
			addSchemaCompletionRef(parts[0], match[2], seenLocations, refs)
		}
	}
}

func addSchemaCompletionRef(relationName string, location int, seenLocations map[int]struct{}, refs *[]schemaCompletionRef) {
	relationName = strings.TrimSpace(relationName)
	if relationName == "" || location < 0 {
		return
	}
	if _, exists := seenLocations[location]; exists {
		return
	}
	seenLocations[location] = struct{}{}
	*refs = append(*refs, schemaCompletionRef{relationName: relationName, location: location})
}

func collectSchemaCompletionRefs(message protoreflect.Message, cteNames map[string]struct{}, seenLocations map[int]struct{}, refs *[]schemaCompletionRef) {
	if !message.IsValid() {
		return
	}
	if rel, ok := message.Interface().(*pg_query.RangeVar); ok {
		relName := strings.TrimSpace(rel.GetRelname())
		if relName != "" && strings.TrimSpace(rel.GetSchemaname()) == "" {
			if _, isCTE := cteNames[strings.ToLower(relName)]; !isCTE {
				location := int(rel.GetLocation())
				if location >= 0 {
					if _, exists := seenLocations[location]; !exists {
						seenLocations[location] = struct{}{}
						*refs = append(*refs, schemaCompletionRef{relationName: relName, location: location})
					}
				}
			}
		}
	}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			continue
		}
		value := message.Get(field)
		if field.IsList() {
			list := value.List()
			for j := 0; j < list.Len(); j++ {
				collectSchemaCompletionRefs(list.Get(j).Message(), cteNames, seenLocations, refs)
			}
			continue
		}
		if value.Message().IsValid() {
			collectSchemaCompletionRefs(value.Message(), cteNames, seenLocations, refs)
		}
	}
}

func rewriteSchemaCompletionRefs(sqlText string, refs []schemaCompletionRef, schemas map[string]string) string {
	completed := sqlText
	for _, ref := range refs {
		if ref.location < 0 || ref.location > len(completed) {
			continue
		}
		schema := strings.TrimSpace(schemas[ref.relationName])
		if schema == "" {
			schema = strings.TrimSpace(schemas[strings.ToLower(ref.relationName)])
		}
		if schema == "" || schema == schemaCompletionAmbiguousSchema {
			continue
		}
		completed = completed[:ref.location] + quotePGIdentifierIfNeeded(schema) + "." + completed[ref.location:]
	}
	return completed
}

func lookupUniqueRelationSchema(ctx context.Context, db *sql.DB, relationName string) (string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT n.nspname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname = $1
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND n.nspname NOT LIKE 'pg_toast%'
ORDER BY n.nspname`, relationName)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			return "", err
		}
		schemas = append(schemas, schema)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(schemas) {
	case 0:
		return "", fmt.Errorf("数据库中未找到对象 %s，无法自动补全 schema", relationName)
	case 1:
		return schemas[0], nil
	default:
		return "", fmt.Errorf("对象 %s 存在多个 schema：%s，请人工确认后修改", relationName, strings.Join(schemas, "、"))
	}
}

func quotePGIdentifierIfNeeded(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || schemaCompletionSchemaNameRegexp.MatchString(identifier) {
		return identifier
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func markSQLBatchPendingAfterFileEdit(db *gorm.DB, batchID uint, username string) error {
	var batch model.SQLChangeBatch
	if err := db.Where("is_deleted = ?", false).First(&batch, batchID).Error; err != nil {
		return err
	}
	batch.ExecuteStatus = "PENDING"
	batch.ExecuteMessage = "SQL 文件已编辑，请重新执行批次"
	batch.ExecuteUser = strings.TrimSpace(username)
	batch.ExecuteTime = nil
	return db.Save(&batch).Error
}

func (s *PostgreSQLService) ParseSQLBatch(req ParseSQLBatchRequest) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	if len(req.Files) == 0 {
		return nil, nil, fmt.Errorf("SQL 文件不能为空")
	}
	if len(req.Files) > 500 {
		return nil, nil, fmt.Errorf("最多导入 500 个 SQL 文件")
	}
	files := append([]ParseSQLBatchFile(nil), req.Files...)
	names := make(map[string]bool)
	total := 0
	for _, file := range files {
		if _, _, _, err := releaseSQLUploadMetadata(file.FileName); err != nil {
			return nil, nil, fmt.Errorf("文件 %s: %w", file.FileName, err)
		}
		if names[file.FileName] {
			return nil, nil, fmt.Errorf("存在同名 SQL 文件: %s", file.FileName)
		}
		names[file.FileName] = true
		total += len(file.Content)
		if total > 50*1024*1024 {
			return nil, nil, fmt.Errorf("SQL 文件总大小超过 50MB")
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		vi, oi, _, _ := releaseSQLUploadMetadata(files[i].FileName)
		vj, oj, _, _ := releaseSQLUploadMetadata(files[j].FileName)
		if vi != vj {
			return vi < vj
		}
		if oi != oj {
			return oi < oj
		}
		return files[i].FileName < files[j].FileName
	})
	batchName := strings.TrimSpace(req.BatchName)
	if batchName == "" {
		batchName = "SQL 批次 " + time.Now().Format("20060102150405")
	}
	batch := &model.SQLChangeBatch{
		BatchName:     batchName,
		ExecuteStatus: "PENDING",
		TotalFiles:    len(files),
	}
	created := make([]model.SQLChangeFile, 0, len(files))
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(batch).Error; err != nil {
			return err
		}
		for i, input := range files {
			file, _, err := s.parseSQLWithDB(tx, ParseSQLRequest{
				FileName:  input.FileName,
				Content:   input.Content,
				Overwrite: req.Overwrite, CreatedUser: req.CreatedUser,
			})
			if err != nil {
				return err
			}
			file.BatchID = batch.ID
			file.BatchSortNo = i + 1
			if err := tx.Save(file).Error; err != nil {
				return err
			}
			created = append(created, *file)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return batch, created, nil
}

func (s *PostgreSQLService) enrichColumnTypeRiskWithMetadata(defaultSchema, sqlText string, analysis RiskAnalysis) RiskAnalysis {
	ref := parseAlterColumnTypeRef(sqlText, defaultSchema)
	if ref.schema == "" || ref.table == "" || ref.column == "" {
		return analysis
	}
	targetType := extractAlterColumnTargetType(sqlText)
	if strings.TrimSpace(targetType) == "" {
		return analysis
	}
	db, err := s.getDB()
	if err != nil {
		return analysis
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	column, err := NewSQLMetadataInspector(db).GetColumnInfo(ctx, ref.schema, ref.table, ref.column)
	if err != nil {
		return analysis
	}
	metadataRisk := classifyColumnTypeChangeWithMetadata(column, targetType, hasAlterColumnUsing(sqlText))
	if riskRank(metadataRisk.RiskLevel) >= riskRank(analysis.RiskLevel) || analysis.RiskLevel == "WARN" {
		return metadataRisk
	}
	return analysis
}

func (s *PostgreSQLService) enrichDestructiveTableRiskWithMetadata(defaultSchema, sqlText string, analysis RiskAnalysis) RiskAnalysis {
	ref := parseDestructiveTableRef(sqlText, defaultSchema)
	if ref.schema == "" || ref.table == "" {
		return analysis
	}
	db, err := s.getDB()
	if err != nil {
		return appendRiskReason(analysis, "未能读取表大小信息，按默认风险处理")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stats, err := NewSQLMetadataInspector(db).GetTableStats(ctx, ref.schema, ref.table)
	if err != nil {
		return appendRiskReason(analysis, "未能读取表大小信息，按默认风险处理")
	}
	return classifyDestructiveTableOperation(analysis.SQLType, stats)
}

func (s *PostgreSQLService) enrichLargeTableSensitiveRiskWithMetadata(defaultSchema, sqlText string, analysis RiskAnalysis) RiskAnalysis {
	ref := parseTableRefForRiskOperation(sqlText, defaultSchema)
	if ref.schema == "" || ref.table == "" {
		return analysis
	}
	db, err := s.getDB()
	if err != nil {
		return appendRiskReason(analysis, "未能读取表大小信息，按默认风险处理")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stats, err := NewSQLMetadataInspector(db).GetTableStats(ctx, ref.schema, ref.table)
	if err != nil {
		return appendRiskReason(analysis, "未能读取表大小信息，按默认风险处理")
	}
	return classifyLargeTableSensitiveOperation(analysis, stats)
}

func (s *PostgreSQLService) enrichDMLRiskWithExplainEstimate(sqlText string, analysis RiskAnalysis) RiskAnalysis {
	db, err := s.getDB()
	if err != nil {
		return analysis
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return analysis
	}
	defer tx.Rollback()

	explainSQL, ok := buildDMLExplainSQL(sqlText)
	if !ok {
		return analysis
	}
	var explainJSON string
	if err := tx.QueryRowContext(ctx, explainSQL).Scan(&explainJSON); err != nil {
		return analysis
	}
	tx.Rollback() // explicit rollback before processing result

	estimatedRows, ok := extractPlanRowsFromExplainJSON(explainJSON)
	if !ok {
		return analysis
	}
	return classifyDMLAffectedRows(analysis, estimatedRows)
}

func (s *PostgreSQLService) enrichReplaceViewRiskWithMetadata(defaultSchema, sqlText string, analysis RiskAnalysis) RiskAnalysis {
	ref := parseCreateOrReplaceViewRef(sqlText, defaultSchema)
	if ref.schema == "" || ref.table == "" {
		return analysis
	}
	selectSQL, ok := extractCreateOrReplaceViewSelectSQL(sqlText)
	if !ok {
		return appendRiskReason(analysis, "无法解析新视图 SELECT，重建视图可能受列名/顺序/类型兼容性限制")
	}
	db, err := s.getDB()
	if err != nil {
		return analysis
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	columns, err := NewSQLMetadataInspector(db).GetViewColumns(ctx, ref.schema, ref.table)
	if err != nil || len(columns) == 0 {
		return analysis
	}
	newColumns, err := NewSQLMetadataInspector(db).ProbeSelectColumns(ctx, selectSQL)
	if err != nil {
		return appendRiskReason(analysis, "无法探测新视图输出列，重建视图可能受列名/顺序/类型兼容性限制")
	}
	result := compareViewColumns(columns, newColumns)
	return applyCreateOrReplaceViewCompatibilityRisk(analysis, result)
}

func applyColumnTypeViewDependencyWarning(analysis RiskAnalysis, deps []ViewDependency) RiskAnalysis {
	if len(deps) == 0 {
		return analysis
	}
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Schema+"."+dep.View)
	}
	if riskRank(analysis.RiskLevel) < riskRank("WARN") {
		analysis.RiskLevel = "WARN"
	}
	analysis.RiskReason = appendRiskText(
		analysis.RiskReason,
		"解析阶段检测到当前库存在视图依赖，直接执行可能失败；如失败可导出不可执行 SQL，或使用视图依赖列变更流程处理: "+strings.Join(names, ", "),
	)
	return analysis
}

func applyCreateOrReplaceViewCompatibilityRisk(analysis RiskAnalysis, result ViewCompatibilityResult) RiskAnalysis {
	if !result.Exists {
		return analysis
	}
	if !result.Compatible {
		if riskRank(analysis.RiskLevel) < riskRank("WARN") {
			analysis.RiskLevel = "WARN"
		}
		return appendRiskReason(analysis, result.Reason)
	}
	return appendRiskReason(analysis, "CREATE OR REPLACE VIEW 输出列与已有视图兼容")
}

func (s *PostgreSQLService) findViewDependencies(defaultSchema, sqlText string) []string {
	deps, err := s.findViewDependenciesWithDefinitions(context.Background(), defaultSchema, sqlText)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(deps))
	for _, dep := range deps {
		names = append(names, dep.Schema+"."+dep.View)
	}
	return names
}

func (s *PostgreSQLService) findViewDependenciesWithDefinitions(ctx context.Context, defaultSchema, sqlText string) ([]ViewDependency, error) {
	ref := parseAlterColumnTypeRef(sqlText, defaultSchema)
	if ref.schema == "" || ref.table == "" || ref.column == "" {
		return nil, nil
	}
	db, err := s.getDB()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
SELECT dependent_ns.nspname,
       dependent_view.relname,
       dependent_view.relkind,
       pg_get_viewdef(dependent_view.oid, true)
FROM pg_depend d
JOIN pg_rewrite r ON r.oid = d.objid
JOIN pg_class dependent_view ON dependent_view.oid = r.ev_class
JOIN pg_namespace dependent_ns ON dependent_ns.oid = dependent_view.relnamespace
JOIN pg_class source_table ON source_table.oid = d.refobjid
JOIN pg_namespace source_ns ON source_ns.oid = source_table.relnamespace
JOIN pg_attribute a ON a.attrelid = source_table.oid AND a.attnum = d.refobjsubid
WHERE source_ns.nspname = $1
  AND source_table.relname = $2
  AND a.attname = $3
  AND dependent_view.relkind IN ('v', 'm')
ORDER BY dependent_ns.nspname, dependent_view.relname`, ref.schema, ref.table, ref.column)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []ViewDependency
	for rows.Next() {
		var dep ViewDependency
		if err := rows.Scan(&dep.Schema, &dep.View, &dep.Kind, &dep.Definition); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func saveViewDependencyBackups(fileID, statementID uint, deps []ViewDependency) error {
	return saveViewDependencyBackupsWithDB(repository.DB, fileID, statementID, deps)
}

func saveViewDependencyBackupsWithDB(db *gorm.DB, fileID, statementID uint, deps []ViewDependency) error {
	for _, dep := range deps {
		plan := BuildViewRebuildPlan(dep)
		backup := model.SQLViewBackup{
			FileID:       fileID,
			StatementID:  statementID,
			SchemaName:   dep.Schema,
			ViewName:     dep.View,
			Definition:   dep.Definition,
			DropSQL:      plan.DropSQL,
			CreateSQL:    plan.CreateSQL,
			BackupReason: "ALTER COLUMN TYPE 依赖视图备份",
		}
		if err := db.Create(&backup).Error; err != nil {
			return err
		}
	}
	return nil
}

type alterColumnRef struct {
	schema string
	table  string
	column string
	sql    string
	line   int
	order  int
}

type tableRef struct {
	schema string
	table  string
}

func parseDestructiveTableRef(sqlText, defaultSchema string) tableRef {
	name := destructiveTableNameWithSchema(sqlText)
	if name == "" {
		return tableRef{}
	}
	parts := strings.Split(name, ".")
	schema := strings.TrimSpace(defaultSchema)
	table := strings.Trim(parts[len(parts)-1], `"`)
	if len(parts) > 1 {
		schema = strings.Trim(parts[len(parts)-2], `"`)
	}
	if schema == "" {
		schema = "public"
	}
	return tableRef{schema: schema, table: table}
}

func parseCreateOrReplaceViewRef(sqlText, defaultSchema string) tableRef {
	if ref := parseCreateOrReplaceViewRefFromAST(sqlText, defaultSchema); ref.schema != "" && ref.table != "" {
		return ref
	}
	if !regexp.MustCompile(`(?i)^\s*CREATE\s+OR\s+REPLACE\s+VIEW\b`).MatchString(sqlText) {
		return tableRef{}
	}
	re := regexp.MustCompile(`(?i)^\s*CREATE\s+OR\s+REPLACE\s+VIEW\s+(?:(?:"?([a-zA-Z_][\w]*)"?)[.])?"?([a-zA-Z_][\w]*)"?\b`)
	m := re.FindStringSubmatch(sqlText)
	if len(m) == 0 {
		return tableRef{}
	}
	schema := strings.TrimSpace(m[1])
	if schema == "" {
		schema = strings.TrimSpace(defaultSchema)
	}
	if schema == "" {
		schema = "public"
	}
	return tableRef{schema: schema, table: m[2]}
}

func extractCreateOrReplaceViewSelectSQL(sqlText string) (string, bool) {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || tree == nil || len(tree.GetStmts()) != 1 {
		return "", false
	}
	viewStmt := tree.GetStmts()[0].GetStmt().GetViewStmt()
	if viewStmt == nil || !viewStmt.GetReplace() || viewStmt.GetQuery() == nil {
		return "", false
	}
	selectSQL, err := pg_query.Deparse(&pg_query.ParseResult{
		Stmts: []*pg_query.RawStmt{{Stmt: viewStmt.GetQuery()}},
	})
	if err != nil {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimSpace(selectSQL), ";"), true
}

func parseTableRefForRiskOperation(sqlText, defaultSchema string) tableRef {
	if ref := parseTableRefForRiskOperationFromAST(sqlText, defaultSchema); ref.schema != "" && ref.table != "" {
		return ref
	}
	re := regexp.MustCompile(`(?i)^\s*(?:ALTER\s+TABLE|CREATE\s+(?:UNIQUE\s+)?INDEX(?:\s+CONCURRENTLY)?\s+\S+\s+ON)\s+(?:(?:"?([a-zA-Z_][\w]*)"?)[.])?"?([a-zA-Z_][\w]*)"?`)
	matches := re.FindStringSubmatch(sqlText)
	if len(matches) == 0 {
		return tableRef{}
	}
	schema := strings.TrimSpace(matches[1])
	if schema == "" {
		schema = strings.TrimSpace(defaultSchema)
	}
	if schema == "" {
		schema = "public"
	}
	return tableRef{schema: schema, table: matches[2]}
}

func parseTableRefForRiskOperationFromAST(sqlText, defaultSchema string) tableRef {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || tree == nil || len(tree.GetStmts()) == 0 {
		return tableRef{}
	}
	stmt := tree.GetStmts()[0].GetStmt()
	var relation *pg_query.RangeVar
	switch {
	case stmt.GetAlterTableStmt() != nil:
		relation = stmt.GetAlterTableStmt().GetRelation()
	case stmt.GetIndexStmt() != nil:
		relation = stmt.GetIndexStmt().GetRelation()
	}
	if relation == nil {
		return tableRef{}
	}
	schema := strings.TrimSpace(relation.GetSchemaname())
	if schema == "" {
		schema = strings.TrimSpace(defaultSchema)
	}
	if schema == "" {
		schema = "public"
	}
	return tableRef{schema: schema, table: relation.GetRelname()}
}

func parseCreateOrReplaceViewRefFromAST(sqlText, defaultSchema string) tableRef {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || tree == nil || len(tree.GetStmts()) == 0 {
		return tableRef{}
	}
	viewStmt := tree.GetStmts()[0].GetStmt().GetViewStmt()
	if viewStmt == nil || !viewStmt.GetReplace() || viewStmt.GetView() == nil {
		return tableRef{}
	}
	view := viewStmt.GetView()
	schema := strings.TrimSpace(view.GetSchemaname())
	if schema == "" {
		schema = strings.TrimSpace(defaultSchema)
	}
	if schema == "" {
		schema = "public"
	}
	return tableRef{schema: schema, table: view.GetRelname()}
}

func destructiveTableNameWithSchema(sqlText string) string {
	normalized := normalizeSQL(sqlText)
	upper := strings.ToUpper(normalized)
	rest := ""
	switch {
	case strings.HasPrefix(upper, "DROP TABLE IF EXISTS "):
		rest = strings.TrimSpace(normalized[len("DROP TABLE IF EXISTS "):])
	case strings.HasPrefix(upper, "DROP TABLE "):
		rest = strings.TrimSpace(normalized[len("DROP TABLE "):])
	case strings.HasPrefix(upper, "TRUNCATE TABLE ONLY "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE TABLE ONLY "):])
	case strings.HasPrefix(upper, "TRUNCATE TABLE "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE TABLE "):])
	case strings.HasPrefix(upper, "TRUNCATE ONLY "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE ONLY "):])
	case strings.HasPrefix(upper, "TRUNCATE "):
		rest = strings.TrimSpace(normalized[len("TRUNCATE "):])
	default:
		return ""
	}
	return firstSQLName(rest)
}

func parseAlterColumnTypeRef(sqlText, defaultSchema string) alterColumnRef {
	if ref := parseAlterColumnTypeRefFromAST(sqlText, defaultSchema); ref.schema != "" && ref.table != "" && ref.column != "" {
		return ref
	}
	re := regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(?:(?:"?([a-zA-Z_][\w]*)"?)[.])?"?([a-zA-Z_][\w]*)"?\s+ALTER\s+COLUMN\s+"?([a-zA-Z_][\w]*)"?\s+TYPE\b`)
	m := re.FindStringSubmatch(sqlText)
	if len(m) == 0 {
		return alterColumnRef{}
	}
	schema := strings.TrimSpace(m[1])
	if schema == "" {
		schema = strings.TrimSpace(defaultSchema)
	}
	if schema == "" {
		schema = "public"
	}
	return alterColumnRef{schema: schema, table: m[2], column: m[3]}
}

func parseAlterColumnTypeRefFromAST(sqlText, defaultSchema string) alterColumnRef {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || len(tree.GetStmts()) == 0 {
		return alterColumnRef{}
	}
	stmt := tree.GetStmts()[0].GetStmt()
	alterStmt := stmt.GetAlterTableStmt()
	if alterStmt == nil || alterStmt.GetRelation() == nil {
		return alterColumnRef{}
	}
	for _, cmdNode := range alterStmt.GetCmds() {
		cmd := cmdNode.GetAlterTableCmd()
		if cmd == nil || cmd.GetSubtype() != pg_query.AlterTableType_AT_AlterColumnType {
			continue
		}
		relation := alterStmt.GetRelation()
		schema := strings.TrimSpace(relation.GetSchemaname())
		if schema == "" {
			schema = strings.TrimSpace(defaultSchema)
		}
		if schema == "" {
			schema = "public"
		}
		return alterColumnRef{schema: schema, table: relation.GetRelname(), column: cmd.GetName()}
	}
	return alterColumnRef{}
}

func inferSQLSchema(statements []string) string {
	for _, sqlText := range statements {
		tree, err := pg_query.Parse(sqlText)
		if err != nil || tree == nil || len(tree.GetStmts()) == 0 {
			continue
		}
		for _, rawStmt := range tree.GetStmts() {
			if schema := schemaFromStmtNode(rawStmt.GetStmt()); schema != "" {
				return schema
			}
		}
	}
	return ""
}

func schemaFromStmtNode(node *pg_query.Node) string {
	if node == nil {
		return ""
	}
	switch {
	case node.GetInsertStmt() != nil:
		return schemaFromRangeVar(node.GetInsertStmt().GetRelation())
	case node.GetUpdateStmt() != nil:
		return schemaFromRangeVar(node.GetUpdateStmt().GetRelation())
	case node.GetDeleteStmt() != nil:
		return schemaFromRangeVar(node.GetDeleteStmt().GetRelation())
	case node.GetCreateStmt() != nil:
		return schemaFromRangeVar(node.GetCreateStmt().GetRelation())
	case node.GetAlterTableStmt() != nil:
		return schemaFromRangeVar(node.GetAlterTableStmt().GetRelation())
	case node.GetIndexStmt() != nil:
		return schemaFromRangeVar(node.GetIndexStmt().GetRelation())
	case node.GetViewStmt() != nil:
		return schemaFromRangeVar(node.GetViewStmt().GetView())
	default:
		return ""
	}
}

func schemaFromRangeVar(relation *pg_query.RangeVar) string {
	if relation == nil {
		return ""
	}
	return strings.TrimSpace(relation.GetSchemaname())
}

func (s *PostgreSQLService) ExecuteSQLFile(ctx context.Context, fileID uint, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	return s.ExecuteSQLFileWithOptions(ctx, fileID, username, SQLExecuteOptions{})
}

func (s *PostgreSQLService) ExecuteSQLFileWithOptions(ctx context.Context, fileID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if s.cancelReg != nil {
		if !s.cancelReg.register(fileID, cancel) {
			return nil, nil, fmt.Errorf("SQL 文件正在执行中")
		}
		defer s.cancelReg.unregister(fileID)
	}
	file, statements, options, err := s.prepareSQLFileExecution(fileID, username, options)
	if err != nil {
		return nil, nil, err
	}
	return s.runPreparedSQLFileExecution(executionCtx, file, statements, username, options)
}

func (s *PostgreSQLService) StartSQLFileExecution(ctx context.Context, fileID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	executionCtx, cancel := context.WithCancel(context.Background())
	if s.cancelReg != nil && !s.cancelReg.register(fileID, cancel) {
		cancel()
		return nil, nil, fmt.Errorf("SQL 文件正在执行中")
	}
	file, statements, options, err := s.prepareSQLFileExecution(fileID, username, options)
	if err != nil {
		cancel()
		if s.cancelReg != nil {
			s.cancelReg.unregister(fileID)
		}
		return nil, nil, err
	}
	responseFile := *file
	responseStatements := append([]model.SQLChangeStatement(nil), statements...)
	go func() {
		defer cancel()
		if s.cancelReg != nil {
			defer s.cancelReg.unregister(fileID)
		}
		if _, _, err := s.runPreparedSQLFileExecution(executionCtx, file, statements, username, options); err != nil {
			s.markSQLFileExecutionFailed(fileID, username, err)
		}
	}()
	return &responseFile, responseStatements, nil
}

func (s *PostgreSQLService) prepareSQLFileExecution(fileID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, SQLExecuteOptions, error) {
	return s.prepareQueuedSQLFileExecution(fileID, 0, username, options)
}

func (s *PostgreSQLService) prepareQueuedSQLFileExecution(fileID, batchID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, SQLExecuteOptions, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, nil, options, err
	}
	var file *model.SQLChangeFile
	var statements []model.SQLChangeStatement
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		file, err = lockSQLFile(tx, fileID)
		if err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, false); err != nil {
			return err
		}
		if file.ExecuteStatus == "RUNNING" || (file.ExecuteStatus == "QUEUED" && (batchID == 0 || file.ExecutionBatchID != batchID)) {
			return fmt.Errorf("SQL 文件正在执行或等待执行中")
		}
		if batchID != 0 && (file.ExecuteStatus != "QUEUED" || file.ExecutionBatchID != batchID) {
			return fmt.Errorf("SQL 文件执行队列已失效")
		}
		if file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" {
			return fmt.Errorf("SQL 文件已执行过，不能重复执行")
		}
		if err := tx.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&statements).Error; err != nil {
			return err
		}
		for _, stmt := range statements {
			if stmt.ExecuteStatus == "RUNNING" || stmt.ExecuteStatus == "UNKNOWN" {
				return fmt.Errorf("第 %d 行执行结果待核实，请先核实数据库结果；确认已生效后可手工跳过", stmt.LineNumber)
			}
		}
		if err := releaseSQLExecutionIssue(file, statements); err != nil {
			return err
		}
		if err := requireReleaseSQLTarget(file, target); err != nil {
			return err
		}
		if file.DatabaseTarget == "" {
			for _, stmt := range statements {
				if stmt.ExecuteStatus != "PENDING" {
					return fmt.Errorf("旧文件的数据库目标未确认，不能续执行，请先迁移执行目标")
				}
			}
			file.DatabaseTarget = target
		}
		statements, _, err = normalizeReleaseSQLAddColumns(tx, file, statements, username)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(file).Updates(map[string]any{"database_target": file.DatabaseTarget, "execute_status": "RUNNING", "execute_message": "后台执行中", "execute_user": username, "execute_time": now, "execution_started_at": gorm.Expr("COALESCE(execution_started_at, ?)", now), "execution_batch_id": batchID}).Error; err != nil {
			return err
		}
		file.ImportMessage = ""
		if err := tx.Model(file).Update("import_message", "").Error; err != nil {
			return err
		}
		file.ExecutionBatchID = batchID
		file.ExecuteStatus, file.ExecuteMessage, file.ExecuteUser, file.ExecuteTime = "RUNNING", "后台执行中", username, &now
		return updateSQLExecutionSnapshot(tx, file)
	})
	return file, statements, options, err
}

func (s *PostgreSQLService) runPreparedSQLFileExecution(ctx context.Context, file *model.SQLChangeFile, statements []model.SQLChangeStatement, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	return s.runReleaseSQLFile(ctx, file, statements, username, options)
}

func (s *PostgreSQLService) markSQLFileExecutionFailed(fileID uint, username string, executionErr error) {
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.SQLChangeFile{}).Where("id = ? AND is_deleted = false AND execute_status = ?", fileID, "RUNNING").Updates(map[string]any{"execute_status": "FAILED", "execute_user": username, "execute_time": time.Now(), "execute_message": executionErr.Error()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&model.SQLStatementRecord{}).Where("file_id = ? AND status = ?", fileID, "RUNNING").Updates(map[string]any{"status": "UNKNOWN", "message": executionErr.Error(), "finished_at": time.Now()}).Error; err != nil {
			return err
		}
		var file model.SQLChangeFile
		if err := tx.First(&file, fileID).Error; err != nil {
			return err
		}
		return updateSQLExecutionSnapshot(tx, &file)
	})
	if err != nil {
		log.Printf("保存 SQL 文件异常状态失败: %v", err)
	}
}

func (s *PostgreSQLService) ExecuteSQLStatementWithOptions(ctx context.Context, statementID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	options = s.applySQLExecuteSettings(options)

	var stmt model.SQLChangeStatement
	if err := repository.DB.First(&stmt, statementID).Error; err != nil {
		return nil, nil, err
	}
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, stmt.FileID).Error; err != nil {
		return nil, nil, err
	}
	if stmt.ExecuteStatus == "SUCCESS" {
		return nil, nil, fmt.Errorf("SQL 已执行成功，不能重复执行")
	}
	if stmt.ExecuteStatus == "RUNNING" {
		return nil, nil, fmt.Errorf("SQL 正在执行中")
	}

	refreshed := []model.SQLChangeStatement{stmt}
	if refreshStatementsStaticRiskBeforeExecution(refreshed) {
		stmt = refreshed[0]
		if err := repository.DB.Save(&stmt).Error; err != nil {
			return nil, nil, err
		}
	}
	if err := requireWarnConfirmation([]model.SQLChangeStatement{stmt}, options); err != nil {
		return nil, nil, err
	}
	forceWhitelist, err := s.loadSQLForceWhitelist()
	if err != nil {
		return nil, nil, err
	}
	if shouldBlockStatementWithWhitelist(stmt, options, forceWhitelist) {
		stmt.ExecuteStatus = "NOT_EXECUTABLE"
		stmt.ExecuteMessage = stmt.RiskReason
		stmt.AffectedRows = 0
		stmt.DurationMs = 0
		stmt.SQLState = ""
		now := time.Now()
		stmt.ExecuteTime = &now
		if err := repository.DB.Save(&stmt).Error; err != nil {
			return nil, nil, err
		}
		return s.finishSQLStatementExecution(stmt.FileID, username)
	}

	cfg, ownerUser, err := s.loadSQLExecutionConfig()
	if err != nil {
		return nil, nil, err
	}
	db, err := openPostgreSQL(cfg)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	execCtx, cancel := context.WithTimeout(ctx, sqlExecutionContextTimeout(stmt.SQLType, options))
	defer cancel()
	start := time.Now()
	stmt.ExecuteStatus = "RUNNING"
	stmt.ExecuteMessage = ""
	if err := repository.DB.Save(&stmt).Error; err != nil {
		return nil, nil, err
	}

	affected, sqlState, execErr := executeOneSQL(execCtx, db, file.SchemaName, stmt.SQLContent, stmt.SQLType, ownerUser, options)
	now := time.Now()
	stmt.ExecuteTime = &now
	stmt.DurationMs = time.Since(start).Milliseconds()
	stmt.AffectedRows = affected
	stmt.SQLState = sqlState
	if execErr != nil {
		if shouldSkip, skipMessage := skipMessageForSQLError(execErr, options); shouldSkip {
			stmt.ExecuteStatus = "SKIPPED"
			stmt.ExecuteMessage = skipMessage
		} else {
			stmt.ExecuteStatus = "FAILED"
			stmt.ExecuteMessage = execErr.Error()
		}
	} else {
		stmt.ExecuteStatus = "SUCCESS"
		stmt.ExecuteMessage = "执行成功"
	}
	if err := repository.DB.Save(&stmt).Error; err != nil {
		return nil, nil, err
	}
	return s.finishSQLStatementExecution(stmt.FileID, username)
}

func (s *PostgreSQLService) finishSQLStatementExecution(fileID uint, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	file, err := recomputeSQLFileStatus(fileID, username)
	if err != nil {
		return nil, nil, err
	}
	var statements []model.SQLChangeStatement
	if err := repository.DB.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&statements).Error; err != nil {
		return nil, nil, err
	}
	return file, statements, nil
}

func persistSQLFileTerminalStatus(fileID uint, status, message, username string) model.SQLChangeFile {
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, fileID).Error; err != nil {
		return model.SQLChangeFile{ID: fileID, ExecuteStatus: status, ExecuteMessage: message, ExecuteUser: username}
	}
	now := time.Now()
	file.ExecuteStatus = status
	file.ExecuteMessage = message
	file.ExecuteUser = username
	file.ExecuteTime = &now
	_ = repository.DB.Save(&file).Error
	return file
}

func (s *PostgreSQLService) CancelSQLFile(fileID uint, username string) (*model.SQLChangeFile, error) {
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, fileID).Error; err != nil {
		return nil, err
	}
	if file.ExecuteStatus != "RUNNING" {
		return nil, fmt.Errorf("SQL 文件未在执行中")
	}
	if s.cancelReg == nil || !s.cancelReg.cancel(fileID) {
		return nil, fmt.Errorf("未找到正在执行的 SQL 任务")
	}
	// Do not save a stale RUNNING snapshot over a worker's terminal result.
	file.ExecuteMessage = "停止请求已提交，已成功提交的 SQL 不会撤销"
	return &file, nil
}

func requireWarnConfirmation(statements []model.SQLChangeStatement, options SQLExecuteOptions) error {
	if !options.RequireRiskConfirmation || options.ConfirmWarnRisk {
		return nil
	}
	warnCount := 0
	for _, stmt := range statements {
		if stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED" {
			continue
		}
		if stmt.RiskLevel == "WARN" {
			warnCount++
		}
	}
	if warnCount == 0 {
		return nil
	}
	return fmt.Errorf("存在 %d 条 WARN 风险 SQL，请确认风险后再执行", warnCount)
}

func shouldBlockStatement(stmt model.SQLChangeStatement, options SQLExecuteOptions) bool {
	return shouldBlockStatementWithWhitelist(stmt, options, defaultSQLForceWhitelist)
}

func shouldBlockStatementWithWhitelist(stmt model.SQLChangeStatement, options SQLExecuteOptions, whitelist []string) bool {
	blocked := stmt.ExecuteStatus == "NOT_EXECUTABLE" || stmt.RiskLevel == "BLOCKED"
	if !blocked {
		return false
	}
	if options.ForceBlockedSQL && forceWhitelistContains(whitelist, stmt.SQLType) {
		return false
	}
	return true
}

func isSQLExecutionCanceled(executionCtxErr error, execErr error) bool {
	return errors.Is(execErr, context.Canceled) || errors.Is(executionCtxErr, context.Canceled)
}

func refreshStatementsStaticRiskBeforeExecution(statements []model.SQLChangeStatement) bool {
	changed := false
	for i := range statements {
		stmt := &statements[i]
		if stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED" {
			continue
		}
		analysis := AnalyzeSQLRisk(stmt.SQLContent)
		if riskRank(analysis.RiskLevel) < riskRank(stmt.RiskLevel) {
			continue
		}
		if riskRank(analysis.RiskLevel) == riskRank(stmt.RiskLevel) && stmt.SQLType != "" && stmt.RiskReason != "" {
			continue
		}
		strategy := DetermineExecutionStrategy(analysis)
		stmt.SQLType = analysis.SQLType
		stmt.RiskLevel = analysis.RiskLevel
		stmt.RiskReason = analysis.RiskReason
		stmt.ExecutionStrategy = strategy.Name
		stmt.CanRunInTransaction = strategy.CanRunInTransaction
		if analysis.RiskLevel == "BLOCKED" {
			stmt.ExecuteStatus = "NOT_EXECUTABLE"
			stmt.ExecuteMessage = analysis.RiskReason
		} else if stmt.ExecuteStatus == "NOT_EXECUTABLE" {
			stmt.ExecuteStatus = defaultStatementStatus(analysis)
			stmt.ExecuteMessage = ""
		}
		changed = true
	}
	return changed
}

func (s *PostgreSQLService) SkipSQLStatement(statementID uint, username string) (*model.SQLChangeStatement, error) {
	var stmt model.SQLChangeStatement
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&stmt, statementID).Error; err != nil {
			return err
		}
		file, err := lockSQLFile(tx, stmt.FileID)
		if err != nil {
			return err
		}
		if err := tx.First(&stmt, statementID).Error; err != nil {
			return err
		}
		if err := guardSQLStatementViewTask(stmt); err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, false); err != nil {
			return err
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" || stmt.ExecuteStatus == "RUNNING" {
			return fmt.Errorf("SQL 正在执行中，不能手工跳过")
		}
		if file.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SUCCESS" {
			return fmt.Errorf("已成功的 SQL 不能跳过")
		}
		if stmt.ExecuteStatus == "SKIPPED" {
			return nil
		}
		rows, err := releaseSQLActionGroup(tx, file, stmt)
		if err != nil {
			return err
		}
		now := time.Now()
		for i := range rows {
			row := &rows[i]
			if row.ExecuteStatus == "SKIPPED" {
				continue
			}
			if err := ensureLegacySQLStatementRecord(tx, file, *row); err != nil {
				return err
			}
			message := "手动跳过此句"
			if row.TransactionStartLine > 0 {
				message = fmt.Sprintf("手动跳过第 %d-%d 行整个事务组", row.TransactionStartLine, row.TransactionEndLine)
				row.TransactionOutcome = "SKIPPED"
			}
			if err := appendSQLActionRecord(tx, file, *row, "MANUAL_SKIP", username, "SKIPPED", message); err != nil {
				return err
			}
			row.ExecuteStatus = "SKIPPED"
			row.ManualFailure = false
			row.SkipKind = "MANUAL"
			row.ExecuteMessage = "手工标记不执行: " + username + "；" + message
			row.ExecuteTime = &now
			row.ExecuteUser = username
			if err := tx.Save(row).Error; err != nil {
				return err
			}
			if row.ID == stmt.ID {
				stmt = *row
			}
		}
		_, err = recomputeSQLFileStatusWithDB(tx, stmt.FileID, username)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &stmt, nil
}

// MarkSQLStatementFailed defers a statement without pretending it was skipped.
// The statement remains in the failed set and may be retried independently.
func (s *PostgreSQLService) MarkSQLStatementFailed(statementID uint, username string) (*model.SQLChangeStatement, error) {
	var stmt model.SQLChangeStatement
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&stmt, statementID).Error; err != nil {
			return err
		}
		file, err := lockSQLFile(tx, stmt.FileID)
		if err != nil {
			return err
		}
		if err := tx.First(&stmt, statementID).Error; err != nil {
			return err
		}
		if err := guardSQLStatementViewTask(stmt); err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, false); err != nil {
			return err
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" || stmt.ExecuteStatus == "RUNNING" {
			return fmt.Errorf("SQL 正在执行中，不能标记失败")
		}
		if stmt.ExecuteStatus == "UNKNOWN" {
			return fmt.Errorf("执行结果待核实，不能标记失败；请先核实数据库状态")
		}
		if stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED" {
			return fmt.Errorf("成功或已跳过的 SQL 不能标记失败")
		}
		rows, err := releaseSQLActionGroup(tx, file, stmt)
		if err != nil {
			return err
		}
		now := time.Now()
		for i := range rows {
			row := &rows[i]
			if row.ExecuteStatus == "UNKNOWN" || row.ExecuteStatus == "RUNNING" || row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" {
				return fmt.Errorf("语句组中存在结果待核实、执行中、成功或已跳过的语句，不能整体标记失败")
			}
			if row.ManualFailure {
				if row.ID == stmt.ID {
					stmt = *row
				}
				continue
			}
			if err := ensureLegacySQLStatementRecord(tx, file, *row); err != nil {
				return err
			}
			message := "手动标记失败，已暂缓执行，可在失败清单中单独重试"
			if row.TransactionStartLine > 0 {
				message = fmt.Sprintf("第 %d-%d 行整个事务组已标记失败并暂缓，可稍后整组重试", row.TransactionStartLine, row.TransactionEndLine)
				row.TransactionOutcome = "MANUAL_FAILED"
			}
			if row.ExecuteStatus == "FAILED" && strings.TrimSpace(row.ExecuteMessage) != "" {
				message += "；最近一次错误：" + row.ExecuteMessage
			}
			if err := appendSQLActionRecord(tx, file, *row, "MANUAL_FAIL", username, "FAILED", message); err != nil {
				return err
			}
			row.ExecuteStatus = "FAILED"
			row.ManualFailure = true
			row.SkipKind = ""
			row.ExecuteMessage = message
			row.ExecuteUser = username
			row.ExecuteTime = &now
			if err := tx.Save(row).Error; err != nil {
				return err
			}
			if row.ID == stmt.ID {
				stmt = *row
			}
		}
		_, err = recomputeSQLFileStatusWithDB(tx, stmt.FileID, username)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &stmt, nil
}

// releaseSQLActionGroup returns the selected statement and, when it is one
// half of an adjacent view replacement pair, its still-actionable partner.
// Previously committed/skipped statements stay untouched for legacy files.
func releaseSQLActionGroup(tx *gorm.DB, file *model.SQLChangeFile, selected model.SQLChangeStatement) ([]model.SQLChangeStatement, error) {
	var rows []model.SQLChangeStatement
	if err := tx.Where("file_id = ?", file.ID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	selectedIndex := -1
	for i := range rows {
		if rows[i].ID == selected.ID {
			selectedIndex = i
			break
		}
	}
	if selectedIndex < 0 {
		return nil, gorm.ErrRecordNotFound
	}
	group, err := releaseSQLTransactionGroupAt(rows, selectedIndex)
	if err != nil {
		return nil, err
	}
	if group != nil {
		annotateReleaseSQLTransactionGroups(rows)
		for _, row := range rows[group.Start : group.End+1] {
			if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "RUNNING" {
				return nil, fmt.Errorf("事务组包含已成功或执行中的语句，不能只处理其中一部分")
			}
		}
		return rows[group.Start : group.End+1], nil
	}
	partnerIndex := releaseSQLViewPairIndex(rows, selectedIndex, file.SchemaName)
	if partnerIndex < 0 {
		return []model.SQLChangeStatement{rows[selectedIndex]}, nil
	}
	partner := rows[partnerIndex]
	if partner.ExecuteStatus == "SUCCESS" || partner.ExecuteStatus == "SKIPPED" || partner.ExecuteStatus == "UNKNOWN" || partner.ExecuteStatus == "RUNNING" {
		return []model.SQLChangeStatement{rows[selectedIndex]}, nil
	}
	if selectedIndex < partnerIndex {
		return []model.SQLChangeStatement{rows[selectedIndex], partner}, nil
	}
	return []model.SQLChangeStatement{partner, rows[selectedIndex]}, nil
}

// StartReleaseSQLStatementExecution retries one failed release statement and
// leaves every other statement in the file untouched.
func (s *PostgreSQLService) StartReleaseSQLStatementExecution(ctx context.Context, statementID uint, username string, options SQLExecuteOptions) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	return s.StartReleaseSQLStatementExecutionWithAccount(ctx, statementID, username, options, nil)
}

func (s *PostgreSQLService) StartReleaseSQLStatementExecutionWithAccount(ctx context.Context, statementID uint, username string, options SQLExecuteOptions, account *SQLRetryExecutionAccount) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	options = s.applySQLExecuteSettings(options)
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, nil, err
	}
	var identity model.SQLChangeStatement
	if err := repository.DB.First(&identity, statementID).Error; err != nil {
		return nil, nil, err
	}
	var retryConfig *PostgreSQLConfig
	originalSchema := ""
	if account != nil {
		if identity.ExecuteStatus != "FAILED" {
			return nil, nil, fmt.Errorf("只有执行失败的 SQL 可以切换账号重试")
		}
		var original model.SQLChangeFile
		if err := repository.DB.Where("is_deleted = false").First(&original, identity.FileID).Error; err != nil {
			return nil, nil, err
		}
		if err := validateSchemaName(original.SchemaName); original.SchemaName == "" || err != nil {
			return nil, nil, fmt.Errorf("原文件 schema 无效，请先配置执行 schema")
		}
		cfg, err := s.loadSQLRetryAccountConfig(*account)
		if err != nil {
			return nil, nil, err
		}
		if err := requireReleaseSQLTarget(&original, releaseSQLTarget(cfg)); err != nil {
			return nil, nil, err
		}
		if err := verifySQLRetryAccount(ctx, cfg); err != nil {
			return nil, nil, err
		}
		retryConfig, originalSchema = &cfg, original.SchemaName
	}
	executionCtx, cancel := context.WithCancel(context.Background())
	if s.cancelReg != nil && !s.cancelReg.register(identity.FileID, cancel) {
		cancel()
		return nil, nil, fmt.Errorf("SQL 文件正在执行中")
	}
	launched := false
	defer func() {
		if !launched {
			cancel()
			if s.cancelReg != nil {
				s.cancelReg.unregister(identity.FileID)
			}
		}
	}()
	var file model.SQLChangeFile
	var stmt model.SQLChangeStatement
	var allStatements []model.SQLChangeStatement
	var retryStatements []model.SQLChangeStatement
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&stmt, statementID).Error; err != nil {
			return err
		}
		locked, err := lockSQLFile(tx, stmt.FileID)
		if err != nil {
			return err
		}
		file = *locked
		if retryConfig != nil {
			if file.SchemaName != originalSchema {
				return fmt.Errorf("SQL 或 schema 已发生变化，请重新加载后选择重试账号")
			}
			if err := requireReleaseSQLTarget(&file, releaseSQLTarget(*retryConfig)); err != nil {
				return err
			}
		}
		if err := guardSQLStatementViewTask(stmt); err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, false); err != nil {
			return err
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
			return fmt.Errorf("SQL 文件正在执行或等待执行中")
		}
		if err := tx.First(&stmt, statementID).Error; err != nil {
			return err
		}
		if retryConfig != nil && identity.SQLContent != stmt.SQLContent {
			return fmt.Errorf("SQL 已发生变化，请重新加载后选择重试账号")
		}
		if stmt.ExecuteStatus != "FAILED" {
			return fmt.Errorf("只有执行失败的 SQL 可以单独重试")
		}
		if err := validateSchemaName(file.SchemaName); err != nil {
			return fmt.Errorf("SQL 文件执行账号/schema 无效: %w", err)
		}
		if err := requireReleaseSQLTarget(&file, target); err != nil {
			return err
		}
		if file.DatabaseTarget == "" && stmt.ExecuteStatus != "PENDING" {
			return fmt.Errorf("旧文件的数据库目标未确认，不能单独重试，请先迁移执行目标")
		}
		if err := tx.Where("file_id = ?", file.ID).Order("line_number ASC, id ASC").Find(&allStatements).Error; err != nil {
			return err
		}
		if err := ensureLegacySQLStatementRecord(tx, &file, stmt); err != nil {
			return err
		}
		retryStatements = []model.SQLChangeStatement{stmt}
		selectedIndex := -1
		for i := range allStatements {
			if allStatements[i].ID == stmt.ID {
				selectedIndex = i
				break
			}
		}
		if selectedIndex >= 0 {
			group, err := releaseSQLTransactionGroupAt(allStatements, selectedIndex)
			if err != nil {
				return err
			}
			if group != nil {
				if err := validateReleaseSQLTransactionStates(allStatements); err != nil {
					return err
				}
				for _, row := range allStatements[group.Start : group.End+1] {
					if row.ExecuteStatus == "SUCCESS" || row.ExecuteStatus == "SKIPPED" || row.ExecuteStatus == "UNKNOWN" || row.ExecuteStatus == "RUNNING" {
						return fmt.Errorf("事务组有已完成或待核实的语句，不能重试")
					}
				}
				retryStatements = nil
				retryStatements = append(retryStatements, allStatements[group.Start:group.End+1]...)
			}
			partnerIndex := -1
			if group == nil {
				partnerIndex = releaseSQLViewPairIndex(allStatements, selectedIndex, file.SchemaName)
			}
			if partnerIndex >= 0 {
				partner := allStatements[partnerIndex]
				// Retry the atomic pair only when both halves failed. A legacy
				// DROP that already committed must not be replayed.
				if partner.ExecuteStatus == "FAILED" {
					if selectedIndex < partnerIndex {
						retryStatements = []model.SQLChangeStatement{allStatements[selectedIndex], partner}
					} else {
						retryStatements = []model.SQLChangeStatement{partner, allStatements[selectedIndex]}
					}
				}
			}
		}
		annotateReleaseSQLTransactionGroups(allStatements)
		if len(retryStatements) > 0 {
			firstID := retryStatements[0].ID
			prefix := []model.SQLChangeStatement{}
			for _, row := range allStatements {
				if row.ID == firstID {
					break
				}
				if row.ExecuteStatus == "SUCCESS" && isReleaseSQLSessionSetting(row.SQLContent) {
					if retryConfig != nil && isReleaseSQLAccountSetting(row.SQLContent) {
						if err := recordSQLRetrySkippedAccountSetting(tx, &file, row, *retryConfig, username); err != nil {
							return err
						}
						continue
					}
					prefix = append(prefix, row)
				}
			}
			retryStatements = append(prefix, retryStatements...)
		}
		annotateReleaseSQLTransactionGroups(retryStatements)
		for i := range retryStatements {
			if retryConfig != nil && retryStatements[i].ExecuteStatus != "SUCCESS" && retryStatements[i].ExecuteStatus != "SKIPPED" {
				if err := recordSQLRetryAccount(tx, &file, retryStatements[i], *retryConfig, username); err != nil {
					return err
				}
			}
			retryStatements[i].ManualFailure = false
			if err := tx.Save(&retryStatements[i]).Error; err != nil {
				return err
			}
			for j := range allStatements {
				if allStatements[j].ID == retryStatements[i].ID {
					allStatements[j] = retryStatements[i]
					break
				}
			}
			if retryStatements[i].ID == stmt.ID {
				stmt = retryStatements[i]
			}
		}
		now := time.Now()
		message := "后台 SQL / 事务组重试中"
		if retryConfig != nil {
			message = fmt.Sprintf("后台 SQL / 事务组重试中；连接账号 %s，原文件 schema %s", retryConfig.User, file.SchemaName)
		}
		if err := tx.Model(&model.SQLChangeFile{}).Where("id = ?", file.ID).Updates(map[string]any{
			"execute_status": "RUNNING", "execute_message": message, "execute_user": username, "execute_time": now, "execution_started_at": gorm.Expr("COALESCE(execution_started_at, ?)", now), "import_message": "",
		}).Error; err != nil {
			return err
		}
		file.ExecuteStatus, file.ExecuteMessage, file.ExecuteUser, file.ExecuteTime = "RUNNING", message, username, &now
		file.ImportMessage = ""
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	fileForExecution := file
	fileForExecution.ExecutionBatchID = 0
	annotateReleaseSQLTransactionGroups(allStatements)
	go func() {
		defer cancel()
		if s.cancelReg != nil {
			defer s.cancelReg.unregister(file.ID)
		}
		_, _, runErr := s.runReleaseSQLFileWithConfig(executionCtx, &fileForExecution, retryStatements, username, options, retryConfig)
		if runErr != nil {
			s.markSQLFileExecutionFailed(file.ID, username, runErr)
		}
		// A single-statement retry only contains one row. Clear the temporary
		// file status written by runReleaseSQLFile before summarizing all rows.
		if err := repository.DB.Model(&model.SQLChangeFile{}).Where("id = ?", file.ID).Update("execute_status", "PENDING").Error; err != nil {
			log.Printf("单条 SQL 重试后重置文件状态失败: %v", err)
			return
		}
		if _, err := recomputeSQLFileStatus(file.ID, username); err != nil {
			log.Printf("单条 SQL 重试后更新文件状态失败: %v", err)
		}
	}()
	launched = true
	return &file, allStatements, nil
}

func (s *PostgreSQLService) SkipSQLFile(fileID uint, username string) (*model.SQLChangeFile, error) {
	var file model.SQLChangeFile
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		locked, err := lockSQLFile(tx, fileID)
		if err != nil {
			return err
		}
		file = *locked
		if err := guardSQLFileViewTask(tx, file.ID, true); err != nil {
			return err
		}
		if file.ExecuteStatus == "SUCCESS" {
			return fmt.Errorf("已成功的 SQL 文件不能跳过")
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
			return fmt.Errorf("SQL 文件正在执行或等待执行中，不能手工跳过")
		}
		if file.ExecuteStatus == "SKIPPED" {
			return nil
		}
		var rows []model.SQLChangeStatement
		if err := tx.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
		annotateReleaseSQLTransactionGroups(rows)
		now := time.Now()
		for _, stmt := range rows {
			if stmt.ExecuteStatus == "SUCCESS" || stmt.ExecuteStatus == "SKIPPED" {
				continue
			}
			if err := ensureLegacySQLStatementRecord(tx, &file, stmt); err != nil {
				return err
			}
			if stmt.TransactionStartLine > 0 {
				stmt.TransactionOutcome = "SKIPPED"
			}
			if err := appendSQLActionRecord(tx, &file, stmt, "FILE_SKIP", username, "SKIPPED", "手动跳过文件"); err != nil {
				return err
			}
			stmt.ExecuteStatus, stmt.ManualFailure, stmt.SkipKind = "SKIPPED", false, "MANUAL"
			stmt.ExecuteMessage, stmt.ExecuteTime, stmt.ExecuteUser = "手工标记不执行: "+username, &now, username
			if err := tx.Save(&stmt).Error; err != nil {
				return err
			}
		}
		file.ExecuteStatus = "SKIPPED"
		file.ExecuteMessage = "手工标记不执行: " + username
		file.ExecuteUser = username
		file.ExecuteTime = &now
		return tx.Save(&file).Error
	})
	return &file, err
}

func recomputeSQLFileStatus(fileID uint, username string) (*model.SQLChangeFile, error) {
	var file *model.SQLChangeFile
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := lockSQLFile(tx, fileID); err != nil {
			return err
		}
		var err error
		file, err = recomputeSQLFileStatusWithDB(tx, fileID, username)
		return err
	})
	return file, err
}
func recomputeSQLFileStatusWithDB(db *gorm.DB, fileID uint, username string) (*model.SQLChangeFile, error) {
	var file model.SQLChangeFile
	if err := db.Where("is_deleted = ?", false).First(&file, fileID).Error; err != nil {
		return nil, err
	}
	if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "SUCCESS" {
		return &file, nil
	}
	var statements []model.SQLChangeStatement
	if err := db.Where("file_id = ?", fileID).Find(&statements).Error; err != nil {
		return nil, err
	}
	if len(statements) == 0 {
		return &file, nil
	}
	successCount, skippedCount, pendingCount, failedCount, blockedCount, canceledCount := 0, 0, 0, 0, 0, 0
	for _, stmt := range statements {
		switch stmt.ExecuteStatus {
		case "SUCCESS":
			successCount++
		case "SKIPPED":
			skippedCount++
		case "FAILED", "PARTIAL_FAILED", "UNKNOWN", "TIMEOUT":
			failedCount++
		case "NOT_EXECUTABLE", "BLOCKED":
			blockedCount++
		case "CANCELED":
			canceledCount++
		default:
			pendingCount++
		}
	}
	if failedCount > 0 {
		file.ExecuteStatus = "FAILED"
	} else if pendingCount > 0 {
		file.ExecuteStatus = "PENDING"
	} else if skippedCount == len(statements) {
		file.ExecuteStatus = "SKIPPED"
	} else if successCount+skippedCount == len(statements) {
		file.ExecuteStatus = "SUCCESS"
	} else if canceledCount > 0 && successCount == 0 && skippedCount == 0 && failedCount == 0 && blockedCount == 0 {
		file.ExecuteStatus = "CANCELED"
	} else if failedCount > 0 || blockedCount > 0 || canceledCount > 0 {
		file.ExecuteStatus = "PARTIAL_FAILED"
	}
	now := time.Now()
	file.ExecuteUser = username
	file.ExecuteTime = &now
	file.ExecuteMessage = fmt.Sprintf("成功 %d，失败 %d，不可执行 %d，跳过 %d，取消 %d", successCount, failedCount, blockedCount, skippedCount, canceledCount)
	if err := db.Save(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

func (s *PostgreSQLService) StartSQLBatchExecution(ctx context.Context, batchID uint, username string, options SQLExecuteOptions) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	_ = ctx
	batch, files, err := s.loadSQLBatchExecution(batchID)
	if err != nil {
		return nil, nil, err
	}
	executionCtx, cancelBatch := context.WithCancel(context.Background())
	if s.batchCancelReg != nil {
		if !s.batchCancelReg.register(batchID, cancelBatch) {
			cancelBatch()
			return nil, nil, fmt.Errorf("SQL 批次正在执行中")
		}
	}
	if err := s.markSQLBatchRunning(batch, username, fmt.Sprintf("后台批次执行中，将按顺序执行 %d 个 SQL 文件", len(files))); err != nil {
		if s.batchCancelReg != nil {
			s.batchCancelReg.unregister(batchID)
		}
		cancelBatch()
		return nil, nil, err
	}
	go func() {
		defer cancelBatch()
		if s.batchCancelReg != nil {
			defer s.batchCancelReg.unregister(batchID)
		}
		if _, _, err := s.runPreparedSQLBatchExecution(executionCtx, batch, files, username, options); err != nil {
			s.markSQLBatchFailed(batchID, username, err)
		}
	}()
	return batch, files, nil
}

func (s *PostgreSQLService) loadSQLBatchExecution(batchID uint) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	var batch model.SQLChangeBatch
	if err := repository.DB.Where("is_deleted = ?", false).First(&batch, batchID).Error; err != nil {
		return nil, nil, err
	}
	var files []model.SQLChangeFile
	if err := repository.DB.Where("batch_id = ? AND is_deleted = ?", batchID, false).Order("batch_sort_no ASC, file_name ASC, id ASC").Find(&files).Error; err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("批次下没有 SQL 文件")
	}
	return &batch, files, nil
}

func (s *PostgreSQLService) ExecuteSQLBatch(ctx context.Context, batchID uint, username string, options SQLExecuteOptions) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	batch, files, err := s.loadSQLBatchExecution(batchID)
	if err != nil {
		return nil, nil, err
	}
	batchCtx, cancelBatch := context.WithCancel(ctx)
	if s.batchCancelReg != nil {
		if !s.batchCancelReg.register(batchID, cancelBatch) {
			cancelBatch()
			return nil, nil, fmt.Errorf("SQL 批次正在执行中")
		}
		defer s.batchCancelReg.unregister(batchID)
	}
	defer cancelBatch()
	return s.runPreparedSQLBatchExecution(batchCtx, batch, files, username, options)
}

func (s *PostgreSQLService) markSQLBatchRunning(batch *model.SQLChangeBatch, username string, message string) error {
	batch.ExecuteStatus = "RUNNING"
	batch.ExecuteUser = username
	batch.ExecuteMessage = message
	return repository.DB.Save(batch).Error
}

func (s *PostgreSQLService) runPreparedSQLBatchExecution(ctx context.Context, batch *model.SQLChangeBatch, files []model.SQLChangeFile, username string, options SQLExecuteOptions) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	if err := s.markSQLBatchRunning(batch, username, fmt.Sprintf("正在按顺序执行 %d 个 SQL 文件", len(files))); err != nil {
		return nil, nil, err
	}
	successFiles := 0
	failedFiles := 0
	skippedFiles := 0
	canceledFiles := 0
	for i := range files {
		if ctx.Err() != nil {
			canceledFiles += markRemainingBatchFilesCanceled(files[i:], username)
			break
		}
		if files[i].ExecuteStatus == "SUCCESS" {
			successFiles++
			continue
		}
		if files[i].ExecuteStatus == "SKIPPED" {
			skippedFiles++
			continue
		}
		s.updateSQLBatchProgress(batch.ID, username, fmt.Sprintf("正在执行第 %d/%d 个文件: %s", i+1, len(files), files[i].FileName))
		file, _, err := s.ExecuteSQLFileWithOptions(ctx, files[i].ID, username, options)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				files[i] = persistSQLFileTerminalStatus(files[i].ID, "CANCELED", "批次取消", username)
				canceledFiles++
				if i+1 < len(files) {
					canceledFiles += markRemainingBatchFilesCanceled(files[i+1:], username)
				}
				break
			} else {
				files[i] = persistSQLFileTerminalStatus(files[i].ID, "FAILED", err.Error(), username)
				failedFiles++
			}
			continue
		}
		files[i] = *file
		switch file.ExecuteStatus {
		case "SUCCESS":
			successFiles++
		case "SKIPPED":
			skippedFiles++
		case "CANCELED":
			canceledFiles++
		default:
			failedFiles++
		}
		if file.ExecuteStatus == "CANCELED" {
			if i+1 < len(files) {
				canceledFiles += markRemainingBatchFilesCanceled(files[i+1:], username)
			}
			break
		}
	}
	now := time.Now()
	batch.ExecuteTime = &now
	batch.SuccessFiles = successFiles
	batch.FailedFiles = failedFiles
	batch.SkippedFiles = skippedFiles
	batch.ExecuteStatus, batch.ExecuteMessage = batchExecutionSummary(successFiles, failedFiles, skippedFiles, canceledFiles)
	if err := repository.DB.Save(&batch).Error; err != nil {
		return nil, nil, err
	}
	if err := repository.DB.Where("batch_id = ? AND is_deleted = ?", batch.ID, false).Order("batch_sort_no ASC, file_name ASC, id ASC").Find(&files).Error; err != nil {
		return nil, nil, err
	}
	return batch, files, nil
}

func (s *PostgreSQLService) updateSQLBatchProgress(batchID uint, username string, message string) {
	_ = repository.DB.Model(&model.SQLChangeBatch{}).
		Where("id = ? AND is_deleted = ?", batchID, false).
		Updates(map[string]any{
			"execute_status":  "RUNNING",
			"execute_user":    username,
			"execute_message": message,
		}).Error
}

func (s *PostgreSQLService) markSQLBatchFailed(batchID uint, username string, err error) {
	now := time.Now()
	_ = repository.DB.Model(&model.SQLChangeBatch{}).
		Where("id = ? AND is_deleted = ?", batchID, false).
		Updates(map[string]any{
			"execute_status":  "FAILED",
			"execute_user":    username,
			"execute_time":    &now,
			"execute_message": err.Error(),
		}).Error
}

func (s *PostgreSQLService) CancelSQLBatch(batchID uint, username string) (*model.SQLChangeBatch, error) {
	var batch model.SQLChangeBatch
	if err := repository.DB.Where("is_deleted = ?", false).First(&batch, batchID).Error; err != nil {
		return nil, err
	}
	if batch.ExecuteStatus != "RUNNING" {
		return nil, fmt.Errorf("SQL 批次未在执行中")
	}
	if s.batchCancelReg == nil || !s.batchCancelReg.cancel(batchID) {
		return nil, fmt.Errorf("未找到正在执行的 SQL 批次")
	}
	batch.ExecuteMessage = "取消批次执行请求已提交: " + username
	if err := repository.DB.Save(&batch).Error; err != nil {
		return nil, err
	}
	return &batch, nil
}

func markRemainingBatchFilesCanceled(files []model.SQLChangeFile, username string) int {
	canceled := 0
	now := time.Now()
	for i := range files {
		if files[i].ExecuteStatus == "SUCCESS" || files[i].ExecuteStatus == "SKIPPED" {
			continue
		}
		files[i].ExecuteStatus = "CANCELED"
		files[i].ExecuteMessage = "批次取消: " + username
		files[i].ExecuteTime = &now
		_ = repository.DB.Save(&files[i]).Error
		canceled++
	}
	return canceled
}

func batchExecutionSummary(successFiles, failedFiles, skippedFiles, canceledFiles int) (string, string) {
	message := fmt.Sprintf("成功文件 %d，失败文件 %d，跳过文件 %d，取消文件 %d", successFiles, failedFiles, skippedFiles, canceledFiles)
	switch {
	case canceledFiles > 0 && successFiles == 0 && failedFiles == 0 && skippedFiles == 0:
		return "CANCELED", message
	case failedFiles > 0 && successFiles == 0 && skippedFiles == 0:
		return "FAILED", message
	case failedFiles > 0 || canceledFiles > 0:
		return "PARTIAL_FAILED", message
	default:
		return "SUCCESS", message
	}
}

func (s *PostgreSQLService) ListSQLBatches(page, pageSize int, executionOnly ...bool) ([]model.SQLChangeBatch, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var total int64
	var batches []model.SQLChangeBatch
	query := repository.DB.Model(&model.SQLChangeBatch{}).Where("is_deleted = ?", false)
	if len(executionOnly) > 0 && executionOnly[0] {
		query = query.Where("file_ids_json <> ''")
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&batches).Error
	return batches, total, err
}

func (s *PostgreSQLService) GetSQLBatch(id uint) (*model.SQLChangeBatch, []model.SQLChangeFile, error) {
	var batch model.SQLChangeBatch
	if err := repository.DB.Where("is_deleted = ?", false).First(&batch, id).Error; err != nil {
		return nil, nil, err
	}
	if batch.FileIDsJSON != "" {
		return s.getSelectedSQLBatchFiles(&batch)
	}
	var files []model.SQLChangeFile
	if err := repository.DB.Where("batch_id = ? AND is_deleted = ?", id, false).Order("batch_sort_no ASC, file_name ASC, id ASC").Find(&files).Error; err != nil {
		return nil, nil, err
	}
	return &batch, files, nil
}

func (s *PostgreSQLService) ListSQLFiles(page, pageSize int) ([]model.SQLChangeFile, int64, error) {
	return s.ListSQLFilesByStatus(page, pageSize, "")
}

func (s *PostgreSQLService) ListSQLFilesByStatus(page, pageSize int, statusGroup string, filters ...SQLFileFilter) ([]model.SQLChangeFile, int64, error) {
	target, err := currentReleaseSQLTarget()
	if err != nil {
		return nil, 0, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var total int64
	var files []model.SQLChangeFile
	query := repository.DB.Model(&model.SQLChangeFile{}).Where("is_deleted = ? AND (database_target = ? OR database_target = '')", false, target)
	if len(filters) > 0 {
		f := filters[0]
		if f.Version != "" {
			query = query.Where("version = ?", f.Version)
		}
		if f.SchemaName != "" {
			query = query.Where("schema_name = ?", f.SchemaName)
		}
		if f.FileName != "" {
			query = query.Where("file_name = ?", f.FileName)
		}
	}
	switch statusGroup {
	case "todo":
		query = query.Where("NOT " + sqlFileHasAttempt)
	case "done":
		query = query.Where(sqlFileHasAttempt)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	// Sort file metadata naturally before pagination; SQL lexical ordering
	// would place V10 before V2. File content is deliberately omitted.
	if err := query.Omit("file_content").Find(&files).Error; err != nil {
		return nil, 0, err
	}
	if err := populateSQLFileCounts(repository.DB, files); err != nil {
		return nil, 0, err
	}
	if len(filters) > 0 && filters[0].Status != "" {
		filtered := files[:0]
		for _, f := range files {
			if f.ResultStatus == filters[0].Status {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}
	total = int64(len(files))
	sort.SliceStable(files, func(i, j int) bool {
		if statusGroup == "done" {
			return lessSQLFiles(files[j], files[i])
		}
		return lessSQLFiles(files[i], files[j])
	})
	start := (page - 1) * pageSize
	if start < 0 {
		start = 0
	}
	if start >= len(files) {
		return []model.SQLChangeFile{}, total, nil
	}
	end := start + pageSize
	if end > len(files) {
		end = len(files)
	}
	return files[start:end], total, nil
}

func (s *PostgreSQLService) GetSQLFile(id uint) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, id).Error; err != nil {
		return nil, nil, err
	}
	var statements []model.SQLChangeStatement
	if err := repository.DB.Where("file_id = ?", id).Order("line_number ASC, id ASC").Find(&statements).Error; err != nil {
		return nil, nil, err
	}
	annotateReleaseSQLTransactionGroups(statements)
	decorated := []model.SQLChangeFile{file}
	if err := populateSQLFileCounts(repository.DB, decorated); err != nil {
		return nil, nil, err
	}
	file = decorated[0]
	file.ImportMessage = ""
	if issue := releaseSQLExecutionIssue(&file, statements); issue != nil {
		file.ImportMessage = issue.Error()
	}
	return &file, statements, nil
}

func (s *PostgreSQLService) DeleteSQLFile(id uint) error {
	return repository.DB.Transaction(func(tx *gorm.DB) error {
		file, err := lockSQLFile(tx, id)
		if err != nil {
			return err
		}
		if err := guardSQLFileViewTask(tx, file.ID, true); err != nil {
			return err
		}
		if file.ExecuteStatus == "SUCCESS" {
			return fmt.Errorf("已执行成功的文件不能删除")
		}
		if file.ExecuteStatus == "RUNNING" || file.ExecuteStatus == "QUEUED" {
			return fmt.Errorf("SQL 文件正在执行或等待执行中，不能删除")
		}
		var uncertain int64
		if err := tx.Model(&model.SQLChangeStatement{}).Where("file_id = ? AND execute_status IN ?", id, []string{"RUNNING", "UNKNOWN"}).Count(&uncertain).Error; err != nil {
			return err
		}
		if uncertain > 0 {
			return fmt.Errorf("文件存在执行结果待核实的语句，核实并处理前不能删除")
		}
		return tx.Model(file).Update("is_deleted", true).Error
	})
}

func (s *PostgreSQLService) ExecuteSQLContent(ctx context.Context, req ExecuteSQLContentRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	file, _, err := s.ParseSQL(req.ParseSQLRequest)
	if err != nil {
		return nil, nil, err
	}
	return s.ExecuteSQLFileWithOptions(ctx, file.ID, username, req.Options)
}

func (s *PostgreSQLService) ImportServerSQL(req ImportServerSQLRequest) (int, error) {
	environment, err := s.settingsRepo.Read("customer_env", false)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(environment) != "本地" {
		return 0, fmt.Errorf("仅部署环境为本地时允许导入服务器文件，请使用上传 SQL 文件")
	}
	path := strings.TrimSpace(req.FilePath)
	if path == "" {
		return 0, fmt.Errorf("文件路径不能为空")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return 0, fmt.Errorf("路径无效: %w", err)
	}
	absPath, err = filepath.EvalSymlinks(absPath)
	if err != nil {
		return 0, fmt.Errorf("路径无效或不存在: %w", err)
	}
	allowed := false
	for _, root := range []string{"/opt/df-build-server", "/root", "/tmp"} {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(resolved, absPath)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return 0, fmt.Errorf("不允许访问该路径，仅支持 /opt/df-build-server、/root、/tmp 下的文件")
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return 0, fmt.Errorf("文件不存在")
	}
	if info.IsDir() {
		return 0, fmt.Errorf("不支持导入目录")
	}
	if info.Size() > 50*1024*1024 {
		return 0, fmt.Errorf("文件超过大小限制 (50MB)")
	}
	switch strings.ToLower(filepath.Ext(absPath)) {
	case ".sql":
		content, err := os.ReadFile(absPath)
		if err != nil {
			return 0, err
		}
		_, _, err = s.ParseSQL(ParseSQLRequest{FileName: filepath.Base(absPath), Content: string(content), Overwrite: req.Overwrite, CreatedUser: req.CreatedUser})
		if err != nil {
			return 0, err
		}
		return 1, nil
	case ".zip":
		return s.importSQLZipAsUser(absPath, req.Overwrite, req.CreatedUser)
	default:
		return 0, fmt.Errorf("仅支持 .sql 或 .zip 文件")
	}
}

func (s *PostgreSQLService) importSQLZip(path string, overwrite bool) (int, error) {
	return s.importSQLZipAsUser(path, overwrite, "")
}
func (s *PostgreSQLService) importSQLZipAsUser(path string, overwrite bool, username string) (int, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("解压提取 SQL 错误: %w", err)
	}
	defer reader.Close()
	const maxSize = 50 * 1024 * 1024
	var files []ParseSQLBatchFile
	total := 0
	for _, f := range reader.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(filepath.Ext(f.Name), ".sql") {
			continue
		}
		if len(files) >= 500 || f.UncompressedSize64 > uint64(maxSize-total) {
			return 0, fmt.Errorf("SQL 文件最多 500 个，总大小不超过 50MB")
		}
		rc, err := f.Open()
		if err != nil {
			return 0, err
		}
		content, readErr := io.ReadAll(io.LimitReader(rc, int64(maxSize-total)+1))
		closeErr := rc.Close()
		if readErr != nil {
			return 0, readErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
		total += len(content)
		if total > maxSize {
			return 0, fmt.Errorf("SQL 文件总大小超过 50MB")
		}
		files = append(files, ParseSQLBatchFile{FileName: filepath.Base(f.Name), Content: string(content)})
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("ZIP 内没有 SQL 文件")
	}
	if _, _, err := s.ParseSQLBatch(ParseSQLBatchRequest{BatchName: filepath.Base(path), Overwrite: overwrite, Files: files, CreatedUser: username}); err != nil {
		return 0, err
	}
	return len(files), nil
}

func (s *PostgreSQLService) BuildNotExecutableSQLForFile(id uint) (string, error) {
	_, statements, err := s.GetSQLFile(id)
	if err != nil {
		return "", err
	}
	var backups []model.SQLViewBackup
	if err := repository.DB.Where("file_id = ?", id).Order("statement_id ASC, id ASC").Find(&backups).Error; err != nil {
		return "", err
	}
	backupsByStatement := make(map[uint][]model.SQLViewBackup)
	for _, backup := range backups {
		backupsByStatement[backup.StatementID] = append(backupsByStatement[backup.StatementID], backup)
	}
	exportItems := make([]SQLExportStatement, 0, len(statements))
	parts := make([]string, 0, len(statements))
	for _, stmt := range statements {
		item := SQLExportStatement{
			LineNumber:     stmt.LineNumber,
			SQLContent:     stmt.SQLContent,
			ExecuteStatus:  stmt.ExecuteStatus,
			ExecuteMessage: stmt.ExecuteMessage,
			RiskReason:     stmt.RiskReason,
		}
		exportItems = append(exportItems, item)
		if !shouldExportSQL(stmt.ExecuteStatus) {
			continue
		}
		if viewBackups := backupsByStatement[stmt.ID]; len(viewBackups) > 0 {
			parts = append(parts, buildViewBackupExport(viewBackups, stmt.SQLContent))
			continue
		}
		parts = append(parts, BuildNotExecutableSQL([]SQLExportStatement{item}))
	}
	if len(parts) > 0 {
		return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
	}
	return BuildNotExecutableSQL(exportItems), nil
}

func buildViewBackupExport(backups []model.SQLViewBackup, originalSQL string) string {
	var b strings.Builder
	for _, backup := range backups {
		b.WriteString(fmt.Sprintf("-- View dependency rebuild plan for %s.%s\n", backup.SchemaName, backup.ViewName))
		b.WriteString(strings.TrimSpace(backup.DropSQL))
		if !strings.HasSuffix(strings.TrimSpace(backup.DropSQL), ";") {
			b.WriteString(";")
		}
		b.WriteString("\n\n")
	}
	b.WriteString("-- Original SQL\n")
	b.WriteString(strings.TrimSpace(originalSQL))
	if !strings.HasSuffix(strings.TrimSpace(originalSQL), ";") {
		b.WriteString(";")
	}
	b.WriteString("\n\n")
	for _, backup := range backups {
		b.WriteString(fmt.Sprintf("-- Recreate view %s.%s\n", backup.SchemaName, backup.ViewName))
		b.WriteString(strings.TrimSpace(backup.CreateSQL))
		if !strings.HasSuffix(strings.TrimSpace(backup.CreateSQL), ";") {
			b.WriteString(";")
		}
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func (s *PostgreSQLService) loadConfig() (PostgreSQLConfig, error) {
	var values = make(map[string]string)
	for _, key := range []string{"postgresql_host", "postgresql_port", "postgresql_database", "postgresql_admin_password"} {
		value, err := s.settingsRepo.Read(key, false)
		if err != nil {
			return PostgreSQLConfig{}, err
		}
		values[key] = value
	}
	return PostgreSQLAdminConfig(values), nil
}

func (s *PostgreSQLService) loadSQLExecutionConfig() (PostgreSQLConfig, string, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return PostgreSQLConfig{}, "", err
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return PostgreSQLConfig{}, "", fmt.Errorf("PostgreSQL 管理员密码未配置")
	}
	return cfg, "", nil
}

func openPostgreSQL(cfg PostgreSQLConfig) (*sql.DB, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("PostgreSQL 主机地址未配置")
	}
	db, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func executeOneSQL(ctx context.Context, db *sql.DB, schemaName, sqlText, sqlType, ownerUser string, options SQLExecuteOptions) (int64, string, error) {
	if err := validateSchemaName(schemaName); err != nil {
		return 0, "", err
	}
	ownerSQL, changeOwner, err := buildCreateTableOwnerSQL(sqlText, schemaName, ownerUser)
	if err != nil {
		return 0, "", err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

	lockTimeout := normalizeSQLTimeoutLiteral(options.LockTimeout, defaultSQLExecuteLockTimeout)
	statementTimeoutValue := sqlStatementTimeoutLiteral(sqlType, options)
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET lock_timeout = '%s'", escapeSQLString(lockTimeout))); err != nil {
		return 0, pgState(err), err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET statement_timeout = '%s'", escapeSQLString(statementTimeoutValue))); err != nil {
		return 0, pgState(err), err
	}
	if _, err := conn.ExecContext(ctx, "SET idle_in_transaction_session_timeout = '60s'"); err != nil {
		return 0, pgState(err), err
	}

	result, err := conn.ExecContext(ctx, sqlText)
	if err != nil {
		return 0, pgState(err), err
	}
	rows, _ := result.RowsAffected()
	if changeOwner {
		if _, err := conn.ExecContext(ctx, ownerSQL); err != nil {
			return rows, pgState(err), fmt.Errorf("修正表 owner 失败: %w", err)
		}
	}
	return rows, "", nil
}

func buildCreateTableOwnerSQL(sqlText, defaultSchema, ownerUser string) (string, bool, error) {
	tree, err := pg_query.Parse(sqlText)
	if err != nil || tree == nil || len(tree.GetStmts()) == 0 {
		return "", false, nil
	}
	createStmt := tree.GetStmts()[0].GetStmt().GetCreateStmt()
	if createStmt == nil || createStmt.GetRelation() == nil {
		return "", false, nil
	}
	relation := createStmt.GetRelation()
	if relation.GetRelpersistence() == "t" {
		return "", false, nil
	}
	tableName := strings.TrimSpace(relation.GetRelname())
	if tableName == "" {
		return "", false, nil
	}
	ownerUser = strings.TrimSpace(ownerUser)
	if ownerUser == "" {
		return "", true, fmt.Errorf("PostgreSQL 业务用户名未配置，无法修正表 owner")
	}
	schemaName := strings.TrimSpace(relation.GetSchemaname())
	if schemaName == "" {
		schemaName = strings.TrimSpace(defaultSchema)
	}
	return fmt.Sprintf("ALTER TABLE %s OWNER TO %s;", quoteQualifiedName(schemaName, tableName), quoteIdent(ownerUser)), true, nil
}

func statementTimeout(sqlType string) time.Duration {
	switch sqlType {
	case "CREATE_INDEX_CONCURRENTLY":
		return 30 * time.Minute
	case "CREATE_INDEX", "ALTER_COLUMN_TYPE", "ALTER_SET_NOT_NULL", "ADD_CHECK":
		return 5 * time.Minute
	default:
		return 2 * time.Minute
	}
}

func sqlExecutionContextTimeout(sqlType string, options SQLExecuteOptions) time.Duration {
	return sqlStatementTimeoutDuration(sqlType, options) + 10*time.Second
}

func sqlStatementTimeoutLiteral(sqlType string, options SQLExecuteOptions) string {
	value := normalizeSQLTimeoutLiteral(options.StatementTimeout, "")
	if value != "" {
		return value
	}
	return fmt.Sprintf("%dms", statementTimeout(sqlType).Milliseconds())
}

func sqlStatementTimeoutDuration(sqlType string, options SQLExecuteOptions) time.Duration {
	if duration, ok := parseSQLTimeoutDuration(options.StatementTimeout); ok && duration > 0 {
		return duration
	}
	return statementTimeout(sqlType)
}

func normalizeSQLTimeoutLiteral(value, fallback string) string {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
	if normalized == "" {
		normalized = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fallback), " ", ""))
	}
	if normalized == "" {
		return ""
	}
	if !regexp.MustCompile(`^\d+(ms|s|m|min|h)?$`).MatchString(normalized) {
		return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fallback), " ", ""))
	}
	if regexp.MustCompile(`^\d+$`).MatchString(normalized) {
		return normalized + "ms"
	}
	if strings.HasSuffix(normalized, "m") && !strings.HasSuffix(normalized, "ms") {
		return strings.TrimSuffix(normalized, "m") + "min"
	}
	return normalized
}

func parseSQLTimeoutDuration(value string) (time.Duration, bool) {
	normalized := normalizeSQLTimeoutLiteral(value, "")
	if normalized == "" {
		return 0, false
	}
	if strings.HasSuffix(normalized, "min") {
		normalized = strings.TrimSuffix(normalized, "min") + "m"
	}
	duration, err := time.ParseDuration(normalized)
	return duration, err == nil
}

func pgState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr != nil {
		return pgErr.Code
	}
	return ""
}

func SplitSQLStatements(input string) []string {
	if statements, err := pg_query.SplitWithScanner(input, true); err == nil {
		cleaned := make([]string, 0, len(statements))
		for _, stmt := range statements {
			appendStatement(&cleaned, strings.TrimSuffix(stripSQLComments(stmt), ";"))
		}
		return cleaned
	}
	var statements []string
	var b strings.Builder
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		next := byte(0)
		if i+1 < len(input) {
			next = input[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				b.WriteByte(ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if !inSingle && !inDouble && ch == '-' && next == '-' {
			inLineComment = true
			i++
			continue
		}
		if !inSingle && !inDouble && ch == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
		} else if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		if ch == ';' && !inSingle && !inDouble {
			appendStatement(&statements, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(ch)
	}
	appendStatement(&statements, b.String())
	return statements
}

func appendStatement(statements *[]string, sqlText string) {
	cleaned := strings.TrimSpace(sqlText)
	if cleaned != "" {
		*statements = append(*statements, cleaned)
	}
}

func defaultStatementStatus(analysis RiskAnalysis) string {
	if analysis.RiskLevel == "BLOCKED" {
		return "NOT_EXECUTABLE"
	}
	return "PENDING"
}

func appendRiskReason(analysis RiskAnalysis, reason string) RiskAnalysis {
	analysis.RiskReason = appendRiskText(analysis.RiskReason, reason)
	return analysis
}

func appendRiskText(current, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return strings.TrimSpace(current)
	}
	current = strings.TrimSpace(current)
	if current == "" {
		return reason
	}
	if strings.Contains(current, reason) {
		return current
	}
	return strings.TrimSpace(current + "；" + reason)
}

func stripSQLComments(input string) string {
	var b strings.Builder
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	dollarTag := ""
	for i := 0; i < len(input); i++ {
		ch := input[i]
		next := byte(0)
		if i+1 < len(input) {
			next = input[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				b.WriteByte(ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if dollarTag != "" {
			if strings.HasPrefix(input[i:], dollarTag) {
				b.WriteString(dollarTag)
				i += len(dollarTag) - 1
				dollarTag = ""
				continue
			}
			b.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble && ch == '$' {
			if tag, ok := readDollarQuoteTag(input[i:]); ok {
				dollarTag = tag
				b.WriteString(tag)
				i += len(tag) - 1
				continue
			}
		}
		if !inSingle && !inDouble && ch == '-' && next == '-' {
			inLineComment = true
			i++
			continue
		}
		if !inSingle && !inDouble && ch == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}
		if ch == '\'' && !inDouble {
			b.WriteByte(ch)
			if inSingle && next == '\'' {
				i++
				b.WriteByte(next)
				continue
			}
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func readDollarQuoteTag(input string) (string, bool) {
	if input == "" || input[0] != '$' {
		return "", false
	}
	for i := 1; i < len(input); i++ {
		ch := input[i]
		if ch == '$' {
			return input[:i+1], true
		}
		if !(ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return "", false
		}
	}
	return "", false
}

func skipMessageForSQLError(err error, options SQLExecuteOptions) (bool, string) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr == nil {
		return false, ""
	}
	switch {
	case options.SkipExistsColumn && pgErr.Code == "42701":
		return true, "字段已存在"
	case options.SkipExistsTable && pgErr.Code == "42P07":
		return true, "对象已存在"
	case options.SkipUniqueConstraint && pgErr.Code == "23505" && strings.Contains(strings.ToLower(pgErr.Message), "unique"):
		return true, "违反唯一约束，数据已存在"
	default:
		return false, ""
	}
}

func BuildNotExecutableSQL(statements []SQLExportStatement) string {
	sort.SliceStable(statements, func(i, j int) bool {
		return statements[i].LineNumber < statements[j].LineNumber
	})
	var b strings.Builder
	for _, stmt := range statements {
		if !shouldExportSQL(stmt.ExecuteStatus) {
			continue
		}
		reason := strings.TrimSpace(stmt.ExecuteMessage)
		if reason == "" {
			reason = strings.TrimSpace(stmt.RiskReason)
		}
		b.WriteString(fmt.Sprintf("-- Line %d | %s", stmt.LineNumber, stmt.ExecuteStatus))
		if reason != "" {
			b.WriteString(" | ")
			b.WriteString(strings.ReplaceAll(reason, "\n", " "))
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(stmt.SQLContent))
		if !strings.HasSuffix(strings.TrimSpace(stmt.SQLContent), ";") {
			b.WriteString(";")
		}
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func shouldExportSQL(status string) bool {
	switch status {
	case "NOT_EXECUTABLE", "FAILED", "PARTIAL_FAILED", "BLOCKED":
		return true
	default:
		return false
	}
}

func isFailedSQLStatus(status string) bool {
	switch status {
	case "FAILED", "PARTIAL_FAILED", "CANCELED":
		return true
	default:
		return false
	}
}

func isPendingSQLStatus(status string) bool {
	switch status {
	case "PENDING", "RUNNING":
		return true
	default:
		return false
	}
}

func parseSQLFileMeta(fileName string) (version string, groupSortNo int, schemaName string) {
	name := filepath.Base(strings.TrimSpace(fileName))
	parts := strings.Split(name, "__")
	if len(parts) >= 3 {
		version = strings.TrimSpace(parts[0])
		if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			groupSortNo = n
		}
	}
	return version, groupSortNo, ""
}

func defaultSQLFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "manual.sql"
	}
	return name
}

func lineNumberForSQL(content, sqlText string) int {
	idx := strings.Index(content, strings.TrimSpace(sqlText))
	if idx < 0 {
		return 1
	}
	line := 1
	for _, r := range content[:idx] {
		if r == '\n' {
			line++
		}
	}
	return line
}

func readPGSettings(ctx context.Context, db *sql.DB) map[string]string {
	keys := []string{
		"server_version", "server_encoding", "TimeZone", "data_directory", "config_file",
		"hba_file", "max_connections", "shared_buffers", "work_mem", "maintenance_work_mem",
		"effective_cache_size", "wal_level", "archive_mode", "max_wal_senders", "hot_standby",
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		var value string
		if err := db.QueryRowContext(ctx, "SELECT current_setting($1, true)", key).Scan(&value); err == nil {
			values[key] = value
		}
	}
	return values
}

func readReplications(ctx context.Context, db *sql.DB) []ReplicationInfo {
	rows, err := db.QueryContext(ctx, `
SELECT COALESCE(client_addr::text,''), state, sync_state,
       COALESCE(write_lag::text,''), COALESCE(flush_lag::text,''), COALESCE(replay_lag::text,'')
FROM pg_stat_replication
ORDER BY client_addr::text`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []ReplicationInfo
	for rows.Next() {
		var item ReplicationInfo
		if err := rows.Scan(&item.ClientAddr, &item.State, &item.SyncState, &item.WriteLag, &item.FlushLag, &item.ReplayLag); err == nil {
			result = append(result, item)
		}
	}
	return result
}

func maskPassword(password string) string {
	if strings.TrimSpace(password) == "" {
		return ""
	}
	return "********"
}

type SQLFileFilter struct {
	Status     string
	Version    string
	SchemaName string
	FileName   string
}

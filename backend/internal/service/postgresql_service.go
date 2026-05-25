package service

import (
	"archive/zip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	pg_query "github.com/pganalyze/pg_query_go/v6"
)

type PostgreSQLService struct {
	settingsRepo *repository.SettingsRepo
}

func NewPostgreSQLService() *PostgreSQLService {
	return &PostgreSQLService{settingsRepo: repository.NewSettingsRepo()}
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
	SystemCode  string `json:"systemCode"`
	Environment string `json:"environment"`
	SchemaName  string `json:"schemaName"`
	Version     string `json:"version"`
	GroupSortNo int    `json:"groupSortNo"`
	FileName    string `json:"fileName"`
	Content     string `json:"content" binding:"required"`
	Overwrite   bool   `json:"overwrite"`
}

type RiskAnalysis struct {
	SQLType    string
	RiskLevel  string
	RiskReason string
}

type SQLExecuteOptions struct {
	SkipExistsColumn     bool `json:"skipExistsColumn"`
	SkipExistsTable      bool `json:"skipExistsTable"`
	SkipUniqueConstraint bool `json:"skipUniqueConstraint"`
}

type ExecuteSQLContentRequest struct {
	ParseSQLRequest
	Options SQLExecuteOptions `json:"options"`
}

type ImportServerSQLRequest struct {
	FilePath  string `json:"filePath" binding:"required"`
	Overwrite bool   `json:"overwrite"`
}

type SQLExportStatement struct {
	LineNumber     int
	SQLContent     string
	ExecuteStatus  string
	ExecuteMessage string
	RiskReason     string
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

func (s *PostgreSQLService) ParseSQL(req ParseSQLRequest) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	statements := SplitSQLStatements(req.Content)
	if len(statements) == 0 {
		return nil, nil, fmt.Errorf("SQL 内容为空")
	}

	fileName := defaultSQLFileName(req.FileName)
	version, groupSortNo, schemaName := parseSQLFileMeta(fileName)
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
		schemaName = inferSQLSchema(statements)
	}

	var exists model.SQLChangeFile
	if fileName != "" {
		err := repository.DB.Where("version = ? AND file_name = ? AND schema_name = ? AND is_deleted = ?", version, fileName, schemaName, false).First(&exists).Error
		if err == nil {
			if !req.Overwrite {
				return nil, nil, fmt.Errorf("SQL 文件已存在")
			}
			exists.IsDeleted = true
			_ = repository.DB.Save(&exists).Error
		}
	}

	file := &model.SQLChangeFile{
		SystemCode:    strings.TrimSpace(req.SystemCode),
		Environment:   strings.TrimSpace(req.Environment),
		SchemaName:    schemaName,
		Version:       version,
		GroupSortNo:   groupSortNo,
		FileName:      fileName,
		FileContent:   req.Content,
		ExecuteStatus: "PENDING",
	}
	if err := repository.DB.Create(file).Error; err != nil {
		return nil, nil, err
	}

	items := make([]model.SQLChangeStatement, 0, len(statements))
	for _, sqlText := range statements {
		analysis := AnalyzeSQLRisk(sqlText)
		if analysis.SQLType == "ALTER_COLUMN_TYPE" {
			analysis = s.enrichColumnTypeRiskWithMetadata(schemaName, sqlText, analysis)
			if deps := s.findViewDependencies(schemaName, sqlText); len(deps) > 0 {
				analysis.RiskReason = strings.TrimSpace(analysis.RiskReason + "；字段被以下视图依赖，直接修改可能失败: " + strings.Join(deps, ", "))
			}
		}
		item := model.SQLChangeStatement{
			FileID:        file.ID,
			LineNumber:    lineNumberForSQL(req.Content, sqlText),
			SQLContent:    sqlText,
			SQLType:       analysis.SQLType,
			RiskLevel:     analysis.RiskLevel,
			RiskReason:    analysis.RiskReason,
			ExecuteStatus: defaultStatementStatus(analysis),
		}
		if err := repository.DB.Create(&item).Error; err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return file, items, nil
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
	cfg, err := s.loadConfig()
	if err != nil {
		return analysis
	}
	db, err := openPostgreSQL(cfg)
	if err != nil {
		return analysis
	}
	defer db.Close()

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

func (s *PostgreSQLService) findViewDependencies(defaultSchema, sqlText string) []string {
	ref := parseAlterColumnTypeRef(sqlText, defaultSchema)
	if ref.schema == "" || ref.table == "" || ref.column == "" {
		return nil
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return nil
	}
	db, err := openPostgreSQL(cfg)
	if err != nil {
		return nil
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `
SELECT dependent_ns.nspname || '.' || dependent_view.relname
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
		return nil
	}
	defer rows.Close()
	var deps []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			deps = append(deps, name)
		}
	}
	return deps
}

type alterColumnRef struct {
	schema string
	table  string
	column string
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
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, fileID).Error; err != nil {
		return nil, nil, err
	}
	if file.ExecuteStatus == "SUCCESS" || file.ExecuteStatus == "SKIPPED" {
		return nil, nil, fmt.Errorf("SQL 文件已执行过，不能重复执行")
	}
	var statements []model.SQLChangeStatement
	if err := repository.DB.Where("file_id = ?", fileID).Order("line_number ASC, id ASC").Find(&statements).Error; err != nil {
		return nil, nil, err
	}

	cfg, err := s.loadConfig()
	if err != nil {
		return nil, nil, err
	}
	db, err := openPostgreSQL(cfg)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	file.ExecuteStatus = "RUNNING"
	file.ExecuteUser = username
	repository.DB.Save(&file)

	successCount := 0
	failedCount := 0
	blockedCount := 0
	skippedCount := 0
	for i := range statements {
		stmt := &statements[i]
		if stmt.ExecuteStatus == "SUCCESS" {
			successCount++
			continue
		}
		if stmt.ExecuteStatus == "SKIPPED" {
			skippedCount++
			continue
		}
		if stmt.ExecuteStatus == "NOT_EXECUTABLE" || stmt.RiskLevel == "BLOCKED" {
			stmt.ExecuteStatus = "NOT_EXECUTABLE"
			stmt.ExecuteMessage = stmt.RiskReason
			blockedCount++
			repository.DB.Save(stmt)
			continue
		}

		execCtx, cancel := context.WithTimeout(ctx, statementTimeout(stmt.SQLType)+10*time.Second)
		start := time.Now()
		stmt.ExecuteStatus = "RUNNING"
		repository.DB.Save(stmt)

		affected, sqlState, execErr := executeOneSQL(execCtx, db, file.SchemaName, stmt.SQLContent, stmt.SQLType)
		cancel()

		now := time.Now()
		stmt.ExecuteTime = &now
		stmt.DurationMs = time.Since(start).Milliseconds()
		stmt.AffectedRows = affected
		stmt.SQLState = sqlState
		if execErr != nil {
			if shouldSkip, skipMessage := skipMessageForSQLError(execErr, options); shouldSkip {
				stmt.ExecuteStatus = "SKIPPED"
				stmt.ExecuteMessage = skipMessage
				skippedCount++
			} else {
				stmt.ExecuteStatus = "FAILED"
				stmt.ExecuteMessage = execErr.Error()
				failedCount++
				repository.DB.Save(stmt)
				break
			}
		} else {
			stmt.ExecuteStatus = "SUCCESS"
			stmt.ExecuteMessage = "执行成功"
			successCount++
		}
		repository.DB.Save(stmt)
	}

	now := time.Now()
	file.ExecuteTime = &now
	switch {
	case failedCount > 0:
		file.ExecuteStatus = "PARTIAL_FAILED"
	case blockedCount > 0 && successCount == 0 && skippedCount == 0:
		file.ExecuteStatus = "NOT_EXECUTABLE"
	case blockedCount > 0:
		file.ExecuteStatus = "PARTIAL_FAILED"
	default:
		file.ExecuteStatus = "SUCCESS"
	}
	file.ExecuteMessage = fmt.Sprintf("成功 %d，失败 %d，不可执行 %d，跳过 %d", successCount, failedCount, blockedCount, skippedCount)
	repository.DB.Save(&file)
	return &file, statements, nil
}

func (s *PostgreSQLService) SkipSQLStatement(statementID uint, username string) (*model.SQLChangeStatement, error) {
	var stmt model.SQLChangeStatement
	if err := repository.DB.First(&stmt, statementID).Error; err != nil {
		return nil, err
	}
	if stmt.ExecuteStatus == "SUCCESS" {
		return nil, fmt.Errorf("已成功的 SQL 不能跳过")
	}
	now := time.Now()
	stmt.ExecuteStatus = "SKIPPED"
	stmt.ExecuteMessage = "手工跳过: " + username
	stmt.ExecuteTime = &now
	if err := repository.DB.Save(&stmt).Error; err != nil {
		return nil, err
	}
	return &stmt, nil
}

func (s *PostgreSQLService) SkipSQLFile(fileID uint, username string) (*model.SQLChangeFile, error) {
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, fileID).Error; err != nil {
		return nil, err
	}
	if file.ExecuteStatus == "SUCCESS" {
		return nil, fmt.Errorf("已成功的 SQL 文件不能跳过")
	}
	now := time.Now()
	file.ExecuteStatus = "SKIPPED"
	file.ExecuteMessage = "手工标记不执行"
	file.ExecuteUser = username
	file.ExecuteTime = &now
	if err := repository.DB.Save(&file).Error; err != nil {
		return nil, err
	}
	return &file, nil
}

func (s *PostgreSQLService) ListSQLFiles(page, pageSize int) ([]model.SQLChangeFile, int64, error) {
	return s.ListSQLFilesByStatus(page, pageSize, "")
}

func (s *PostgreSQLService) ListSQLFilesByStatus(page, pageSize int, statusGroup string) ([]model.SQLChangeFile, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var total int64
	var files []model.SQLChangeFile
	query := repository.DB.Model(&model.SQLChangeFile{}).Where("is_deleted = ?", false)
	switch statusGroup {
	case "todo":
		query = query.Where("execute_status IN ?", []string{"PENDING", "FAILED", "PARTIAL_FAILED", "NOT_EXECUTABLE"})
	case "done":
		query = query.Where("execute_status IN ?", []string{"SUCCESS", "SKIPPED"})
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := "id DESC"
	if statusGroup == "todo" {
		order = "version ASC, group_sort_no ASC, file_name ASC, id ASC"
	}
	err := query.Omit("file_content").Order(order).Offset((page - 1) * pageSize).Limit(pageSize).Find(&files).Error
	return files, total, err
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
	return &file, statements, nil
}

func (s *PostgreSQLService) DeleteSQLFile(id uint) error {
	var file model.SQLChangeFile
	if err := repository.DB.Where("is_deleted = ?", false).First(&file, id).Error; err != nil {
		return err
	}
	if file.ExecuteStatus == "SUCCESS" {
		return fmt.Errorf("已执行成功的文件不能删除")
	}
	file.IsDeleted = true
	return repository.DB.Save(&file).Error
}

func (s *PostgreSQLService) ExecuteSQLContent(ctx context.Context, req ExecuteSQLContentRequest, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	file, _, err := s.ParseSQL(req.ParseSQLRequest)
	if err != nil {
		return nil, nil, err
	}
	return s.ExecuteSQLFileWithOptions(ctx, file.ID, username, req.Options)
}

func (s *PostgreSQLService) ImportServerSQL(req ImportServerSQLRequest) (int, error) {
	path := strings.TrimSpace(req.FilePath)
	if path == "" {
		return 0, fmt.Errorf("文件路径不能为空")
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("文件不存在")
	}
	if info.IsDir() {
		return 0, fmt.Errorf("不支持导入目录")
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sql":
		content, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		_, _, err = s.ParseSQL(ParseSQLRequest{FileName: filepath.Base(path), Content: string(content), Overwrite: req.Overwrite})
		if err != nil {
			return 0, err
		}
		return 1, nil
	case ".zip":
		return s.importSQLZip(path, req.Overwrite)
	default:
		return 0, fmt.Errorf("仅支持 .sql 或 .zip 文件")
	}
}

func (s *PostgreSQLService) importSQLZip(path string, overwrite bool) (int, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("解压提取 SQL 错误: %w", err)
	}
	defer reader.Close()
	count := 0
	for _, f := range reader.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(filepath.Ext(f.Name), ".sql") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		content, readErr := io.ReadAll(rc)
		_ = rc.Close()
		if readErr != nil {
			return count, readErr
		}
		_, _, err = s.ParseSQL(ParseSQLRequest{FileName: filepath.Base(f.Name), Content: string(content), Overwrite: overwrite})
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *PostgreSQLService) BuildNotExecutableSQLForFile(id uint) (string, error) {
	_, statements, err := s.GetSQLFile(id)
	if err != nil {
		return "", err
	}
	exportItems := make([]SQLExportStatement, 0, len(statements))
	for _, stmt := range statements {
		exportItems = append(exportItems, SQLExportStatement{
			LineNumber:     stmt.LineNumber,
			SQLContent:     stmt.SQLContent,
			ExecuteStatus:  stmt.ExecuteStatus,
			ExecuteMessage: stmt.ExecuteMessage,
			RiskReason:     stmt.RiskReason,
		})
	}
	return BuildNotExecutableSQL(exportItems), nil
}

func (s *PostgreSQLService) loadConfig() (PostgreSQLConfig, error) {
	host, _ := s.settingsRepo.GetByKey("postgresql_host")
	port, _ := s.settingsRepo.GetByKey("postgresql_port")
	user, _ := s.settingsRepo.GetByKey("postgresql_user")
	password, _ := s.settingsRepo.GetByKey("postgresql_password")
	database, _ := s.settingsRepo.GetByKey("postgresql_database")
	if port == "" {
		port = "5432"
	}
	if database == "" {
		database = "postgres"
	}
	return PostgreSQLConfig{Host: host, Port: port, User: user, Password: password, Database: database}, nil
}

func openPostgreSQL(cfg PostgreSQLConfig) (*sql.DB, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("PostgreSQL 主机地址未配置")
	}
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func executeOneSQL(ctx context.Context, db *sql.DB, schemaName, sqlText, sqlType string) (int64, string, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

	if strings.TrimSpace(schemaName) != "" {
		if _, err := conn.ExecContext(ctx, "SELECT set_config('search_path', $1, false)", searchPathValue(schemaName)); err != nil {
			return 0, pgState(err), err
		}
	}
	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '5s'"); err != nil {
		return 0, pgState(err), err
	}
	timeout := statementTimeout(sqlType)
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET statement_timeout = '%dms'", timeout.Milliseconds())); err != nil {
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
	return rows, "", nil
}

func searchPathValue(schemaName string) string {
	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return ""
	}
	return `"` + strings.ReplaceAll(schemaName, `"`, `""`) + `"`
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

func parseSQLFileMeta(fileName string) (version string, groupSortNo int, schemaName string) {
	name := filepath.Base(strings.TrimSpace(fileName))
	parts := strings.Split(name, "__")
	if len(parts) >= 3 {
		version = strings.TrimSpace(parts[0])
		if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
			groupSortNo = n
		}
		schemaName = strings.TrimSpace(parts[2])
		if idx := strings.Index(schemaName, "."); idx >= 0 {
			schemaName = schemaName[:idx]
		}
	}
	return version, groupSortNo, schemaName
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
	password = strings.TrimSpace(password)
	if password == "" {
		return ""
	}
	if len([]rune(password)) <= 2 {
		return "**"
	}
	runes := []rune(password)
	return string(runes[:1]) + strings.Repeat("*", len(runes)-2) + string(runes[len(runes)-1:])
}

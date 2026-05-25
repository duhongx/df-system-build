package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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
	FileName    string `json:"fileName"`
	Content     string `json:"content" binding:"required"`
}

type RiskAnalysis struct {
	SQLType    string
	RiskLevel  string
	RiskReason string
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

	file := &model.SQLChangeFile{
		SystemCode:    strings.TrimSpace(req.SystemCode),
		Environment:   strings.TrimSpace(req.Environment),
		SchemaName:    strings.TrimSpace(req.SchemaName),
		Version:       strings.TrimSpace(req.Version),
		FileName:      defaultSQLFileName(req.FileName),
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
			if deps := s.findViewDependencies(req.SchemaName, sqlText); len(deps) > 0 {
				analysis.RiskReason = strings.TrimSpace(analysis.RiskReason + "；字段被以下视图依赖，直接修改可能失败: " + strings.Join(deps, ", "))
			}
		}
		status := "PENDING"
		if analysis.RiskLevel == "BLOCKED" {
			status = "BLOCKED"
		}
		item := model.SQLChangeStatement{
			FileID:        file.ID,
			LineNumber:    lineNumberForSQL(req.Content, sqlText),
			SQLContent:    sqlText,
			SQLType:       analysis.SQLType,
			RiskLevel:     analysis.RiskLevel,
			RiskReason:    analysis.RiskReason,
			ExecuteStatus: status,
		}
		if err := repository.DB.Create(&item).Error; err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return file, items, nil
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

func (s *PostgreSQLService) ExecuteSQLFile(ctx context.Context, fileID uint, username string) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	var file model.SQLChangeFile
	if err := repository.DB.First(&file, fileID).Error; err != nil {
		return nil, nil, err
	}
	var statements []model.SQLChangeStatement
	if err := repository.DB.Where("file_id = ?", fileID).Order("id ASC").Find(&statements).Error; err != nil {
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
		if stmt.RiskLevel == "BLOCKED" {
			stmt.ExecuteStatus = "BLOCKED"
			stmt.ExecuteMessage = stmt.RiskReason
			blockedCount++
			repository.DB.Save(stmt)
			continue
		}

		execCtx, cancel := context.WithTimeout(ctx, statementTimeout(stmt.SQLType)+10*time.Second)
		start := time.Now()
		stmt.ExecuteStatus = "RUNNING"
		repository.DB.Save(stmt)

		affected, sqlState, execErr := executeOneSQL(execCtx, db, stmt.SQLContent, stmt.SQLType)
		cancel()

		now := time.Now()
		stmt.ExecuteTime = &now
		stmt.DurationMs = time.Since(start).Milliseconds()
		stmt.AffectedRows = affected
		stmt.SQLState = sqlState
		if execErr != nil {
			stmt.ExecuteStatus = "FAILED"
			stmt.ExecuteMessage = execErr.Error()
			failedCount++
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
		file.ExecuteStatus = "BLOCKED"
	default:
		file.ExecuteStatus = "SUCCESS"
	}
	file.ExecuteMessage = fmt.Sprintf("成功 %d，失败 %d，拦截 %d，跳过 %d", successCount, failedCount, blockedCount, skippedCount)
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

func (s *PostgreSQLService) ListSQLFiles(page, pageSize int) ([]model.SQLChangeFile, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	var total int64
	var files []model.SQLChangeFile
	query := repository.DB.Model(&model.SQLChangeFile{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Omit("file_content").Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&files).Error
	return files, total, err
}

func (s *PostgreSQLService) GetSQLFile(id uint) (*model.SQLChangeFile, []model.SQLChangeStatement, error) {
	var file model.SQLChangeFile
	if err := repository.DB.First(&file, id).Error; err != nil {
		return nil, nil, err
	}
	var statements []model.SQLChangeStatement
	if err := repository.DB.Where("file_id = ?", id).Order("id ASC").Find(&statements).Error; err != nil {
		return nil, nil, err
	}
	return &file, statements, nil
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

func executeOneSQL(ctx context.Context, db *sql.DB, sqlText, sqlType string) (int64, string, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()

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

func AnalyzeSQLRisk(sqlText string) RiskAnalysis {
	tree, err := pg_query.Parse(sqlText)
	if err != nil {
		return RiskAnalysis{SQLType: "SQL_PARSE_ERROR", RiskLevel: "BLOCKED", RiskReason: "SQL 语法解析失败: " + err.Error()}
	}
	if risk, ok := analyzeSQLRiskFromAST(tree); ok {
		return risk
	}

	normalized := normalizeSQL(sqlText)
	upper := strings.ToUpper(normalized)

	blocked := []struct {
		pattern string
		typ     string
		reason  string
	}{
		{`^DROP\s+DATABASE\b`, "DROP_DATABASE", "禁止通过普通入口删除数据库"},
		{`^DROP\s+SCHEMA\b`, "DROP_SCHEMA", "禁止通过普通入口删除 schema"},
		{`^DROP\s+OWNED\b`, "DROP_OWNED", "禁止执行 DROP OWNED"},
		{`^ALTER\s+SYSTEM\b`, "ALTER_SYSTEM", "禁止修改数据库系统级参数"},
		{`^COPY\b.*\bPROGRAM\b`, "COPY_PROGRAM", "禁止执行 COPY PROGRAM"},
		{`^REINDEX\s+DATABASE\b`, "REINDEX_DATABASE", "禁止执行 REINDEX DATABASE"},
		{`^VACUUM\s+FULL\b`, "VACUUM_FULL", "禁止执行 VACUUM FULL"},
	}
	for _, rule := range blocked {
		if regexp.MustCompile(rule.pattern).MatchString(upper) {
			return RiskAnalysis{SQLType: rule.typ, RiskLevel: "BLOCKED", RiskReason: rule.reason}
		}
	}

	switch {
	case strings.HasPrefix(upper, "INSERT"):
		return RiskAnalysis{SQLType: "INSERT", RiskLevel: "LOW"}
	case strings.HasPrefix(upper, "UPDATE"):
		return RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}
	case strings.HasPrefix(upper, "DELETE"):
		return RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}
	case regexp.MustCompile(`^CREATE\s+TABLE\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}
	case regexp.MustCompile(`^CREATE\s+(OR\s+REPLACE\s+)?VIEW\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bADD\s+COLUMN\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ADD_COLUMN", RiskLevel: "LOW"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bALTER\s+COLUMN\b.*\bTYPE\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ALTER_COLUMN_TYPE", RiskLevel: "WARN", RiskReason: "字段类型变更可能触发表重写或被视图依赖阻塞"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bSET\s+NOT\s+NULL\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ALTER_SET_NOT_NULL", RiskLevel: "WARN", RiskReason: "设置 NOT NULL 可能扫描全表"}
	case regexp.MustCompile(`^ALTER\s+TABLE\b.*\bADD\s+CHECK\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "ADD_CHECK", RiskLevel: "WARN", RiskReason: "新增 CHECK 默认校验已有数据，可能扫描全表"}
	case regexp.MustCompile(`^CREATE\s+INDEX\s+CONCURRENTLY\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发索引耗时可能较长"}
	case regexp.MustCompile(`^CREATE\s+INDEX\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}
	case regexp.MustCompile(`^TRUNCATE\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"}
	case regexp.MustCompile(`^DROP\s+TABLE\b`).MatchString(upper):
		return RiskAnalysis{SQLType: "DROP_TABLE", RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"}
	default:
		return RiskAnalysis{SQLType: firstKeyword(upper), RiskLevel: "LOW"}
	}
}

func analyzeSQLRiskFromAST(tree *pg_query.ParseResult) (RiskAnalysis, bool) {
	if tree == nil || len(tree.GetStmts()) == 0 {
		return RiskAnalysis{SQLType: "UNKNOWN", RiskLevel: "LOW"}, true
	}
	if len(tree.GetStmts()) > 1 {
		return RiskAnalysis{SQLType: "MULTI_STATEMENT", RiskLevel: "WARN", RiskReason: "单条记录包含多条 SQL，请确认拆分结果"}, true
	}
	node := tree.GetStmts()[0].GetStmt()
	if node == nil {
		return RiskAnalysis{SQLType: "UNKNOWN", RiskLevel: "LOW"}, true
	}

	switch {
	case node.GetInsertStmt() != nil:
		return RiskAnalysis{SQLType: "INSERT", RiskLevel: "LOW"}, true
	case node.GetUpdateStmt() != nil:
		return RiskAnalysis{SQLType: "UPDATE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}, true
	case node.GetDeleteStmt() != nil:
		return RiskAnalysis{SQLType: "DELETE", RiskLevel: "LOW", RiskReason: "建议关注影响行数"}, true
	case node.GetCreateStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE", RiskLevel: "LOW"}, true
	case node.GetCreateTableAsStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_TABLE_AS", RiskLevel: "WARN", RiskReason: "CREATE TABLE AS 可能写入大量数据，请确认数据规模"}, true
	case node.GetViewStmt() != nil:
		return RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}, true
	case node.GetAlterSystemStmt() != nil:
		return RiskAnalysis{SQLType: "ALTER_SYSTEM", RiskLevel: "BLOCKED", RiskReason: "禁止修改数据库系统级参数"}, true
	case node.GetDropOwnedStmt() != nil:
		return RiskAnalysis{SQLType: "DROP_OWNED", RiskLevel: "BLOCKED", RiskReason: "禁止执行 DROP OWNED"}, true
	case node.GetCopyStmt() != nil:
		copyStmt := node.GetCopyStmt()
		if copyStmt.GetIsProgram() {
			return RiskAnalysis{SQLType: "COPY_PROGRAM", RiskLevel: "BLOCKED", RiskReason: "禁止执行 COPY PROGRAM"}, true
		}
		return RiskAnalysis{SQLType: "COPY", RiskLevel: "WARN", RiskReason: "COPY 可能批量导入或导出数据，请确认文件和数据范围"}, true
	case node.GetDropStmt() != nil:
		return analyzeDropStmt(node.GetDropStmt()), true
	case node.GetAlterTableStmt() != nil:
		return analyzeAlterTableStmt(node.GetAlterTableStmt()), true
	case node.GetIndexStmt() != nil:
		indexStmt := node.GetIndexStmt()
		if indexStmt.GetConcurrent() {
			return RiskAnalysis{SQLType: "CREATE_INDEX_CONCURRENTLY", RiskLevel: "WARN", RiskReason: "并发索引耗时可能较长"}, true
		}
		return RiskAnalysis{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入"}, true
	case node.GetTruncateStmt() != nil:
		return RiskAnalysis{SQLType: "TRUNCATE", RiskLevel: "WARN", RiskReason: "TRUNCATE 会快速清空表数据，请确认对象范围"}, true
	case node.GetReindexStmt() != nil:
		reindexStmt := node.GetReindexStmt()
		if reindexStmt.GetKind() == pg_query.ReindexObjectType_REINDEX_OBJECT_DATABASE {
			return RiskAnalysis{SQLType: "REINDEX_DATABASE", RiskLevel: "BLOCKED", RiskReason: "禁止执行 REINDEX DATABASE"}, true
		}
		return RiskAnalysis{SQLType: "REINDEX", RiskLevel: "WARN", RiskReason: "REINDEX 可能长时间持锁，请确认对象范围和窗口期"}, true
	case node.GetVacuumStmt() != nil:
		if vacuumHasOption(node.GetVacuumStmt(), "full") {
			return RiskAnalysis{SQLType: "VACUUM_FULL", RiskLevel: "BLOCKED", RiskReason: "禁止执行 VACUUM FULL"}, true
		}
		return RiskAnalysis{SQLType: "VACUUM", RiskLevel: "LOW"}, true
	default:
		return RiskAnalysis{}, false
	}
}

func analyzeDropStmt(stmt *pg_query.DropStmt) RiskAnalysis {
	switch stmt.GetRemoveType() {
	case pg_query.ObjectType_OBJECT_DATABASE:
		return RiskAnalysis{SQLType: "DROP_DATABASE", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口删除数据库"}
	case pg_query.ObjectType_OBJECT_SCHEMA:
		return RiskAnalysis{SQLType: "DROP_SCHEMA", RiskLevel: "BLOCKED", RiskReason: "禁止通过普通入口删除 schema"}
	case pg_query.ObjectType_OBJECT_TABLE:
		return RiskAnalysis{SQLType: "DROP_TABLE", RiskLevel: "WARN", RiskReason: "DROP TABLE 会删除表结构和数据，请确认对象范围"}
	default:
		return RiskAnalysis{SQLType: "DROP", RiskLevel: "WARN", RiskReason: "DROP 会删除数据库对象，请确认对象范围"}
	}
}

func analyzeAlterTableStmt(stmt *pg_query.AlterTableStmt) RiskAnalysis {
	risk := RiskAnalysis{SQLType: "ALTER_TABLE", RiskLevel: "LOW"}
	for _, cmdNode := range stmt.GetCmds() {
		cmd := cmdNode.GetAlterTableCmd()
		if cmd == nil {
			continue
		}
		switch cmd.GetSubtype() {
		case pg_query.AlterTableType_AT_AddColumn:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_COLUMN", RiskLevel: "LOW"})
		case pg_query.AlterTableType_AT_AlterColumnType:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_COLUMN_TYPE", RiskLevel: "WARN", RiskReason: "字段类型变更可能触发表重写或被视图依赖阻塞"})
		case pg_query.AlterTableType_AT_SetNotNull:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_SET_NOT_NULL", RiskLevel: "WARN", RiskReason: "设置 NOT NULL 可能扫描全表"})
		case pg_query.AlterTableType_AT_AddConstraint:
			if constraint := cmd.GetDef().GetConstraint(); constraint != nil && constraint.GetContype() == pg_query.ConstrType_CONSTR_CHECK {
				risk = mergeRisk(risk, RiskAnalysis{SQLType: "ADD_CHECK", RiskLevel: "WARN", RiskReason: "新增 CHECK 默认校验已有数据，可能扫描全表"})
			}
		case pg_query.AlterTableType_AT_DropColumn, pg_query.AlterTableType_AT_DropConstraint:
			risk = mergeRisk(risk, RiskAnalysis{SQLType: "ALTER_TABLE_DROP", RiskLevel: "WARN", RiskReason: "ALTER TABLE 删除字段或约束可能影响业务，请确认依赖范围"})
		}
	}
	return risk
}

func vacuumHasOption(stmt *pg_query.VacuumStmt, option string) bool {
	for _, node := range stmt.GetOptions() {
		def := node.GetDefElem()
		if def != nil && strings.EqualFold(def.GetDefname(), option) {
			return true
		}
	}
	return false
}

func mergeRisk(current, candidate RiskAnalysis) RiskAnalysis {
	if riskRank(candidate.RiskLevel) > riskRank(current.RiskLevel) {
		return candidate
	}
	if current.SQLType == "ALTER_TABLE" && candidate.SQLType != "" {
		return candidate
	}
	return current
}

func riskRank(level string) int {
	switch level {
	case "BLOCKED":
		return 3
	case "WARN":
		return 2
	case "LOW":
		return 1
	default:
		return 0
	}
}

func normalizeSQL(sqlText string) string {
	return strings.Join(strings.Fields(sqlText), " ")
}

func firstKeyword(sqlText string) string {
	for _, part := range strings.Fields(sqlText) {
		return strings.ToUpper(part)
	}
	return "UNKNOWN"
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

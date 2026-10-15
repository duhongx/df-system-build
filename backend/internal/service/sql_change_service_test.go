package service

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/logger"

	"github.com/jackc/pgx/v5/pgconn"
)

func setupSQLChangeServiceTestDB(t *testing.T) {
	t.Helper()
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(testutil.PostgresConfig(t)); err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
}

func setSQLExecutionSettingsToTestDatabase(t *testing.T) {
	t.Helper()
	settings := repository.NewSettingsRepo()
	values := map[string]string{
		"postgresql_host":           os.Getenv("TEST_DB_HOST"),
		"postgresql_port":           os.Getenv("TEST_DB_PORT"),
		"postgresql_database":       os.Getenv("TEST_DB_NAME"),
		"postgresql_user":           os.Getenv("TEST_DB_USER"),
		"postgresql_password":       os.Getenv("TEST_DB_PASSWORD"),
		"postgresql_admin_password": os.Getenv("TEST_DB_PASSWORD"),
	}
	if values["postgresql_port"] == "" {
		values["postgresql_port"] = "5432"
	}
	if values["postgresql_host"] == "" || values["postgresql_database"] == "" || values["postgresql_user"] == "" {
		for key, value := range parseTestPostgresDSN(os.Getenv("TEST_DATABASE_DSN")) {
			switch key {
			case "host":
				values["postgresql_host"] = value
			case "port":
				values["postgresql_port"] = value
			case "dbname":
				values["postgresql_database"] = value
			case "user":
				values["postgresql_user"] = value
			case "password":
				values["postgresql_password"] = value
				values["postgresql_admin_password"] = value
			}
		}
	}
	for key, value := range values {
		if err := settings.Set(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
}

func parseTestPostgresDSN(dsn string) map[string]string {
	values := map[string]string{}
	for _, part := range strings.Fields(dsn) {
		key, value, ok := strings.Cut(part, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func TestSplitSQLStatementsHandlesSemicolonsInStringsAndComments(t *testing.T) {
	input := `
-- comment with ;
INSERT INTO demo(name) VALUES ('a;b');
/* block ; comment */
CREATE OR REPLACE VIEW v_demo AS SELECT 'x;y' AS value;
`

	statements := SplitSQLStatements(input)

	if len(statements) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(statements), statements)
	}
	if statements[0] != "INSERT INTO demo(name) VALUES ('a;b')" {
		t.Fatalf("unexpected first statement: %q", statements[0])
	}
	if statements[1] != "CREATE OR REPLACE VIEW v_demo AS SELECT 'x;y' AS value" {
		t.Fatalf("unexpected second statement: %q", statements[1])
	}
}

func TestSplitSQLStatementsHandlesDollarQuotedFunctionBodies(t *testing.T) {
	input := `
CREATE OR REPLACE FUNCTION public.touch_updated_at()
RETURNS trigger AS $$
BEGIN
  RAISE NOTICE 'updated; at';
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2);
`

	statements := SplitSQLStatements(input)

	if len(statements) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "RAISE NOTICE 'updated; at';") {
		t.Fatalf("expected function body to stay intact, got %q", statements[0])
	}
	if statements[1] != `ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2)` {
		t.Fatalf("unexpected second statement: %q", statements[1])
	}
}

func TestAnalyzeSQLRiskBlocksDangerousStatements(t *testing.T) {
	result := AnalyzeSQLRisk("DROP DATABASE his_prod")

	if result.SQLType != "DROP_DATABASE" {
		t.Fatalf("expected DROP_DATABASE, got %q", result.SQLType)
	}
	if result.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED risk, got %q", result.RiskLevel)
	}
	if result.RiskReason == "" {
		t.Fatalf("expected risk reason")
	}
}

func TestAnalyzeSQLRiskUsesParserForQuotedAlterColumnType(t *testing.T) {
	result := AnalyzeSQLRisk(`ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2)`)

	if result.SQLType != "ALTER_COLUMN_TYPE" {
		t.Fatalf("expected ALTER_COLUMN_TYPE, got %q", result.SQLType)
	}
	if result.RiskLevel != "WARN" {
		t.Fatalf("expected WARN risk, got %q", result.RiskLevel)
	}
	if result.RiskReason == "" {
		t.Fatalf("expected risk reason")
	}
}

func TestAnalyzeSQLRiskBlocksInvalidSQL(t *testing.T) {
	result := AnalyzeSQLRisk("CREATE TABLE broken (")

	if result.SQLType != "SQL_PARSE_ERROR" {
		t.Fatalf("expected SQL_PARSE_ERROR, got %q", result.SQLType)
	}
	if result.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED risk, got %q", result.RiskLevel)
	}
	if result.RiskReason == "" {
		t.Fatalf("expected risk reason")
	}
}

func TestAnalyzeSQLRiskMarksDDLWarningsWithoutBlocking(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		typ  string
	}{
		{name: "alter type", sql: "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(20)", typ: "ALTER_COLUMN_TYPE"},
		{name: "create index", sql: "CREATE INDEX idx_patient_name ON his.patient(name)", typ: "CREATE_INDEX"},
		{name: "set not null", sql: "ALTER TABLE his.patient ALTER COLUMN code SET NOT NULL", typ: "ALTER_SET_NOT_NULL"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := AnalyzeSQLRisk(tc.sql)
			if result.SQLType != tc.typ {
				t.Fatalf("expected type %q, got %q", tc.typ, result.SQLType)
			}
			if result.RiskLevel != "WARN" {
				t.Fatalf("expected WARN risk, got %q", result.RiskLevel)
			}
			if result.RiskReason == "" {
				t.Fatalf("expected risk reason")
			}
		})
	}
}

func TestParseAlterColumnTypeRefUsesDefaultSchema(t *testing.T) {
	ref := parseAlterColumnTypeRef(`ALTER TABLE patient ALTER COLUMN code TYPE varchar(20)`, "his")

	if ref.schema != "his" || ref.table != "patient" || ref.column != "code" {
		t.Fatalf("unexpected alter ref: %+v", ref)
	}
}

func TestParseAlterColumnTypeRefHandlesQuotedIdentifiers(t *testing.T) {
	ref := parseAlterColumnTypeRef(`ALTER TABLE "billing"."Order Detail" ALTER COLUMN "total amount" TYPE numeric(12,2)`, "public")

	if ref.schema != "billing" || ref.table != "Order Detail" || ref.column != "total amount" {
		t.Fatalf("unexpected alter ref: %+v", ref)
	}
}

func TestInferSQLSchemaFromQualifiedStatements(t *testing.T) {
	schema := inferSQLSchema([]string{
		`INSERT INTO "billing"."order_detail"(id) VALUES (1)`,
		`ALTER TABLE public.patient ALTER COLUMN code TYPE varchar(20)`,
	})

	if schema != "billing" {
		t.Fatalf("expected billing schema, got %q", schema)
	}
}

func TestSQLFileExecuteRouteStartsBackgroundJob(t *testing.T) {
	source, err := os.ReadFile("../handler/postgresql.go")
	if err != nil {
		t.Fatalf("read postgresql handler source: %v", err)
	}
	content := string(source)
	if !strings.Contains(content, "StartSQLFileExecution") {
		t.Fatalf("SQL file execute route should start a background execution job")
	}
	if strings.Contains(content, "ExecuteSQLFileWithOptions(c.Request.Context()") {
		t.Fatalf("SQL file execute route must not run the whole file synchronously on the HTTP request context")
	}
}

func TestParseCreateOrReplaceViewRef(t *testing.T) {
	ref := parseCreateOrReplaceViewRef(`CREATE OR REPLACE VIEW "his"."v_patient" AS SELECT 1`, "public")

	if ref.schema != "his" || ref.table != "v_patient" {
		t.Fatalf("unexpected view ref: %+v", ref)
	}
}

func TestExtractCreateOrReplaceViewSelectSQL(t *testing.T) {
	got, ok := extractCreateOrReplaceViewSelectSQL(`CREATE OR REPLACE VIEW "his"."v_patient" AS SELECT id, name FROM his.patient WHERE deleted = false`)

	if !ok {
		t.Fatalf("expected view select SQL to be extracted")
	}
	if !strings.HasPrefix(strings.ToUpper(got), "SELECT") {
		t.Fatalf("expected SELECT SQL, got %q", got)
	}
	if !strings.Contains(got, "his.patient") {
		t.Fatalf("expected source table in SELECT SQL, got %q", got)
	}
}

func TestParseTableRefForRiskOperations(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "set not null", sql: "ALTER TABLE his.patient ALTER COLUMN code SET NOT NULL"},
		{name: "add check", sql: "ALTER TABLE his.patient ADD CONSTRAINT chk_code CHECK (code <> '')"},
		{name: "create index", sql: "CREATE INDEX idx_patient_code ON his.patient(code)"},
		{name: "create index concurrently", sql: "CREATE INDEX CONCURRENTLY idx_patient_code ON his.patient(code)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := parseTableRefForRiskOperation(tc.sql, "public")
			if ref.schema != "his" || ref.table != "patient" {
				t.Fatalf("unexpected table ref for %s: %+v", tc.sql, ref)
			}
		})
	}
}

func TestLoadSQLExecutionConfigUsesPostgresAdmin(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	settings := repository.NewSettingsRepo()
	for key, value := range map[string]string{
		"postgresql_host":           "192.168.199.102",
		"postgresql_port":           "5432",
		"postgresql_database":       "df_his",
		"postgresql_user":           "df_zhushuju",
		"postgresql_password":       "business-password",
		"postgresql_admin_password": "admin-password",
	} {
		if err := settings.Set(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	cfg, ownerUser, err := NewPostgreSQLService().loadSQLExecutionConfig()
	if err != nil {
		t.Fatalf("load execution config: %v", err)
	}

	if cfg.Host != "192.168.199.102" || cfg.Port != "5432" || cfg.Database != "df_his" {
		t.Fatalf("unexpected connection target: %+v", cfg)
	}
	if cfg.User != "postgres" {
		t.Fatalf("SQL execution must use postgres admin user, got %q", cfg.User)
	}
	if cfg.Password != "admin-password" {
		t.Fatalf("SQL execution must use admin password, got %q", cfg.Password)
	}
	if ownerUser != "" {
		t.Fatalf("retired business username must not affect administrator operations, got %q", ownerUser)
	}
}

func TestBuildCreateTableOwnerSQL(t *testing.T) {
	tests := []struct {
		name          string
		sqlText       string
		defaultSchema string
		want          string
		wantOwnerSQL  bool
	}{
		{
			name:          "qualified table",
			sqlText:       `CREATE TABLE his.patient(id int)`,
			defaultSchema: "public",
			want:          `ALTER TABLE "his"."patient" OWNER TO "df_zhushuju";`,
			wantOwnerSQL:  true,
		},
		{
			name:          "if not exists uses default schema",
			sqlText:       `CREATE TABLE IF NOT EXISTS patient(id int)`,
			defaultSchema: "his",
			want:          `ALTER TABLE "his"."patient" OWNER TO "df_zhushuju";`,
			wantOwnerSQL:  true,
		},
		{
			name:          "quoted identifiers",
			sqlText:       `CREATE TABLE "Billing"."Order Detail"(id int)`,
			defaultSchema: "public",
			want:          `ALTER TABLE "Billing"."Order Detail" OWNER TO "df_zhushuju";`,
			wantOwnerSQL:  true,
		},
		{
			name:          "non create table",
			sqlText:       `CREATE INDEX idx_patient_id ON his.patient(id)`,
			defaultSchema: "his",
			wantOwnerSQL:  false,
		},
		{
			name:          "temporary table",
			sqlText:       `CREATE TEMP TABLE session_tmp(id int)`,
			defaultSchema: "his",
			wantOwnerSQL:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := buildCreateTableOwnerSQL(tc.sqlText, tc.defaultSchema, "df_zhushuju")
			if err != nil {
				t.Fatalf("build owner sql: %v", err)
			}
			if ok != tc.wantOwnerSQL {
				t.Fatalf("expected owner SQL flag %v, got %v with %q", tc.wantOwnerSQL, ok, got)
			}
			if got != tc.want {
				t.Fatalf("unexpected owner SQL:\nwant: %s\n got: %s", tc.want, got)
			}
		})
	}
}

func TestParseSQLUsesFileNameMetadata(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file, _, err := NewPostgreSQLService().ParseSQL(ParseSQLRequest{FileName: "V1__2__his.sql", Version: "ignored", SchemaName: "ignored", Content: "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}
	if file.Version != "V1" || file.GroupSortNo != 2 || file.SchemaName != "his" {
		t.Fatalf("unexpected metadata: %+v", file)
	}
}

func TestParseSQLFileMetaDoesNotTreatDescriptionAsSchema(t *testing.T) {
	version, groupSortNo, schemaName := parseSQLFileMeta("15.3_SP15_260507__18__有需要再执行小数位数相关-有视图依赖需手动执行.sql")

	if version != "15.3_SP15_260507" {
		t.Fatalf("expected version from file name, got %q", version)
	}
	if groupSortNo != 18 {
		t.Fatalf("expected group sort number from file name, got %d", groupSortNo)
	}
	if schemaName != "" {
		t.Fatalf("file description must not be treated as schema, got %q", schemaName)
	}
}

func TestParseSQLRejectsDescriptionInPlaceOfSchema(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	_, _, err := NewPostgreSQLService().ParseSQL(ParseSQLRequest{FileName: "V1__18__有需要再执行.sql", Content: "ALTER TABLE his.patient ADD COLUMN note text"})
	if err == nil || !strings.Contains(err.Error(), "文件名格式") {
		t.Fatalf("expected explicit filename validation, got %v", err)
	}
}

func TestParseSQLDoesNotAnalyzeOrRewriteStatements(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	_, rows, err := NewPostgreSQLService().ParseSQL(ParseSQLRequest{FileName: "V1__1__his.sql", Content: "DELETE FROM patient; CREATE INDEX CONCURRENTLY idx_patient_name ON patient(name);"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].SQLContent != "DELETE FROM patient" {
		t.Fatalf("SQL was changed: %+v", rows)
	}
	for _, row := range rows {
		if row.RiskLevel != "" || row.ExecutionStrategy != "" || row.ExecuteStatus != "PENDING" {
			t.Fatalf("unexpected analysis: %+v", row)
		}
	}
}

func TestGetSQLFileOrdersStatementsByLineNumber(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file := model.SQLChangeFile{FileName: "ordered.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	statements := []model.SQLChangeStatement{
		{FileID: file.ID, LineNumber: 20, SQLContent: "SELECT 20", ExecuteStatus: "PENDING"},
		{FileID: file.ID, LineNumber: 10, SQLContent: "SELECT 10", ExecuteStatus: "PENDING"},
	}
	if err := repository.DB.Create(&statements).Error; err != nil {
		t.Fatalf("create statements: %v", err)
	}

	_, got, err := NewPostgreSQLService().GetSQLFile(file.ID)
	if err != nil {
		t.Fatalf("get sql file: %v", err)
	}
	if len(got) != 2 || got[0].LineNumber != 10 || got[1].LineNumber != 20 {
		t.Fatalf("expected line order 10,20, got %#v", got)
	}
}

func TestUpdateSQLFileContentReparsesAndResetsFile(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()
	file, statements, err := svc.ParseSQL(ParseSQLRequest{
		FileName:   "V1__1__public.sql",
		SchemaName: "public",
		Content:    "SELECT 1;",
		Overwrite:  true,
	})
	if err != nil {
		t.Fatalf("parse sql: %v", err)
	}
	if err := repository.DB.Model(&model.SQLChangeFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"execute_status":  "PARTIAL_FAILED",
		"execute_message": "旧错误",
		"execute_user":    "tester",
	}).Error; err != nil {
		t.Fatalf("mark file failed: %v", err)
	}

	updated, updatedStatements, err := svc.UpdateSQLFileContent(file.ID, UpdateSQLFileContentRequest{
		Content: "SELECT 2;\nSELECT 3;",
	}, "editor")
	if err != nil {
		t.Fatalf("update sql file content: %v", err)
	}
	if updated.ID != file.ID {
		t.Fatalf("expected in-place update of file id %d, got %d", file.ID, updated.ID)
	}
	if updated.ExecuteStatus != "PENDING" || updated.ExecuteMessage != "" || updated.ExecuteUser != "" || updated.ExecuteTime != nil {
		t.Fatalf("expected file execution state reset, got %+v", updated)
	}
	if updated.FileContent != "SELECT 2;\nSELECT 3;" {
		t.Fatalf("expected updated file content, got %q", updated.FileContent)
	}
	if len(updatedStatements) != 2 {
		t.Fatalf("expected 2 reparsed statements, got %d", len(updatedStatements))
	}
	if updatedStatements[0].ID == statements[0].ID {
		t.Fatalf("expected old statements to be replaced")
	}
	if updatedStatements[0].SQLContent != "SELECT 2" || updatedStatements[1].SQLContent != "SELECT 3" {
		t.Fatalf("unexpected reparsed statements: %#v", updatedStatements)
	}
}

func TestUpdateSQLFileContentRejectsRunningAndSuccessfulFiles(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()
	for _, status := range []string{"RUNNING", "SUCCESS"} {
		t.Run(status, func(t *testing.T) {
			file := model.SQLChangeFile{FileName: status + ".sql", FileContent: "SELECT 1", ExecuteStatus: status}
			if err := repository.DB.Create(&file).Error; err != nil {
				t.Fatalf("create file: %v", err)
			}

			_, _, err := svc.UpdateSQLFileContent(file.ID, UpdateSQLFileContentRequest{Content: "SELECT 2;"}, "editor")
			if err == nil {
				t.Fatalf("expected %s file update to be rejected", status)
			}
		})
	}
}

func TestPostgreSQLServiceDoesNotSetSearchPathForTargetSQL(t *testing.T) {
	source, err := os.ReadFile("postgresql_service.go")
	if err != nil {
		t.Fatalf("read postgresql service source: %v", err)
	}
	content := string(source)
	if strings.Contains(content, "set_config('search_path'") || strings.Contains(content, "searchPathValue(") {
		t.Fatalf("target SQL execution must not set search_path; SQL statements must use explicit schema.table")
	}
}

func TestAnalyzeSQLRiskMarksBlockedSQLAsNotExecutable(t *testing.T) {
	result := AnalyzeSQLRisk("ALTER SYSTEM SET work_mem = '64MB'")

	if result.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED risk, got %q", result.RiskLevel)
	}
	if defaultStatementStatus(result) != "NOT_EXECUTABLE" {
		t.Fatalf("expected NOT_EXECUTABLE status, got %q", defaultStatementStatus(result))
	}
}

func TestSQLExecuteOptionsSkipLegacyPostgreSQLErrors(t *testing.T) {
	tests := []struct {
		name    string
		option  SQLExecuteOptions
		err     error
		message string
		want    bool
	}{
		{
			name:    "skip existing column",
			option:  SQLExecuteOptions{SkipExistsColumn: true},
			err:     &pgconn.PgError{Code: "42701", Message: "column already exists"},
			message: "字段已存在",
			want:    true,
		},
		{
			name:    "skip existing object",
			option:  SQLExecuteOptions{SkipExistsTable: true},
			err:     &pgconn.PgError{Code: "42P07", Message: "relation already exists"},
			message: "对象已存在",
			want:    true,
		},
		{
			name:    "skip unique conflict",
			option:  SQLExecuteOptions{SkipUniqueConstraint: true},
			err:     &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"},
			message: "违反唯一约束，数据已存在",
			want:    true,
		},
		{
			name:   "does not skip disabled option",
			option: SQLExecuteOptions{},
			err:    &pgconn.PgError{Code: "42701", Message: "column already exists"},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := skipMessageForSQLError(tc.err, tc.option)
			if got != tc.want {
				t.Fatalf("expected skip=%v, got %v", tc.want, got)
			}
			if got && msg != tc.message {
				t.Fatalf("expected message %q, got %q", tc.message, msg)
			}
		})
	}
}

func TestSQLExecuteOptionsRequireWarnConfirmation(t *testing.T) {
	statements := []model.SQLChangeStatement{
		{SQLType: "CREATE_TABLE", RiskLevel: "LOW", ExecuteStatus: "PENDING"},
		{SQLType: "CREATE_INDEX", RiskLevel: "WARN", RiskReason: "非 CONCURRENTLY 创建索引可能阻塞写入", ExecuteStatus: "PENDING"},
	}

	err := requireWarnConfirmation(statements, SQLExecuteOptions{RequireRiskConfirmation: true})
	if err == nil {
		t.Fatalf("expected warn confirmation error")
	}
	if !strings.Contains(err.Error(), "存在 1 条 WARN") {
		t.Fatalf("unexpected error: %v", err)
	}

	err = requireWarnConfirmation(statements, SQLExecuteOptions{RequireRiskConfirmation: true, ConfirmWarnRisk: true})
	if err != nil {
		t.Fatalf("expected confirmed warn risk to pass, got %v", err)
	}
}

func TestShouldBlockStatementAllowsForcedBlockedSQL(t *testing.T) {
	stmt := model.SQLChangeStatement{
		SQLType:       "UPDATE_WEAK_WHERE",
		RiskLevel:     "BLOCKED",
		ExecuteStatus: "NOT_EXECUTABLE",
	}

	if !shouldBlockStatement(stmt, SQLExecuteOptions{}) {
		t.Fatalf("expected blocked statement to be skipped by default")
	}
	if !shouldBlockStatementWithWhitelist(stmt, SQLExecuteOptions{ForceBlockedSQL: true}, nil) {
		t.Fatalf("expected force option without whitelist to keep blocked statement skipped")
	}
	if shouldBlockStatementWithWhitelist(stmt, SQLExecuteOptions{ForceBlockedSQL: true}, []string{"UPDATE_WEAK_WHERE"}) {
		t.Fatalf("expected configured force whitelist to allow blocked statement execution")
	}
}

func TestDefaultForceWhitelistAllowsBusinessHighRiskSQL(t *testing.T) {
	for _, sqlType := range []string{
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
	} {
		stmt := model.SQLChangeStatement{
			SQLType:       sqlType,
			RiskLevel:     "BLOCKED",
			ExecuteStatus: "NOT_EXECUTABLE",
		}
		if shouldBlockStatement(stmt, SQLExecuteOptions{ForceBlockedSQL: true}) {
			t.Fatalf("expected default force whitelist to allow business high-risk SQL type %s", sqlType)
		}
	}
}

func TestSavedForceWhitelistKeepsDefaultBusinessHighRiskTypes(t *testing.T) {
	got := mergeDefaultSQLForceWhitelist([]string{"UPDATE_WEAK_WHERE"})
	for _, sqlType := range []string{"CREATE_INDEX", "ALTER_COLUMN_TYPE", "DROP_TABLE", "TRUNCATE", "UPDATE_WITHOUT_WHERE"} {
		if !forceWhitelistContains(got, sqlType) {
			t.Fatalf("expected saved whitelist to include default business high-risk SQL type %s, got %v", sqlType, got)
		}
	}
}

func TestForceBlockedSQLWhitelistNeverAllowsHardBlockedTypes(t *testing.T) {
	stmt := model.SQLChangeStatement{
		SQLType:       "DROP_DATABASE",
		RiskLevel:     "BLOCKED",
		ExecuteStatus: "NOT_EXECUTABLE",
	}

	if !shouldBlockStatementWithWhitelist(stmt, SQLExecuteOptions{ForceBlockedSQL: true}, []string{"DROP_DATABASE"}) {
		t.Fatalf("expected hard blocked SQL type to stay blocked even when forced")
	}
	if isForceableBlockedSQLType("DROP_DATABASE") {
		t.Fatalf("DROP_DATABASE must not be forceable")
	}
	if !isForceableBlockedSQLType("UPDATE_WEAK_WHERE") {
		t.Fatalf("UPDATE_WEAK_WHERE should be configurable as forceable")
	}
}

func TestSQLExecutionErrorClassificationDoesNotTreatDatabaseErrorAsCanceled(t *testing.T) {
	err := &pgconn.PgError{Code: "42501", Message: "permission denied"}

	if isSQLExecutionCanceled(nil, err) {
		t.Fatalf("database execution errors must be reported as FAILED, not CANCELED")
	}
}

func TestColumnTypeViewDependencyRiskWarnsWithoutBlockingExecution(t *testing.T) {
	analysis := RiskAnalysis{
		SQLType:    "ALTER_COLUMN_TYPE",
		RiskLevel:  "WARN",
		RiskReason: "字段类型变更可能触发表重写",
	}
	deps := []ViewDependency{{Schema: "public", View: "v_patient"}}

	got := applyColumnTypeViewDependencyWarning(analysis, deps)

	if got.RiskLevel != "WARN" {
		t.Fatalf("expected view dependency to warn instead of block, got %+v", got)
	}
	if defaultStatementStatus(got) == "NOT_EXECUTABLE" {
		t.Fatalf("view dependency warning should remain executable, got %+v", got)
	}
	if !strings.Contains(got.RiskReason, "解析阶段检测到当前库存在视图依赖") {
		t.Fatalf("expected parse-time dependency warning, got %q", got.RiskReason)
	}
	if !strings.Contains(got.RiskReason, "public.v_patient") {
		t.Fatalf("expected dependent view name in warning, got %q", got.RiskReason)
	}
}

func TestCreateOrReplaceViewCompatibilityRiskWarnsWithoutBlockingExecution(t *testing.T) {
	analysis := RiskAnalysis{SQLType: "CREATE_VIEW", RiskLevel: "LOW"}
	compat := ViewCompatibilityResult{
		Exists:     true,
		Compatible: false,
		Reason:     "视图输出列类型变化，CREATE OR REPLACE VIEW 可能不兼容",
	}

	got := applyCreateOrReplaceViewCompatibilityRisk(analysis, compat)

	if got.RiskLevel != "WARN" {
		t.Fatalf("expected incompatible CREATE OR REPLACE VIEW to warn instead of block, got %+v", got)
	}
	if defaultStatementStatus(got) == "NOT_EXECUTABLE" {
		t.Fatalf("view compatibility warning should remain executable, got %+v", got)
	}
	if !strings.Contains(got.RiskReason, compat.Reason) {
		t.Fatalf("expected compatibility reason, got %q", got.RiskReason)
	}
}

func TestRefreshStatementsStaticRiskBeforeExecutionUpgradesBlockedRisk(t *testing.T) {
	statements := []model.SQLChangeStatement{
		{
			SQLContent:        "UPDATE his.patient SET status = 1",
			SQLType:           "UPDATE",
			RiskLevel:         "LOW",
			ExecuteStatus:     "PENDING",
			ExecutionStrategy: "DIRECT",
		},
		{
			SQLContent:    "UPDATE his.patient SET status = 1 WHERE id = 1",
			SQLType:       "UPDATE",
			RiskLevel:     "LOW",
			ExecuteStatus: "PENDING",
		},
	}

	changed := refreshStatementsStaticRiskBeforeExecution(statements)

	if !changed {
		t.Fatalf("expected risk refresh to report changed statements")
	}
	if statements[0].SQLType != "UPDATE_WITHOUT_WHERE" || statements[0].RiskLevel != "BLOCKED" {
		t.Fatalf("expected first statement to be upgraded to blocked risk, got %+v", statements[0])
	}
	if statements[0].ExecuteStatus != "NOT_EXECUTABLE" || statements[0].ExecutionStrategy != "MANUAL_EXPORT" {
		t.Fatalf("expected blocked execution strategy/status, got %+v", statements[0])
	}
	if statements[1].RiskLevel != "LOW" || statements[1].ExecuteStatus != "PENDING" {
		t.Fatalf("expected safe statement to remain pending, got %+v", statements[1])
	}
}

func TestSQLExecutionCancelRegistry(t *testing.T) {
	registry := newSQLExecutionCancelRegistry()
	canceled := false
	ok := registry.register(42, func() { canceled = true })
	if !ok {
		t.Fatalf("expected first register to succeed")
	}
	if registry.register(42, func() {}) {
		t.Fatalf("expected duplicate register to fail")
	}
	if !registry.cancel(42) {
		t.Fatalf("expected cancel to find running file")
	}
	if !canceled {
		t.Fatalf("expected cancel func to be called")
	}
	registry.unregister(42)
	if registry.cancel(42) {
		t.Fatalf("expected cancel to miss after unregister")
	}
}

func TestBatchExecutionSummaryHandlesCanceledFiles(t *testing.T) {
	status, message := batchExecutionSummary(1, 0, 0, 2)

	if status != "PARTIAL_FAILED" {
		t.Fatalf("expected PARTIAL_FAILED, got %q", status)
	}
	if !strings.Contains(message, "取消文件 2") {
		t.Fatalf("expected canceled file count in message, got %q", message)
	}

	status, _ = batchExecutionSummary(0, 0, 0, 2)
	if status != "CANCELED" {
		t.Fatalf("expected CANCELED for only canceled files, got %q", status)
	}
}

func TestParseSQLBatchCreatesOrderedFiles(t *testing.T) {
	setupSQLChangeServiceTestDB(t)

	batch, files, err := NewPostgreSQLService().ParseSQLBatch(ParseSQLBatchRequest{
		BatchName: "2026-upgrade",
		Overwrite: true,
		Files: []ParseSQLBatchFile{
			{FileName: "V1__2__his.sql", Content: "CREATE TABLE second(id int);"},
			{FileName: "V1__1__his.sql", Content: "CREATE TABLE first(id int);"},
		},
	})
	if err != nil {
		t.Fatalf("parse batch: %v", err)
	}
	if batch.TotalFiles != 2 || batch.ExecuteStatus != "PENDING" {
		t.Fatalf("unexpected batch: %+v", batch)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].FileName != "V1__1__his.sql" || files[0].BatchSortNo != 1 {
		t.Fatalf("expected first sorted file, got %+v", files[0])
	}
	if files[1].FileName != "V1__2__his.sql" || files[1].BatchSortNo != 2 {
		t.Fatalf("expected second sorted file, got %+v", files[1])
	}
}

func TestParseSQLBatchRejectsInvalidFileName(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	_, _, err := NewPostgreSQLService().ParseSQLBatch(ParseSQLBatchRequest{Files: []ParseSQLBatchFile{{FileName: "V1__1__说明.sql", Content: "SELECT 1"}}})
	if err == nil {
		t.Fatal("batch import accepted a filename without schema")
	}
}

func TestParseSQLBatchRollsBackWhenOneFileFails(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()
	if _, _, err := svc.ParseSQL(ParseSQLRequest{
		FileName:  "V1__2__public.sql",
		Content:   "SELECT 1;",
		Overwrite: false,
	}); err != nil {
		t.Fatalf("create existing SQL file: %v", err)
	}

	_, _, err := svc.ParseSQLBatch(ParseSQLBatchRequest{
		BatchName: "rollback-batch",
		Overwrite: false,
		Files: []ParseSQLBatchFile{
			{FileName: "V1__1__public.sql", Content: "SELECT 1;"},
			{FileName: "V1__2__public.sql", Content: "SELECT 2;"},
		},
	})
	if err == nil {
		t.Fatalf("expected duplicate file to fail batch parse")
	}

	var batchCount int64
	if err := repository.DB.Model(&model.SQLChangeBatch{}).Where("batch_name = ?", "rollback-batch").Count(&batchCount).Error; err != nil {
		t.Fatalf("count batches: %v", err)
	}
	if batchCount != 0 {
		t.Fatalf("expected failed batch parse to roll back batch row, got %d", batchCount)
	}
	var createdCount int64
	if err := repository.DB.Model(&model.SQLChangeFile{}).Where("file_name = ?", "V1__1__public.sql").Count(&createdCount).Error; err != nil {
		t.Fatalf("count files: %v", err)
	}
	if createdCount != 0 {
		t.Fatalf("expected failed batch parse to roll back previously created file, got %d", createdCount)
	}
}

func TestExecuteSQLBatchPersistsFileLevelErrors(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	batch := model.SQLChangeBatch{BatchName: "file-error", ExecuteStatus: "PENDING", TotalFiles: 2}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatalf("create batch: %v", err)
	}
	file := model.SQLChangeFile{BatchID: batch.ID, BatchSortNo: 1, FileName: "missing-config.sql", SchemaName: "his", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	nextFile := model.SQLChangeFile{BatchID: batch.ID, BatchSortNo: 2, FileName: "missing-config-next.sql", SchemaName: "his", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&nextFile).Error; err != nil {
		t.Fatalf("create next file: %v", err)
	}
	stmt := model.SQLChangeStatement{FileID: file.ID, LineNumber: 1, SQLContent: "SELECT 1", SQLType: "UNKNOWN", RiskLevel: "LOW", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}
	nextStmt := model.SQLChangeStatement{FileID: nextFile.ID, LineNumber: 1, SQLContent: "SELECT 2", SQLType: "UNKNOWN", RiskLevel: "LOW", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&nextStmt).Error; err != nil {
		t.Fatalf("create next statement: %v", err)
	}

	_, _, _ = NewPostgreSQLService().ExecuteSQLBatch(context.Background(), batch.ID, "tester", SQLExecuteOptions{})

	var got model.SQLChangeFile
	if err := repository.DB.First(&got, file.ID).Error; err != nil {
		t.Fatalf("reload file: %v", err)
	}
	if got.ExecuteStatus != "FAILED" || !strings.Contains(got.ExecuteMessage, "postgresql_host") {
		t.Fatalf("expected file-level error to be persisted, got %+v", got)
	}
	var gotNext model.SQLChangeFile
	if err := repository.DB.First(&gotNext, nextFile.ID).Error; err != nil {
		t.Fatalf("reload next file: %v", err)
	}
	if gotNext.ExecuteStatus != "FAILED" || !strings.Contains(gotNext.ExecuteMessage, "postgresql_host") {
		t.Fatalf("expected batch execution to continue and persist next file-level error, got %+v", gotNext)
	}
}

func TestExportNotExecutableSQLIncludesBlockedAndFailed(t *testing.T) {
	statements := []SQLExportStatement{
		{LineNumber: 1, SQLContent: "CREATE TABLE demo(id int)", ExecuteStatus: "SUCCESS"},
		{LineNumber: 2, SQLContent: "ALTER SYSTEM SET work_mem = '64MB'", ExecuteStatus: "NOT_EXECUTABLE", ExecuteMessage: "禁止修改数据库系统级参数"},
		{LineNumber: 3, SQLContent: "INSERT INTO demo VALUES (1)", ExecuteStatus: "FAILED", ExecuteMessage: "timeout"},
	}

	out := BuildNotExecutableSQL(statements)

	if strings.Contains(out, "CREATE TABLE demo") {
		t.Fatalf("success sql should not be exported: %s", out)
	}
	if !strings.Contains(out, "Line 2") || !strings.Contains(out, "ALTER SYSTEM") {
		t.Fatalf("expected blocked sql in export: %s", out)
	}
	if !strings.Contains(out, "Line 3") || !strings.Contains(out, "timeout") {
		t.Fatalf("expected failed sql in export: %s", out)
	}
}

func TestBuildNotExecutableSQLForFileDoesNotRewritePendingAfterFailure(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file := model.SQLChangeFile{FileName: "partial.sql", ExecuteStatus: "PARTIAL_FAILED"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	statements := []model.SQLChangeStatement{
		{FileID: file.ID, LineNumber: 1, SQLContent: "CREATE TABLE his.demo(id int)", ExecuteStatus: "SUCCESS"},
		{FileID: file.ID, LineNumber: 2, SQLContent: "INSERT INTO his.demo VALUES (1)", ExecuteStatus: "FAILED", ExecuteMessage: "relation does not exist"},
		{FileID: file.ID, LineNumber: 3, SQLContent: "UPDATE his.demo SET id = 2 WHERE id = 1", ExecuteStatus: "PENDING"},
	}
	if err := repository.DB.Create(&statements).Error; err != nil {
		t.Fatalf("create statements: %v", err)
	}

	out, err := NewPostgreSQLService().BuildNotExecutableSQLForFile(file.ID)
	if err != nil {
		t.Fatalf("build export: %v", err)
	}

	if strings.Contains(out, "CREATE TABLE his.demo") {
		t.Fatalf("success sql should not be exported: %s", out)
	}
	if !strings.Contains(out, "Line 2") || !strings.Contains(out, "relation does not exist") {
		t.Fatalf("expected failed SQL in export: %s", out)
	}
	if strings.Contains(out, "Line 3") || strings.Contains(out, "前序 SQL 执行失败") {
		t.Fatalf("pending SQL must not be rewritten as failed by a previous statement: %s", out)
	}
}

func TestPendingSQLStatusIsNotExportedBecausePreviousStatementFailed(t *testing.T) {
	if shouldExportSQL("PENDING") {
		t.Fatalf("pending SQL must not be treated as failed just because a previous statement failed")
	}
}

func TestSkipSQLFileMarksPendingStatementsSkipped(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file := model.SQLChangeFile{FileName: "skip-file.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	statements := []model.SQLChangeStatement{
		{FileID: file.ID, LineNumber: 1, SQLContent: "SELECT 1", ExecuteStatus: "PENDING"},
		{FileID: file.ID, LineNumber: 2, SQLContent: "SELECT 2", ExecuteStatus: "NOT_EXECUTABLE"},
	}
	if err := repository.DB.Create(&statements).Error; err != nil {
		t.Fatalf("create statements: %v", err)
	}

	got, err := NewPostgreSQLService().SkipSQLFile(file.ID, "tester")
	if err != nil {
		t.Fatalf("skip file: %v", err)
	}
	if got.ExecuteStatus != "SKIPPED" {
		t.Fatalf("expected file skipped, got %+v", got)
	}
	var pendingCount int64
	if err := repository.DB.Model(&model.SQLChangeStatement{}).Where("file_id = ? AND execute_status <> ?", file.ID, "SKIPPED").Count(&pendingCount).Error; err != nil {
		t.Fatalf("count statements: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("expected skip file to mark all unfinished statements skipped, got %d unfinished", pendingCount)
	}
}

func TestSkipSQLStatementRecomputesFileStatus(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file := model.SQLChangeFile{FileName: "skip-statement.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{FileID: file.ID, LineNumber: 1, SQLContent: "SELECT 1", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}

	if _, err := NewPostgreSQLService().SkipSQLStatement(stmt.ID, "tester"); err != nil {
		t.Fatalf("skip statement: %v", err)
	}
	var got model.SQLChangeFile
	if err := repository.DB.First(&got, file.ID).Error; err != nil {
		t.Fatalf("reload file: %v", err)
	}
	if got.ExecuteStatus != "SKIPPED" {
		t.Fatalf("expected file status recomputed to SKIPPED, got %+v", got)
	}
}

func TestUpdateSQLStatementContentReanalyzesAndResetsStatement(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	now := time.Now()
	file := model.SQLChangeFile{
		FileName:       "missing-schema.sql",
		SchemaName:     "df_zhushuju",
		ExecuteStatus:  "PARTIAL_FAILED",
		ExecuteMessage: "旧的执行结果",
		ExecuteUser:    "tester",
		ExecuteTime:    &now,
	}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{
		FileID:         file.ID,
		LineNumber:     1,
		SQLContent:     "ALTER TABLE gy_taocanmx ADD COLUMN zhuxiangid varchar(64)",
		SQLType:        "MISSING_SCHEMA",
		RiskLevel:      "BLOCKED",
		RiskReason:     "旧的风险",
		ExecuteStatus:  "NOT_EXECUTABLE",
		ExecuteMessage: "旧的错误",
		SQLState:       "55P03",
		AffectedRows:   12,
		DurationMs:     5000,
		ExecuteTime:    &now,
	}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}

	updatedFile, statements, err := NewPostgreSQLService().UpdateSQLStatementContent(stmt.ID, UpdateSQLStatementContentRequest{
		Content: "ALTER TABLE df_zhushuju.gy_taocanmx ADD COLUMN zhuxiangid varchar(64)",
	}, "editor")
	if err != nil {
		t.Fatalf("update statement content: %v", err)
	}

	if updatedFile.ExecuteStatus != "PENDING" {
		t.Fatalf("expected file to become pending after statement edit, got %+v", updatedFile)
	}
	if len(statements) != 1 {
		t.Fatalf("expected one statement, got %d", len(statements))
	}
	updated := statements[0]
	if !strings.Contains(updated.SQLContent, "df_zhushuju.gy_taocanmx") {
		t.Fatalf("expected updated SQL content, got %q", updated.SQLContent)
	}
	if updated.SQLType == "MISSING_SCHEMA" || updated.RiskLevel == "BLOCKED" {
		t.Fatalf("expected edited statement to be re-analyzed as executable, got type=%q risk=%q reason=%q", updated.SQLType, updated.RiskLevel, updated.RiskReason)
	}
	if updated.ExecuteStatus != "PENDING" {
		t.Fatalf("expected edited statement to reset to PENDING, got %q", updated.ExecuteStatus)
	}
	if updated.ExecuteMessage != "" || updated.SQLState != "" || updated.AffectedRows != 0 || updated.DurationMs != 0 || updated.ExecuteTime != nil {
		t.Fatalf("expected old execution result to be cleared, got %+v", updated)
	}
}

func TestAutocompleteSQLStatementSchemaFillsUniqueRelation(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	setSQLExecutionSettingsToTestDatabase(t)
	if err := repository.DB.Exec(`DROP SCHEMA IF EXISTS schema_autocomplete CASCADE`).Error; err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := repository.DB.Exec(`CREATE SCHEMA schema_autocomplete`).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := repository.DB.Exec(`CREATE TABLE schema_autocomplete.gy_taocanmx (id bigint)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer repository.DB.Exec(`DROP SCHEMA IF EXISTS schema_autocomplete CASCADE`)

	now := time.Now()
	file := model.SQLChangeFile{FileName: "missing-schema.sql", ExecuteStatus: "PARTIAL_FAILED", ExecuteMessage: "旧状态", ExecuteUser: "tester", ExecuteTime: &now}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{
		FileID:         file.ID,
		LineNumber:     1,
		SQLContent:     "ALTER TABLE gy_taocanmx ADD COLUMN zhuxiangid varchar(64)",
		SQLType:        "MISSING_SCHEMA",
		RiskLevel:      "BLOCKED",
		RiskReason:     "ALTER TABLE 目标对象 未显式指定 schema，请使用 schema.object 形式",
		ExecuteStatus:  "NOT_EXECUTABLE",
		ExecuteMessage: "旧错误",
		SQLState:       "55P03",
		AffectedRows:   12,
		DurationMs:     5000,
		ExecuteTime:    &now,
	}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}

	updatedFile, statements, err := NewPostgreSQLService().AutocompleteSQLStatementSchema(context.Background(), stmt.ID, "tester")
	if err != nil {
		t.Fatalf("autocomplete schema: %v", err)
	}

	if updatedFile.ExecuteStatus != "PENDING" {
		t.Fatalf("expected file to become pending, got %+v", updatedFile)
	}
	if len(statements) != 1 {
		t.Fatalf("expected one statement, got %d", len(statements))
	}
	updated := statements[0]
	if !strings.Contains(updated.SQLContent, "schema_autocomplete.gy_taocanmx") {
		t.Fatalf("expected schema to be completed, got %q", updated.SQLContent)
	}
	if updated.SQLType == "MISSING_SCHEMA" || updated.RiskLevel == "BLOCKED" {
		t.Fatalf("expected completed statement to be re-analyzed as executable, got type=%q risk=%q reason=%q", updated.SQLType, updated.RiskLevel, updated.RiskReason)
	}
	if updated.ExecuteStatus != "PENDING" || updated.ExecuteMessage != "" || updated.SQLState != "" || updated.AffectedRows != 0 || updated.DurationMs != 0 || updated.ExecuteTime != nil {
		t.Fatalf("expected old execution result to be cleared, got %+v", updated)
	}
}

func TestCreateSQLStatementsForFilePreservesCommentSQL(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	content := `
CREATE TABLE df_yaokufang.gy_mashangfxdjmx (id bigint);
-- DFHIS-1
COMMENT ON TABLE gy_mashangfxdjmx IS '药品出库单据明细';
-- DFHIS-2
COMMENT ON COLUMN gy_mashangfxdjmx.id IS '主键';
SELECT 1;`
	file := model.SQLChangeFile{FileName: "comment-schema-context.sql", SchemaName: "df_yaokufang", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}

	items, err := NewPostgreSQLService().createSQLStatementsForFile(repository.DB, file.ID, content, "df_yaokufang", SplitSQLStatements(content))
	if err != nil {
		t.Fatalf("create statements: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 statements, got %d", len(items))
	}
	if !strings.Contains(items[1].SQLContent, "COMMENT ON TABLE gy_mashangfxdjmx") {
		t.Fatalf("expected original table comment, got %q", items[1].SQLContent)
	}
	if !strings.Contains(items[2].SQLContent, "COMMENT ON COLUMN gy_mashangfxdjmx.id") {
		t.Fatalf("expected original column comment, got %q", items[2].SQLContent)
	}
	for _, item := range items[1:3] {
		if item.SQLType == "MISSING_SCHEMA" || item.ExecuteStatus == "NOT_EXECUTABLE" {
			t.Fatalf("expected completed comment to be executable, got %+v", item)
		}
	}
}

func TestCreateSQLStatementsForFileLeavesAmbiguousCommentSchemaForManualHandling(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	content := `
CREATE TABLE df_a.gy_same_name (id bigint);
CREATE TABLE df_b.gy_same_name (id bigint);
COMMENT ON TABLE gy_same_name IS 'same table';`
	file := model.SQLChangeFile{FileName: "comment-ambiguous-schema.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}

	items, err := NewPostgreSQLService().createSQLStatementsForFile(repository.DB, file.ID, content, "df_a", SplitSQLStatements(content))
	if err != nil {
		t.Fatalf("create statements: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(items))
	}
	if strings.Contains(items[2].SQLContent, "df_a.gy_same_name") || strings.Contains(items[2].SQLContent, "df_b.gy_same_name") {
		t.Fatalf("ambiguous relation must not be auto-completed, got %q", items[2].SQLContent)
	}
	if items[2].RiskLevel != "" || items[2].ExecuteStatus != "PENDING" {
		t.Fatalf("expected original comment ready for database execution, got %+v", items[2])
	}
}

func TestCreateSQLStatementsForFileMarksUnresolvedCommentSchemaWithoutBlockingOtherStatements(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	content := `
COMMENT ON TABLE gy_unknown IS 'unknown table';
SELECT 1;`
	file := model.SQLChangeFile{FileName: "comment-unresolved-schema.sql", SchemaName: "df_yaokufang", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}

	items, err := NewPostgreSQLService().createSQLStatementsForFile(repository.DB, file.ID, content, "df_yaokufang", SplitSQLStatements(content))
	if err != nil {
		t.Fatalf("create statements: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(items))
	}
	if items[0].RiskLevel != "" || items[0].ExecuteStatus != "PENDING" {
		t.Fatalf("expected original comment ready for database execution, got %+v", items[0])
	}
	if items[1].ExecuteStatus != "PENDING" {
		t.Fatalf("expected following SQL to remain executable, got %+v", items[1])
	}
}

func TestAutocompleteSQLStatementSchemaFillsCreateViewReferencesFromAST(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	setSQLExecutionSettingsToTestDatabase(t)
	if err := repository.DB.Exec(`DROP SCHEMA IF EXISTS schema_autocomplete_ast CASCADE`).Error; err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := repository.DB.Exec(`CREATE SCHEMA schema_autocomplete_ast`).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	for _, table := range []string{"patient", "invoice", "doctor", "charge_item", "order_item", "visit"} {
		if err := repository.DB.Exec(`CREATE TABLE schema_autocomplete_ast.` + table + ` (id bigint, patient_id bigint, doctor_id bigint)`).Error; err != nil {
			t.Fatalf("create table %s: %v", table, err)
		}
	}
	defer repository.DB.Exec(`DROP SCHEMA IF EXISTS schema_autocomplete_ast CASCADE`)

	file := model.SQLChangeFile{FileName: "view-missing-schema.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{
		FileID:     file.ID,
		LineNumber: 1,
		SQLContent: `CREATE OR REPLACE VIEW schema_autocomplete_ast.v_patient_bill AS
WITH recent_charge AS (
  SELECT id, patient_id FROM charge_item
)
SELECT p.id, c.id, d.id,
       (SELECT max(o.id) FROM order_item o WHERE o.patient_id = p.id) AS latest_order
FROM patient p, invoice c
JOIN doctor d ON d.id = c.doctor_id
JOIN recent_charge rc ON rc.patient_id = p.id
WHERE EXISTS (SELECT 1 FROM visit v WHERE v.patient_id = p.id)`,
		SQLType:       "MISSING_SCHEMA",
		RiskLevel:     "BLOCKED",
		ExecuteStatus: "NOT_EXECUTABLE",
	}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}

	_, statements, err := NewPostgreSQLService().AutocompleteSQLStatementSchema(context.Background(), stmt.ID, "tester")
	if err != nil {
		t.Fatalf("autocomplete schema: %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("expected one statement, got %d", len(statements))
	}
	updated := statements[0]
	for _, want := range []string{
		"schema_autocomplete_ast.patient p",
		"schema_autocomplete_ast.invoice c",
		"schema_autocomplete_ast.doctor d",
		"schema_autocomplete_ast.charge_item",
		"schema_autocomplete_ast.order_item o",
		"schema_autocomplete_ast.visit v",
	} {
		if !strings.Contains(updated.SQLContent, want) {
			t.Fatalf("expected %s to be completed, got %q", want, updated.SQLContent)
		}
	}
	if strings.Contains(updated.SQLContent, "schema_autocomplete_ast.recent_charge") {
		t.Fatalf("cte name must not be schema-qualified, got %q", updated.SQLContent)
	}
	if updated.SQLType == "MISSING_SCHEMA" || updated.RiskLevel == "BLOCKED" {
		t.Fatalf("expected completed view statement to be executable, got type=%q risk=%q reason=%q sql=%q", updated.SQLType, updated.RiskLevel, updated.RiskReason, updated.SQLContent)
	}
}

func TestCollectSchemaCompletionTargetsFromASTIncludesCreateViewReferences(t *testing.T) {
	sqlText := `CREATE OR REPLACE VIEW his.v_patient_bill AS
WITH recent_charge AS (
  SELECT id, patient_id FROM charge_item
)
SELECT p.id, c.id, d.id,
       (SELECT max(o.id) FROM order_item o WHERE o.patient_id = p.id) AS latest_order
FROM patient p, invoice c
JOIN doctor d ON d.id = c.doctor_id
JOIN recent_charge rc ON rc.patient_id = p.id
WHERE EXISTS (SELECT 1 FROM visit v WHERE v.patient_id = p.id)`
	targets, err := collectSchemaCompletionTargetsFromAST(sqlText)
	if err != nil {
		t.Fatalf("collect targets: %v", err)
	}
	for _, table := range []string{"charge_item", "order_item", "patient", "invoice", "doctor", "visit"} {
		if _, ok := targets[table]; !ok {
			t.Fatalf("expected target %s, got %+v", table, targets)
		}
	}
	if _, ok := targets["recent_charge"]; ok {
		t.Fatalf("cte name recent_charge must not be collected, got %+v", targets)
	}

	refs, err := collectSchemaCompletionRefsFromAST(sqlText)
	if err != nil {
		t.Fatalf("collect refs: %v", err)
	}
	schemas := make(map[string]string)
	for target := range targets {
		schemas[target] = "his"
	}
	completed := rewriteSchemaCompletionRefs(sqlText, refs, schemas)
	for _, want := range []string{"FROM his.patient p, his.invoice c", "FROM his.charge_item", "FROM his.order_item o", "JOIN his.doctor d", "FROM his.visit v"} {
		if !strings.Contains(completed, want) {
			t.Fatalf("expected completed SQL to contain %q, got %q", want, completed)
		}
	}
	if strings.Contains(completed, "his.recent_charge") {
		t.Fatalf("cte name must not be schema-qualified, got %q", completed)
	}
}

func TestAutocompleteSQLStatementSchemaRejectsAmbiguousRelation(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	setSQLExecutionSettingsToTestDatabase(t)
	for _, schema := range []string{"schema_autocomplete_a", "schema_autocomplete_b"} {
		if err := repository.DB.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`).Error; err != nil {
			t.Fatalf("drop schema %s: %v", schema, err)
		}
		if err := repository.DB.Exec(`CREATE SCHEMA ` + schema).Error; err != nil {
			t.Fatalf("create schema %s: %v", schema, err)
		}
		if err := repository.DB.Exec(`CREATE TABLE ` + schema + `.gy_dup (id bigint)`).Error; err != nil {
			t.Fatalf("create table %s.gy_dup: %v", schema, err)
		}
		defer repository.DB.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
	}

	file := model.SQLChangeFile{FileName: "ambiguous-schema.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{
		FileID:        file.ID,
		LineNumber:    1,
		SQLContent:    "ALTER TABLE gy_dup ADD COLUMN zhuxiangid varchar(64)",
		SQLType:       "MISSING_SCHEMA",
		RiskLevel:     "BLOCKED",
		ExecuteStatus: "NOT_EXECUTABLE",
	}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}

	_, _, err := NewPostgreSQLService().AutocompleteSQLStatementSchema(context.Background(), stmt.ID, "tester")
	if err == nil || !strings.Contains(err.Error(), "多个 schema") {
		t.Fatalf("expected ambiguous schema error, got %v", err)
	}
	var got model.SQLChangeStatement
	if err := repository.DB.First(&got, stmt.ID).Error; err != nil {
		t.Fatalf("reload statement: %v", err)
	}
	if got.SQLContent != stmt.SQLContent {
		t.Fatalf("ambiguous completion must not change SQL, got %q", got.SQLContent)
	}
}

func TestExecuteSQLStatementWithOptionsOnlyProcessesSelectedStatement(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file := model.SQLChangeFile{FileName: "single-statement.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	statements := []model.SQLChangeStatement{
		{
			FileID:         file.ID,
			LineNumber:     1,
			SQLContent:     "ALTER TABLE gy_taocanmx ADD COLUMN zhuxiangid varchar(64)",
			SQLType:        "MISSING_SCHEMA",
			RiskLevel:      "BLOCKED",
			RiskReason:     "ALTER TABLE 目标对象 未显式指定 schema，请使用 schema.object 形式",
			ExecuteStatus:  "NOT_EXECUTABLE",
			ExecuteMessage: "旧错误",
		},
		{
			FileID:         file.ID,
			LineNumber:     2,
			SQLContent:     "SELECT 1",
			SQLType:        "SELECT",
			RiskLevel:      "LOW",
			ExecuteStatus:  "PENDING",
			ExecuteMessage: "不要改动",
		},
	}
	if err := repository.DB.Create(&statements).Error; err != nil {
		t.Fatalf("create statements: %v", err)
	}

	updatedFile, gotStatements, err := NewPostgreSQLService().ExecuteSQLStatementWithOptions(context.Background(), statements[0].ID, "tester", SQLExecuteOptions{})
	if err != nil {
		t.Fatalf("execute selected statement: %v", err)
	}

	if updatedFile.ExecuteStatus != "PENDING" {
		t.Fatalf("expected file to remain pending because another statement is untouched, got %+v", updatedFile)
	}
	if len(gotStatements) != 2 {
		t.Fatalf("expected two statements, got %d", len(gotStatements))
	}
	if gotStatements[0].ExecuteStatus != "NOT_EXECUTABLE" {
		t.Fatalf("expected selected blocked statement to remain not executable, got %+v", gotStatements[0])
	}
	if gotStatements[0].ExecuteMessage == "旧错误" || gotStatements[0].ExecuteMessage == "" {
		t.Fatalf("expected selected statement execution message to be refreshed, got %+v", gotStatements[0])
	}
	if gotStatements[1].ExecuteStatus != "PENDING" || gotStatements[1].ExecuteMessage != "不要改动" {
		t.Fatalf("unselected statement must not be executed or reset, got %+v", gotStatements[1])
	}
}

func TestBuildNotExecutableSQLForFileIncludesViewRebuildPlan(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	file := model.SQLChangeFile{FileName: "view-dep.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{
		FileID:        file.ID,
		LineNumber:    1,
		SQLContent:    "ALTER TABLE patient ALTER COLUMN code TYPE varchar(20)",
		SQLType:       "ALTER_COLUMN_TYPE",
		RiskLevel:     "BLOCKED",
		ExecuteStatus: "NOT_EXECUTABLE",
	}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}
	backup := model.SQLViewBackup{
		FileID:      file.ID,
		StatementID: stmt.ID,
		SchemaName:  "public",
		ViewName:    "v_patient",
		Definition:  "SELECT patient.code FROM patient",
		DropSQL:     `DROP VIEW IF EXISTS "public"."v_patient";`,
		CreateSQL:   `CREATE OR REPLACE VIEW "public"."v_patient" AS SELECT patient.code FROM patient;`,
	}
	if err := repository.DB.Create(&backup).Error; err != nil {
		t.Fatalf("create backup: %v", err)
	}

	out, err := NewPostgreSQLService().BuildNotExecutableSQLForFile(file.ID)
	if err != nil {
		t.Fatalf("build export: %v", err)
	}

	if !strings.Contains(out, "View dependency rebuild plan for public.v_patient") {
		t.Fatalf("expected view rebuild plan in export: %s", out)
	}
	if !strings.Contains(out, backup.DropSQL) || !strings.Contains(out, backup.CreateSQL) {
		t.Fatalf("expected drop/create SQL in export: %s", out)
	}
}

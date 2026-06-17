package service

import (
	"context"
	"os"
	"strings"
	"testing"

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

func TestParseSQLAllowsSameFileNameAcrossVersionAndSchema(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()
	if err := repository.DB.Create(&model.SQLChangeFile{
		Version:       "15.3_SP13_20260524",
		SchemaName:    "public",
		FileName:      "change.sql",
		FileContent:   "SELECT 1",
		ExecuteStatus: "PENDING",
	}).Error; err != nil {
		t.Fatalf("create existing file: %v", err)
	}

	_, _, err := svc.ParseSQL(ParseSQLRequest{
		Version:    "15.3_SP13_20260525",
		SchemaName: "public",
		FileName:   "change.sql",
		Content:    "SELECT 2",
		Overwrite:  false,
	})
	if err != nil {
		t.Fatalf("same file name in a different version should be allowed: %v", err)
	}
}

func TestParseSQLPersistsExecutionStrategy(t *testing.T) {
	setupSQLChangeServiceTestDB(t)

	_, statements, err := NewPostgreSQLService().ParseSQL(ParseSQLRequest{
		FileName:  "strategy.sql",
		Content:   "CREATE INDEX CONCURRENTLY idx_patient_name ON his.patient(name);",
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("parse sql: %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("expected one statement, got %d", len(statements))
	}
	if statements[0].ExecutionStrategy != "DIRECT_NO_TRANSACTION" {
		t.Fatalf("expected DIRECT_NO_TRANSACTION, got %#v", statements[0])
	}
	if statements[0].CanRunInTransaction {
		t.Fatalf("concurrent index should not run in transaction")
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
			{FileName: "002-second.sql", Content: "CREATE TABLE second(id int);"},
			{FileName: "001-first.sql", Content: "CREATE TABLE first(id int);"},
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
	if files[0].FileName != "001-first.sql" || files[0].BatchSortNo != 1 {
		t.Fatalf("expected first sorted file, got %+v", files[0])
	}
	if files[1].FileName != "002-second.sql" || files[1].BatchSortNo != 2 {
		t.Fatalf("expected second sorted file, got %+v", files[1])
	}
}

func TestParseSQLBatchRollsBackWhenOneFileFails(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()
	if _, _, err := svc.ParseSQL(ParseSQLRequest{
		FileName:  "002-existing.sql",
		Content:   "SELECT 1;",
		Overwrite: false,
	}); err != nil {
		t.Fatalf("create existing SQL file: %v", err)
	}

	_, _, err := svc.ParseSQLBatch(ParseSQLBatchRequest{
		BatchName: "rollback-batch",
		Overwrite: false,
		Files: []ParseSQLBatchFile{
			{FileName: "001-created-before-failure.sql", Content: "SELECT 1;"},
			{FileName: "002-existing.sql", Content: "SELECT 2;"},
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
	if err := repository.DB.Model(&model.SQLChangeFile{}).Where("file_name = ?", "001-created-before-failure.sql").Count(&createdCount).Error; err != nil {
		t.Fatalf("count files: %v", err)
	}
	if createdCount != 0 {
		t.Fatalf("expected failed batch parse to roll back previously created file, got %d", createdCount)
	}
}

func TestExecuteSQLBatchPersistsFileLevelErrors(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	batch := model.SQLChangeBatch{BatchName: "file-error", ExecuteStatus: "PENDING", TotalFiles: 1}
	if err := repository.DB.Create(&batch).Error; err != nil {
		t.Fatalf("create batch: %v", err)
	}
	file := model.SQLChangeFile{BatchID: batch.ID, BatchSortNo: 1, FileName: "missing-config.sql", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&file).Error; err != nil {
		t.Fatalf("create file: %v", err)
	}
	stmt := model.SQLChangeStatement{FileID: file.ID, LineNumber: 1, SQLContent: "SELECT 1", SQLType: "UNKNOWN", RiskLevel: "LOW", ExecuteStatus: "PENDING"}
	if err := repository.DB.Create(&stmt).Error; err != nil {
		t.Fatalf("create statement: %v", err)
	}

	_, _, _ = NewPostgreSQLService().ExecuteSQLBatch(context.Background(), batch.ID, "tester", SQLExecuteOptions{})

	var got model.SQLChangeFile
	if err := repository.DB.First(&got, file.ID).Error; err != nil {
		t.Fatalf("reload file: %v", err)
	}
	if got.ExecuteStatus != "FAILED" || !strings.Contains(got.ExecuteMessage, "PostgreSQL 主机地址未配置") {
		t.Fatalf("expected file-level error to be persisted, got %+v", got)
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

func TestBuildNotExecutableSQLForFileIncludesPendingAfterFailure(t *testing.T) {
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
	if !strings.Contains(out, "Line 3") || !strings.Contains(out, "前序 SQL 执行失败") {
		t.Fatalf("expected pending SQL after failure in export: %s", out)
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

package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAnalyzeSQLRiskClosesBypasses(t *testing.T) {
	tests := []struct {
		sql       string
		wantType  string
		wantLevel string
	}{
		{"DROP TABLE his.tmp_a, his.patient", "DROP_TABLE", "BLOCKED"},
		{"DROP TABLE his.patient, his.tmp_a", "DROP_TABLE", "BLOCKED"},
		{"DROP TABLE his.tmp_a CASCADE", "DROP_TABLE", "WARN"},
		{"DROP TABLE his.tmp_a", "DROP_TABLE", "LOW"},
		{"TRUNCATE his.tmp_a CASCADE", "TRUNCATE", "WARN"},
		{"DROP SEQUENCE his.seq_a CASCADE", "DROP", "WARN"},
		{"DO $$ BEGIN EXECUTE 'DROP TABLE his.patient'; END $$", "DO_BLOCK", "WARN"},
		{"CALL his.cleanup()", "CALL", "WARN"},
		{"WITH d AS (DELETE FROM his.patient RETURNING *) SELECT count(*) FROM d", "DELETE_WITHOUT_WHERE", "BLOCKED"},
		{"WITH u AS (UPDATE his.patient SET status = 1 RETURNING id) SELECT id FROM u", "UPDATE_WITHOUT_WHERE", "BLOCKED"},
		{"WITH d AS (DELETE FROM his.patient WHERE id = 1 RETURNING *) SELECT count(*) FROM d", "WRITABLE_CTE", "WARN"},
		{"MERGE INTO his.patient p USING his.patient_new n ON p.id = n.id WHEN MATCHED THEN DELETE", "MERGE", "WARN"},
		{"UPDATE his.patient SET status = 1 WHERE 2 > 1", "UPDATE_TRIVIAL_WHERE", "BLOCKED"},
		{"DELETE FROM his.patient WHERE 'a' = 'a'", "DELETE_TRIVIAL_WHERE", "BLOCKED"},
		{"DELETE FROM his.patient WHERE id IN (SELECT id FROM his.patient_old)", "DELETE", "LOW"},
		{"EXPLAIN ANALYZE DELETE FROM his.patient", "DELETE_WITHOUT_WHERE", "BLOCKED"},
		{"EXPLAIN DELETE FROM his.patient", "EXPLAIN", "LOW"},
		{"GRANT ALL ON ALL TABLES IN SCHEMA his TO public", "GRANT", "WARN"},
		{"ALTER ROLE app SUPERUSER", "ROLE_MANAGEMENT", "BLOCKED"},
		{"CREATE USER evil PASSWORD 'x'", "ROLE_MANAGEMENT", "BLOCKED"},
		{"COPY his.patient TO '/tmp/patient.csv'", "COPY_FILE", "BLOCKED"},
		{"COPY his.patient FROM STDIN", "COPY", "WARN"},
		{"LOCK TABLE his.patient IN ACCESS EXCLUSIVE MODE", "LOCK_TABLE", "WARN"},
		{"BEGIN", "TRANSACTION_CONTROL", "WARN"},
		{"SET statement_timeout = 0", "SET", "WARN"},
		{"COMMENT ON TABLE his.patient IS 'x'", "COMMENT", "LOW"},
		{"CREATE SEQUENCE his.seq_a", "CREATE_SEQUENCE", "LOW"},
		{"SELECT 1", "SELECT", "LOW"},
	}
	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.SQLType != tc.wantType || got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s %s, got %+v", tc.wantType, tc.wantLevel, got)
			}
		})
	}
}

func TestRefreshRiskKeepsMetadataDowngrade(t *testing.T) {
	statements := []model.SQLChangeStatement{{
		SQLContent:    "DROP TABLE his.small_table",
		SQLType:       "DROP_TABLE",
		RiskLevel:     "WARN",
		RiskReason:    "DROP TABLE 会删除表结构和数据，请确认对象范围",
		ExecuteStatus: "PENDING",
	}}
	metadataAnalyze := func(sqlText string) RiskAnalysis {
		return classifyDestructiveTableOperation("DROP_TABLE", TableStats{SchemaName: "his", TableName: "small_table", TotalBytes: 1024, EstimatedRows: 10})
	}
	if refreshStatementsRiskBeforeExecution(statements, metadataAnalyze) {
		t.Fatalf("expected metadata-confirmed WARN to be kept, got %+v", statements[0])
	}
	if statements[0].RiskLevel != "WARN" || statements[0].ExecuteStatus != "PENDING" {
		t.Fatalf("expected statement to stay executable, got %+v", statements[0])
	}
}

func TestRefreshRiskUpgradesLegacyMultiTableDrop(t *testing.T) {
	statements := []model.SQLChangeStatement{{
		SQLContent:    "DROP TABLE his.tmp_a, his.patient",
		SQLType:       "DROP_TABLE",
		RiskLevel:     "LOW",
		RiskReason:    "临时/备份表命名规则命中",
		ExecuteStatus: "PENDING",
	}}
	if !refreshStatementsRiskBeforeExecution(statements, AnalyzeSQLRisk) {
		t.Fatalf("expected legacy LOW multi-table drop to be refreshed")
	}
	if statements[0].RiskLevel != "BLOCKED" || statements[0].ExecuteStatus != "NOT_EXECUTABLE" {
		t.Fatalf("expected multi-table drop to be blocked, got %+v", statements[0])
	}
}

func TestStepExecutionRestoresAfterDropFailure(t *testing.T) {
	executor := &dropFailingViewDependencyExecutor{failOn: `DROP VIEW IF EXISTS "his"."v_b";`}
	plan := SQLViewDependencyStepExecutionPlan{
		DropSteps:    []string{`DROP VIEW IF EXISTS "his"."v_a";`, `DROP VIEW IF EXISTS "his"."v_b";`},
		AlterSteps:   []string{"ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);"},
		RestoreSteps: []string{`CREATE OR REPLACE VIEW "his"."v_a" AS SELECT code FROM his.patient;`},
	}
	err := runSQLViewDependencyStepExecution(executor, plan)
	stepErr, ok := err.(SQLViewDependencyStepExecutionError)
	if !ok || !stepErr.RestoreAttempted || !stepErr.RestoreSucceeded {
		t.Fatalf("expected restore after drop failure, got %#v", err)
	}
	if executor.steps[len(executor.steps)-1] != plan.RestoreSteps[0] {
		t.Fatalf("expected restore step to run, got %+v", executor.steps)
	}
}

type dropFailingViewDependencyExecutor struct {
	failOn string
	steps  []string
}

func (e *dropFailingViewDependencyExecutor) Exec(sqlText string) error {
	e.steps = append(e.steps, sqlText)
	if sqlText == e.failOn {
		return fmt.Errorf("boom")
	}
	return nil
}

// ---- 以下为真实 PostgreSQL 集成测试，目标库复用 TEST_DATABASE_DSN ----

func setupTargetPostgreSQL(t *testing.T) *sql.DB {
	t.Helper()
	setupSQLChangeServiceTestDB(t)
	dsn, _ := testutil.PostgresDSNFromEnv()
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	repo := repository.NewSettingsRepo()
	settings := map[string]string{
		"postgresql_host":     cfg.Host,
		"postgresql_port":     fmt.Sprint(cfg.Port),
		"postgresql_user":     cfg.User,
		"postgresql_password": cfg.Password,
		"postgresql_database": cfg.Database,
	}
	for key, value := range settings {
		if err := repo.Set(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	t.Cleanup(func() {
		for key := range settings {
			_ = repo.Set(key, "")
		}
	})
	db, err := openPostgreSQL(PostgreSQLConfig{Host: cfg.Host, Port: fmt.Sprint(cfg.Port), User: cfg.User, Password: cfg.Password, Database: cfg.Database})
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

func TestExecuteSQLFileStopsAtBlockedStatement(t *testing.T) {
	target := setupTargetPostgreSQL(t)
	mustExec(t, target, "DROP SCHEMA IF EXISTS fix_blocked CASCADE", "CREATE SCHEMA fix_blocked")
	t.Cleanup(func() { _, _ = target.Exec("DROP SCHEMA IF EXISTS fix_blocked CASCADE") })

	svc := NewPostgreSQLService()
	file, _, err := svc.ParseSQL(ParseSQLRequest{
		FileName:  fmt.Sprintf("blocked-%d.sql", time.Now().UnixNano()),
		Content:   "CREATE TABLE fix_blocked.t (id int, status int);\nUPDATE fix_blocked.t SET status = 1;\nINSERT INTO fix_blocked.t VALUES (1, 0);",
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got, statements, err := svc.ExecuteSQLFileWithOptions(context.Background(), file.ID, "tester", SQLExecuteOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if statements[0].ExecuteStatus != "SUCCESS" || statements[1].ExecuteStatus != "NOT_EXECUTABLE" || statements[2].ExecuteStatus != "PENDING" {
		t.Fatalf("expected execution to stop at blocked statement, got %s/%s/%s", statements[0].ExecuteStatus, statements[1].ExecuteStatus, statements[2].ExecuteStatus)
	}
	if got.ExecuteStatus != "PARTIAL_FAILED" {
		t.Fatalf("expected PARTIAL_FAILED, got %s", got.ExecuteStatus)
	}
	var count int
	if err := target.QueryRow("SELECT count(*) FROM fix_blocked.t").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expected statement after blocked SQL not to run, count=%d err=%v", count, err)
	}
}

func TestExecuteSQLFileKeepsSmallTableDropExecutable(t *testing.T) {
	target := setupTargetPostgreSQL(t)
	mustExec(t, target, "DROP SCHEMA IF EXISTS fix_drop CASCADE", "CREATE SCHEMA fix_drop", "CREATE TABLE fix_drop.small_t (id int)")
	t.Cleanup(func() { _, _ = target.Exec("DROP SCHEMA IF EXISTS fix_drop CASCADE") })

	svc := NewPostgreSQLService()
	file, statements, err := svc.ParseSQL(ParseSQLRequest{
		FileName:  fmt.Sprintf("drop-%d.sql", time.Now().UnixNano()),
		Content:   "DROP TABLE fix_drop.small_t;",
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if statements[0].RiskLevel != "WARN" {
		t.Fatalf("expected metadata to downgrade small table drop to WARN, got %+v", statements[0])
	}
	got, executed, err := svc.ExecuteSQLFileWithOptions(context.Background(), file.ID, "tester", SQLExecuteOptions{RequireRiskConfirmation: true, ConfirmWarnRisk: true})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.ExecuteStatus != "SUCCESS" || executed[0].ExecuteStatus != "SUCCESS" {
		t.Fatalf("expected small table drop to execute, got file=%s stmt=%+v", got.ExecuteStatus, executed[0])
	}
}

func setupViewDependencyFixture(t *testing.T, target *sql.DB, schema string) {
	t.Helper()
	mustExec(t, target,
		"DROP SCHEMA IF EXISTS "+schema+" CASCADE",
		"DROP ROLE IF EXISTS "+schema+"_reader",
		"CREATE ROLE "+schema+"_reader",
		"CREATE SCHEMA "+schema,
		"CREATE TABLE "+schema+".patient (id int, code varchar(10))",
		"INSERT INTO "+schema+".patient VALUES (1, 'A1')",
		"CREATE VIEW "+schema+".v_a AS SELECT id, code FROM "+schema+".patient",
		"CREATE VIEW "+schema+".v_b AS SELECT code FROM "+schema+".v_a",
		"GRANT SELECT ON "+schema+".v_a TO "+schema+"_reader",
		"GRANT SELECT (code) ON "+schema+".v_b TO "+schema+"_reader",
	)
	t.Cleanup(func() {
		_, _ = target.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
		_, _ = target.Exec("DROP ROLE IF EXISTS " + schema + "_reader")
	})
}

func createAnalyzedViewDependencyTask(t *testing.T, svc *PostgreSQLService, schema, alterSQL, lockTimeout string) uint {
	t.Helper()
	task, err := svc.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{
		SchemaName:    schema,
		TableName:     "patient",
		ColumnName:    "code",
		AlterSQL:      alterSQL,
		ExecutionMode: SQLViewDependencyExecutionModeStep,
		LockTimeout:   lockTimeout,
	}, "tester")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, items, err := svc.AnalyzeSQLViewDependencyTask(context.Background(), task.ID, "tester"); err != nil || len(items) != 2 {
		t.Fatalf("analyze: items=%d err=%v", len(items), err)
	}
	if _, _, err := svc.PrecheckSQLViewDependencyTask(context.Background(), task.ID, "tester"); err != nil {
		t.Fatalf("precheck: %v", err)
	}
	return task.ID
}

func assertReaderGrants(t *testing.T, target *sql.DB, schema string) {
	t.Helper()
	var viewGrant, columnGrant bool
	err := target.QueryRow(fmt.Sprintf("SELECT has_table_privilege('%[1]s_reader', '%[1]s.v_a', 'SELECT'), has_column_privilege('%[1]s_reader', '%[1]s.v_b', 'code', 'SELECT')", schema)).Scan(&viewGrant, &columnGrant)
	if err != nil {
		t.Fatalf("check grants: %v", err)
	}
	if !viewGrant || !columnGrant {
		t.Fatalf("expected grants to other roles to be restored, view=%v column=%v", viewGrant, columnGrant)
	}
}

func TestViewDependencyStepModeRestoresGrantsForOtherRoles(t *testing.T) {
	target := setupTargetPostgreSQL(t)
	setupViewDependencyFixture(t, target, "fix_vd_ok")
	svc := NewPostgreSQLService()
	id := createAnalyzedViewDependencyTask(t, svc, "fix_vd_ok", "ALTER TABLE fix_vd_ok.patient ALTER COLUMN code TYPE varchar(64)", "3s")

	task, _, err := svc.ExecuteSQLViewDependencyTask(context.Background(), id, "tester")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if task.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s %s", task.Status, task.ExecuteMessage)
	}
	assertReaderGrants(t, target, "fix_vd_ok")

	restored, _, err := svc.RestoreSQLViewDependencyTask(context.Background(), id, "tester")
	if err != nil {
		t.Fatalf("restore on existing views should be a no-op, got %v", err)
	}
	if restored.Status != "RESTORED" || !strings.Contains(restored.ExecuteMessage, "无需恢复") {
		t.Fatalf("expected idempotent restore, got %s %s", restored.Status, restored.ExecuteMessage)
	}
}

func TestViewDependencyStepModeAppliesLockTimeoutAndRestores(t *testing.T) {
	target := setupTargetPostgreSQL(t)
	setupViewDependencyFixture(t, target, "fix_vd_lock")
	svc := NewPostgreSQLService()
	id := createAnalyzedViewDependencyTask(t, svc, "fix_vd_lock", "ALTER TABLE fix_vd_lock.patient ALTER COLUMN code TYPE varchar(64)", "500ms")

	holder, err := target.Begin()
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer holder.Rollback()
	if _, err := holder.Exec("SELECT * FROM fix_vd_lock.patient"); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	start := time.Now()
	task, _, err := svc.ExecuteSQLViewDependencyTask(context.Background(), id, "tester")
	if err == nil {
		t.Fatalf("expected ALTER to fail on lock_timeout")
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("expected lock_timeout to apply in STEP mode, took %s", time.Since(start))
	}
	if task.Status != "RESTORED" {
		t.Fatalf("expected views restored after failure, got %s %s", task.Status, task.ExecuteMessage)
	}
	_ = holder.Rollback()
	assertReaderGrants(t, target, "fix_vd_lock")
}

func TestViewDependencyRestoresEvenWhenCallerContextCanceled(t *testing.T) {
	target := setupTargetPostgreSQL(t)
	setupViewDependencyFixture(t, target, "fix_vd_cancel")
	svc := NewPostgreSQLService()
	id := createAnalyzedViewDependencyTask(t, svc, "fix_vd_cancel", "ALTER TABLE fix_vd_cancel.patient ALTER COLUMN code TYPE varchar(64)", "30s")

	holder, err := target.Begin()
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	if _, err := holder.Exec("SELECT * FROM fix_vd_cancel.patient"); err != nil {
		t.Fatalf("hold lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	go func() {
		<-ctx.Done()
		time.Sleep(200 * time.Millisecond)
		_ = holder.Rollback()
	}()

	task, _, err := svc.ExecuteSQLViewDependencyTask(ctx, id, "tester")
	if err == nil {
		t.Fatalf("expected canceled ALTER to fail")
	}
	if task.Status != "RESTORED" {
		t.Fatalf("expected views restored with detached context, got %s %s", task.Status, task.ExecuteMessage)
	}
	var exists bool
	if err := target.QueryRow("SELECT to_regclass('fix_vd_cancel.v_b') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Fatalf("expected dependent view to exist after restore, exists=%v err=%v", exists, err)
	}
}

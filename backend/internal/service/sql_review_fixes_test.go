package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/gorm"
)

// Regressions for the six defects reproduced during the SQL execution review.
// Every test uses a fresh disposable PostgreSQL database.
func sqlReviewFixExec(t *testing.T, text string) {
	t.Helper()
	if err := repository.DB.Exec(text).Error; err != nil {
		t.Fatal(err)
	}
}

func sqlReviewFixService(t *testing.T) *PostgreSQLService {
	t.Helper()
	return isolatedSQLContinuationService(t)
}

func TestSQLReviewFixSQLOverwriteContextInheritance(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE SCHEMA review_a AUTHORIZATION sql_release; CREATE SCHEMA review_b AUTHORIZATION sql_release;
CREATE TABLE review_a.counter(value int); ALTER TABLE review_a.counter OWNER TO sql_release; INSERT INTO review_a.counter VALUES(0);
CREATE TABLE review_b.counter(value int); ALTER TABLE review_b.counter OWNER TO sql_release; INSERT INTO review_b.counter VALUES(0);`)
	file, _ := saveReleaseTestFile(t, s, "SET search_path=review_a;\nUPDATE counter SET value=value+1;\nSELECT 1/0;")
	first, initialRows, err := s.ExecuteSQLFile(context.Background(), file.ID, "review")
	if err != nil || first.ExecuteStatus != "FAILED" {
		t.Fatalf("initial execution: %+v %v", first, err)
	}
	t.Logf("initial result: %s rows=%+v", first.ExecuteMessage, initialRows)
	_, _, err = s.ParseSQL(ParseSQLRequest{FileName: file.FileName, Content: "SET search_path=review_b;\nUPDATE counter SET value=value+1;\nSELECT 1;", Overwrite: true})
	if err == nil || !strings.Contains(err.Error(), "会话配置已变化") {
		t.Fatalf("unsafe inheritance accepted: %v", err)
	}
	current, _, err := s.GetSQLFile(file.ID)
	if err != nil || current.IsDeleted {
		t.Fatalf("original progress was lost: %+v %v", current, err)
	}

	var a, b int
	if err := repository.DB.Raw("SELECT value FROM review_a.counter").Scan(&a).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Raw("SELECT value FROM review_b.counter").Scan(&b).Error; err != nil {
		t.Fatal(err)
	}
	if a != 1 || b != 0 {
		t.Fatalf("unexpected values: a=%d b=%d", a, b)
	}
	t.Logf("unsafe overwrite rejected; prior progress unchanged: a=%d b=%d", a, b)
}

func TestSQLReviewFixManualViewTaskTargetSwitch(t *testing.T) {
	s := sqlReviewFixService(t)
	ddl := `CREATE TABLE sql_release.review_target(x varchar(10)); CREATE VIEW sql_release.review_target_view AS SELECT x FROM sql_release.review_target;`
	sqlReviewFixExec(t, ddl)
	task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.review_target ALTER COLUMN x TYPE varchar(20);", ExecutionMode: "TRANSACTION"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AnalyzeSQLViewDependencyTask(context.Background(), task.ID, "review"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PrecheckSQLViewDependencyTask(context.Background(), task.ID, "review"); err != nil {
		t.Fatal(err)
	}
	original, err := s.settingsRepo.Read("postgresql_database", true)
	if err != nil {
		t.Fatal(err)
	}
	other := fmt.Sprintf("dfops_review_target_%d", time.Now().UnixNano())
	sqlReviewFixExec(t, "CREATE DATABASE "+quoteIdent(other))
	cfg, _, err := s.loadSQLExecutionConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = other
	second, err := openPostgreSQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s.dbPool != nil {
			_ = s.dbPool.Close()
		}
		_ = second.Close()
		_ = repository.DB.Exec("DROP DATABASE " + quoteIdent(other) + " WITH (FORCE)").Error
	})
	if _, err := second.Exec("CREATE SCHEMA sql_release AUTHORIZATION sql_release;" + ddl); err != nil {
		t.Fatal(err)
	}
	if err := s.settingsRepo.Set("postgresql_database", other); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ExecuteSQLViewDependencyTask(context.Background(), task.ID, "review")
	if err == nil || !strings.Contains(err.Error(), "数据库连接目标已变化") {
		t.Fatalf("target switch accepted: %v", err)
	}
	result, _, err := s.GetSQLViewDependencyTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var beforeType, afterType string
	query := "SELECT format_type(atttypid,atttypmod) FROM pg_attribute WHERE attrelid='sql_release.review_target'::regclass AND attname='x'"
	if err := repository.DB.Raw(query).Scan(&beforeType).Error; err != nil {
		t.Fatal(err)
	}
	if err := second.QueryRow(query).Scan(&afterType); err != nil {
		t.Fatal(err)
	}
	if result.DatabaseTarget == "" || result.Status != "PRECHECK_PASSED" || beforeType != "character varying(10)" || afterType != "character varying(10)" {
		t.Fatalf("unexpected result: target=%q original=%s switched=%s", result.DatabaseTarget, beforeType, afterType)
	}
	t.Logf("task pinned to %s; neither database changed", original)
}

func TestSQLReviewFixViewPermissionLoss(t *testing.T) {
	s := sqlReviewFixService(t)
	reader := fmt.Sprintf("dfops_review_reader_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if err := repository.DB.Exec("DROP OWNED BY " + quoteIdent(reader) + " CASCADE; DROP ROLE " + quoteIdent(reader)).Error; err != nil {
			t.Errorf("clean up view permission fixture: %v", err)
		}
	})
	sqlReviewFixExec(t, strings.ReplaceAll(`CREATE ROLE dfops_review_reader;
CREATE TABLE sql_release.review_acl(x varchar(10));
CREATE VIEW sql_release.review_public_view AS SELECT x FROM sql_release.review_acl;
CREATE VIEW sql_release.review_column_view AS SELECT x FROM sql_release.review_acl;
GRANT USAGE ON SCHEMA sql_release TO dfops_review_reader;
GRANT SELECT ON sql_release.review_public_view TO PUBLIC;
GRANT SELECT(x) ON sql_release.review_column_view TO dfops_review_reader WITH GRANT OPTION;
CREATE MATERIALIZED VIEW sql_release.review_acl_mv AS SELECT x FROM sql_release.review_acl;
GRANT SELECT ON sql_release.review_acl_mv TO dfops_review_reader;`, "dfops_review_reader", quoteIdent(reader)))
	check := func() (bool, bool) {
		t.Helper()
		var publicGrant, columnGrant bool
		row := repository.DB.Raw("SELECT has_table_privilege(?,'sql_release.review_public_view','SELECT'), has_column_privilege(?,'sql_release.review_column_view','x','SELECT')", reader, reader).Row()
		if err := row.Scan(&publicGrant, &columnGrant); err != nil {
			t.Fatal(err)
		}
		return publicGrant, columnGrant
	}
	if p, c := check(); !p || !c {
		t.Fatal("fixture lacks grants")
	}
	task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.review_acl ALTER COLUMN x TYPE varchar(20);", ExecutionMode: "TRANSACTION"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AnalyzeSQLViewDependencyTask(context.Background(), task.ID, "review"); err != nil {
		t.Fatal(err)
	}
	result, _, err := s.ExecuteSQLViewDependencyTask(context.Background(), task.ID, "review")
	if err != nil || result.Status != "SUCCESS" {
		t.Fatalf("execution: %+v %v", result, err)
	}
	p, c := check()
	if !p || !c {
		t.Fatalf("unexpected permission state public=%t column=%t", p, c)
	}
	var columnGrantable, materializedGrant bool
	if err := repository.DB.Raw(`SELECT EXISTS(SELECT FROM pg_attribute a CROSS JOIN LATERAL aclexplode(a.attacl) x WHERE a.attrelid='sql_release.review_column_view'::regclass AND a.attname='x' AND x.grantee=?::regrole AND x.is_grantable), has_table_privilege(?,'sql_release.review_acl_mv','SELECT')`, reader, reader).Row().Scan(&columnGrantable, &materializedGrant); err != nil || !columnGrantable || !materializedGrant {
		t.Fatalf("grant option/materialized grants missing: %t %t %v", columnGrantable, materializedGrant, err)
	}
	t.Logf("table and column privileges preserved: public=%t column=%t", p, c)
}

func TestSQLReviewFixOrdinarySQLErrorLosesDetail(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE FUNCTION sql_release.review_error() RETURNS integer LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='review-primary', DETAIL='review-detail-evidence', HINT='review-fix-guidance'; END $$;`)
	file, _ := saveReleaseTestFile(t, s, "SELECT sql_release.review_error();")
	result, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "review")
	if err != nil || result.ExecuteStatus != "FAILED" || rows[0].SQLState != "P0001" {
		t.Fatalf("unexpected execution: %+v %v", result, err)
	}
	var record model.SQLStatementRecord
	if err := repository.DB.Where("statement_id=? AND action='EXECUTE'", rows[0].ID).Order("id desc").First(&record).Error; err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{rows[0].ExecuteMessage, record.Message, result.ExecuteMessage} {
		if !strings.Contains(message, "DETAIL: review-detail-evidence") || !strings.Contains(message, "HINT: review-fix-guidance") || !strings.Contains(message, "CONTEXT:") {
			t.Fatalf("missing PostgreSQL diagnostics: %s", message)
		}
	}
	// Single-statement retries use the same durable diagnostics, independently
	// of the request lifetime and without replacing the original failed attempt.
	if _, _, err := s.StartReleaseSQLStatementExecution(context.Background(), rows[0].ID, "review-retry", SQLExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		retried, retryRows, err := s.GetSQLFile(file.ID)
		if err != nil {
			t.Fatal(err)
		}
		s.cancelReg.mu.Lock()
		_, running := s.cancelReg.cancels[file.ID]
		s.cancelReg.mu.Unlock()
		if !running {
			if retried.ExecuteStatus != "FAILED" {
				t.Fatalf("retry status=%s", retried.ExecuteStatus)
			}
			if err := repository.DB.Where("statement_id=? AND action='EXECUTE'", rows[0].ID).Order("id desc").First(&record).Error; err != nil {
				t.Fatal(err)
			}
			for _, message := range []string{retried.ExecuteMessage, retryRows[0].ExecuteMessage, record.Message} {
				if !strings.Contains(message, "DETAIL: review-detail-evidence") || !strings.Contains(message, "HINT: review-fix-guidance") {
					t.Fatalf("retry lost diagnostics: %s", message)
				}
			}
			var attempts int64
			if err := repository.DB.Model(&model.SQLStatementRecord{}).Where("statement_id=? AND action='EXECUTE'", rows[0].ID).Count(&attempts).Error; err != nil || attempts != 2 {
				t.Fatalf("retry history: %d %v", attempts, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("single retry did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Log("file, statement and attempt preserve DETAIL, HINT and CONTEXT")
}

func TestSQLRetrySummaryDoesNotExposeAnUnexecutedIntermediateState(t *testing.T) {
	s := sqlReviewFixService(t)
	file, rows := saveReleaseTestFile(t, s, "SELECT 1/0;")
	if first, _, err := s.ExecuteSQLFile(context.Background(), file.ID, "first"); err != nil || first.ExecuteStatus != "FAILED" {
		t.Fatal("initial failure was not recorded", err)
	}
	paused, release := make(chan struct{}), make(chan struct{})
	var pauseOnce, releaseOnce sync.Once
	name := "test:retry-summary-visibility"
	if err := repository.DB.Callback().Update().After("gorm:commit_or_rollback_transaction").Register(name, func(tx *gorm.DB) {
		values, ok := tx.Statement.Dest.(map[string]any)
		if tx.Statement.Table == "sql_change_files" && ok && values["execute_status"] == "PENDING" {
			pauseOnce.Do(func() { close(paused) })
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }); repository.DB.Callback().Update().Remove(name) })
	if _, _, err := s.StartReleaseSQLStatementExecution(context.Background(), rows[0].ID, "retry", SQLExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("retry did not reach its final summary")
	}
	observed, _, err := s.GetSQLFile(file.ID)
	releaseOnce.Do(func() { close(release) })
	if err != nil {
		t.Fatal(err)
	}
	if observed.ExecuteStatus == "PENDING" {
		t.Error("readers observed the uncommitted summary reset")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.cancelReg.mu.Lock()
		_, running := s.cancelReg.cancels[file.ID]
		s.cancelReg.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry summary did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	final, _, err := s.GetSQLFile(file.ID)
	if err != nil || final.ExecuteStatus != "FAILED" || !strings.Contains(final.ExecuteMessage, "division by zero") {
		t.Fatal("final retry result was lost", err)
	}
}

func TestSQLReviewFixMaterializedViewPartialRestore(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE TABLE sql_release.review_mv_table(x integer); INSERT INTO sql_release.review_mv_table VALUES(1);
CREATE FUNCTION sql_release.review_idx_fn(integer) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT $1';
CREATE MATERIALIZED VIEW sql_release.review_mv AS SELECT x FROM sql_release.review_mv_table;
CREATE INDEX review_mv_a_plain_idx ON sql_release.review_mv(x);
CREATE INDEX review_mv_idx ON sql_release.review_mv(sql_release.review_idx_fn(x));`)
	task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.review_mv_table ALTER COLUMN x TYPE bigint;", ExecutionMode: "STEP"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AnalyzeSQLViewDependencyTask(context.Background(), task.ID, "review"); err != nil {
		t.Fatal(err)
	}
	failed, _, firstErr := s.ExecuteSQLViewDependencyTask(context.Background(), task.ID, "review")
	if firstErr == nil || failed.Status != "RESTORE_FAILED" {
		t.Fatalf("expected restore-index failure: %+v %v", failed, firstErr)
	}
	sqlReviewFixExec(t, `CREATE FUNCTION sql_release.review_idx_fn(bigint) RETURNS bigint LANGUAGE sql IMMUTABLE AS 'SELECT $1';`)
	result, _, restoreErr := s.RestoreSQLViewDependencyTask(context.Background(), task.ID, "review")
	if restoreErr != nil || result.Status != "RESTORED" {
		t.Fatalf("unexpected recovery: %+v %v", result, restoreErr)
	}
	var indexes int
	if err := repository.DB.Raw("SELECT count(*) FROM pg_indexes WHERE schemaname='sql_release' AND tablename='review_mv'").Scan(&indexes).Error; err != nil {
		t.Fatal(err)
	}
	if indexes != 2 {
		t.Fatalf("missing restored index: %d", indexes)
	}
	t.Log("reused matching materialized view and restored its missing index")
}

type reviewLockLatency struct{ queries atomic.Int64 }

func (d *reviewLockLatency) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "FROM pg_locks") {
		d.queries.Add(1)
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		}
	}
	return ctx
}
func (*reviewLockLatency) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestSQLReviewFixBulkPrecheckSharedDeadline(t *testing.T) {
	s := sqlReviewFixService(t)
	var ddl, changes strings.Builder
	const tables = 180
	for i := 0; i < tables; i++ {
		fmt.Fprintf(&ddl, "CREATE TABLE sql_release.review_lock_%03d(x varchar(10));", i)
		fmt.Fprintf(&changes, "ALTER TABLE sql_release.review_lock_%03d ALTER COLUMN x TYPE varchar(20);\n", i)
	}
	sqlReviewFixExec(t, ddl.String())
	task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: changes.String(), ExecutionMode: "TRANSACTION"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AnalyzeSQLViewDependencyTask(context.Background(), task.ID, "review"); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := s.loadSQLExecutionConfig()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%s dbname=%s user=%s sslmode=require", cfg.Host, cfg.Port, cfg.Database, cfg.User))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Password = cfg.Password
	delay := &reviewLockLatency{}
	parsed.Tracer = delay
	traced := stdlib.OpenDB(*parsed)
	traced.SetMaxOpenConns(5)
	old := s.dbPool
	s.dbPool = traced
	_ = old.Close()
	t.Cleanup(func() { _ = traced.Close() })
	start := time.Now()
	_, _, err = s.PrecheckSQLViewDependencyTask(context.Background(), task.ID, "review")
	if err != nil {
		t.Fatalf("unexpected precheck: %v", err)
	}
	var result model.SQLViewDependencyTask
	if err := repository.DB.First(&result, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if result.Status != "PRECHECK_PASSED" || delay.queries.Load() != 1 {
		t.Fatalf("status=%s", result.Status)
	}
	t.Logf("%d-table batch prechecked in %s with %d lock probe", tables, time.Since(start), delay.queries.Load())
}

func TestSQLReviewFixLegacyTargetBinding(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE TABLE sql_release.legacy_target(x varchar(10)); CREATE VIEW sql_release.legacy_target_view AS SELECT x FROM sql_release.legacy_target;`)
	task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.legacy_target ALTER COLUMN x TYPE varchar(20);"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	sqlReviewFixExec(t, fmt.Sprintf("UPDATE sql_view_dependency_tasks SET database_target='' WHERE id=%d", task.ID))
	if _, _, err := s.PrecheckSQLViewDependencyTask(context.Background(), task.ID, "review"); err == nil {
		t.Fatal("legacy task executed without binding")
	}
	// Binding must work on the existing platform transaction with a one-slot pool.
	platform, err := repository.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	platform.SetMaxOpenConns(1)
	defer platform.SetMaxOpenConns(5)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bound, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "review")
	if err != nil || bound.DatabaseTarget == "" {
		t.Fatalf("safe legacy analysis: %+v %v", bound, err)
	}
	sqlReviewFixExec(t, fmt.Sprintf("UPDATE sql_view_dependency_tasks SET database_target='',status='FAILED' WHERE id=%d", task.ID))
	if err := repository.DB.Create(&model.SQLViewDependencyStep{TaskID: task.ID, StepName: "DROP_VIEW", Status: "SUCCESS", SQLContent: "DROP VIEW sql_release.legacy_target_view"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "review"); err == nil {
		t.Fatal("legacy DDL history rebound without proof")
	}
}

func TestSQLReviewFixRecoveryPreservesMatchingMetadataAndRejectsConflicts(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE TABLE sql_release.recovery_base(x varchar(10));
 CREATE VIEW sql_release.recovery_view AS SELECT x FROM sql_release.recovery_base;
 CREATE FUNCTION sql_release.recovery_trigger_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
 CREATE TRIGGER recovery_trigger INSTEAD OF INSERT ON sql_release.recovery_view FOR EACH ROW EXECUTE FUNCTION sql_release.recovery_trigger_fn();
 CREATE RULE recovery_rule AS ON DELETE TO sql_release.recovery_view DO INSTEAD NOTHING;`)
	task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.recovery_base ALTER COLUMN x TYPE varchar(20);"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AnalyzeSQLViewDependencyTask(context.Background(), task.ID, "review"); err != nil {
		t.Fatal(err)
	}
	sqlReviewFixExec(t, fmt.Sprintf("UPDATE sql_view_dependency_tasks SET status='RESTORE_FAILED' WHERE id=%d", task.ID))
	restored, items, err := s.RestoreSQLViewDependencyTask(context.Background(), task.ID, "review")
	if err != nil || restored.Status != "RESTORED" || len(items) != 1 {
		t.Fatalf("existing metadata reuse: %+v %v", restored, err)
	}
	// A subsequent external change must survive a recovery attempt unchanged.
	sqlReviewFixExec(t, `CREATE OR REPLACE VIEW sql_release.recovery_view AS SELECT 'changed'::varchar(10) AS x;`)
	sqlReviewFixExec(t, fmt.Sprintf("UPDATE sql_view_dependency_tasks SET status='RESTORE_FAILED' WHERE id=%d", task.ID))
	_, _, err = s.RestoreSQLViewDependencyTask(context.Background(), task.ID, "review")
	if err == nil || !strings.Contains(err.Error(), "恢复冲突") {
		t.Fatalf("conflict hidden: %v", err)
	}
	var value string
	if err := repository.DB.Raw("SELECT x FROM sql_release.recovery_view").Scan(&value).Error; err != nil || value != "changed" {
		t.Fatalf("external view overwritten: %q %v", value, err)
	}
	// An existing index on another relation must never count as repaired.
	sqlReviewFixExec(t, `CREATE MATERIALIZED VIEW sql_release.recovery_mv AS SELECT x FROM sql_release.recovery_base; CREATE TABLE sql_release.other_index_target(x int); CREATE INDEX recovery_idx ON sql_release.other_index_target(x);`)
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := execSQLViewRecovery(context.Background(), conn, "CREATE INDEX recovery_idx ON sql_release.recovery_mv(x)"); err == nil || !strings.Contains(err.Error(), "目标表不同") {
		t.Fatalf("index collision hidden: %v", err)
	}
}

func TestSQLReviewFixInheritanceSessionContexts(t *testing.T) {
	row := func(id uint, sql, status string) model.SQLChangeStatement {
		return model.SQLChangeStatement{ID: id, LineNumber: int(id), SQLContent: sql, ExecuteStatus: status, TransactionOutcome: "COMMITTED"}
	}
	previous := []model.SQLChangeStatement{row(1, "SET search_path=a", "SUCCESS"), row(2, "UPDATE counter SET n=n+1", "SUCCESS")}
	pending := func(sqls ...string) []model.SQLChangeStatement {
		var rows []model.SQLChangeStatement
		for i, sql := range sqls {
			rows = append(rows, row(uint(i+10), sql, "PENDING"))
		}
		return rows
	}
	for _, tc := range []struct {
		name    string
		current []model.SQLChangeStatement
		reject  bool
	}{
		{"same", pending("SET search_path=a", "UPDATE counter SET n=n+1", "SELECT 1"), false},
		{"changed", pending("SET search_path=b", "UPDATE counter SET n=n+1"), true},
		{"removed", pending("UPDATE counter SET n=n+1"), true},
		{"prepended", pending("SET ROLE another_role", "SET search_path=a", "UPDATE counter SET n=n+1"), true},
		{"rolled_back_setting", pending("SET search_path=a", "BEGIN", "SET search_path=b", "ROLLBACK", "UPDATE counter SET n=n+1"), false},
		{"committed_setting", pending("SET search_path=a", "BEGIN", "SET search_path=b", "COMMIT", "UPDATE counter SET n=n+1"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReleaseSQLInheritanceContexts(previous, tc.current)
			if (err != nil) != tc.reject {
				t.Fatalf("reject=%t err=%v", tc.reject, err)
			}
		})
	}
	previous[0].ExecuteStatus = "SKIPPED"
	if err := validateReleaseSQLInheritanceContexts(previous, pending("SET search_path=a", "UPDATE counter SET n=n+1")); err != nil {
		t.Fatalf("unchanged skipped SET: %v", err)
	}
	committed := []model.SQLChangeStatement{row(21, "BEGIN", "SUCCESS"), row(22, "SET search_path=a", "SUCCESS"), row(23, "UPDATE counter SET n=n+1", "SUCCESS"), row(24, "COMMIT", "SUCCESS")}
	if err := validateReleaseSQLInheritanceContexts(committed, pending("BEGIN", "SET search_path=b", "UPDATE counter SET n=n+1", "COMMIT")); err == nil {
		t.Fatal("changed context inside committed group accepted")
	}

}

func TestSQLReviewFixTransactionAndViewPairDiagnostics(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE FUNCTION sql_release.diagnostic_error() RETURNS integer LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='review-primary', DETAIL='review-detail-evidence', HINT='review-fix-guidance'; END $$;`)
	assertDetails := func(message string) {
		t.Helper()
		for _, part := range []string{"DETAIL: review-detail-evidence", "HINT: review-fix-guidance", "CONTEXT:"} {
			if !strings.Contains(message, part) {
				t.Fatalf("missing %s: %s", part, message)
			}
		}
	}
	file, _ := saveReleaseTestFile(t, s, "BEGIN; INSERT INTO progress VALUES(9); SELECT diagnostic_error(); COMMIT;")
	result, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "review")
	if err != nil || result.ExecuteStatus != "FAILED" {
		t.Fatalf("transaction: %+v %v", result, err)
	}
	assertDetails(result.ExecuteMessage)
	for _, row := range rows {
		assertDetails(row.ExecuteMessage)
	}
	var count int64
	if err := repository.DB.Table("sql_release.progress").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("transaction effects survived: %d %v", count, err)
	}
	var records []model.SQLStatementRecord
	if err := repository.DB.Where("file_id=? AND action='EXECUTE'", file.ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 {
		t.Fatalf("attempts=%d", len(records))
	}
	for _, record := range records {
		assertDetails(record.Message)
	}
	sqlReviewFixExec(t, `CREATE VIEW sql_release.diagnostic_pair AS SELECT 1 AS x; ALTER VIEW sql_release.diagnostic_pair OWNER TO sql_release;
 CREATE FUNCTION sql_release.diagnostic_ddl_error() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION USING ERRCODE='P0001',MESSAGE='review-primary',DETAIL='review-detail-evidence',HINT='review-fix-guidance'; END $$;
 CREATE EVENT TRIGGER diagnostic_ddl ON ddl_command_end WHEN TAG IN ('CREATE VIEW') EXECUTE FUNCTION sql_release.diagnostic_ddl_error();`)
	pair, _, err := s.ParseSQL(ParseSQLRequest{FileName: "V1__2__sql_release.sql", Content: "DROP VIEW diagnostic_pair; CREATE VIEW diagnostic_pair AS SELECT 2 AS x;"})
	if err != nil {
		t.Fatal(err)
	}
	result, rows, err = s.ExecuteSQLFile(context.Background(), pair.ID, "review")
	if err != nil || result.ExecuteStatus != "FAILED" {
		t.Fatalf("view pair: %+v %v", result, err)
	}
	assertDetails(result.ExecuteMessage)
	assertDetails(rows[1].ExecuteMessage)
	records = nil
	if err := repository.DB.Where("statement_id=? AND action='EXECUTE'", rows[1].ID).Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("pair attempts=%d", len(records))
	}
	assertDetails(records[0].Message)
	var x int
	if err := repository.DB.Raw("SELECT x FROM sql_release.diagnostic_pair").Scan(&x).Error; err != nil || x != 1 {
		t.Fatalf("original view lost: %d %v", x, err)
	}
}

func TestSQLReviewFixBulkLockProbeKeepsQuotedNames(t *testing.T) {
	s := sqlReviewFixService(t)
	sqlReviewFixExec(t, `CREATE TABLE sql_release."Mixed"(x int); CREATE TABLE sql_release.mixed(x int);`)
	db, err := s.getDB("")
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.Exec(`LOCK TABLE sql_release."Mixed" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	waiter, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer waiter.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := waiter.ExecContext(ctx, `SELECT * FROM sql_release."Mixed"`); done <- err }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pg_locks WHERE NOT granted AND relation='sql_release."Mixed"'::regclass AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock waiter did not queue")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := validateSQLViewDependencyLocks(ctx, db, []model.SQLViewDependencyChange{{SchemaName: "sql_release", TableName: "mixed"}}); err != nil {
		t.Fatalf("wrong table blocked: %v", err)
	}
	if err := validateSQLViewDependencyLocks(ctx, db, []model.SQLViewDependencyChange{{SchemaName: "sql_release", TableName: "Mixed"}, {SchemaName: "sql_release", TableName: "mixed"}}); err == nil || !strings.Contains(err.Error(), "Mixed") {
		t.Fatalf("quoted target lock lost: %v", err)
	}
}

func TestSQLReviewFixRecoveryConflictSkipsOnlyItsMetadata(t *testing.T) {
	plan := SQLViewDependencyStepExecutionPlan{
		DropSteps: []string{"drop first", "drop second"}, AlterSteps: []string{"alter"},
		RestoreSteps:  []string{"create conflict", "grant conflict", "create second", "grant second"},
		RestoreGroups: []SQLViewDependencyRestoreGroup{{DropIndex: 0, Start: 0, End: 2}, {DropIndex: 1, Start: 2, End: 4}},
	}
	var ran, skipped []int
	err := runViewStepPlan(plan, func(index int, stage, text string, recovering bool) error {
		ran = append(ran, index)
		if text == "create conflict" {
			return sqlViewRecoveryConflictError{name: "conflict_view"}
		}
		return nil
	}, func(index int) error { skipped = append(skipped, index); return nil })
	if err == nil || !strings.Contains(err.Error(), "恢复冲突") {
		t.Fatalf("conflict lost: %v", err)
	}
	if fmt.Sprint(ran) != "[0 1 2 3 5 6]" || fmt.Sprint(skipped) != "[4]" {
		t.Fatalf("unsafe metadata recovery: ran=%v skipped=%v", ran, skipped)
	}
}

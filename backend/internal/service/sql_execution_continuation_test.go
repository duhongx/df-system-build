package service

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"df-build-server/internal/testutil"
	"df-build-server/pkg/config"
	"df-build-server/pkg/logger"
	"github.com/jackc/pgx/v5"
)

// These execution regressions need real PostgreSQL transactions. Use a new
// disposable database per test so they never reuse application data.
func isolatedSQLContinuationService(t *testing.T) *PostgreSQLService {
	t.Helper()
	dsn, ok := testutil.PostgresDSNFromEnv()
	if !ok {
		t.Skip("set TEST_DATABASE_DSN or TEST_DB_* for PostgreSQL execution regressions")
	}
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	database := fmt.Sprintf("dfops_continuation_%d", time.Now().UnixNano())
	if _, err := admin.Exec("CREATE DATABASE " + quoteIdent(database)); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	previousDB := repository.DB
	t.Cleanup(func() {
		if repository.DB != nil && repository.DB != previousDB {
			if db, err := repository.DB.DB(); err == nil {
				db.Close()
			}
		}
		repository.DB = previousDB
		if _, err := admin.Exec("DROP DATABASE " + quoteIdent(database) + " WITH (FORCE)"); err != nil {
			t.Errorf("drop isolated database: %v", err)
		}
		admin.Close()
	})
	logger.Init("error", "stdout", "")
	if err := repository.InitDB(&config.DatabaseConfig{
		Host: parsed.Host, Port: int(parsed.Port), Name: database, User: parsed.User,
		Password: parsed.Password, MaxOpenConns: 5, MaxIdleConns: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"postgresql_host": parsed.Host, "postgresql_port": strconv.Itoa(int(parsed.Port)),
		"postgresql_database": database, "postgresql_user": parsed.User,
		"postgresql_password": "isolated-test-password", "postgresql_password_mode": "shared",
	} {
		if err := repository.NewSettingsRepo().Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.DB.Exec(`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='sql_release') THEN CREATE ROLE sql_release LOGIN; END IF; END $$;
CREATE SCHEMA sql_release AUTHORIZATION sql_release;
CREATE TABLE sql_release.progress(step integer);
ALTER TABLE sql_release.progress OWNER TO sql_release;`).Error; err != nil {
		t.Fatal(err)
	}
	return NewPostgreSQLService()
}

func TestReleaseSQLContinuationAdvancesPendingThenNextFile(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	content := strings.Repeat("SELECT 1;\n", 8) + "SELECT * FROM permanently_missing_view;\nINSERT INTO progress VALUES (10);"
	file, rows := saveReleaseTestFile(t, s, content)
	for i := 0; i < 9; i++ {
		rows[i].ExecuteStatus = "SUCCESS"
		if i == 5 || i == 7 || i == 8 {
			rows[i].ExecuteStatus, rows[i].SQLState, rows[i].ExecuteMessage = "FAILED", "42P01", "original missing view error"
			rows[i].ManualFailure = i != 8
		}
		if err := repository.DB.Save(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	target, _ := currentReleaseSQLTarget()
	file.DatabaseTarget, file.ExecuteStatus = target, "FAILED"
	if err := repository.DB.Save(file).Error; err != nil {
		t.Fatal(err)
	}
	next := saveSequenceFile(t, s, "V1__2__sql_release.sql", "INSERT INTO progress VALUES (11)")
	batch, files, err := s.prepareSQLFileSequence([]uint{file.ID, next.ID}, "continue-operator")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(batch, files, "continue-operator", SQLExecuteOptions{})
	_, result, err := s.GetSQLFile(file.ID)
	if err != nil || result[8].ExecuteStatus != "FAILED" || !result[8].ManualFailure || result[8].SQLState != "42P01" || result[9].ExecuteStatus != "SUCCESS" {
		t.Fatalf("continuation did not retain failure and execute SQL 10: %+v, %v", result, err)
	}
	var steps []int
	repository.DB.Raw("SELECT step FROM sql_release.progress ORDER BY step").Scan(&steps)
	if fmt.Sprint(steps) != "[10 11]" {
		t.Fatalf("did not advance SQL 10 then next file: %v", steps)
	}
	var retries int64
	repository.DB.Model(&model.SQLStatementRecord{}).Where("statement_id = ? AND action = 'EXECUTE'", rows[8].ID).Count(&retries)
	if retries != 0 {
		t.Fatal("continuation retried a previously failed SQL")
	}
	_, nextRows, _ := s.GetSQLFile(next.ID)
	if nextRows[0].ExecuteStatus != "SUCCESS" {
		t.Fatal("next SQL file did not execute")
	}
	ended, _, err := s.GetSQLBatch(batch.ID)
	if err != nil || ended.Outcome != "COMPLETED" || ended.DeferredFiles != 1 || ended.FinishedAt == nil || !strings.Contains(ended.ExecuteMessage, "全部处理") {
		t.Fatalf("completion with deferred failures needs an explicit result: %+v, %v", ended, err)
	}
	if _, _, err := s.prepareSQLFileSequence([]uint{file.ID}, "again"); err == nil {
		t.Fatal("failed-only file should require explicit retry")
	}
}

func TestReleaseSQLContinuationStopsAtNewFailure(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	file, rows := saveReleaseTestFile(t, s, "SELECT * FROM old_missing; SELECT * FROM new_missing;")
	rows[0].ExecuteStatus, rows[0].ExecuteMessage = "FAILED", "old failure"
	repository.DB.Save(&rows[0])
	target, _ := currentReleaseSQLTarget()
	file.DatabaseTarget, file.ExecuteStatus = target, "FAILED"
	repository.DB.Save(file)
	next := saveSequenceFile(t, s, "V1__2__sql_release.sql", "INSERT INTO progress VALUES (2)")
	batch, files, err := s.prepareSQLFileSequence([]uint{file.ID, next.ID}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(batch, files, "operator", SQLExecuteOptions{})
	_, got, _ := s.GetSQLFile(file.ID)
	if got[1].ExecuteStatus != "FAILED" || got[1].ManualFailure {
		t.Fatalf("a new failure must stop, not auto-defer: %+v", got[1])
	}
	nextFile, _, _ := s.GetSQLFile(next.ID)
	if nextFile.ExecuteStatus != "CANCELED" {
		t.Fatalf("next file ran after new failure: %+v", nextFile)
	}
	ended, _, err := s.GetSQLBatch(batch.ID)
	if err != nil || ended.Outcome != "ERROR" || ended.StoppedFileID != file.ID || ended.StoppedFileName != file.FileName || !strings.Contains(ended.ExecuteMessage, "new_missing") {
		t.Fatalf("stopping error must identify the file and database error: %+v, %v", ended, err)
	}
}

func TestReleaseSQLBatchSummaryKeepsFilteredReasons(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	valid := saveSequenceFile(t, s, "V1__1__sql_release.sql", "SELECT 1")
	failed, rows, err := s.ParseSQL(ParseSQLRequest{FileName: "V1__2__sql_release.sql", Content: "SELECT * FROM missing_view"})
	if err != nil {
		t.Fatal(err)
	}
	rows[0].ExecuteStatus, rows[0].ExecuteMessage = "FAILED", "previous failure"
	repository.DB.Save(&rows[0])
	failed.ExecuteStatus = "FAILED"
	repository.DB.Save(failed)
	manual := saveSequenceFile(t, s, "needs_manual.sql", "SELECT 2")
	batch, files, err := s.prepareSQLFileSequence([]uint{valid.ID, failed.ID, manual.ID}, "operator")
	if err != nil || len(files) != 1 || len(batch.FilteredFiles) != 2 {
		t.Fatalf("invalid filtering: %+v, %+v, %v", batch, files, err)
	}
	s.runSQLFileSequence(batch, files, "operator", SQLExecuteOptions{})
	ended, _, err := s.GetSQLBatch(batch.ID)
	if err != nil || ended.Outcome != "COMPLETED" || ended.SuccessFiles != 1 || len(ended.FilteredFiles) != 2 {
		t.Fatalf("filtered reasons were lost after polling/reopen: %+v, %v", ended, err)
	}
	codes := map[uint]string{}
	for _, file := range ended.FilteredFiles {
		codes[file.ID] = file.Code
		if file.Reason == "" {
			t.Fatal("filtered file has no reason")
		}
	}
	if codes[failed.ID] != "NO_PENDING_SQL" || codes[manual.ID] != "MISSING_SCHEMA" {
		t.Fatalf("cannot distinguish failed-only from missing execution account: %v", codes)
	}
	live, _, err := s.GetCurrentSQLBatchFiles(batch.ID)
	if err != nil || live.Outcome != "COMPLETED" || len(live.FilteredFiles) != 2 {
		t.Fatalf("workbench polling/reopen must retain the same reasons as history: %+v, %v", live, err)
	}
}

func TestReleaseSQLBatchInterruptionHasOutcome(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	file := saveSequenceFile(t, s, "V1__1__sql_release.sql", "SELECT 1")
	batch, _, err := s.prepareSQLFileSequence([]uint{file.ID}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterruptedSQLFiles(); err != nil {
		t.Fatal(err)
	}
	ended, _, err := s.GetSQLBatch(batch.ID)
	if err != nil || ended.Outcome != "INTERRUPTED" || ended.FinishedAt == nil {
		t.Fatalf("interrupted run has no explanation: %+v, %v", ended, err)
	}
}

func TestReleaseSQLViewPairRollbackPreservesOriginal(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	if err := repository.DB.Exec("CREATE VIEW sql_release.protected_view AS SELECT 1 AS old_column; ALTER VIEW sql_release.protected_view OWNER TO sql_release").Error; err != nil {
		t.Fatal(err)
	}
	file, rows := saveReleaseTestFile(t, s, "DROP VIEW protected_view; CREATE OR REPLACE VIEW protected_view AS SELECT non_existent_function() AS new_column; INSERT INTO progress VALUES (3);")
	result, got, err := s.ExecuteSQLFile(context.Background(), file.ID, "operator")
	if err != nil || result.ExecuteStatus != "FAILED" || got[0].ExecuteStatus != "FAILED" || got[1].ExecuteStatus != "FAILED" || got[2].ExecuteStatus != "PENDING" {
		t.Fatalf("unexpected view pair result: %+v, %+v, %v", result, got, err)
	}
	var original int
	if err := repository.DB.Raw("SELECT old_column FROM sql_release.protected_view").Scan(&original).Error; err != nil || original != 1 {
		t.Fatalf("original view lost after CREATE failure: %d, %v", original, err)
	}
	if _, err := s.MarkSQLStatementFailed(rows[1].ID, "defer"); err != nil {
		t.Fatal(err)
	}
	_, deferred, _ := s.GetSQLFile(file.ID)
	if !deferred[0].ManualFailure || !deferred[1].ManualFailure {
		t.Fatal("manual failure must defer both halves of a view pair")
	}
	if _, err := s.SkipSQLStatement(rows[1].ID, "skip"); err != nil {
		t.Fatal(err)
	}
	_, skipped, _ := s.GetSQLFile(file.ID)
	if skipped[0].ExecuteStatus != "SKIPPED" || skipped[1].ExecuteStatus != "SKIPPED" {
		t.Fatal("skipping must apply to both halves")
	}
}

func TestReleaseSQLViewPairCreatesAbsentView(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	file, _ := saveReleaseTestFile(t, s, "DROP VIEW absent_view; CREATE OR REPLACE VIEW absent_view AS SELECT 2 AS value; INSERT INTO progress VALUES (4);")
	result, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "operator")
	if err != nil || result.ExecuteStatus != "SUCCESS" || rows[0].ExecuteStatus != "SKIPPED" || rows[1].ExecuteStatus != "SUCCESS" || rows[2].ExecuteStatus != "SUCCESS" {
		t.Fatalf("absent DROP must not abort its CREATE transaction: %+v, %+v, %v", result, rows, err)
	}
	var value int
	if err := repository.DB.Raw("SELECT value FROM sql_release.absent_view").Scan(&value).Error; err != nil || value != 2 {
		t.Fatalf("view was not created: %d %v", value, err)
	}
}

func TestReleaseSQLViewPairEditedCreateRetriesWholeTransaction(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	repository.DB.Exec("CREATE VIEW sql_release.edited_view AS SELECT 1 AS old_column; ALTER VIEW sql_release.edited_view OWNER TO sql_release")
	file, _ := saveReleaseTestFile(t, s, "DROP VIEW edited_view; CREATE OR REPLACE VIEW edited_view AS SELECT missing_function() AS new_column; INSERT INTO progress VALUES (5)")
	_, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "operator")
	if err != nil || rows[1].ExecuteStatus != "FAILED" {
		t.Fatalf("expected initial failure: %+v, %v", rows, err)
	}
	_, edited, err := s.EditReleaseSQLStatement(rows[1].ID, EditReleaseSQLStatementRequest{
		Content: "CREATE OR REPLACE VIEW edited_view AS SELECT 2 AS new_column", OriginalContent: rows[1].SQLContent,
	}, "editor")
	if err != nil || edited[0].ExecuteStatus != "PENDING" || edited[1].ExecuteStatus != "PENDING" {
		t.Fatalf("editing must reset both rolled-back halves: %+v, %v", edited, err)
	}
	batch, files, err := s.prepareSQLFileSequence([]uint{file.ID}, "continue")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(batch, files, "continue", SQLExecuteOptions{})
	var value int
	if err := repository.DB.Raw("SELECT new_column FROM sql_release.edited_view").Scan(&value).Error; err != nil || value != 2 {
		t.Fatalf("edited pair failed to replace columns: %d, %v", value, err)
	}
}

func TestReleaseSQLViewPairSingleRetryLeavesLaterSQLPending(t *testing.T) {
	s := isolatedSQLContinuationService(t)
	repository.DB.Exec("CREATE VIEW sql_release.retried_view AS SELECT 1 AS old_column; ALTER VIEW sql_release.retried_view OWNER TO sql_release")
	file, _ := saveReleaseTestFile(t, s, "DROP VIEW retried_view; CREATE OR REPLACE VIEW retried_view AS SELECT retry_function() AS new_column; INSERT INTO progress VALUES (6)")
	_, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "operator")
	if err != nil || rows[1].ExecuteStatus != "FAILED" {
		t.Fatalf("expected initial failure: %+v, %v", rows, err)
	}
	if _, err := s.MarkSQLStatementFailed(rows[1].ID, "defer"); err != nil {
		t.Fatal(err)
	}
	if err := repository.DB.Exec("CREATE FUNCTION sql_release.retry_function() RETURNS integer LANGUAGE SQL AS 'SELECT 2'").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StartReleaseSQLStatementExecution(context.Background(), rows[1].ID, "retry", SQLExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, statements, err := s.GetSQLFile(file.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ExecuteStatus != "RUNNING" {
			if statements[0].ExecuteStatus != "SUCCESS" || statements[1].ExecuteStatus != "SUCCESS" || statements[2].ExecuteStatus != "PENDING" {
				t.Fatalf("single pair retry changed later SQL: %+v", statements)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("pair retry did not complete")
}

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

func TestReleaseSQLRejectsTopLevelTransactions(t *testing.T) {
	for _, text := range []string{"BEGIN", "START TRANSACTION", "COMMIT", "END", "ABORT", "ROLLBACK", "SAVEPOINT s", "RELEASE SAVEPOINT s", "ROLLBACK TO s", "PREPARE TRANSACTION 's'", "COMMIT PREPARED 's'", "ROLLBACK PREPARED 's'", "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"} {
		if err := validateReleaseSQLTransactions([]model.SQLChangeStatement{{SQLContent: text, LineNumber: 3}}); err == nil {
			t.Fatalf("accepted %s", text)
		}
	}
	for _, text := range []string{"SELECT 'BEGIN; COMMIT'", "DO $$ BEGIN RAISE NOTICE 'COMMIT'; END $$", "CREATE FUNCTION f() RETURNS void AS $$ BEGIN RETURN; END $$ LANGUAGE plpgsql"} {
		if err := validateReleaseSQLTransactions([]model.SQLChangeStatement{{SQLContent: text}}); err != nil {
			t.Fatalf("rejected function/string: %v", err)
		}
	}
}

// One empty isolated database for the suite; every subtest has distinct identities.
func TestSQLExecutionAuditFixes(t *testing.T) {
	s := releaseSQLTestService(t)
	ctx := context.Background()
	exec := func(t *testing.T, text string) {
		t.Helper()
		if err := repository.DB.Exec(text).Error; err != nil {
			t.Fatal(err)
		}
	}
	save := func(t *testing.T, name, content string) *model.SQLChangeFile {
		t.Helper()
		f, _, err := s.ParseSQL(ParseSQLRequest{FileName: name, Content: content, Overwrite: true})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	count := func(t *testing.T, table string) int {
		t.Helper()
		var n int
		if err := repository.DB.Raw("SELECT value FROM sql_release." + table).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	wait := func(t *testing.T, id uint) *model.SQLChangeFile {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			f, _, err := s.GetSQLFile(id)
			if err != nil {
				t.Fatal(err)
			}
			if f.ExecuteStatus != "RUNNING" {
				return f
			}
			if time.Now().After(deadline) {
				t.Fatal("worker timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	viewTask := func(t *testing.T, name string) *model.SQLViewDependencyTask {
		t.Helper()
		exec(t, "CREATE TABLE sql_release."+name+"(id int); CREATE VIEW sql_release."+name+"_view AS SELECT id FROM sql_release."+name)
		task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release." + name + " ALTER COLUMN id TYPE bigint"}, "test")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PrecheckSQLViewDependencyTask(ctx, task.ID, "test"); err != nil {
			t.Fatal(err)
		}
		return task
	}

	t.Run("transaction_import_and_legacy_execution_rejected", func(t *testing.T) {
		if _, _, err := s.ParseSQL(ParseSQLRequest{FileName: "FIX__1__sql_release.sql", Content: "BEGIN; SELECT 1; COMMIT;"}); err == nil {
			t.Fatal("transaction import allowed")
		}
		f := save(t, "FIX__1__sql_release.sql", "SELECT 1")
		exec(t, "UPDATE sql_change_statements SET sql_content='BEGIN' WHERE file_id="+fmtUint(f.ID))
		if _, _, err := s.ExecuteSQLFile(ctx, f.ID, "test"); err == nil {
			t.Fatal("legacy transaction allowed")
		}
		current, _, _ := s.GetSQLFile(f.ID)
		if current.ExecuteStatus != "PENDING" {
			t.Fatalf("claim was not rolled back: %+v", current)
		}
	})
	t.Run("unknown_cannot_be_deleted_or_overwritten", func(t *testing.T) {
		exec(t, "CREATE TABLE sql_release.fix_unknown(value int); ALTER TABLE sql_release.fix_unknown OWNER TO sql_release; INSERT INTO sql_release.fix_unknown VALUES(0)")
		name, content := "FIX__2__sql_release.sql", "UPDATE fix_unknown SET value=value+1;"
		f := save(t, name, content)
		callback := "fix:fail-result"
		if err := repository.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if stmt, ok := tx.Statement.Dest.(*model.SQLChangeStatement); ok && stmt.FileID == f.ID && stmt.ExecuteStatus == "SUCCESS" {
				tx.AddError(errors.New("injected save failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		result, rows, err := s.ExecuteSQLFile(ctx, f.ID, "test")
		repository.DB.Callback().Update().Remove(callback)
		if err != nil || result.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "UNKNOWN" || count(t, "fix_unknown") != 1 {
			t.Fatalf("%+v %+v %v", result, rows, err)
		}
		if err := s.DeleteSQLFile(f.ID); err == nil {
			t.Fatal("deleted UNKNOWN")
		}
		if _, _, err := s.ParseSQL(ParseSQLRequest{FileName: name, Content: content, Overwrite: true}); err == nil {
			t.Fatal("overwrote UNKNOWN")
		}
		if _, _, err := s.ExecuteSQLFile(ctx, f.ID, "test"); err == nil {
			t.Fatal("replayed UNKNOWN")
		}
	})
	t.Run("deleted_partial_success_inherited", func(t *testing.T) {
		exec(t, "CREATE TABLE sql_release.fix_history(value int); ALTER TABLE sql_release.fix_history OWNER TO sql_release; INSERT INTO sql_release.fix_history VALUES(0)")
		name, content := "FIX__3__sql_release.sql", "UPDATE fix_history SET value=value+1; SELECT * FROM fix_history_missing;"
		f := save(t, name, content)
		first, _, err := s.ExecuteSQLFile(ctx, f.ID, "test")
		if err != nil || first.ExecuteStatus != "FAILED" {
			t.Fatalf("%+v %v", first, err)
		}
		if err := s.DeleteSQLFile(f.ID); err != nil {
			t.Fatal(err)
		}
		fresh := save(t, name, content)
		_, rows, _ := s.GetSQLFile(fresh.ID)
		if rows[0].ExecuteStatus != "SUCCESS" {
			t.Fatal("lost committed history")
		}
		exec(t, "CREATE TABLE sql_release.fix_history_missing(id int); ALTER TABLE sql_release.fix_history_missing OWNER TO sql_release")
		last, _, err := s.ExecuteSQLFile(ctx, fresh.ID, "test")
		if err != nil || last.ExecuteStatus != "SUCCESS" || count(t, "fix_history") != 1 {
			t.Fatalf("history replayed: %+v %v", last, err)
		}
	})
	t.Run("execution_claim_serializes_overwrite", func(t *testing.T) {
		f := save(t, "FIX__4__sql_release.sql", "SELECT pg_sleep(0.3); SELECT 1")
		loaded, resume := make(chan struct{}), make(chan struct{})
		var once atomic.Bool
		callback := "fix:pause-claim"
		if err := repository.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "sql_change_statements" && once.CompareAndSwap(false, true) {
				close(loaded)
				<-resume
			}
		}); err != nil {
			t.Fatal(err)
		}
		started := make(chan error, 1)
		go func() { _, _, err := s.StartSQLFileExecution(ctx, f.ID, "test", SQLExecuteOptions{}); started <- err }()
		select {
		case <-loaded:
		case <-time.After(5 * time.Second):
			close(resume)
			t.Fatal("claim not reached")
		}
		imported := make(chan error, 1)
		go func() {
			_, _, err := s.ParseSQL(ParseSQLRequest{FileName: f.FileName, Content: f.FileContent, Overwrite: true})
			imported <- err
		}()
		select {
		case err := <-imported:
			close(resume)
			t.Fatalf("overwrite crossed locked claim: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(resume)
		if err := <-started; err != nil {
			t.Fatal(err)
		}
		if err := <-imported; err == nil || !strings.Contains(err.Error(), "正在执行") {
			t.Fatalf("overwrite allowed: %v", err)
		}
		if wait(t, f.ID).ExecuteStatus != "SUCCESS" {
			t.Fatal("worker failed")
		}
		repository.DB.Callback().Query().Remove(callback)
		var n int64
		repository.DB.Model(&model.SQLChangeFile{}).Where("file_name=? AND is_deleted=false", f.FileName).Count(&n)
		if n != 1 {
			t.Fatalf("active revisions=%d", n)
		}
	})
	t.Run("database_claim_blocks_second_process", func(t *testing.T) {
		f := save(t, "FIX__5__sql_release.sql", "SELECT pg_sleep(0.15)")
		a, b := NewPostgreSQLService(), NewPostgreSQLService()
		a.cancelReg = newSQLExecutionCancelRegistry()
		b.cancelReg = newSQLExecutionCancelRegistry()
		if _, _, err := a.StartSQLFileExecution(ctx, f.ID, "first", SQLExecuteOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.StartSQLFileExecution(ctx, f.ID, "second", SQLExecuteOptions{}); err == nil {
			t.Fatal("second process claimed same file")
		}
		if wait(t, f.ID).ExecuteStatus != "SUCCESS" {
			t.Fatal("worker failed")
		}
	})
	t.Run("concurrent_first_imports_have_one_active_identity", func(t *testing.T) {
		start, done := make(chan struct{}), make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				<-start
				_, _, err := s.ParseSQL(ParseSQLRequest{FileName: "FIX__6__sql_release.sql", Content: "SELECT 1"})
				done <- err
			}()
		}
		close(start)
		first, second := <-done, <-done
		if (first == nil) == (second == nil) {
			t.Fatalf("imports were not exclusive: %v / %v", first, second)
		}
		duplicate := model.SQLChangeFile{Version: "FIX", FileName: "FIX__6__sql_release.sql", SchemaName: "sql_release", ExecuteStatus: "PENDING"}
		if err := repository.DB.Create(&duplicate).Error; pgState(err) != "23505" {
			t.Fatalf("database uniqueness missing: %v", err)
		}
	})
	t.Run("view_phases_and_backup_are_exclusive", func(t *testing.T) {
		task := viewTask(t, "fix_guard")
		if _, _, err := s.markSQLViewDependencyTaskRunning(task.ID, "EXECUTING", "first worker", "test", ensureSQLViewDependencyTaskCanExecute); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PrecheckSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("precheck unlocked running task")
		}
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("analysis replaced running backup")
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("second worker allowed")
		}
		current, items, _ := s.GetSQLViewDependencyTask(task.ID)
		if current.Status != "EXECUTING" || len(items) != 1 {
			t.Fatalf("claim overwritten: %+v %+v", current, items)
		}
		if _, _, err := s.VerifySQLViewDependencyItem(ctx, task.ID, items[0].ID, "test"); err == nil {
			t.Fatal("single-item action crossed running task")
		}
		if err := RecoverInterruptedSQLFiles(); err != nil {
			t.Fatal(err)
		}
		current, _, _ = s.GetSQLViewDependencyTask(task.ID)
		if current.Status != "UNKNOWN" || !current.OutcomeUncertain {
			t.Fatalf("interruption not recovered: %+v", current)
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("interrupted task replayed")
		}
		if _, _, err := s.RestoreSQLViewDependencyTask(ctx, task.ID, "test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("restoring views cleared ALTER uncertainty")
		}
	})
	t.Run("step_timeouts_applied_without_pool_pollution", func(t *testing.T) {
		exec(t, "CREATE TABLE sql_release.fix_timeout(value int)")
		plan := BuildSQLViewDependencyStepExecutionPlan(model.SQLViewDependencyTask{AlterSQL: "ALTER TABLE sql_release.fix_timeout ALTER COLUMN value TYPE bigint", LockTimeout: "1s", StatementTimeout: "2s"}, []model.SQLViewDependencyItem{{DropSQL: "DROP MATERIALIZED VIEW IF EXISTS sql_release.fix_timeout_values", CreateSQL: "CREATE MATERIALIZED VIEW sql_release.fix_timeout_values AS SELECT current_setting('lock_timeout') AS lock_timeout, current_setting('statement_timeout') AS statement_timeout"}})
		db, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 901, plan); err != nil {
			t.Fatal(err)
		}
		var values struct{ LockTimeout, StatementTimeout string }
		if err := repository.DB.Raw("SELECT * FROM sql_release.fix_timeout_values").Scan(&values).Error; err != nil {
			t.Fatal(err)
		}
		if values.LockTimeout != "1s" || values.StatementTimeout != "2s" {
			t.Fatalf("timeouts=%+v", values)
		}
		var lock, statement string
		if err := db.QueryRow("SELECT current_setting('lock_timeout'), current_setting('statement_timeout')").Scan(&lock, &statement); err != nil {
			t.Fatal(err)
		}
		if lock != "0" || statement != "0" {
			t.Fatalf("pooled connection polluted: %s %s", lock, statement)
		}
		if err := s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 902, SQLViewDependencyStepExecutionPlan{LockTimeout: "50ms", StatementTimeout: "50ms", AlterSteps: []string{"SELECT pg_sleep(0.2)"}}); pgState(err) != "57014" {
			t.Fatalf("statement timeout ineffective: %v", err)
		}
		blocking := repository.DB.Begin()
		if blocking.Error != nil {
			t.Fatal(blocking.Error)
		}
		defer blocking.Rollback()
		if err := blocking.Exec("LOCK TABLE sql_release.fix_timeout IN ACCESS EXCLUSIVE MODE").Error; err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		err = s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 905, SQLViewDependencyStepExecutionPlan{LockTimeout: "50ms", StatementTimeout: "500ms", AlterSteps: []string{"ALTER TABLE sql_release.fix_timeout ADD COLUMN locked int"}})
		if pgState(err) != "55P03" || time.Since(started) > 2*time.Second {
			t.Fatalf("lock timeout ineffective: %v", err)
		}
	})
	t.Run("partial_drop_restores_only_deleted_objects", func(t *testing.T) {
		exec(t, "CREATE VIEW sql_release.fix_drop_a AS SELECT 1 AS id; CREATE VIEW sql_release.fix_drop_b AS SELECT 2 AS id; CREATE VIEW sql_release.fix_drop_external AS SELECT id FROM sql_release.fix_drop_b")
		items := []model.SQLViewDependencyItem{{DropOrder: 1, RestoreOrder: 2, DropSQL: "DROP VIEW sql_release.fix_drop_a", CreateSQL: "CREATE VIEW sql_release.fix_drop_a AS SELECT 1 AS id"}, {DropOrder: 2, RestoreOrder: 1, DropSQL: "DROP VIEW sql_release.fix_drop_b", CreateSQL: "CREATE VIEW sql_release.fix_drop_b AS SELECT 2 AS id"}}
		plan := BuildSQLViewDependencyStepExecutionPlan(model.SQLViewDependencyTask{AlterSQL: "SELECT 1"}, items)
		db, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		err = s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 903, plan)
		var stepErr SQLViewDependencyStepExecutionError
		if !errors.As(err, &stepErr) || !stepErr.RestoreAttempted || !stepErr.RestoreSucceeded {
			t.Fatalf("partial restore failed: %v", err)
		}
		var exists bool
		repository.DB.Raw("SELECT to_regclass('sql_release.fix_drop_a') IS NOT NULL AND to_regclass('sql_release.fix_drop_b') IS NOT NULL AND to_regclass('sql_release.fix_drop_external') IS NOT NULL").Scan(&exists)
		if !exists {
			t.Fatal("views missing")
		}
		steps, _ := s.GetSQLViewDependencySteps(903)
		if steps[2].Status != "SKIPPED" || steps[3].Status != "SKIPPED" || steps[4].Status != "SUCCESS" {
			t.Fatalf("wrong restoration records: %+v", steps)
		}
	})
	t.Run("early_alter_failure_keeps_restore_record_indices", func(t *testing.T) {
		exec(t, "CREATE VIEW sql_release.fix_index_view AS SELECT 1 AS id")
		plan := BuildSQLViewDependencyStepExecutionPlan(model.SQLViewDependencyTask{AlterSQL: "ALTER TABLE sql_release.fix_missing ALTER COLUMN id TYPE bigint; ALTER TABLE sql_release.fix_missing ALTER COLUMN id TYPE text"}, []model.SQLViewDependencyItem{{DropSQL: "DROP VIEW sql_release.fix_index_view", CreateSQL: "CREATE VIEW sql_release.fix_index_view AS SELECT 1 AS id"}})
		db, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		if err = s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 904, plan); err == nil {
			t.Fatal("expected first ALTER failure")
		}
		steps, _ := s.GetSQLViewDependencySteps(904)
		if len(steps) != 4 || steps[1].Status != "FAILED" || steps[2].Status != "SKIPPED" || steps[3].Status != "SUCCESS" {
			t.Fatalf("restore marked unexecuted ALTER: %+v", steps)
		}
	})
	t.Run("full_view_execution_cannot_be_replayed", func(t *testing.T) {
		task := viewTask(t, "fix_full")
		result, items, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test")
		if err != nil || result.Status != "SUCCESS" {
			t.Fatalf("full execution: %+v %v", result, err)
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("completed task replayed")
		}
		if _, _, err := s.PrecheckSQLViewDependencyTask(ctx, task.ID, "test"); err == nil {
			t.Fatal("precheck reset completed task")
		}
		verified, _, err := s.VerifySQLViewDependencyItem(ctx, task.ID, items[0].ID, "test")
		if err != nil || verified.Status != "SUCCESS" || verified.OutcomeUncertain {
			t.Fatalf("verification lost task outcome: %+v %v", verified, err)
		}
	})
	t.Run("drop_result_save_failure_restores_known_committed_drop", func(t *testing.T) {
		task := viewTask(t, "fix_log")
		var failed atomic.Bool
		callback := "fix:fail-view-log"
		if err := repository.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if fields, ok := tx.Statement.Dest.(map[string]interface{}); ok && tx.Statement.Table == "sql_view_dependency_steps" && fields["status"] == "SUCCESS" && failed.CompareAndSwap(false, true) {
				tx.AddError(errors.New("injected view result save failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		result, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test")
		repository.DB.Callback().Update().Remove(callback)
		if !failed.Load() || err == nil || result.Status != "UNKNOWN" || !result.OutcomeUncertain {
			t.Fatalf("uncertain result not protected: %+v %v", result, err)
		}
		var exists bool
		if err := repository.DB.Raw("SELECT to_regclass('sql_release.fix_log_view') IS NOT NULL").Scan(&exists).Error; err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatal("known committed DROP was not restored after log failure")
		}
	})
	t.Run("transaction_view_failure_records_rollback", func(t *testing.T) {
		exec(t, "CREATE TABLE sql_release.fix_rollback(id text); INSERT INTO sql_release.fix_rollback VALUES('not_number'); CREATE VIEW sql_release.fix_rollback_view AS SELECT id FROM sql_release.fix_rollback")
		task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.fix_rollback ALTER COLUMN id TYPE integer USING id::integer", ExecutionMode: "TRANSACTION"}, "test")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "test"); err != nil {
			t.Fatal(err)
		}
		result, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test")
		if err == nil || result.Status != "FAILED" {
			t.Fatalf("expected cast failure: %+v %v", result, err)
		}
		var exists bool
		repository.DB.Raw("SELECT to_regclass('sql_release.fix_rollback_view') IS NOT NULL").Scan(&exists)
		if !exists {
			t.Fatal("transaction rollback lost view")
		}
		steps, err := s.GetSQLViewDependencySteps(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, step := range steps {
			if step.StepName == "DROP_DEPENDENCY" {
				found = true
				if step.Status != "ROLLED_BACK" {
					t.Fatalf("rollback recorded as committed success: %+v", step)
				}
			}
		}
		if !found {
			t.Fatal("missing drop log")
		}
	})
	t.Run("password_rotation_invalidates_cached_pool", func(t *testing.T) {
		old, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		settings := repository.NewSettingsRepo()
		password, err := settings.GetByKey("postgresql_admin_password")
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.Set("postgresql_admin_password", "rotated-audit-fixture"); err != nil {
			t.Fatal(err)
		}
		fresh, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		if old == fresh {
			t.Fatal("password-only change kept old pool")
		}
		if err := old.Ping(); err == nil {
			t.Fatal("old pool still accepts connections")
		}
		if err := settings.Set("postgresql_admin_password", password); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("connection_failure_releases_task_and_startup_marks_steps_unknown", func(t *testing.T) {
		task := viewTask(t, "fix_early")
		settings := repository.NewSettingsRepo()
		password, err := settings.GetByKey("postgresql_admin_password")
		if err != nil {
			t.Fatal(err)
		}
		settings.Set("postgresql_admin_password", "")
		_, _, executionErr := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "test")
		settings.Set("postgresql_admin_password", password)
		if executionErr == nil {
			t.Fatal("expected missing admin password")
		}
		current, _, _ := s.GetSQLViewDependencyTask(task.ID)
		if viewTaskBusy(current.Status) {
			t.Fatalf("task still running: %+v", current)
		}
		exec(t, "UPDATE sql_view_dependency_tasks SET status='RESTORING' WHERE id="+fmtUint(task.ID))
		step := model.SQLViewDependencyStep{TaskID: task.ID, Status: "RUNNING", SQLContent: "SELECT 1"}
		if err := repository.DB.Create(&step).Error; err != nil {
			t.Fatal(err)
		}
		if err := RecoverInterruptedSQLFiles(); err != nil {
			t.Fatal(err)
		}
		repository.DB.First(&step, step.ID)
		current, _, _ = s.GetSQLViewDependencyTask(task.ID)
		if step.Status != "UNKNOWN" || current.Status != "UNKNOWN" || !current.OutcomeUncertain {
			t.Fatalf("recovery mismatch: %+v %+v", step, current)
		}
	})
}

func fmtUint(id uint) string { return fmt.Sprintf("%d", id) }

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"gorm.io/gorm"
)

// Audit probes assert the observed defects; they are not regression tests.
func TestSQLAuditProof20261014(t *testing.T) {
	s := releaseSQLTestService(t)
	ctx := context.Background()
	save := func(t *testing.T, name, content string) *model.SQLChangeFile {
		t.Helper()
		f, _, err := s.ParseSQL(ParseSQLRequest{FileName: name, Content: content, Overwrite: true})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	mustExec := func(t *testing.T, text string) {
		t.Helper()
		if err := repository.DB.Exec(text).Error; err != nil {
			t.Fatal(err)
		}
	}
	value := func(t *testing.T, table string) int {
		t.Helper()
		var n int
		if err := repository.DB.Raw("SELECT value FROM sql_release." + table).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Run("rollback_reported_success", func(t *testing.T) {
		mustExec(t, "CREATE TABLE sql_release.audit_tx(value int); ALTER TABLE sql_release.audit_tx OWNER TO sql_release; INSERT INTO sql_release.audit_tx VALUES(0)")
		f := save(t, "AUDIT__1__sql_release.sql", "BEGIN; UPDATE audit_tx SET value=value+1; ROLLBACK;")
		result, rows, err := s.ExecuteSQLFile(ctx, f.ID, "audit")
		if err != nil || result.ExecuteStatus != "SUCCESS" || value(t, "audit_tx") != 0 || rows[1].ExecuteStatus != "SUCCESS" {
			t.Fatalf("unexpected evidence: result=%+v rows=%+v err=%v", result, rows, err)
		}
		t.Log("CONFIRMED: UPDATE and file recorded SUCCESS, but UPDATE was rolled back and value stayed 0")
	})
	t.Run("delete_bypasses_unknown", func(t *testing.T) {
		mustExec(t, "CREATE TABLE sql_release.audit_delete(value int); ALTER TABLE sql_release.audit_delete OWNER TO sql_release; INSERT INTO sql_release.audit_delete VALUES(0)")
		name, content := "AUDIT__2__sql_release.sql", "UPDATE audit_delete SET value=value+1;"
		f := save(t, name, content)
		callback := "audit:fail-result"
		err := repository.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			if stmt, ok := tx.Statement.Dest.(*model.SQLChangeStatement); ok && stmt.FileID == f.ID && stmt.ExecuteStatus == "SUCCESS" {
				tx.AddError(errors.New("audit injected result save failure"))
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		result, rows, executionErr := s.ExecuteSQLFile(ctx, f.ID, "audit")
		repository.DB.Callback().Update().Remove(callback)
		if executionErr != nil || result.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "UNKNOWN" || value(t, "audit_delete") != 1 {
			t.Fatalf("unexpected first execution: %+v %+v %v", result, rows, executionErr)
		}
		if _, _, err := s.ExecuteSQLFile(ctx, f.ID, "audit"); err == nil {
			t.Fatal("normal retry unexpectedly allowed")
		}
		if err := s.DeleteSQLFile(f.ID); err != nil {
			t.Fatal(err)
		}
		fresh := save(t, name, content)
		if _, _, err := s.ExecuteSQLFile(ctx, fresh.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		if value(t, "audit_delete") != 2 {
			t.Fatal("expected replay evidence")
		}
		t.Log("CONFIRMED: normal retry blocked UNKNOWN, delete + same-name reimport replayed the committed UPDATE; value 1 -> 2")
	})
	t.Run("overwrite_races_execution_start", func(t *testing.T) {
		mustExec(t, "CREATE TABLE sql_release.audit_race(value int); ALTER TABLE sql_release.audit_race OWNER TO sql_release; INSERT INTO sql_release.audit_race VALUES(0)")
		name, content := "AUDIT__3__sql_release.sql", "UPDATE audit_race SET value=value+1;"
		f := save(t, name, content)
		loaded, resume := make(chan struct{}), make(chan struct{})
		var once atomic.Bool
		callback := "audit:pause-prepare"
		if err := repository.DB.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "sql_change_statements" && once.CompareAndSwap(false, true) {
				close(loaded)
				<-resume
			}
		}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, _, err := s.StartSQLFileExecution(ctx, f.ID, "audit", SQLExecuteOptions{}); done <- err }()
		select {
		case <-loaded:
		case <-time.After(5 * time.Second):
			close(resume)
			t.Fatal("prepare did not reach barrier")
		}
		fresh, _, importErr := s.ParseSQL(ParseSQLRequest{FileName: name, Content: content, Overwrite: true})
		close(resume)
		startErr := <-done
		repository.DB.Callback().Query().Remove(callback)
		if importErr != nil || startErr != nil {
			t.Fatalf("import=%v start=%v", importErr, startErr)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			current, _, err := s.GetSQLFile(f.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.ExecuteStatus != "RUNNING" {
				if current.ExecuteStatus != "SUCCESS" {
					t.Fatal(current)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("worker timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
		var count int64
		if err := repository.DB.Model(&model.SQLChangeFile{}).Where("file_name = ? AND is_deleted = false", name).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("expected two active copies, got %d", count)
		}
		if _, _, err := s.ExecuteSQLFile(ctx, fresh.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		if value(t, "audit_race") != 2 {
			t.Fatal("expected duplicate execution")
		}
		t.Log("CONFIRMED: overwrite during execution preparation created a second active copy; stale Save resurrected old copy; both executed the same UPDATE")
	})
	t.Run("step_timeouts_ignored", func(t *testing.T) {
		mustExec(t, "CREATE TABLE sql_release.audit_timeout(value int)")
		task := model.SQLViewDependencyTask{AlterSQL: "ALTER TABLE sql_release.audit_timeout ALTER COLUMN value TYPE bigint", LockTimeout: "1s", StatementTimeout: "2s"}
		items := []model.SQLViewDependencyItem{{DropSQL: "DROP MATERIALIZED VIEW IF EXISTS sql_release.audit_timeout_settings", CreateSQL: "CREATE MATERIALIZED VIEW sql_release.audit_timeout_settings AS SELECT current_setting('lock_timeout') AS lock_timeout, current_setting('statement_timeout') AS statement_timeout"}}
		plan := BuildSQLViewDependencyStepExecutionPlan(task, items)
		db, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 902, plan); err != nil {
			t.Fatal(err)
		}
		var settings struct{ LockTimeout, StatementTimeout string }
		if err := repository.DB.Raw("SELECT * FROM sql_release.audit_timeout_settings").Scan(&settings).Error; err != nil {
			t.Fatal(err)
		}
		if settings.LockTimeout != "0" || settings.StatementTimeout != "0" {
			t.Fatalf("unexpected defaults: %+v", settings)
		}
		t.Log("CONFIRMED: STEP configured lock_timeout=1s and statement_timeout=2s, execution used database defaults 0 / 0")
	})
	t.Run("precheck_overwrites_execution_guard", func(t *testing.T) {
		mustExec(t, "CREATE TABLE sql_release.audit_guard(id int); CREATE VIEW sql_release.audit_guard_view AS SELECT id FROM sql_release.audit_guard")
		task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE sql_release.audit_guard ALTER COLUMN id TYPE bigint"}, "audit")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PrecheckSQLViewDependencyTask(ctx, task.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.markSQLViewDependencyTaskRunning(task.ID, "EXECUTING", "first worker", "audit", ensureSQLViewDependencyTaskCanExecute); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.markSQLViewDependencyTaskRunning(task.ID, "EXECUTING", "second worker", "audit", ensureSQLViewDependencyTaskCanExecute); err == nil {
			t.Fatal("expected ordinary duplicate guard")
		}
		checked, _, err := s.PrecheckSQLViewDependencyTask(ctx, task.ID, "audit")
		if err != nil || checked.Status != "PRECHECK_PASSED" {
			t.Fatalf("unexpected precheck: %+v %v", checked, err)
		}
		if _, _, err := s.markSQLViewDependencyTaskRunning(task.ID, "EXECUTING", "second worker", "audit", ensureSQLViewDependencyTaskCanExecute); err != nil {
			t.Fatal(err)
		}
		t.Log("CONFIRMED: precheck replaced EXECUTING with PRECHECK_PASSED and allowed a second execution claim")
	})
	t.Run("partial_view_drop_not_restored", func(t *testing.T) {
		mustExec(t, "CREATE VIEW sql_release.audit_view AS SELECT 1 AS id")
		db, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		plan := SQLViewDependencyStepExecutionPlan{DropSteps: []string{"DROP VIEW sql_release.audit_view", "DROP VIEW sql_release.audit_missing_view"}, AlterSteps: []string{"SELECT 1"}, RestoreSteps: []string{"CREATE VIEW sql_release.audit_view AS SELECT 1 AS id"}}
		err = s.runSQLViewDependencyStepExecutionOnDB(ctx, db, 901, plan)
		if err == nil {
			t.Fatal("expected second DROP to fail")
		}
		var stepErr SQLViewDependencyStepExecutionError
		if !errors.As(err, &stepErr) || stepErr.RestoreAttempted {
			t.Fatalf("unexpected error: %v", err)
		}
		var exists bool
		if err := repository.DB.Raw("SELECT to_regclass('sql_release.audit_view') IS NOT NULL").Scan(&exists).Error; err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("unexpected automatic restoration")
		}
		t.Log("CONFIRMED: first view DROP committed, second DROP failed, restore was never attempted and first view remained missing")
	})
}

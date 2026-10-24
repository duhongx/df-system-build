package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestViewTaskMergedExecutionAndProgress(t *testing.T) {
	s := releaseSQLTestService(t)
	ctx := context.Background()
	exec := func(t *testing.T, text string) {
		t.Helper()
		if err := repository.DB.Exec(text).Error; err != nil {
			t.Fatal(err)
		}
	}
	taskFor := func(t *testing.T, text, mode string) *model.SQLViewDependencyTask {
		t.Helper()
		task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: text, ExecutionMode: mode, LockTimeout: "5s", StatementTimeout: "10s"}, "tester")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "tester"); err != nil {
			t.Fatal(err)
		}
		if _, _, err = s.PrecheckSQLViewDependencyTask(ctx, task.ID, "tester"); err != nil {
			t.Fatal(err)
		}
		return task
	}
	waitFor := func(t *testing.T, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("task state timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	changesFor := func(t *testing.T, id uint) []model.SQLViewDependencyChange {
		t.Helper()
		v, e := s.GetSQLViewDependencyChanges(id)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	columnType := func(t *testing.T, table, column string) string {
		t.Helper()
		var v string
		if e := repository.DB.Raw("SELECT format_type(atttypid,atttypmod) FROM pg_attribute WHERE attrelid=to_regclass(?) AND attname=?", "sql_release."+table, column).Scan(&v).Error; e != nil {
			t.Fatal(e)
		}
		return v
	}

	t.Run("background_survives_disconnect_and_reports_grouped_columns", func(t *testing.T) {
		exec(t, "CREATE TABLE sql_release.merge_async(a numeric(18,4),b numeric(18,4)); INSERT INTO sql_release.merge_async VALUES(1.2345,2.3456); CREATE VIEW sql_release.merge_async_v AS SELECT a,b FROM sql_release.merge_async; ALTER VIEW sql_release.merge_async_v OWNER TO sql_release; GRANT SELECT ON sql_release.merge_async_v TO PUBLIC")
		task := taskFor(t, "ALTER TABLE sql_release.merge_async ALTER COLUMN a TYPE numeric(18,7) USING a::numeric(18,7); ALTER TABLE sql_release.merge_async ALTER COLUMN b TYPE numeric(18,5) USING b::numeric(18,5)", "STEP")
		db, err := s.getDB()
		if err != nil {
			t.Fatal(err)
		}
		lock, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		if _, err = lock.Exec("LOCK TABLE sql_release.merge_async IN SHARE MODE"); err != nil {
			t.Fatal(err)
		}
		request, cancel := context.WithCancel(ctx)
		defer cancel()
		start := time.Now()
		submitted, _, err := s.StartSQLViewDependencyTask(request, task.ID, "tester")
		if err != nil || submitted.Status != "EXECUTING" || time.Since(start) > time.Second {
			t.Fatalf("submission waited for SQL: %+v %v", submitted, err)
		}
		cancel()
		waitFor(t, func() bool {
			rows := changesFor(t, task.ID)
			return len(rows) == 2 && rows[0].Status == "RUNNING" && rows[1].Status == "RUNNING"
		})
		rows := changesFor(t, task.ID)
		if rows[0].ExecutionStepID == 0 || rows[0].ExecutionStepID != rows[1].ExecutionStepID {
			t.Fatalf("columns not linked: %+v", rows)
		}
		if _, _, err = s.StartSQLViewDependencyTask(ctx, task.ID, "tester"); err == nil {
			t.Fatal("duplicate claim accepted")
		}
		if err = lock.Commit(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			task, _, e := s.GetSQLViewDependencyTask(task.ID)
			return e == nil && !viewTaskBusy(task.Status)
		})
		result, _, _ := s.GetSQLViewDependencyTask(task.ID)
		if result.Status != "SUCCESS" {
			t.Fatalf("background result %+v", result)
		}
		steps, _ := s.GetSQLViewDependencySteps(task.ID)
		n := 0
		for _, step := range steps {
			if step.StepName == "ALTER_TABLE" {
				n++
				if !strings.Contains(step.SQLContent, ",") {
					t.Fatal("not grouped")
				}
			}
		}
		if n != 1 {
			t.Fatal("expected one actual ALTER")
		}
		rows = changesFor(t, task.ID)
		if rows[0].Status != "SUCCESS" || rows[1].Status != "SUCCESS" || !strings.Contains(rows[0].AlterSQL, "numeric(18,7)") {
			t.Fatalf("original audit lost: %+v", rows)
		}
		if columnType(t, "merge_async", "a") != "numeric(18,7)" || columnType(t, "merge_async", "b") != "numeric(18,5)" {
			t.Fatal("wrong target type")
		}
		var valid bool
		repository.DB.Raw("SELECT pg_get_userbyid(relowner)='sql_release' AND EXISTS(SELECT 1 FROM information_schema.role_table_grants WHERE table_schema='sql_release' AND table_name='merge_async_v' AND grantee='PUBLIC' AND privilege_type='SELECT') FROM pg_class WHERE oid='sql_release.merge_async_v'::regclass").Scan(&valid)
		if !valid {
			t.Fatal("view owner or grants lost")
		}
	})

	for _, mode := range []string{"STEP", "TRANSACTION"} {
		t.Run("failure_is_atomic_and_history_retained_"+mode, func(t *testing.T) {
			base := "merge_fail_" + strings.ToLower(mode)
			exec(t, fmt.Sprintf("CREATE TABLE sql_release.%s_first(a numeric(18,4),b numeric(18,4)); INSERT INTO sql_release.%s_first VALUES(1.2345,2.3456); CREATE VIEW sql_release.%s_first_v AS SELECT * FROM sql_release.%s_first; CREATE TABLE sql_release.%s_last(a numeric(18,4),b text); INSERT INTO sql_release.%s_last VALUES(3.4567,'bad'); CREATE VIEW sql_release.%s_last_v AS SELECT * FROM sql_release.%s_last", base, base, base, base, base, base, base, base))
			sql := fmt.Sprintf("ALTER TABLE sql_release.%s_first ALTER COLUMN a TYPE numeric(18,7); ALTER TABLE sql_release.%s_first ALTER COLUMN b TYPE numeric(18,7); ALTER TABLE sql_release.%s_last ALTER COLUMN a TYPE numeric(18,7); ALTER TABLE sql_release.%s_last ALTER COLUMN b TYPE integer USING b::integer;", base, base, base, base)
			task := taskFor(t, sql, mode)
			result, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "tester")
			if err == nil {
				t.Fatal("invalid cast succeeded")
			}
			if result == nil {
				t.Fatal(err)
			}
			rows := changesFor(t, task.ID)
			firstStatus, firstType := "SUCCESS", "numeric(18,7)"
			if mode == "TRANSACTION" {
				firstStatus, firstType = "ROLLED_BACK", "numeric(18,4)"
			}
			if rows[0].Status != firstStatus || rows[1].Status != firstStatus || rows[2].Status != "FAILED" || rows[3].Status != "FAILED" {
				t.Fatalf("wrong column states: %+v", rows)
			}
			if columnType(t, base+"_first", "a") != firstType || columnType(t, base+"_last", "a") != "numeric(18,4)" {
				t.Fatal("failed group partly committed")
			}
			var exists bool
			repository.DB.Raw("SELECT to_regclass(?) IS NOT NULL AND to_regclass(?) IS NOT NULL", "sql_release."+base+"_first_v", "sql_release."+base+"_last_v").Scan(&exists)
			if !exists {
				t.Fatal("views not recovered")
			}
			before, _ := s.GetSQLViewDependencySteps(task.ID)
			if _, _, e := s.RestoreSQLViewDependencyTask(ctx, task.ID, "tester"); e != nil {
				t.Fatal(e)
			}
			after, _ := s.GetSQLViewDependencySteps(task.ID)
			if len(after) <= len(before) {
				t.Fatal("recovery erased history")
			}
			for i := range before {
				if before[i].ID != after[i].ID || before[i].Status != after[i].Status {
					t.Fatal("original audit modified")
				}
			}
			if mode == "STEP" {
				if _, _, e := s.StartSQLViewDependencyTask(ctx, task.ID, "tester"); e == nil {
					t.Fatal("partial success replayed after restore")
				}
			}
		})
	}
	t.Run("merged_result_save_failure_remains_uncertain", func(t *testing.T) {
		exec(t, "CREATE TABLE sql_release.merge_unknown(a int,b int);CREATE VIEW sql_release.merge_unknown_v AS SELECT * FROM sql_release.merge_unknown")
		task := taskFor(t, "ALTER TABLE sql_release.merge_unknown ALTER COLUMN a TYPE bigint;ALTER TABLE sql_release.merge_unknown ALTER COLUMN b TYPE bigint", "STEP")
		var failed atomic.Bool
		name := "merge:result-save-failure"
		if err := repository.DB.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
			fields, ok := tx.Statement.Dest.(map[string]any)
			if !ok || tx.Statement.Table != "sql_view_dependency_steps" || fields["status"] != "SUCCESS" || failed.Load() {
				return
			}
			var running int64
			tx.Session(&gorm.Session{NewDB: true}).Model(&model.SQLViewDependencyChange{}).Where("task_id = ? AND status = ?", task.ID, "RUNNING").Count(&running)
			if running > 0 && failed.CompareAndSwap(false, true) {
				tx.AddError(errors.New("injected audit failure"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		result, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "tester")
		repository.DB.Callback().Update().Remove(name)
		if err == nil || result == nil || result.Status != "UNKNOWN" || !result.OutcomeUncertain {
			t.Fatalf("uncertainty lost: %+v %v", result, err)
		}
		rows := changesFor(t, task.ID)
		if rows[0].Status != "UNKNOWN" || rows[1].Status != "UNKNOWN" {
			t.Fatalf("column uncertainty lost: %+v", rows)
		}
		if columnType(t, "merge_unknown", "a") != "bigint" || columnType(t, "merge_unknown", "b") != "bigint" {
			t.Fatal("injection did not occur after commit")
		}
		if _, _, e := s.StartSQLViewDependencyTask(ctx, task.ID, "tester"); e == nil {
			t.Fatal("uncertain group replayed")
		}
	})
	t.Run("restart_marks_running_and_uncommitted_columns_uncertain", func(t *testing.T) {
		task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: "ALTER TABLE qa.a ALTER COLUMN id TYPE bigint;ALTER TABLE qa.b ALTER COLUMN id TYPE bigint", ExecutionMode: "TRANSACTION"}, "tester")
		if err != nil {
			t.Fatal(err)
		}
		steps, err := replaceSQLViewDependencySteps(task.ID, alterSQLStatementsForTask(*task))
		if err != nil {
			t.Fatal(err)
		}
		if err = markSQLViewDependencyStepRunning(steps[0].ID); err != nil {
			t.Fatal(err)
		}
		if err = markSQLViewDependencyStepFinished(steps[0].ID, "SUCCESS", ""); err != nil {
			t.Fatal(err)
		}
		if err = markSQLViewDependencyStepRunning(steps[1].ID); err != nil {
			t.Fatal(err)
		}
		exec(t, fmt.Sprintf("UPDATE sql_view_dependency_tasks SET status='EXECUTING' WHERE id=%d", task.ID))
		if err = RecoverInterruptedSQLFiles(); err != nil {
			t.Fatal(err)
		}
		rows := changesFor(t, task.ID)
		if rows[0].Status != "UNKNOWN" || rows[1].Status != "UNKNOWN" {
			t.Fatalf("restart progress incorrect: %+v", rows)
		}
	})

}

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

func TestSQLViewTransferPreviewKeepsOriginalTaskLinks(t *testing.T) {
	file := model.SQLChangeFile{ID: 67, SchemaName: "sql_release", FileName: "linked.sql"}
	rows := []model.SQLChangeStatement{
		{ID: 1, FileID: 67, LineNumber: 1, SQLContent: "ALTER TABLE sql_release.t ALTER COLUMN a TYPE bigint", ExecuteStatus: "FAILED", ViewDependencyTaskID: 8},
		{ID: 2, FileID: 67, LineNumber: 2, SQLContent: "ALTER TABLE sql_release.t ALTER COLUMN b TYPE bigint", ExecuteStatus: "FAILED", ViewDependencyTaskID: 8},
		{ID: 3, FileID: 67, LineNumber: 3, SQLContent: "ALTER TABLE sql_release.t ALTER COLUMN c TYPE bigint", ExecuteStatus: "FAILED"},
	}
	preview, err := buildSQLFileViewTransferPreview(file, rows, SQLFileViewTransferRequest{All: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Units) != 1 || len(preview.Excluded) != 2 {
		t.Fatalf("wrong selection: %+v", preview)
	}
	for _, excluded := range preview.Excluded {
		if len(excluded.TaskIDs) != 1 || excluded.TaskIDs[0] != 8 {
			t.Fatalf("original task is not discoverable: %+v", excluded)
		}
	}
	if rows[0].ViewDependencyTaskID != 8 {
		t.Fatal("preview detached a live task")
	}
}

func TestSQLViewTransferWorkflowAudit(t *testing.T) {
	s := releaseSQLTestService(t)
	ctx := context.Background()
	sequence := 0
	exec := func(sql string) {
		t.Helper()
		if err := repository.DB.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	fileFor := func(sql string) (*model.SQLChangeFile, []model.SQLChangeStatement) {
		t.Helper()
		sequence++
		file, rows, err := s.ParseSQL(ParseSQLRequest{FileName: fmt.Sprintf("V1__%d__sql_release.sql", sequence), SchemaName: "sql_release", Content: sql})
		if err != nil {
			t.Fatal(err)
		}
		return file, rows
	}
	transferAll := func(file *model.SQLChangeFile) *model.SQLViewDependencyTask {
		t.Helper()
		preview, err := s.PreviewSQLFileViewTransfer(file.ID, SQLFileViewTransferRequest{All: true})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]uint, len(preview.Units))
		for i := range ids {
			ids[i] = preview.Units[i].ID
		}
		task, err := s.TransferSQLFileToViewTask(file.ID, SQLFileViewTransferRequest{All: true, SelectedIDs: ids, PreviewHash: preview.PreviewHash}, "audit")
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	typeOf := func(table, column string) string {
		t.Helper()
		var value string
		if err := repository.DB.Raw("SELECT format_type(atttypid,atttypmod) FROM pg_attribute WHERE attrelid=to_regclass(?) AND attname=?", "sql_release."+table, column).Scan(&value).Error; err != nil {
			t.Fatal(err)
		}
		return value
	}
	failedSource := func(file *model.SQLChangeFile) {
		t.Helper()
		result, _, err := s.ExecuteSQLFile(ctx, file.ID, "audit")
		if err != nil || result.ExecuteStatus != "FAILED" {
			t.Fatalf("expected original view dependency failure: %+v %v", result, err)
		}
	}
	createTable := func(name, columns string) {
		t.Helper()
		exec(fmt.Sprintf("CREATE TABLE sql_release.%s(%s);ALTER TABLE sql_release.%s OWNER TO sql_release;CREATE VIEW sql_release.%s_v AS SELECT * FROM sql_release.%s;ALTER VIEW sql_release.%s_v OWNER TO sql_release", name, columns, name, name, name, name))
	}
	t.Run("linked_draft_remains_listed_and_repeated_transfer_is_idempotent", func(t *testing.T) {
		createTable("audit_draft", "amount int")
		file, rows := fileFor("ALTER TABLE sql_release.audit_draft ALTER COLUMN amount TYPE bigint;")
		failedSource(file)
		task, err := s.TransferFailedSQLToViewTask(rows[0].ID, "audit")
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.TransferFailedSQLToViewTask(rows[0].ID, "audit")
		if err != nil || again.ID != task.ID {
			t.Fatalf("duplicate transfer changed identity: %+v %v", again, err)
		}
		list, total, err := s.ListSQLViewDependencyTasks(1, 100)
		if err != nil || total != 1 || len(list) != 1 || list[0].ID != task.ID {
			t.Fatalf("linked task missing from list: %+v %d %v", list, total, err)
		}
		stored, items, err := s.GetSQLViewDependencyTask(task.ID)
		changes, _ := s.GetSQLViewDependencyChanges(task.ID)
		if err != nil || stored.AlterSQL == "" || len(items) != 0 || len(changes) != 1 || stored.AnalyzedAt != nil {
			t.Fatalf("draft differs from a missing task: %+v %v", stored, err)
		}
		preview, err := s.PreviewSQLFileViewTransfer(file.ID, SQLFileViewTransferRequest{All: true})
		if err != nil || len(preview.Units) != 0 || len(preview.Excluded) != 1 || preview.Excluded[0].TaskIDs[0] != task.ID {
			t.Fatalf("linked source was transferable again: %+v %v", preview, err)
		}
		if err := s.CancelSQLViewTaskTransfer(task.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		_, original, err := s.GetSQLFile(file.ID)
		if err != nil || original[0].ViewDependencyTaskID != 0 || original[0].ExecuteStatus != "FAILED" {
			t.Fatalf("withdrawal lost failure: %+v %v", original, err)
		}
		if next := transferAll(file); next.ID == task.ID {
			t.Fatal("withdrawn task reused")
		}
		if typeOf("audit_draft", "amount") != "integer" {
			t.Fatal("transfer itself executed business SQL")
		}
	})
	t.Run("background_execution_survives_disconnect_and_syncs_every_source", func(t *testing.T) {
		createTable("audit_success", "a numeric(18,4), b numeric(18,4)")
		exec("INSERT INTO sql_release.audit_success VALUES(1.2345,2.3456);CREATE VIEW sql_release.audit_nested AS SELECT a FROM sql_release.audit_success_v;ALTER VIEW sql_release.audit_nested OWNER TO sql_release;COMMENT ON VIEW sql_release.audit_success_v IS 'retained'")
		file, _ := fileFor("ALTER TABLE sql_release.audit_success ALTER COLUMN a TYPE numeric(18,7);ALTER TABLE sql_release.audit_success ALTER COLUMN b TYPE numeric(18,7);")
		failedSource(file)
		task := transferAll(file)
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		db, err := s.getDB(task.DatabaseTarget)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		if _, err := lock.Exec("LOCK TABLE sql_release.audit_success IN SHARE MODE"); err != nil {
			t.Fatal(err)
		}
		request, cancel := context.WithCancel(ctx)
		if _, _, err := s.StartSQLViewDependencyTask(request, task.ID, "audit"); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if err := lock.Commit(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			stored, _, err := s.GetSQLViewDependencyTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !viewTaskBusy(stored.Status) {
				if stored.Status != "SUCCESS" || stored.SourceSyncStatus != "SUCCESS" {
					t.Fatalf("background transfer failed: %+v", stored)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, rows, err := s.GetSQLFile(file.ID)
		if err != nil || len(rows) != 2 {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ExecuteStatus != "SUCCESS" || row.ViewDependencyTaskID != task.ID {
				t.Fatalf("source result not synchronized: %+v", row)
			}
		}
		var value, comment string
		repository.DB.Raw("SELECT a::text FROM sql_release.audit_nested").Scan(&value)
		repository.DB.Raw("SELECT obj_description('sql_release.audit_success_v'::regclass,'pg_class')").Scan(&comment)
		if value != "1.2345000" || comment != "retained" {
			t.Fatalf("restored view or comment changed: %s %s", value, comment)
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "audit"); err == nil {
			t.Fatal("successful task replayed")
		}
	})
	t.Run("cast_failure_rolls_back_and_can_be_withdrawn_without_losing_failures", func(t *testing.T) {
		createTable("audit_rollback", "a numeric(18,4), b text")
		exec("INSERT INTO sql_release.audit_rollback VALUES(1.2345,'bad')")
		file, _ := fileFor("ALTER TABLE sql_release.audit_rollback ALTER COLUMN a TYPE numeric(18,7);ALTER TABLE sql_release.audit_rollback ALTER COLUMN b TYPE integer USING b::integer;")
		failedSource(file)
		task := transferAll(file)
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "audit"); err == nil {
			t.Fatal("bad cast succeeded")
		}
		if typeOf("audit_rollback", "a") != "numeric(18,4)" {
			t.Fatal("failed transaction partly committed")
		}
		var count int64
		if err := repository.DB.Raw("SELECT count(*) FROM sql_release.audit_rollback_v").Scan(&count).Error; err != nil || count != 1 {
			t.Fatal("dependent view was not restored", err)
		}
		if err := s.CancelSQLViewTaskTransfer(task.ID, "audit"); err != nil {
			t.Fatal("confirmed rollback could not be withdrawn", err)
		}
		_, rows, _ := s.GetSQLFile(file.ID)
		for _, row := range rows {
			if row.ExecuteStatus != "FAILED" || row.ViewDependencyTaskID != 0 {
				t.Fatal("withdrawal reset or hid failed SQL")
			}
		}
	})
	t.Run("source_edit_is_rejected_before_any_ddl", func(t *testing.T) {
		createTable("audit_edit", "amount int")
		file, rows := fileFor("ALTER TABLE sql_release.audit_edit ALTER COLUMN amount TYPE bigint;")
		failedSource(file)
		task := transferAll(file)
		exec(fmt.Sprintf("UPDATE sql_change_statements SET sql_content='ALTER TABLE sql_release.audit_edit ALTER COLUMN amount TYPE text' WHERE id=%d", rows[0].ID))
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "audit"); err == nil || !strings.Contains(err.Error(), "源 SQL") {
			t.Fatalf("source edit was accepted: %v", err)
		}
		if typeOf("audit_edit", "amount") != "integer" {
			t.Fatal("changed source reached database")
		}
	})
	t.Run("committed_ddl_with_source_writeback_failure_cannot_be_replayed", func(t *testing.T) {
		createTable("audit_uncertain", "amount int")
		file, _ := fileFor("ALTER TABLE sql_release.audit_uncertain ALTER COLUMN amount TYPE bigint;")
		failedSource(file)
		task := transferAll(file)
		if _, _, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		var injected atomic.Bool
		const callback = "audit:source-writeback-failure"
		if err := repository.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
			fields, ok := tx.Statement.Dest.(map[string]any)
			if ok && tx.Statement.Table == "sql_change_statements" && fields["execute_status"] == "SUCCESS" && injected.CompareAndSwap(false, true) {
				tx.AddError(errors.New("injected source writeback error"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "audit")
		repository.DB.Callback().Update().Remove(callback)
		if err == nil || !injected.Load() {
			t.Fatal("writeback failure was not exercised")
		}
		stored, _, err := s.GetSQLViewDependencyTask(task.ID)
		if err != nil || stored.Status != "UNKNOWN" || !stored.OutcomeUncertain || stored.SourceSyncStatus != "WAITING" {
			t.Fatalf("uncertain result not protected: %+v %v", stored, err)
		}
		if typeOf("audit_uncertain", "amount") != "bigint" {
			t.Fatal("failure did not occur after commit")
		}
		if _, _, err := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "audit"); err == nil {
			t.Fatal("uncertain source replayed")
		}
		if err := s.CancelSQLViewTaskTransfer(task.ID, "audit"); err == nil {
			t.Fatal("uncertain source detached")
		}
	})
	t.Run("analysis_failure_preserves_the_original_error", func(t *testing.T) {
		file, _ := fileFor("ALTER TABLE sql_release.audit_missing_table ALTER COLUMN amount TYPE bigint;")
		if _, err := s.MarkSQLFileFailed(file.ID, "audit"); err != nil {
			t.Fatal(err)
		}
		task := transferAll(file)
		_, _, cause := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "audit")
		if cause == nil {
			t.Fatal("missing target unexpectedly analyzed")
		}
		stored, _, err := s.GetSQLViewDependencyTask(task.ID)
		if err != nil || stored.Status != "FAILED" || !strings.Contains(stored.ExecuteMessage, cause.Error()) {
			t.Fatalf("original error lost: %+v cause=%v read=%v", stored, cause, err)
		}
	})
}

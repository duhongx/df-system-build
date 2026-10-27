package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"strings"
	"testing"
	"time"
)

func TestReleaseSQLHistorySkipResumeAndImmutableBatch(t *testing.T) {
	s := releaseSQLTestService(t)
	a, rows := saveReleaseTestFile(t, s, "CREATE TABLE history_counter(value int); INSERT INTO history_counter VALUES(0); UPDATE history_counter SET value=value+1; INSERT INTO history_missing VALUES(1); UPDATE history_counter SET value=value+10;")
	b := saveSequenceFile(t, s, "V1__2__sql_release.sql", "UPDATE history_counter SET value=value+100;")
	first, files, err := s.prepareSQLFileSequence([]uint{b.ID, a.ID}, "first")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(first, files, "first", SQLExecuteOptions{})
	_, failed, _ := s.GetSQLFile(a.ID)
	oldError := failed[3].ExecuteMessage
	if _, err := s.SkipSQLStatement(rows[3].ID, "skip-op"); err != nil {
		t.Fatal(err)
	}
	records, total, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{StatementID: rows[3].ID})
	if err != nil || total != 2 || records[0].Action != "MANUAL_SKIP" || records[0].Submitted == nil || *records[0].Submitted || records[0].BeforeMessage != oldError || records[0].BeforeSQLState != "42P01" || records[1].Status != "FAILED" || records[1].Message != oldError || records[1].Submitted == nil || !*records[1].Submitted {
		t.Fatalf("missing original attempt or explicit skip: %+v %v", records, err)
	}
	oldBatch, oldFiles, _ := s.GetSQLBatch(first.ID)
	if oldBatch.ExecuteStatus != "FAILED" || oldFiles[0].ExecuteStatus != "FAILED" {
		t.Fatal("skip rewrote old batch")
	}
	_, liveFiles, _ := s.GetCurrentSQLBatchFiles(first.ID)
	if liveFiles[0].ExecuteStatus != "PENDING" {
		t.Fatal("retry workbench did not get current state")
	}
	retry, ready, err := s.prepareSQLFileSequence([]uint{a.ID, b.ID}, "retry")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(retry, ready, "retry", SQLExecuteOptions{})
	var value int
	repository.DB.Raw("SELECT value FROM sql_release.history_counter").Scan(&value)
	if value != 111 {
		t.Fatal("successful or skipped statements replayed", value)
	}
	_, oldFiles, _ = s.GetSQLBatch(first.ID)
	if oldFiles[0].ExecuteStatus != "FAILED" || oldFiles[1].ExecuteStatus != "CANCELED" {
		t.Fatal("retry changed old file statuses")
	}
	_, snapshot, err := s.GetSQLBatchFile(first.ID, a.ID)
	if err != nil || snapshot[3].ExecuteStatus != "FAILED" || snapshot[3].ExecuteMessage != oldError || snapshot[4].ExecuteStatus != "PENDING" {
		t.Fatalf("old SQL results changed: %+v %v", snapshot, err)
	}
	_, retrySnapshot, err := s.GetSQLBatchFile(retry.ID, a.ID)
	if err != nil || retrySnapshot[3].ExecuteStatus != "SKIPPED" || retrySnapshot[3].SkipKind != "MANUAL" || retrySnapshot[4].ExecuteStatus != "SUCCESS" || retrySnapshot[0].ResultOrigin != "PREVIOUS" || retrySnapshot[3].ResultOrigin != "PREVIOUS" || retrySnapshot[4].ResultOrigin != "CURRENT" {
		t.Fatalf("retry snapshot wrong: %+v %v", retrySnapshot, err)
	}
	var count int64
	repository.DB.Model(&model.SQLStatementRecord{}).Where("statement_id = ? AND action = ?", rows[2].ID, "EXECUTE").Count(&count)
	if count != 1 {
		t.Fatal("recorded earlier SQL as reexecuted")
	}
	if err := s.DeleteSQLFile(a.ID); err == nil {
		t.Fatal("successful file deletable")
	}
}
func TestReleaseSQLHistoryAutomaticAndFileSkip(t *testing.T) {
	s := releaseSQLTestService(t)
	file, rows := saveReleaseTestFile(t, s, "CREATE TABLE history_config(id int primary key); INSERT INTO history_config VALUES(1); INSERT INTO history_config VALUES(1); SELECT 4;")
	_, result, err := s.ExecuteSQLFileWithOptions(context.Background(), file.ID, "op", SQLExecuteOptions{SkipUniqueConstraint: true})
	if err != nil || result[2].SkipKind != "AUTO" {
		t.Fatal(err, result)
	}
	records, n, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{Action: "AUTO_SKIP", StatementID: rows[2].ID})
	if err != nil || n != 1 || records[0].Submitted == nil || !*records[0].Submitted || records[0].SQLState != "23505" {
		t.Fatalf("auto skip not distinct: %+v %v", records, err)
	}
	pending := saveSequenceFile(t, s, "V1__2__sql_release.sql", "SELECT 9; SELECT 10;")
	if _, err := s.SkipSQLFile(pending.ID, "file-op"); err != nil {
		t.Fatal(err)
	}
	records, n, err = s.ListSQLStatementRecords(1, 30, SQLRecordFilter{FileID: pending.ID, Action: "MANUAL_SKIP"})
	if err != nil || n != 2 {
		t.Fatal("file skip audit missing", n, err)
	}
	for _, r := range records {
		if r.Action != "FILE_SKIP" || r.Submitted == nil || *r.Submitted {
			t.Fatal("file skip presented as execution")
		}
	}
	_, n, _ = s.ListSQLStatementRecords(1, 30, SQLRecordFilter{FileID: pending.ID, Action: "AUTO_SKIP"})
	if n != 0 {
		t.Fatal("action filter leaked other files")
	}
}
func TestReleaseSQLHistoryLegacyBackfillAndLastSkip(t *testing.T) {
	s := releaseSQLTestService(t)
	f, rows := saveReleaseTestFile(t, s, "SELECT 1; SELECT 2;")
	repository.DB.Model(&rows[0]).Updates(map[string]any{"execute_status": "SUCCESS", "execute_message": "执行成功", "execute_user": "old-op", "execute_time": time.Now()})
	repository.DB.Model(&rows[1]).Updates(map[string]any{"execute_status": "FAILED", "execute_message": "original-error", "sql_state": "42P01"})
	repository.DB.Model(f).Update("execute_status", "FAILED")
	if err := BackfillSQLStatementHistory(); err != nil {
		t.Fatal(err)
	}
	if err := BackfillSQLStatementHistory(); err != nil {
		t.Fatal(err)
	}
	records, n, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{FileID: f.ID})
	if err != nil || n != 2 {
		t.Fatal("backfill duplicated", n, err)
	}
	for _, r := range records {
		if r.Action != "LEGACY_SNAPSHOT" || r.Submitted != nil {
			t.Fatal("legacy falsely presented as complete execution log")
		}
	}
	if _, err := s.SkipSQLStatement(rows[1].ID, "new-op"); err != nil {
		t.Fatal(err)
	}
	latest, _, _ := s.GetSQLFile(f.ID)
	if latest.ExecuteStatus != "SUCCESS" {
		t.Fatal("last skip did not finish file")
	}
	records, n, _ = s.ListSQLStatementRecords(1, 30, SQLRecordFilter{StatementID: rows[1].ID})
	if n != 2 || records[0].BeforeMessage != "original-error" || records[1].Message != "original-error" {
		t.Fatal("last skip lost original failure")
	}
}
func TestReleaseSQLBatchStopDurableAcrossFileTransition(t *testing.T) {
	s := releaseSQLTestService(t)
	a := saveSequenceFile(t, s, "V1__1__sql_release.sql", "CREATE TABLE stop_counter(value int); INSERT INTO stop_counter VALUES(0);")
	b := saveSequenceFile(t, s, "V1__2__sql_release.sql", "SELECT pg_sleep(0.5); UPDATE stop_counter SET value=value+1;")
	c := saveSequenceFile(t, s, "V1__3__sql_release.sql", "UPDATE stop_counter SET value=value+100;")
	batch, files, err := s.prepareSQLFileSequence([]uint{a.ID, b.ID, c.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.runSQLFileSequence(batch, files, "op", SQLExecuteOptions{}) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var running model.SQLChangeStatement
		repository.DB.Where("file_id = ? AND execute_status = ?", b.ID, "RUNNING").First(&running)
		if running.ID != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second file not running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Request uses batch ID; stale browser file ID is irrelevant.
	if _, err := s.CancelSQLFileSequence(batch.ID, "stop-op"); err != nil {
		t.Fatal(err)
	}
	<-done
	result, states, err := s.GetSQLBatch(batch.ID)
	if err != nil || result.ExecuteStatus != "CANCELED" || !result.StopRequested || result.StopUser != "stop-op" || states[0].ExecuteStatus != "SUCCESS" || states[1].ExecuteStatus != "CANCELED" || states[2].ExecuteStatus != "CANCELED" {
		t.Fatalf("stop did not cover queue: %+v %+v %v", result, states, err)
	}
	var value int
	repository.DB.Raw("SELECT value FROM sql_release.stop_counter").Scan(&value)
	if value != 0 {
		t.Fatal("SQL after stop executed", value)
	}
	queued := saveSequenceFile(t, s, "V1__4__sql_release.sql", "UPDATE stop_counter SET value=value+1000;")
	before, ready, err := s.prepareSQLFileSequence([]uint{queued.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelSQLFileSequence(before.ID, "stop-before-worker"); err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(before, ready, "op", SQLExecuteOptions{})
	repository.DB.Raw("SELECT value FROM sql_release.stop_counter").Scan(&value)
	if value != 0 {
		t.Fatal("persisted stop before worker ignored")
	}
}
func TestReleaseSQLManualSchemaAndNaturalOrder(t *testing.T) {
	s := releaseSQLTestService(t)
	missing, _, err := s.ParseSQL(ParseSQLRequest{FileName: "手工执行.sql", Content: "SELECT 1;"})
	if err != nil {
		t.Fatal(err)
	}
	original := ""
	configured, _, err := s.SetSQLFileExecutionConfig(context.Background(), missing.ID, SQLFileExecutionConfigRequest{SchemaName: "sql_release", OriginalSchemaName: &original}, "config-op")
	if err != nil || configured.SchemaName != "sql_release" || !configured.SchemaConfigured || configured.ImportMessage != "" {
		t.Fatalf("cannot configure uploaded file: %+v %v", configured, err)
	}
	overwritten, _, err := s.ParseSQL(ParseSQLRequest{FileName: missing.FileName, Content: "SELECT 2;", Overwrite: true})
	if err != nil || overwritten.SchemaName != "sql_release" || !overwritten.SchemaConfigured || overwritten.ImportMessage != "" {
		t.Fatalf("overwrite lost manual config: %+v %v", overwritten, err)
	}
	finished, _, err := s.ExecuteSQLFile(context.Background(), overwritten.ID, "op")
	if err != nil || finished.ExecuteStatus != "SUCCESS" {
		t.Fatal("configured file not executable", err)
	}
	old := "sql_release"
	if _, _, err := s.SetSQLFileExecutionConfig(context.Background(), overwritten.ID, SQLFileExecutionConfigRequest{SchemaName: old, OriginalSchemaName: &old}, "op"); err == nil {
		t.Fatal("executed schema change allowed")
	}
	archived, _, err := s.ParseSQL(ParseSQLRequest{FileName: "需保留进度.sql", Content: "CREATE TABLE configured_archive(id int); INSERT INTO no_such_table VALUES(1);"})
	if err != nil {
		t.Fatal(err)
	}
	blank := ""
	if _, _, err = s.SetSQLFileExecutionConfig(context.Background(), archived.ID, SQLFileExecutionConfigRequest{SchemaName: "sql_release", OriginalSchemaName: &blank}, "op"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.ExecuteSQLFile(context.Background(), archived.ID, "op"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSQLFile(archived.ID); err != nil {
		t.Fatal(err)
	}
	restored, restoredRows, err := s.ParseSQL(ParseSQLRequest{FileName: archived.FileName, Content: archived.FileContent, Overwrite: true})
	if err != nil || restored.SchemaName != "sql_release" || restoredRows[0].ExecuteStatus != "SUCCESS" {
		t.Fatalf("deleted revision lost manual config or committed progress: %+v %+v %v", restored, restoredRows, err)
	}
	var ids []uint
	for _, v := range []string{"V1", "V2", "V10"} {
		f := saveSequenceFile(t, s, v+"__5__sql_release.sql", "SELECT 1;")
		ids = append(ids, f.ID)
	}

	list, _, err := s.ListSQLFilesByStatus(1, 100, "todo")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, f := range list {
		if f.GroupSortNo == 5 {
			found = append(found, f.Version)
		}
	}
	if strings.Join(found, ",") != "V1,V2,V10" {
		t.Fatal("list order differs from execution", found)
	}
	_, ordered, err := s.prepareSQLFileSequence(ids, "op")
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{ordered[0].Version, ordered[1].Version, ordered[2].Version}
	if strings.Join(versions, ",") != "V1,V2,V10" {
		t.Fatal("not natural order", versions)
	}
}
func TestReleaseSQLHistoryMustPersistBeforeDatabaseExecution(t *testing.T) {
	s := releaseSQLTestService(t)
	f, _ := saveReleaseTestFile(t, s, "CREATE TABLE history_must_not_run(id int);")
	if err := repository.DB.Migrator().DropTable(&model.SQLStatementRecord{}); err != nil {
		t.Fatal(err)
	}
	result, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "op")
	if err != nil || result.ExecuteStatus != "FAILED" {
		t.Fatal("history write failure not reported", err)
	}
	var exists bool
	repository.DB.Raw("SELECT to_regclass('sql_release.history_must_not_run') IS NOT NULL").Scan(&exists)
	if exists {
		t.Fatal("business SQL executed without durable starting record")
	}
}

func TestReleaseSQLHistoryOverwriteKeepsExecutionProvenance(t *testing.T) {
	s := releaseSQLTestService(t)
	first, old := saveReleaseTestFile(t, s, "CREATE TABLE inherit_counter(value int); INSERT INTO inherit_counter VALUES(1);")
	if _, _, err := s.ExecuteSQLFile(context.Background(), first.ID, "first-op"); err != nil {
		t.Fatal(err)
	}
	_, next, err := s.ParseSQL(ParseSQLRequest{FileName: first.FileName, Content: first.FileContent, Overwrite: true, CreatedUser: "reupload-op"})
	if err != nil {
		t.Fatal(err)
	}
	if next[0].ExecuteStatus != "SUCCESS" || next[0].ID == old[0].ID {
		t.Fatal("result was not inherited")
	}
	records, n, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{StatementID: next[0].ID})
	if err != nil || n != 2 || records[0].Action != "INHERIT" || records[0].SourceStatementID != old[0].ID || records[0].Submitted == nil || *records[0].Submitted || records[1].Action != "EXECUTE" || records[1].Operator != "first-op" {
		t.Fatalf("cannot trace original execution: %+v %v", records, err)
	}
	if err := BackfillSQLStatementHistory(); err != nil {
		t.Fatal(err)
	}
	records, n, _ = s.ListSQLStatementRecords(1, 30, SQLRecordFilter{StatementID: next[0].ID})
	if n != 2 {
		t.Fatal("new inherited result was incorrectly backfilled as legacy")
	}
}

package service

import (
	"archive/zip"
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"errors"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func releaseSQLTestService(t *testing.T) *PostgreSQLService {
	t.Helper()
	setupSQLChangeServiceTestDB(t)
	setSQLExecutionSettingsToTestDatabase(t)
	if err := repository.DB.Exec(`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='sql_release') THEN CREATE ROLE sql_release LOGIN; END IF; END $$; CREATE SCHEMA sql_release AUTHORIZATION sql_release;`).Error; err != nil {
		t.Fatal(err)
	}
	return NewPostgreSQLService()
}
func saveReleaseTestFile(t *testing.T, s *PostgreSQLService, content string) (*model.SQLChangeFile, []model.SQLChangeStatement) {
	t.Helper()
	file, stmts, err := s.ParseSQL(ParseSQLRequest{FileName: "V1__1__sql_release.sql", Content: content, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	return file, stmts
}

func TestReleaseSQLSplitAndNaming(t *testing.T) {
	for _, name := range []string{"change.sql", "V1__x__his.sql", "V1__1__中文.sql", "../V1__1__his.sql", "V1__-1__his.sql"} {
		if _, _, _, err := parseReleaseSQLFileName(name); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	version, order, schema, err := parseReleaseSQLFileName("V2.0__012__his__说明.sql")
	if err != nil || version != "V2.0" || order != 12 || schema != "his" {
		t.Fatalf("%s %d %s %v", version, order, schema, err)
	}
	items, err := splitReleaseSQL("-- comment\nSELECT 'a;b';\n/* comment */\nSELECT 'a;b';\nDO $$ BEGIN RAISE NOTICE 'x;y'; END $$;")
	if err != nil || len(items) != 3 {
		t.Fatalf("%+v %v", items, err)
	}
	if items[0].LineNumber != 2 || items[1].LineNumber != 4 || items[2].LineNumber != 5 {
		t.Fatalf("wrong source lines: %+v", items)
	}
	if items[2].SQLContent != "DO $$ BEGIN RAISE NOTICE 'x;y'; END $$" {
		t.Fatalf("function changed: %s", items[2].SQLContent)
	}
	if _, err := splitReleaseSQL("SELECT 'unterminated"); err == nil {
		t.Fatal("unterminated string should fail splitting")
	}
}

func TestReleaseSQLStopsAndResumesUsingSchemaAccount(t *testing.T) {
	s := releaseSQLTestService(t)
	file, _ := saveReleaseTestFile(t, s, `CREATE TABLE counter AS SELECT 1 AS value, current_user::text AS account, current_schema()::text AS schema, current_setting('statement_timeout') AS timeout;
INSERT INTO missing VALUES (1);
UPDATE counter SET value=value+1;`)
	first, stmts, err := s.ExecuteSQLFileWithOptions(context.Background(), file.ID, "operator", SQLExecuteOptions{})
	if err != nil || first.ExecuteStatus != "FAILED" || stmts[0].ExecuteStatus != "SUCCESS" || stmts[1].ExecuteStatus != "FAILED" || stmts[2].ExecuteStatus != "PENDING" {
		t.Fatalf("first=%+v statements=%+v err=%v", first, stmts, err)
	}
	if stmts[1].SQLState != "42P01" || !strings.Contains(first.ExecuteMessage, "第 2 行") {
		t.Fatalf("missing detailed error: %+v", first)
	}
	var identity struct {
		Value                    int
		Account, Schema, Timeout string
	}
	repository.DB.Raw("SELECT * FROM sql_release.counter").Scan(&identity)
	if identity.Value != 1 || identity.Account != "sql_release" || identity.Schema != "sql_release" || identity.Timeout != "1min" {
		t.Fatalf("wrong execution identity or timeout: %+v", identity)
	}
	var owner string
	repository.DB.Raw("SELECT tableowner FROM pg_tables WHERE schemaname='sql_release' AND tablename='counter'").Scan(&owner)
	if owner != "sql_release" {
		t.Fatalf("owner=%s", owner)
	}
	if err := repository.DB.Exec("CREATE TABLE sql_release.missing(id int); ALTER TABLE sql_release.missing OWNER TO sql_release").Error; err != nil {
		t.Fatal(err)
	}
	second, stmts, err := s.ExecuteSQLFileWithOptions(context.Background(), file.ID, "operator2", SQLExecuteOptions{})
	if err != nil || second.ExecuteStatus != "SUCCESS" {
		t.Fatalf("%+v %v", second, err)
	}
	repository.DB.Raw("SELECT * FROM sql_release.counter").Scan(&identity)
	if identity.Value != 2 || stmts[0].ExecuteUser != "operator" || stmts[1].ExecuteUser != "operator2" {
		t.Fatalf("retry replayed previous success: %+v %+v", identity, stmts)
	}
	if _, _, err := s.ExecuteSQLFile(context.Background(), file.ID, "operator"); err == nil {
		t.Fatal("successful file executed twice")
	}
}

func TestReleaseSQLSkipOptionsAndStopByDefault(t *testing.T) {
	s := releaseSQLTestService(t)
	if err := repository.DB.Exec("CREATE TABLE sql_release.existing(id int primary key); ALTER TABLE sql_release.existing OWNER TO sql_release; INSERT INTO sql_release.existing VALUES (1)").Error; err != nil {
		t.Fatal(err)
	}
	file, _ := saveReleaseTestFile(t, s, "CREATE TABLE existing(id int);\nALTER TABLE existing ADD COLUMN id int;\nINSERT INTO existing VALUES(1);\nINSERT INTO existing VALUES(2);")
	first, stmts, err := s.ExecuteSQLFile(context.Background(), file.ID, "op")
	if err != nil || first.ExecuteStatus != "FAILED" || stmts[0].SQLState != "42P07" || stmts[1].ExecuteStatus != "PENDING" {
		t.Fatalf("%+v %+v %v", first, stmts, err)
	}
	second, stmts, err := s.ExecuteSQLFileWithOptions(context.Background(), file.ID, "op", SQLExecuteOptions{SkipExistsColumn: true, SkipExistsTable: true, SkipUniqueConstraint: true})
	if err != nil || second.ExecuteStatus != "SUCCESS" {
		t.Fatalf("%+v %v", second, err)
	}
	for i := 0; i < 3; i++ {
		if stmts[i].ExecuteStatus != "SKIPPED" {
			t.Fatalf("statement %d not skipped: %+v", i, stmts[i])
		}
	}
	if stmts[3].ExecuteStatus != "SUCCESS" {
		t.Fatal(stmts[3])
	}
}

func TestReleaseSQLOverwritePreservesSuccessByOccurrence(t *testing.T) {
	s := releaseSQLTestService(t)
	old, stmts := saveReleaseTestFile(t, s, "SELECT 1;\nSELECT 1;\nSELECT 2;")
	now := time.Now()
	stmts[0].ExecuteStatus = "SUCCESS"
	stmts[0].ExecuteUser = "first-user"
	stmts[0].ExecuteTime = &now
	if err := repository.DB.Save(&stmts[0]).Error; err != nil {
		t.Fatal(err)
	}
	next, rows := saveReleaseTestFile(t, s, "SELECT 1;\nSELECT 1;\nSELECT 1;\nSELECT 3;")
	if next.ID == old.ID || rows[0].ExecuteStatus != "SUCCESS" || rows[0].ExecuteUser != "first-user" {
		t.Fatalf("history not carried: %+v", rows)
	}
	for _, stmt := range rows[1:] {
		if stmt.ExecuteStatus != "PENDING" {
			t.Fatalf("duplicate inherited success: %+v", stmt)
		}
	}
	var saved model.SQLChangeFile
	repository.DB.First(&saved, old.ID)
	if !saved.IsDeleted {
		t.Fatal("old file should remain as deleted history")
	}
	rows[1].ExecuteStatus = "UNKNOWN"
	repository.DB.Save(&rows[1])
	if _, _, err := s.ParseSQL(ParseSQLRequest{FileName: next.FileName, Content: "SELECT 1", Overwrite: true}); err == nil {
		t.Fatal("overwrite discarded an uncertain result")
	}
}

func TestReleaseSQLResultWriteFailureDoesNotReplay(t *testing.T) {
	s := releaseSQLTestService(t)
	repository.DB.Exec("CREATE TABLE sql_release.counter(value int); ALTER TABLE sql_release.counter OWNER TO sql_release; INSERT INTO sql_release.counter VALUES(0)")
	file, stmts := saveReleaseTestFile(t, s, "UPDATE counter SET value=value+1;\nUPDATE counter SET value=value+10;")
	callback := "release:fail-result"
	repository.DB.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if stmt, ok := tx.Statement.Dest.(*model.SQLChangeStatement); ok && stmt.ID == stmts[0].ID && stmt.ExecuteStatus == "SUCCESS" {
			tx.AddError(errors.New("injected result save failure"))
		}
	})
	defer repository.DB.Callback().Update().Remove(callback)
	result, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "op")
	if err != nil || result.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "UNKNOWN" || rows[1].ExecuteStatus != "PENDING" {
		t.Fatalf("%+v %+v %v", result, rows, err)
	}
	if _, _, err := s.ExecuteSQLFile(context.Background(), file.ID, "op"); err == nil {
		t.Fatal("uncertain statement must not be replayed")
	}
	var value int
	repository.DB.Raw("SELECT value FROM sql_release.counter").Scan(&value)
	if value != 1 {
		t.Fatalf("unexpected replay/continued execution: %d", value)
	}
}

func TestReleaseSQLConfigurationFailureIsRecorded(t *testing.T) {
	s := releaseSQLTestService(t)
	file, _ := saveReleaseTestFile(t, s, "SELECT 1")
	repository.NewSettingsRepo().Set("postgresql_password", "")
	result, rows, err := s.ExecuteSQLFile(context.Background(), file.ID, "op")
	if err != nil || result.ExecuteStatus != "FAILED" || !strings.Contains(result.ExecuteMessage, "postgresql_password") || rows[0].ExecuteStatus != "PENDING" {
		t.Fatalf("%+v %+v %v", result, rows, err)
	}
}

func TestReleaseSQLBackgroundIgnoresRequestLifetime(t *testing.T) {
	s := releaseSQLTestService(t)
	file, _ := saveReleaseTestFile(t, s, "SELECT pg_sleep(0.2); SELECT 1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, _, err := s.StartSQLFileExecution(ctx, file.ID, "op", SQLExecuteOptions{})
	if err != nil || response.ExecuteStatus != "RUNNING" {
		t.Fatalf("%+v %v", response, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, _, err := s.GetSQLFile(file.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.ExecuteStatus != "RUNNING" {
			if saved.ExecuteStatus != "SUCCESS" || response.ExecuteStatus != "RUNNING" {
				t.Fatalf("worker failed or mutated response: saved=%+v response=%+v", saved, response)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background execution did not finish")
}

func TestReleaseSQLManualSkipAndRecovery(t *testing.T) {
	s := releaseSQLTestService(t)
	file, stmts := saveReleaseTestFile(t, s, "SELECT 1; SELECT 2")
	file.ExecuteStatus = "RUNNING"
	repository.DB.Save(file)
	stmts[0].ExecuteStatus = "RUNNING"
	repository.DB.Save(&stmts[0])
	if _, err := s.SkipSQLFile(file.ID, "op"); err == nil {
		t.Fatal("running file skipped")
	}
	if _, err := s.SkipSQLStatement(stmts[0].ID, "op"); err == nil {
		t.Fatal("running statement skipped")
	}
	if err := s.DeleteSQLFile(file.ID); err == nil {
		t.Fatal("running file deleted")
	}
	if err := RecoverInterruptedSQLFiles(); err != nil {
		t.Fatal(err)
	}
	recovered, rows, _ := s.GetSQLFile(file.ID)
	if recovered.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "UNKNOWN" || rows[1].ExecuteStatus != "PENDING" {
		t.Fatalf("%+v %+v", recovered, rows)
	}
	if _, err := s.SkipSQLStatement(rows[0].ID, "op"); err != nil {
		t.Fatal(err)
	}
	finished, _, err := s.ExecuteSQLFile(context.Background(), file.ID, "op")
	if err != nil || finished.ExecuteStatus != "SUCCESS" {
		t.Fatalf("%+v %v", finished, err)
	}
}

func TestReleaseSQLServerZipImportIsAtomic(t *testing.T) {
	s := releaseSQLTestService(t)
	dir, err := os.MkdirTemp("/tmp", "sql-import-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "release.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(f)
	for _, name := range []string{"V1__1__sql_release.sql", "invalid.sql"} {
		entry, _ := writer.Create(name)
		entry.Write([]byte("SELECT 1"))
	}
	writer.Close()
	f.Close()
	if _, err := s.ImportServerSQL(ImportServerSQLRequest{FilePath: path, Overwrite: true}); err == nil {
		t.Fatal("invalid SQL filename accepted")
	}
	var count int64
	repository.DB.Model(&model.SQLChangeFile{}).Count(&count)
	if count != 0 {
		t.Fatalf("partially imported archive: %d", count)
	}
	f, _ = os.Create(path)
	writer = zip.NewWriter(f)
	for _, name := range []string{"nested/V1__10__sql_release.sql", "V1__2__sql_release.sql"} {
		entry, _ := writer.Create(name)
		entry.Write([]byte("SELECT 1"))
	}
	writer.Close()
	f.Close()
	n, err := s.ImportServerSQL(ImportServerSQLRequest{FilePath: path, Overwrite: true})
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	files, total, err := s.ListSQLFilesByStatus(1, 20, "todo", SQLFileFilter{Version: "V1", SchemaName: "sql_release"})
	if err != nil || total != 2 || files[0].GroupSortNo != 2 || files[1].GroupSortNo != 10 {
		t.Fatalf("%+v %d %v", files, total, err)
	}
}

func TestReleaseSQLStopWaitsForCurrentStatement(t *testing.T) {
	s := releaseSQLTestService(t)
	file, _ := saveReleaseTestFile(t, s, "SELECT pg_sleep(0.3); SELECT 1;")
	if _, _, err := s.StartSQLFileExecution(context.Background(), file.ID, "op", SQLExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	requested := false
	for time.Now().Before(deadline) {
		current, rows, err := s.GetSQLFile(file.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].ExecuteStatus == "RUNNING" && !requested {
			if _, err := s.CancelSQLFile(file.ID, "op"); err != nil {
				t.Fatal(err)
			}
			requested = true
		}
		if current.ExecuteStatus != "RUNNING" {
			if !requested || current.ExecuteStatus != "CANCELED" || rows[0].ExecuteStatus != "SUCCESS" || rows[1].ExecuteStatus != "PENDING" {
				t.Fatalf("unexpected stop result: %+v %+v", current, rows)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stop did not finish")
}

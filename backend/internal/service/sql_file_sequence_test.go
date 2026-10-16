package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestReleaseSQLScopedSkipRules(t *testing.T) {
	opts := SQLExecuteOptions{SkipExistsColumn: true, SkipExistsTable: true, SkipUniqueConstraint: true}
	cases := []struct {
		sql, code string
		skip      bool
	}{
		{"INSERT INTO config VALUES(1)", "23505", true},
		{"WITH v AS (SELECT 1) INSERT INTO config SELECT * FROM v", "23505", true},
		{"CREATE UNIQUE INDEX ux ON config(id)", "23505", false},
		{"UPDATE config SET id=1", "23505", false},
		{"CREATE TABLE config(id int)", "42P07", true},
		{"CREATE INDEX ix ON config(id)", "42P07", true},
		{"CREATE VIEW config AS SELECT 1", "42P07", false},
		{"CREATE OR REPLACE VIEW config AS SELECT 1", "42P16", false},
		{"ALTER TABLE config ADD COLUMN id int", "42701", true},
		{"ALTER TABLE config ADD COLUMN id int, DROP COLUMN old", "42701", false},
		{"DROP VIEW config", "42P01", true},
		{"DROP VIEW config", "2BP01", false},
		{"DROP TABLE config", "42P01", false},
		{"INSERT INTO config VALUES(1)", "23503", false},
		{"DELETE FROM config", "42501", false},
	}
	for _, tc := range cases {
		t.Run(tc.sql+tc.code, func(t *testing.T) {
			got, _ := skipReleaseSQLError(tc.sql, &pgconn.PgError{Code: tc.code, Message: "中文错误信息"}, opts)
			if got != tc.skip {
				t.Fatalf("skip=%v want %v", got, tc.skip)
			}
		})
	}
	if skip, _ := skipReleaseSQLError("INSERT INTO config VALUES(1)", &pgconn.PgError{Code: "23505"}, SQLExecuteOptions{}); skip {
		t.Fatal("disabled option ignored")
	}
}

func saveSequenceFile(t *testing.T, s *PostgreSQLService, name, sql string) *model.SQLChangeFile {
	t.Helper()
	f, _, err := s.ParseSQL(ParseSQLRequest{FileName: name, Content: sql, CreatedUser: "uploader"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func TestReleaseSQLSequenceOrdersAndStops(t *testing.T) {
	s := releaseSQLTestService(t)
	last := saveSequenceFile(t, s, "V1__30__sql_release.sql", "CREATE TABLE should_not_exist(id int)")
	bad := saveSequenceFile(t, s, "V1__20__sql_release.sql", "INSERT INTO missing VALUES(1)")
	first := saveSequenceFile(t, s, "V1__10__sql_release.sql", "CREATE TABLE first_done(id int)")
	batch, files, err := s.prepareSQLFileSequence([]uint{last.ID, bad.ID, first.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	if files[0].ID != first.ID || files[1].ID != bad.ID || files[2].ID != last.ID {
		t.Fatalf("wrong ordering: %+v", files)
	}
	for _, f := range files {
		if _, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "other"); err == nil {
			t.Fatal("queue file executed outside owner")
		}
		if err := s.DeleteSQLFile(f.ID); err == nil {
			t.Fatal("queued file deleted")
		}
		if _, err := s.SkipSQLFile(f.ID, "other"); err == nil {
			t.Fatal("queued file skipped")
		}
		if _, _, err := s.ParseSQL(ParseSQLRequest{FileName: f.FileName, Content: "SELECT 1", Overwrite: true}); err == nil {
			t.Fatal("queued file overwritten")
		}
	}
	s.runSQLFileSequence(batch, files, "op", SQLExecuteOptions{})
	got, rows, err := s.GetSQLBatch(batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecuteStatus != "PARTIAL_FAILED" || rows[0].ExecuteStatus != "SUCCESS" || rows[1].ExecuteStatus != "FAILED" || rows[2].ExecuteStatus != "CANCELED" {
		t.Fatalf("%+v %+v", got, rows)
	}
	var exists bool
	repository.DB.Raw("SELECT to_regclass('sql_release.should_not_exist') IS NOT NULL").Scan(&exists)
	if exists {
		t.Fatal("later file executed after failure")
	}
	if rows[0].CreatedUser != "uploader" {
		t.Fatal("uploader lost")
	}
}
func TestReleaseSQLSequenceSkipsThenContinues(t *testing.T) {
	s := releaseSQLTestService(t)
	f := saveSequenceFile(t, s, "V1__1__sql_release.sql", "CREATE TABLE config(id int primary key); INSERT INTO config VALUES(1); INSERT INTO config VALUES(1); ALTER TABLE config ADD COLUMN id int; CREATE INDEX ix ON config(id); CREATE INDEX ix ON config(id); DROP VIEW absent_view; CREATE VIEW absent_view AS SELECT * FROM config;")
	next := saveSequenceFile(t, s, "V1__2__sql_release.sql", "INSERT INTO config VALUES(2)")
	batch, files, err := s.prepareSQLFileSequence([]uint{next.ID, f.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(batch, files, "op", SQLExecuteOptions{SkipExistsColumn: true, SkipExistsTable: true, SkipUniqueConstraint: true})
	got, rows, err := s.GetSQLBatch(batch.ID)
	if err != nil || got.ExecuteStatus != "SUCCESS" || rows[1].ExecuteStatus != "SUCCESS" {
		t.Fatalf("%+v %+v %v", got, rows, err)
	}
	_, stmts, _ := s.GetSQLFile(f.ID)
	for _, i := range []int{2, 3, 5, 6} {
		if stmts[i].ExecuteStatus != "SKIPPED" {
			t.Fatalf("not skipped: %+v", stmts[i])
		}
	}
	var count int
	repository.DB.Raw("SELECT count(*) FROM sql_release.config").Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
}
func TestReleaseSQLSequenceUniqueIndexFailureStops(t *testing.T) {
	s := releaseSQLTestService(t)
	f := saveSequenceFile(t, s, "V1__1__sql_release.sql", "CREATE TABLE duplicates(id int); INSERT INTO duplicates VALUES(1); INSERT INTO duplicates VALUES(1); CREATE UNIQUE INDEX ux ON duplicates(id); DELETE FROM duplicates;")
	batch, files, err := s.prepareSQLFileSequence([]uint{f.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(batch, files, "op", SQLExecuteOptions{SkipUniqueConstraint: true})
	_, stmts, _ := s.GetSQLFile(f.ID)
	if stmts[3].ExecuteStatus != "FAILED" || stmts[4].ExecuteStatus != "PENDING" {
		t.Fatalf("%+v", stmts)
	}
}
func TestReleaseSQLSequenceClaimIsAtomic(t *testing.T) {
	s := releaseSQLTestService(t)
	a := saveSequenceFile(t, s, "V1__1__sql_release.sql", "SELECT 1")
	b := saveSequenceFile(t, s, "V1__2__sql_release.sql", "SELECT 2")
	repository.DB.Model(b).Update("execute_status", "RUNNING")
	if _, _, err := s.prepareSQLFileSequence([]uint{a.ID, b.ID}, "op"); err == nil {
		t.Fatal("busy file accepted")
	}
	repository.DB.First(a, a.ID)
	if a.ExecuteStatus != "PENDING" {
		t.Fatal("partial queue claim")
	}
}
func TestReleaseSQLSequenceRecovery(t *testing.T) {
	s := releaseSQLTestService(t)
	f := saveSequenceFile(t, s, "V1__1__sql_release.sql", "SELECT 1")
	batch, _, err := s.prepareSQLFileSequence([]uint{f.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterruptedSQLFiles(); err != nil {
		t.Fatal(err)
	}
	got, files, err := s.GetSQLBatch(batch.ID)
	if err != nil || got.ExecuteStatus != "FAILED" || files[0].ExecuteStatus != "CANCELED" || !strings.Contains(files[0].ExecuteMessage, "未执行") {
		t.Fatalf("%+v %+v %v", got, files, err)
	}
}

func TestReleaseSQLSequenceStopAfterLastStatementDoesNotStartNextFile(t *testing.T) {
	s := releaseSQLTestService(t)
	first := saveSequenceFile(t, s, "V1__1__sql_release.sql", "SELECT pg_sleep(0.4)")
	next := saveSequenceFile(t, s, "V1__2__sql_release.sql", "CREATE TABLE must_not_start(id int)")
	batch, files, err := s.prepareSQLFileSequence([]uint{first.ID, next.ID}, "op")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); s.runSQLFileSequence(batch, files, "op", SQLExecuteOptions{}) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var stmt model.SQLChangeStatement
		repository.DB.Where("file_id = ?", first.ID).First(&stmt)
		if stmt.ExecuteStatus == "RUNNING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := s.CancelSQLFile(first.ID, "op"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not stop")
	}
	got, rows, err := s.GetSQLBatch(batch.ID)
	if err != nil || got.ExecuteStatus != "CANCELED" || rows[0].ExecuteStatus != "SUCCESS" || rows[1].ExecuteStatus != "CANCELED" {
		t.Fatalf("%+v %+v %v", got, rows, err)
	}
	var exists bool
	repository.DB.Raw("SELECT to_regclass('sql_release.must_not_start') IS NOT NULL").Scan(&exists)
	if exists {
		t.Fatal("next file executed after stop")
	}
}

func TestReleaseSQLSequenceStartsInBackgroundWithSmallResponse(t *testing.T) {
	s := releaseSQLTestService(t)
	f := saveSequenceFile(t, s, "V1__1__sql_release.sql", "CREATE TABLE background_done(id int)")
	batch, files, err := s.StartSQLFileSequence([]uint{f.ID}, "op", SQLExecuteOptions{})
	if err != nil || len(files) != 1 || files[0].FileContent != "" {
		t.Fatalf("%+v %v", files, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, rows, err := s.GetSQLBatch(batch.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ExecuteStatus != "RUNNING" {
			if got.ExecuteStatus != "SUCCESS" || rows[0].ExecuteStatus != "SUCCESS" {
				t.Fatalf("%+v %+v", got, rows)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background queue did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

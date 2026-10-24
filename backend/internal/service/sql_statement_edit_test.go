package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestReleaseSQLStatementReplacementPreservesSource(t *testing.T) {
	for _, source := range []string{
		"-- 前置说明\r\nSELECT 1;\r\n/* 第二句 */ SELECT 1;\r\n-- 后续\r\nSELECT 3; -- 结尾\r\n",
		"SELECT 1; SELECT 1; SELECT 3",
	} {
		rows, err := splitReleaseSQL(source)
		if err != nil {
			t.Fatal(err)
		}
		replacement := "DO $$\nBEGIN\n RAISE NOTICE 'x;y';\nEND $$"
		updated, positions, err := replaceReleaseSQLStatement(source, rows, 1, replacement)
		if err != nil {
			t.Fatal(err)
		}
		expected := source // first identical SQL must remain intact
		second := strings.Index(expected, "SELECT 1") + len("SELECT 1")
		pos := second + strings.Index(expected[second:], "SELECT 1")
		expected = expected[:pos] + replacement + expected[pos+len("SELECT 1"):]
		if updated != expected || positions[0].SQLContent != rows[0].SQLContent || positions[2].SQLContent != rows[2].SQLContent {
			t.Fatalf("unrelated source changed: %q", updated)
		}
		if positions[2].LineNumber != rows[2].LineNumber+3 {
			t.Fatalf("wrong subsequent line: %+v", positions)
		}
	}
}

func TestReleaseSQLStatementEditCorrectsDOAndResumes(t *testing.T) {
	s := releaseSQLTestService(t)
	source := "-- 原始说明\nCREATE TABLE edit_counter AS SELECT 1 AS value;\n" +
		"DO $$ BEGIN IF NOT EXISTS (SELECT FROM information_schema.columns WHERE table_name='wrong_table' AND column_name='value') THEN ALTER TABLE edit_counter ADD COLUMN value int; END IF; END $$;\n" +
		"UPDATE edit_counter SET value=value+1;\nSELECT 99;"
	file, rows := saveReleaseTestFile(t, s, source)
	if _, err := s.SkipSQLStatement(rows[3].ID, "skip-op"); err != nil {
		t.Fatal(err)
	}
	failed, rows, err := s.ExecuteSQLFileWithOptions(context.Background(), file.ID, "first-op", SQLExecuteOptions{SkipExistsColumn: true})
	if err != nil || failed.ExecuteStatus != "FAILED" || rows[1].SQLState != "42701" {
		t.Fatalf("expected DO failure: %+v %v", rows, err)
	}
	old := rows[1]
	replacement := "DO $$\nBEGIN\n IF NOT EXISTS (SELECT FROM information_schema.columns WHERE table_schema='sql_release' AND table_name='edit_counter' AND column_name='value') THEN\n  ALTER TABLE edit_counter ADD COLUMN value int;\n END IF;\nEND $$"
	edited, next, err := s.EditReleaseSQLStatement(old.ID, EditReleaseSQLStatementRequest{Content: replacement + ";", OriginalContent: old.SQLContent}, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if edited.ExecuteStatus != "PENDING" || edited.ExecuteMessage != "" || next[1].ExecuteStatus != "PENDING" || next[1].SQLState != "" || next[1].ExecuteTime != nil || next[2].LineNumber != rows[2].LineNumber+strings.Count(replacement, "\n") {
		t.Fatalf("edited SQL not reset/repositioned: %+v %+v", edited, next)
	}
	if next[0].ID != rows[0].ID || next[0].ExecuteUser != "first-op" || next[0].ExecuteStatus != "SUCCESS" || next[3].ExecuteStatus != "SKIPPED" || next[3].ExecuteUser != "skip-op" {
		t.Fatal("previous results lost")
	}
	if edited.FileContent != strings.Replace(source, old.SQLContent, replacement, 1) {
		t.Fatal("full file not synchronized")
	}
	var audit model.SQLStatementEdit
	if err := repository.DB.Where("statement_id = ?", old.ID).First(&audit).Error; err != nil {
		t.Fatal(err)
	}
	if audit.BeforeSQL != old.SQLContent || audit.AfterSQL != replacement || audit.BeforeStatus != "FAILED" || audit.BeforeSQLState != "42701" || audit.BeforeMessage != old.ExecuteMessage || audit.Editor != "editor" {
		t.Fatalf("audit lost: %+v", audit)
	}
	var value int
	repository.DB.Raw("SELECT value FROM sql_release.edit_counter").Scan(&value)
	if value != 1 {
		t.Fatal("save executed SQL")
	}
	done, final, err := s.ExecuteSQLFile(context.Background(), file.ID, "retry-op")
	if err != nil || done.ExecuteStatus != "SUCCESS" || final[1].ExecuteUser != "retry-op" || final[0].ExecuteUser != "first-op" {
		t.Fatalf("retry failed: %+v %v", final, err)
	}
	repository.DB.Raw("SELECT value FROM sql_release.edit_counter").Scan(&value)
	if value != 2 {
		t.Fatal("retry replayed successful SQL")
	}
}

func TestReleaseSQLStatementEditGuards(t *testing.T) {
	s := releaseSQLTestService(t)
	file, rows := saveReleaseTestFile(t, s, "SELECT 1; SELECT 2;")
	req := EditReleaseSQLStatementRequest{Content: "SELECT 3;", OriginalContent: rows[0].SQLContent}
	for _, status := range []string{"RUNNING", "QUEUED", "SUCCESS", "SKIPPED", "UNKNOWN"} {
		repository.DB.Model(file).Update("execute_status", status)
		if _, _, err := s.EditReleaseSQLStatement(rows[0].ID, req, "op"); err == nil {
			t.Fatalf("edited file %s", status)
		}
	}
	repository.DB.Model(file).Update("execute_status", "PENDING")
	for _, status := range []string{"RUNNING", "SUCCESS", "SKIPPED", "UNKNOWN"} {
		repository.DB.Model(&rows[0]).Update("execute_status", status)
		if _, _, err := s.EditReleaseSQLStatement(rows[0].ID, req, "op"); err == nil {
			t.Fatalf("edited statement %s", status)
		}
	}
	repository.DB.Model(&rows[0]).Update("execute_status", "PENDING")
	repository.DB.Model(&rows[1]).Update("execute_status", "UNKNOWN")
	if _, _, err := s.EditReleaseSQLStatement(rows[0].ID, req, "op"); err == nil {
		t.Fatal("ignored another uncertain statement")
	}
	repository.DB.Model(&rows[1]).Update("execute_status", "PENDING")
	for _, text := range []string{"", "SELECT 'unterminated", "BEGIN;", "COMMIT;", "SELECT 3 -- swallow next delimiter"} {
		bad := req
		bad.Content = text
		if _, _, err := s.EditReleaseSQLStatement(rows[0].ID, bad, "op"); err == nil {
			t.Fatalf("accepted invalid edit %q", text)
		}
	}
	stale := req
	stale.OriginalContent = "old content"
	if _, _, err := s.EditReleaseSQLStatement(rows[0].ID, stale, "op"); err == nil {
		t.Fatal("accepted stale edit")
	}
	var count int64
	repository.DB.Model(&model.SQLStatementEdit{}).Count(&count)
	current, after, _ := s.GetSQLFile(file.ID)
	if count != 0 || current.FileContent != "SELECT 1; SELECT 2;" || after[0].SQLContent != "SELECT 1" {
		t.Fatal("rejected edit changed records")
	}
	if err := s.DeleteSQLFile(file.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EditReleaseSQLStatement(rows[0].ID, req, "op"); err == nil {
		t.Fatal("edited deleted revision")
	}
}

func TestReleaseSQLStatementEditConcurrentDrafts(t *testing.T) {
	s := releaseSQLTestService(t)
	file, rows := saveReleaseTestFile(t, s, "SELECT 1;")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, text := range []string{"SELECT 2", "SELECT 3"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			<-start
			_, _, err := s.EditReleaseSQLStatement(rows[0].ID, EditReleaseSQLStatementRequest{Content: content, OriginalContent: rows[0].SQLContent}, "op")
			results <- err
		}(text)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	var count int64
	repository.DB.Model(&model.SQLStatementEdit{}).Where("file_id = ?", file.ID).Count(&count)
	if succeeded != 1 || count != 1 {
		t.Fatalf("concurrent edits overwrote each other: success=%d audits=%d", succeeded, count)
	}
}

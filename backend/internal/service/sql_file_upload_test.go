package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"strings"
	"testing"
)

func TestReleaseSQLUploadAcceptsFilesAndFiltersOnlyAtExecution(t *testing.T) {
	s := releaseSQLTestService(t)
	input := []ParseSQLBatchFile{
		{FileName: "手工执行.sql", Content: "CREATE TABLE must_not_run(id int);"},
		{FileName: "15.3_SP15_260507__18__有需要再执行小数位数相关-有视图依赖需手动执行.sql", Content: "SELECT 1;"},
		{FileName: "V1__17__sql_release 程序更新完执行.sql", Content: "SELECT 2;"},
		{FileName: "V1__2__sql_release.sql", Content: "CREATE TABLE upload_ok(id int);"},
		{FileName: "V1__3__sql_release.sql", Content: "SELECT 'unterminated"},
		{FileName: "V1__4__sql_release.sql", Content: ""},
		{FileName: "V1__5__sql_release.sql", Content: "BEGIN; SELECT 1; COMMIT;"},
	}
	_, files, err := s.ParseSQLBatch(ParseSQLBatchRequest{Files: input, CreatedUser: "uploader"})
	if err != nil || len(files) != len(input) {
		t.Fatalf("batch upload blocked: %+v %v", files, err)
	}
	byName := map[string]model.SQLChangeFile{}
	ids := []uint{}
	for _, file := range files {
		byName[file.FileName] = file
		ids = append(ids, file.ID)
	}
	for _, original := range input {
		file, _, err := s.GetSQLFile(byName[original.FileName].ID)
		if err != nil || file.FileContent != original.Content || file.CreatedUser != "uploader" || file.ExecuteStatus != "PENDING" {
			t.Fatalf("original file not retained: %+v %v", file, err)
		}
	}
	unknown := byName[input[0].FileName]
	if unknown.SchemaName != "" || unknown.ImportMessage == "" {
		t.Fatalf("missing upload issue: %+v", unknown)
	}
	if _, _, err := s.ExecuteSQLFile(context.Background(), unknown.ID, "op"); err == nil {
		t.Fatal("unknown login executed")
	}
	if _, _, err := s.prepareSQLFileSequence([]uint{unknown.ID}, "op"); err == nil {
		t.Fatal("all filtered selection started")
	}
	var afterRejected model.SQLChangeFile
	repository.DB.First(&afterRejected, unknown.ID)
	if afterRejected.ExecuteStatus != "PENDING" {
		t.Fatal("filtered file was claimed")
	}
	batch, ready, err := s.prepareSQLFileSequence(ids, "op")
	if err != nil || len(ready) != 2 || len(batch.FilteredFiles) != 5 || ready[0].FileName != input[3].FileName || batch.TotalFiles != 2 {
		t.Fatalf("wrong selection filter: %+v %+v %v", batch, ready, err)
	}
	s.runSQLFileSequence(batch, ready, "op", SQLExecuteOptions{})
	got, done, err := s.GetSQLBatch(batch.ID)
	if err != nil || got.ExecuteStatus != "SUCCESS" || len(done) != 2 || done[0].ExecuteStatus != "SUCCESS" {
		t.Fatalf("normal file blocked by invalid files: %+v %+v %v", got, done, err)
	}
	var exists bool
	repository.DB.Raw("SELECT to_regclass('sql_release.must_not_run') IS NOT NULL").Scan(&exists)
	if exists {
		t.Fatal("filtered SQL executed")
	}
	for _, filtered := range batch.FilteredFiles {
		file, _, err := s.GetSQLFile(filtered.ID)
		if err != nil || file.ExecuteStatus != "PENDING" {
			t.Fatalf("filtered file consumed: %+v %v", file, err)
		}
	}
	if _, err := s.SkipSQLFile(unknown.ID, "op"); err != nil {
		t.Fatal("cannot skip uploaded nonstandard file", err)
	}
	if err := s.DeleteSQLFile(byName[input[1].FileName].ID); err != nil {
		t.Fatal("cannot delete uploaded nonstandard file", err)
	}
}

func TestReleaseSQLUploadEmptyAndBadSyntaxRemainManageable(t *testing.T) {
	s := releaseSQLTestService(t)
	for _, tc := range []struct{ name, content, issue string }{
		{"empty.sql", "", ""},
		{"V1__1__sql_release.sql", "", "为空"},
		{"V1__2__sql_release.sql", "SELECT 'broken", "拆分"},
		{"V1__3__sql_release.sql", "BEGIN; SELECT 1;", "事务"},
	} {
		file, _, err := s.ParseSQL(ParseSQLRequest{FileName: tc.name, Content: tc.content})
		if err != nil || file == nil || file.ImportMessage == "" {
			t.Fatalf("upload blocked: %+v %v", file, err)
		}
		if !strings.Contains(file.ImportMessage, tc.issue) {
			t.Fatalf("wrong issue: %+v", file)
		}
		if _, _, err := s.ExecuteSQLFile(context.Background(), file.ID, "op"); err == nil {
			t.Fatal("unready file executed")
		}
		if err := s.DeleteSQLFile(file.ID); err != nil {
			t.Fatal(err)
		}
	}
}

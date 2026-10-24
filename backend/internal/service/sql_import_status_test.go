package service

import (
	"context"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestSQLImportInheritedFileStatuses(t *testing.T) {
	s := releaseSQLTestService(t)
	ctx := context.Background()
	exec := func(text string) {
		t.Helper()
		if err := repository.DB.Exec(text).Error; err != nil {
			t.Fatal(err)
		}
	}
	upload := func(t *testing.T, name, content string) (*model.SQLChangeFile, []model.SQLChangeStatement) {
		t.Helper()
		file, rows, err := s.ParseSQL(ParseSQLRequest{FileName: name, Content: content, Overwrite: true, CreatedUser: "uploader"})
		if err != nil {
			t.Fatal(err)
		}
		return file, rows
	}
	check := func(t *testing.T, file *model.SQLChangeFile, status string) {
		t.Helper()
		stored, _, err := s.GetSQLFile(file.ID)
		if err != nil || file.ExecuteStatus != status || stored.ExecuteStatus != status {
			t.Fatalf("returned=%+v stored=%+v expected=%s err=%v", file, stored, status, err)
		}
		done, _, err := s.ListSQLFilesByStatus(1, 100, "done")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range done {
			found = found || row.ID == file.ID
		}
		if found != (status == "SUCCESS" || status == "SKIPPED") {
			t.Fatal("file is in the wrong list", file.ID, status)
		}
	}

	t.Run("completed_reimports_keep_status_provenance_and_batch_snapshot", func(t *testing.T) {
		exec("CREATE TABLE sql_release.import_counter(value int);ALTER TABLE sql_release.import_counter OWNER TO sql_release;INSERT INTO sql_release.import_counter VALUES(0)")
		file, _ := upload(t, "IMPORT__1__sql_release.sql", "UPDATE import_counter SET value=value+1;")
		batch, files, err := s.prepareSQLFileSequence([]uint{file.ID}, "original-executor")
		if err != nil {
			t.Fatal(err)
		}
		s.runSQLFileSequence(batch, files, "original-executor", SQLExecuteOptions{})
		original, originalRows, err := s.GetSQLFile(file.ID)
		if err != nil || original.ExecuteStatus != "SUCCESS" {
			t.Fatal(original, err)
		}
		var last *model.SQLChangeFile
		for i := 0; i < 3; i++ {
			_, imported, err := s.ParseSQLBatch(ParseSQLBatchRequest{Files: []ParseSQLBatchFile{{FileName: file.FileName, Content: file.FileContent}}, Overwrite: true, CreatedUser: "reimporter"})
			if err != nil || len(imported) != 1 {
				t.Fatal(imported, err)
			}
			last = &imported[0]
			check(t, last, "SUCCESS")
			if last.ExecuteUser != original.ExecuteUser || last.ExecuteTime == nil || !last.ExecuteTime.Equal(*originalRows[0].ExecuteTime) || !strings.Contains(last.ExecuteMessage, "沿用历史结果") {
				t.Fatal("reimport fabricated execution identity or time", last)
			}
		}
		if _, _, err := s.ExecuteSQLFile(ctx, last.ID, "uploader"); err == nil {
			t.Fatal("completed reimport accepted for execution")
		}
		var value int
		repository.DB.Raw("SELECT value FROM sql_release.import_counter").Scan(&value)
		var submitted int64
		repository.DB.Model(&model.SQLStatementRecord{}).Where("action = ?", "EXECUTE").Count(&submitted)
		if value != 1 || submitted != 1 {
			t.Fatal("reimport replayed SQL", value, submitted)
		}
		historical, rows, err := s.GetSQLBatchFile(batch.ID, file.ID)
		if err != nil || historical.ExecuteStatus != "SUCCESS" || rows[0].ExecuteUser != "original-executor" || rows[0].ResultOrigin != "CURRENT" {
			t.Fatal("original batch history changed", historical, rows, err)
		}
	})

	t.Run("all_skipped_preserves_manual_skip", func(t *testing.T) {
		file, _ := upload(t, "IMPORT__2__sql_release.sql", "SELECT 2;SELECT 3;")
		if _, err := s.SkipSQLFile(file.ID, "skip-operator"); err != nil {
			t.Fatal(err)
		}
		next, rows := upload(t, file.FileName, file.FileContent)
		check(t, next, "SKIPPED")
		if next.ExecuteUser != "skip-operator" || next.ExecuteTime == nil || rows[0].SkipKind != "MANUAL" || rows[1].SkipKind != "MANUAL" {
			t.Fatal("lost manual skip provenance", next, rows)
		}
	})

	t.Run("success_with_auto_skip_is_completed", func(t *testing.T) {
		file, _ := upload(t, "IMPORT__3__sql_release.sql", "CREATE TABLE import_counter(value int);SELECT 3;")
		result, rows, err := s.ExecuteSQLFileWithOptions(ctx, file.ID, "mixed-executor", SQLExecuteOptions{SkipExistsTable: true})
		if err != nil || result.ExecuteStatus != "SUCCESS" || rows[0].SkipKind != "AUTO" {
			t.Fatal(result, rows, err)
		}
		next, rows := upload(t, file.FileName, file.FileContent)
		check(t, next, "SUCCESS")
		if rows[0].SkipKind != "AUTO" || rows[1].ExecuteStatus != "SUCCESS" {
			t.Fatal("mixed results changed", rows)
		}
	})

	t.Run("new_sql_stays_pending_and_only_new_sql_executes", func(t *testing.T) {
		file, rows := upload(t, "IMPORT__1__sql_release.sql", "UPDATE import_counter SET value=value+1;UPDATE import_counter SET value=value+10;")
		check(t, file, "PENDING")
		if rows[0].ExecuteStatus != "SUCCESS" || rows[1].ExecuteStatus != "PENDING" || file.ExecuteTime != nil || file.ExecuteUser != "" {
			t.Fatal("partial inheritance was reported as a new execution", file, rows)
		}
		result, _, err := s.ExecuteSQLFile(ctx, file.ID, "new-sql-executor")
		var value int
		repository.DB.Raw("SELECT value FROM sql_release.import_counter").Scan(&value)
		if err != nil || result.ExecuteStatus != "SUCCESS" || value != 11 {
			t.Fatal("old SQL replayed or new SQL skipped", result, value, err)
		}
	})

	t.Run("failed_sql_is_still_pending_after_reimport", func(t *testing.T) {
		file, _ := upload(t, "IMPORT__4__sql_release.sql", "SELECT 4;SELECT 1/0;")
		result, _, err := s.ExecuteSQLFile(ctx, file.ID, "failed-executor")
		if err != nil || result.ExecuteStatus != "FAILED" {
			t.Fatal(result, err)
		}
		next, rows := upload(t, file.FileName, file.FileContent)
		check(t, next, "PENDING")
		if rows[0].ExecuteStatus != "SUCCESS" || rows[1].ExecuteStatus != "PENDING" {
			t.Fatal("failed SQL was incorrectly inherited as complete", rows)
		}
	})

	t.Run("different_database_never_inherits_completed_status", func(t *testing.T) {
		original, err := s.settingsRepo.Read("postgresql_database", true)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.settingsRepo.Set("postgresql_database", "different_import_target"); err != nil {
			t.Fatal(err)
		}
		defer s.settingsRepo.Set("postgresql_database", original)
		file, rows := upload(t, "IMPORT__1__sql_release.sql", "UPDATE import_counter SET value=value+1;UPDATE import_counter SET value=value+10;")
		check(t, file, "PENDING")
		for _, row := range rows {
			if row.ExecuteStatus != "PENDING" {
				t.Fatal("reused another database's result", rows)
			}
		}
	})

	t.Run("startup_repairs_old_imports_without_rewriting_execution_history", func(t *testing.T) {
		done, _ := upload(t, "IMPORT__2__sql_release.sql", "SELECT 2;SELECT 3;")
		partial, _ := upload(t, "IMPORT__4__sql_release.sql", "SELECT 4;SELECT 1/0;")
		if err := repository.DB.Model(&model.SQLChangeFile{}).Where("id = ?", done.ID).Updates(map[string]any{"execute_status": "PENDING", "execute_message": "", "execute_user": "", "execute_time": nil}).Error; err != nil {
			t.Fatal(err)
		}
		var recordsBefore, recordsAfter int64
		repository.DB.Model(&model.SQLStatementRecord{}).Count(&recordsBefore)
		for i := 0; i < 2; i++ {
			if err := BackfillImportedSQLFileStatuses(); err != nil {
				t.Fatal(err)
			}
		}
		stored, _, err := s.GetSQLFile(done.ID)
		if err != nil || stored.ExecuteStatus != "SKIPPED" || stored.ExecuteUser != "skip-operator" {
			t.Fatal("old completed import not repaired", stored, err)
		}
		check(t, partial, "PENDING")
		repository.DB.Model(&model.SQLStatementRecord{}).Count(&recordsAfter)
		if recordsBefore != recordsAfter {
			t.Fatal("repair modified execution audit", recordsBefore, recordsAfter)
		}
	})
}

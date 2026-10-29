package service

import (
	"context"
	"df-build-server/internal/model"
	"df-build-server/internal/repository"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fTargetDatabase(t *testing.T, file *model.SQLChangeFile) string {
	t.Helper()
	var target struct{ Database string }
	if err := json.Unmarshal([]byte(file.DatabaseTarget), &target); err != nil {
		t.Fatal(err)
	}
	return target.Database
}

func TestReleaseSQLTargetUpgradePreservesProgressAndReplacesLegacyIndex(t *testing.T) {
	s := releaseSQLTestService(t)
	f, rows := saveReleaseTestFile(t, s, "SELECT 1;")
	if _, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "before-upgrade"); err != nil {
		t.Fatal(err)
	}
	// A real upgrade starts a new process: do not reuse prepared SELECT * plans
	// from the synthetic pre-upgrade schema after dropping/re-adding a column.
	pool, err := repository.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxIdleConns(0)
	if err := repository.DB.Exec(`DROP INDEX idx_sql_change_files_active_target_identity;
	ALTER TABLE sql_change_files DROP COLUMN database_target;
	CREATE UNIQUE INDEX idx_sql_change_files_active_identity ON sql_change_files(version,file_name,schema_name) WHERE is_deleted=false AND file_name<>'';`).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	if err := BackfillSQLExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	got, statements, err := s.GetSQLFile(f.ID)
	if err != nil || got.DatabaseTarget != f.DatabaseTarget || statements[0].ID != rows[0].ID || statements[0].ExecuteStatus != "SUCCESS" {
		t.Fatal("upgrade changed execution progress", err, got, statements)
	}
	_, count, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{FileID: f.ID})
	if err != nil || count != 1 {
		t.Fatal("upgrade changed original audit records", count, err)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_database", "new_upgrade_target"); err != nil {
		t.Fatal(err)
	}
	upload, pending, err := s.ParseSQL(ParseSQLRequest{FileName: f.FileName, Content: f.FileContent, Overwrite: true})
	if err != nil || upload.ID == f.ID || pending[0].ExecuteStatus != "PENDING" {
		t.Fatal("old global unique index still blocks independent target", err)
	}
}

func TestReleaseSQLResumeRestoresResetAndNeverReplaysDMLOrSkippedSet(t *testing.T) {
	s := releaseSQLTestService(t)
	if err := repository.DB.Exec(`CREATE SCHEMA restore_target AUTHORIZATION sql_release;
	CREATE TABLE sql_release.restore_config(value int); ALTER TABLE sql_release.restore_config OWNER TO sql_release;
	CREATE TABLE restore_target.restore_config(value int); ALTER TABLE restore_target.restore_config OWNER TO sql_release;`).Error; err != nil {
		t.Fatal(err)
	}
	f, rows := saveReleaseTestFile(t, s, `SET search_path TO restore_target;
INSERT INTO restore_config VALUES(1);
RESET search_path;
INSERT INTO restore_missing VALUES(1);
SET search_path TO restore_target;
INSERT INTO restore_config VALUES(2);`)
	first, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "first")
	if err != nil || first.ExecuteStatus != "FAILED" {
		t.Fatal(err, first)
	}
	for _, i := range []int{3, 4} {
		if _, err := s.SkipSQLStatement(rows[i].ID, "skip"); err != nil {
			t.Fatal(err)
		}
	}
	retry, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "retry")
	if err != nil || retry.ExecuteStatus != "SUCCESS" {
		t.Fatal(err, retry)
	}
	var a, b int64
	repository.DB.Raw("SELECT count(*) FROM restore_target.restore_config").Scan(&a)
	repository.DB.Raw("SELECT count(*) FROM sql_release.restore_config WHERE value=2").Scan(&b)
	if a != 1 || b != 1 {
		t.Fatal("SET/RESET order, DML reuse or skipped SET changed", a, b)
	}
	_, n, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{FileID: f.ID, Action: "SESSION_RESTORE"})
	if err != nil || n != 2 {
		t.Fatal("incorrect number of restored settings", n, err)
	}
}

func TestReleaseSQLNonRestorableSetIsUploadedButNotExecuted(t *testing.T) {
	s := releaseSQLTestService(t)
	for _, content := range []string{"SET search_path FROM CURRENT; SELECT 1;", "SET LOCAL search_path TO public; SELECT 1;"} {
		f, _ := saveReleaseTestFile(t, s, content)
		if f.ImportMessage == "" {
			t.Fatal("unsupported session setting not identified")
		}
		if _, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "review"); err == nil {
			t.Fatal("non-restorable SET executed")
		}
	}
}

func TestReleaseSQLQueuedFilesCannotSwitchDatabase(t *testing.T) {
	s := releaseSQLTestService(t)
	a := saveSequenceFile(t, s, "V1__1__sql_release.sql", "CREATE TABLE queue_old_target(id int);")
	b := saveSequenceFile(t, s, "V1__2__sql_release.sql", "CREATE TABLE queue_old_target_two(id int);")
	batch, files, err := s.prepareSQLFileSequence([]uint{a.ID, b.ID}, "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_database", "different_database"); err != nil {
		t.Fatal(err)
	}
	s.runSQLFileSequence(batch, files, "review", SQLExecuteOptions{})
	result, _, err := s.GetSQLBatch(batch.ID)
	if err != nil || result.ExecuteStatus != "FAILED" || !strings.Contains(result.ExecuteMessage, "目标已变化") {
		t.Fatal("queue followed new target", err, result)
	}
	var count int64
	repository.DB.Model(&model.SQLStatementRecord{}).Where("batch_id = ?", batch.ID).Count(&count)
	if count != 0 {
		t.Fatal("SQL submitted despite target mismatch", count)
	}
	var exists bool
	repository.DB.Raw("SELECT to_regclass('sql_release.queue_old_target') IS NOT NULL").Scan(&exists)
	if exists {
		t.Fatal("queue SQL unexpectedly executed")
	}
}

func TestReleaseSQLLegacyTargetMigrationIsStableAndPrefersRecordedDatabase(t *testing.T) {
	s := releaseSQLTestService(t)
	f, _ := saveReleaseTestFile(t, s, "SELECT 1;")
	if _, _, err := s.ExecuteSQLFile(context.Background(), f.ID, "review"); err != nil {
		t.Fatal(err)
	}
	// An overwritten revision has only INHERIT records. Follow its source rather
	// than attributing the inherited success to the current connection.
	inherited, _, err := s.ParseSQL(ParseSQLRequest{FileName: f.FileName, Content: f.FileContent, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	pending := saveSequenceFile(t, s, "V1__2__sql_release.sql", "SELECT 2;")
	if err := repository.DB.Model(&model.SQLChangeFile{}).Where("id IN ?", []uint{f.ID, pending.ID, inherited.ID}).Update("database_target", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_database", "upgrade_baseline"); err != nil {
		t.Fatal(err)
	}
	if err := BackfillSQLExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	var a model.SQLChangeFile
	if err := repository.DB.First(&a, f.ID).Error; err != nil {
		t.Fatal(err)
	}
	b, _, _ := s.GetSQLFile(pending.ID)
	if a.DatabaseTarget != f.DatabaseTarget || fTargetDatabase(t, b) != "upgrade_baseline" {
		t.Fatal("migration did not prefer evidence", a, b)
	}
	inheritAfter, _, err := s.GetSQLFile(inherited.ID)
	if err != nil || inheritAfter.DatabaseTarget != f.DatabaseTarget {
		t.Fatal("inherited progress attributed to wrong database", err, inheritAfter)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_database", "changed_again"); err != nil {
		t.Fatal(err)
	}
	if err := BackfillSQLExecutionTargets(); err != nil {
		t.Fatal(err)
	}
	b, _, _ = s.GetSQLFile(pending.ID)
	if fTargetDatabase(t, b) != "upgrade_baseline" {
		t.Fatal("migration rebound target")
	}
}

func TestReleaseSQLIndexValidationUsesTableSchemaAndRejectsRelationCollision(t *testing.T) {
	s := releaseSQLTestService(t)
	if err := repository.DB.Exec("CREATE SCHEMA index_target AUTHORIZATION sql_release").Error; err != nil {
		t.Fatal(err)
	}
	setup := saveSequenceFile(t, s, "V1__1__sql_release.sql", `CREATE TABLE index_target.config(id int);
CREATE INDEX valid_config_idx ON index_target.config(id);
CREATE TABLE index_target.collision_idx(id int);`)
	if f, _, err := s.ExecuteSQLFile(context.Background(), setup.ID, "review"); err != nil || f.ExecuteStatus != "SUCCESS" {
		t.Fatal(err, f)
	}
	f := saveSequenceFile(t, s, "V1__2__sql_release.sql", "CREATE INDEX valid_config_idx ON index_target.config(id);")
	done, rows, err := s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{SkipExistsTable: true})
	if err != nil || done.ExecuteStatus != "SUCCESS" || rows[0].ExecuteStatus != "SKIPPED" {
		t.Fatal("valid index in another schema not accepted", err, done, rows)
	}
	f = saveSequenceFile(t, s, "V1__3__sql_release.sql", "CREATE INDEX collision_idx ON index_target.config(id);")
	done, rows, err = s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{SkipExistsTable: true})
	if err != nil || done.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "FAILED" {
		t.Fatal("same-named table was accepted as an index", err, done, rows)
	}
}

func TestReleaseSQLInvalidConcurrentIndexIsNotSuccess(t *testing.T) {
	s := releaseSQLTestService(t)
	setup := saveSequenceFile(t, s, "V1__1__sql_release.sql", "CREATE TABLE concurrent_config(id int); INSERT INTO concurrent_config VALUES(1),(1);")
	if f, _, err := s.ExecuteSQLFileWithOptions(context.Background(), setup.ID, "review", SQLExecuteOptions{}); err != nil || f.ExecuteStatus != "SUCCESS" {
		t.Fatalf("setup: %v %+v", err, f)
	}
	f := saveSequenceFile(t, s, "V1__2__sql_release.sql", "CREATE UNIQUE INDEX CONCURRENTLY concurrent_config_idx ON concurrent_config(id);")
	first, rows, err := s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{SkipExistsTable: true})
	if err != nil || first.ExecuteStatus != "FAILED" || rows[0].SQLState != "23505" {
		t.Fatalf("first: %v %+v %+v", err, first, rows)
	}
	var valid bool
	if err := repository.DB.Raw("SELECT indisvalid FROM pg_index WHERE indexrelid='sql_release.concurrent_config_idx'::regclass").Scan(&valid).Error; err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("expected invalid index after failed concurrent creation")
	}
	retry, rows, err := s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{SkipExistsTable: true})
	t.Logf("retry file=%s statement=%s state=%s message=%s indexValid=%v err=%v", retry.ExecuteStatus, rows[0].ExecuteStatus, rows[0].SQLState, rows[0].ExecuteMessage, valid, err)
	if retry.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "FAILED" || !strings.Contains(rows[0].ExecuteMessage, "无效") {
		t.Fatal("invalid index was accepted", retry, rows)
	}
	if err := repository.DB.Raw("SELECT indisvalid FROM pg_index WHERE indexrelid='sql_release.concurrent_config_idx'::regclass").Scan(&valid).Error; err != nil || valid {
		t.Fatal("invalid index changed automatically", err)
	}
	edited, _, err := s.EditReleaseSQLStatement(rows[0].ID, EditReleaseSQLStatementRequest{Content: "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS concurrent_config_idx ON concurrent_config(id)", OriginalContent: rows[0].SQLContent}, "review")
	if err != nil {
		t.Fatal(err)
	}
	retry, rows, err = s.ExecuteSQLFileWithOptions(context.Background(), edited.ID, "review", SQLExecuteOptions{SkipExistsTable: true})
	if err != nil || retry.ExecuteStatus != "FAILED" || rows[0].ExecuteStatus != "FAILED" {
		t.Fatal("IF NOT EXISTS hid invalid index", err, retry, rows)
	}
}

func TestReleaseSQLResumePreservesSessionSearchPath(t *testing.T) {
	s := releaseSQLTestService(t)
	if err := repository.DB.Exec("CREATE SCHEMA resume_target AUTHORIZATION sql_release; CREATE TABLE sql_release.session_config(value int); CREATE TABLE resume_target.session_config(value int); ALTER TABLE sql_release.session_config OWNER TO sql_release; ALTER TABLE resume_target.session_config OWNER TO sql_release;").Error; err != nil {
		t.Fatal(err)
	}
	f, rows := saveReleaseTestFile(t, s, "SET search_path TO resume_target; INSERT INTO session_missing VALUES(1); INSERT INTO session_config VALUES(7);")
	first, _, err := s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{})
	if err != nil || first.ExecuteStatus != "FAILED" {
		t.Fatalf("first: %v %+v", err, first)
	}
	if _, err := s.SkipSQLStatement(rows[1].ID, "review"); err != nil {
		t.Fatal(err)
	}
	retry, _, err := s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{})
	var expected, wrong int64
	repository.DB.Raw("SELECT count(*) FROM resume_target.session_config").Scan(&expected)
	repository.DB.Raw("SELECT count(*) FROM sql_release.session_config").Scan(&wrong)
	t.Logf("retry file=%s expectedSchemaRows=%d wrongSchemaRows=%d err=%v", retry.ExecuteStatus, expected, wrong, err)
	if err != nil || retry.ExecuteStatus != "SUCCESS" || expected != 1 || wrong != 0 {
		t.Fatal("session configuration not restored", err)
	}
	records, _, err := s.ListSQLStatementRecords(1, 30, SQLRecordFilter{StatementID: rows[0].ID})
	if err != nil || len(records) != 2 || records[0].Action != "SESSION_RESTORE" || records[0].Status != "SUCCESS" {
		t.Fatal("session restoration not auditable", err, records)
	}
}

func TestReleaseSQLOverwriteDoesNotReuseDifferentDatabase(t *testing.T) {
	s := releaseSQLTestService(t)
	f, _ := saveReleaseTestFile(t, s, "CREATE TABLE database_marker(id int);")
	first, _, err := s.ExecuteSQLFileWithOptions(context.Background(), f.ID, "review", SQLExecuteOptions{})
	if err != nil || first.ExecuteStatus != "SUCCESS" {
		t.Fatalf("first: %v %+v", err, first)
	}
	cfg, _, err := s.loadSQLExecutionConfig()
	if err != nil {
		t.Fatal(err)
	}
	other := fmt.Sprintf("dfops_other_%d", time.Now().UnixNano())
	if err := repository.DB.Exec("CREATE DATABASE " + quoteIdent(other)).Error; err != nil {
		t.Fatal(err)
	}
	defer repository.DB.Exec("DROP DATABASE " + quoteIdent(other) + " WITH (FORCE)")
	cfg.Database = other
	target, err := openReleaseSQLDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := target.Exec("CREATE SCHEMA sql_release AUTHORIZATION sql_release"); err != nil {
		t.Fatal(err)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_database", cfg.Database); err != nil {
		t.Fatal(err)
	}
	uploaded, rows, err := s.ParseSQL(ParseSQLRequest{FileName: f.FileName, Content: f.FileContent, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	retry, _, err := s.ExecuteSQLFileWithOptions(context.Background(), uploaded.ID, "review", SQLExecuteOptions{})
	var exists bool
	if err := target.QueryRow("SELECT to_regclass('sql_release.database_marker') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	states := make([]string, len(rows))
	for i, r := range rows {
		states[i] = r.ExecuteStatus
	}
	t.Logf("new target file=%s inherited=%s tableExists=%v err=%v", retry.ExecuteStatus, strings.Join(states, ","), exists, err)
	if err != nil || retry.ExecuteStatus != "SUCCESS" || !exists || rows[0].ExecuteStatus != "PENDING" {
		t.Fatal("cross database progress was reused", err)
	}
	files, n, err := s.ListSQLFilesByStatus(1, 100, "done")
	if err != nil || n != 1 || files[0].ID != uploaded.ID {
		t.Fatal("current database list leaked other target", err, files)
	}
	if err := repository.NewSettingsRepo().Set("postgresql_database", fTargetDatabase(t, f)); err != nil {
		t.Fatal(err)
	}
	restored, restoredRows, err := s.ParseSQL(ParseSQLRequest{FileName: f.FileName, Content: f.FileContent, Overwrite: true})
	if err != nil || restoredRows[0].ExecuteStatus != "SUCCESS" || restored.DatabaseTarget != f.DatabaseTarget {
		t.Fatal("switching back lost original progress", err)
	}
}

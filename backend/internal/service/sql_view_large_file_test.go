package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

// Run only against the explicitly configured, disposable integration database.
// Mirrors the reported 879-statement source, including an existing 100-row task.
func TestSQLViewLargeFileTransfer(t *testing.T) {
	svc := releaseSQLTestService(t)
	const count = 879
	var columns, statements []string
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("c%04d", i)
		columns = append(columns, name+" numeric(18,4)")
		statements = append(statements, "ALTER TABLE sql_release.bulk_columns ALTER COLUMN "+name+" TYPE numeric(18,7);")
	}
	ddl := "CREATE TABLE sql_release.bulk_columns (" + strings.Join(columns, ",") + ");" +
		"ALTER TABLE sql_release.bulk_columns OWNER TO sql_release;" +
		"INSERT INTO sql_release.bulk_columns(c0000) VALUES(12.3456);" +
		"CREATE VIEW sql_release.bulk_view AS SELECT * FROM sql_release.bulk_columns;" +
		"ALTER VIEW sql_release.bulk_view OWNER TO sql_release;" +
		"GRANT SELECT ON sql_release.bulk_view TO PUBLIC;" +
		"CREATE VIEW sql_release.bulk_nested AS SELECT c0000 FROM sql_release.bulk_view;" +
		"ALTER VIEW sql_release.bulk_nested OWNER TO sql_release;"
	if err := repository.DB.Exec(ddl).Error; err != nil {
		t.Fatal(err)
	}
	file, rows := saveReleaseTestFile(t, svc, strings.Join(statements, "\n"))
	if len(rows) != count {
		t.Fatalf("parsed %d statements, want %d", len(rows), count)
	}
	if _, err := svc.MarkSQLFileFailed(file.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	previewFor := func() *SQLFileViewTransferPreview {
		t.Helper()
		preview, err := svc.PreviewSQLFileViewTransfer(file.ID, SQLFileViewTransferRequest{All: true})
		if err != nil {
			t.Fatal(err)
		}
		return preview
	}
	transfer := func(preview *SQLFileViewTransferPreview, n int) *model.SQLViewDependencyTask {
		t.Helper()
		ids := make([]uint, n)
		for i := range ids {
			ids[i] = preview.Units[i].ID
		}
		task, err := svc.TransferSQLFileToViewTask(file.ID, SQLFileViewTransferRequest{All: true, SelectedIDs: ids, PreviewHash: preview.PreviewHash}, "tester")
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	preview := previewFor()
	if len(preview.Units) != count || len(preview.Excluded) != 0 {
		t.Fatalf("whole-file preview was truncated: eligible=%d excluded=%d", len(preview.Units), len(preview.Excluded))
	}
	first := transfer(preview, 100)
	remaining := previewFor()
	if len(remaining.Units) != 779 || len(remaining.Excluded) != 100 {
		t.Fatalf("linked statements not excluded: eligible=%d excluded=%d", len(remaining.Units), len(remaining.Excluded))
	}
	if err := svc.CancelSQLViewTaskTransfer(first.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	preview = previewFor()
	if len(preview.Units) != count {
		t.Fatalf("explicitly withdrawn statements were not available: %d", len(preview.Units))
	}
	task := transfer(preview, count)
	changes, err := svc.GetSQLViewDependencyChanges(task.ID)
	if err != nil || len(changes) != count {
		t.Fatalf("not all changes reached one task: %d, %v", len(changes), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	analyze := func() []model.SQLViewDependencyItem {
		t.Helper()
		start := time.Now()
		_, items, err := svc.AnalyzeSQLViewDependencyTask(ctx, task.ID, "tester")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("analyzed %d changes, %d unique views in %s", count, len(items), time.Since(start))
		if len(items) != 2 || items[0].Depth != 2 || items[1].Depth != 1 {
			t.Fatalf("shared views or dependency depth lost: %+v", items)
		}
		return items
	}
	analyze()
	db, err := svc.getDB(task.DatabaseTarget)
	if err != nil {
		t.Fatal(err)
	}
	missing := append([]model.SQLViewDependencyChange(nil), changes...)
	missing[len(missing)-1].ColumnName = "missing_target"
	if err := validateSQLViewDependencyColumns(ctx, db, missing); err == nil || !strings.Contains(err.Error(), "missing_target") {
		t.Fatalf("missing target not rejected: %v", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if err := validateSQLViewDependencyColumns(canceled, db, changes); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
	if err := repository.DB.Exec("COMMENT ON VIEW sql_release.bulk_view IS 'changed after analysis'").Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.PrecheckSQLViewDependencyTask(ctx, task.ID, "tester"); err == nil || !strings.Contains(err.Error(), "已变化") {
		t.Fatalf("metadata cache hid a later view change: %v", err)
	}
	analyze() // Each analysis and precheck must read fresh metadata.
	if _, _, err := svc.PrecheckSQLViewDependencyTask(ctx, task.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	result, _, err := svc.ExecuteSQLViewDependencyTask(ctx, task.ID, "tester")
	if err != nil || result == nil || result.Status != "SUCCESS" {
		t.Fatalf("whole-file execution failed: %+v %v", result, err)
	}
	changes, err = svc.GetSQLViewDependencyChanges(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range changes {
		if change.Status != "SUCCESS" {
			t.Fatalf("change %d did not succeed: %s", change.ChangeOrder, change.Status)
		}
	}
	var updated, linkedSuccess int64
	repository.DB.Raw("SELECT count(*) FROM pg_attribute WHERE attrelid='sql_release.bulk_columns'::regclass AND attnum>0 AND NOT attisdropped AND format_type(atttypid,atttypmod)='numeric(18,7)'").Scan(&updated)
	repository.DB.Model(&model.SQLChangeStatement{}).Where("file_id=? AND view_dependency_task_id=? AND execute_status='SUCCESS'", file.ID, task.ID).Count(&linkedSuccess)
	if updated != count || linkedSuccess != count {
		t.Fatalf("incomplete batch: columns=%d source successes=%d", updated, linkedSuccess)
	}
	var comment string
	repository.DB.Raw("SELECT obj_description('sql_release.bulk_view'::regclass, 'pg_class')").Scan(&comment)
	if comment != "changed after analysis" {
		t.Fatalf("view comment not restored: %q", comment)
	}
	var value string
	if err := repository.DB.Raw("SELECT c0000::text FROM sql_release.bulk_nested").Scan(&value).Error; err != nil || value != "12.3456000" {
		t.Fatalf("nested view/data not restored: %q %v", value, err)
	}
}

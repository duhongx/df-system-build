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

// The previous 879-column fixture had only two views. Exercise the reported
// hundreds of dependent views, including rollback and durable error evidence.
func TestSQLView879Changes220Dependencies(t *testing.T) {
	s := releaseSQLTestService(t)
	const columns, views = 879, 220
	var definitions, alterations []string
	for i := 0; i < columns-1; i++ {
		name := fmt.Sprintf("c%04d", i)
		definitions = append(definitions, name+" numeric(18,4)")
		alterations = append(alterations, "ALTER TABLE sql_release.mass_columns ALTER COLUMN "+name+" TYPE numeric(18,7);")
	}
	definitions = append(definitions, "bad text")
	alterations = append(alterations, "ALTER TABLE sql_release.mass_columns ALTER COLUMN bad TYPE integer USING bad::integer;")
	var ddl strings.Builder
	fmt.Fprintf(&ddl, "CREATE TABLE sql_release.mass_columns (%s); ALTER TABLE sql_release.mass_columns OWNER TO sql_release; INSERT INTO sql_release.mass_columns(bad) VALUES ('bad_integer'); CREATE VIEW sql_release.mass_base AS SELECT * FROM sql_release.mass_columns; ALTER VIEW sql_release.mass_base OWNER TO sql_release;", strings.Join(definitions, ","))
	for i := 0; i < views-1; i++ {
		fmt.Fprintf(&ddl, "CREATE VIEW sql_release.mass_%03d AS SELECT c0000 FROM sql_release.mass_base; ALTER VIEW sql_release.mass_%03d OWNER TO sql_release; COMMENT ON VIEW sql_release.mass_%03d IS 'preserved %d'; GRANT SELECT ON sql_release.mass_%03d TO PUBLIC;", i, i, i, i, i)
	}
	if err := repository.DB.Exec(ddl.String()).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	taskFor := func() *model.SQLViewDependencyTask {
		t.Helper()
		task, err := s.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{AlterSQL: strings.Join(alterations, "\n"), ExecutionMode: SQLViewDependencyExecutionModeTransaction, LockTimeout: "3s", StatementTimeout: "1min"}, "mass-test")
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	task := taskFor()
	db, err := s.getDB(task.DatabaseTarget)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate small per-view overhead across 220 views. The graph query must
	// stay bounded, while a whole inspection may legitimately exceed ten seconds.
	start := time.Now()
	snapshots, err := s.loadViewDependencySnapshotsCached(ctx, db, "sql_release", "mass_columns", "c0000", nil, func(message string) error {
		if strings.HasPrefix(message, "核对依赖对象") {
			time.Sleep(50 * time.Millisecond)
		}
		return nil
	})
	if err != nil || len(snapshots) != views {
		t.Fatalf("many-view metadata budget failed after %s: %d views %v", time.Since(start), len(snapshots), err)
	}
	t.Logf("backed up %d views with per-view latency in %s", views, time.Since(start))
	analyze := func(task *model.SQLViewDependencyTask) {
		t.Helper()
		start := time.Now()
		_, items, err := s.AnalyzeSQLViewDependencyTask(ctx, task.ID, "mass-test")
		if err != nil || len(items) != views {
			t.Fatalf("bulk analysis: %d %v", len(items), err)
		}
		t.Logf("analyzed %d SQL / %d views in %s", columns, views, time.Since(start))
	}
	analyze(task)
	failedTask, _, execErr := s.ExecuteSQLViewDependencyTask(ctx, task.ID, "mass-test")
	if execErr == nil || failedTask == nil || failedTask.Status != "FAILED" || !strings.Contains(failedTask.ExecuteMessage, "22P02") {
		t.Fatalf("original failure lost: %+v %v", failedTask, execErr)
	}
	steps, err := s.GetSQLViewDependencySteps(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	failedID := uint(0)
	rolledBack := false
	for _, step := range steps {
		if step.Status == "FAILED" {
			failedID = step.ID
			if !strings.Contains(step.SQLContent, "bad") || !strings.Contains(step.ErrorMessage, "SQLSTATE: 22P02") {
				t.Fatalf("failed SQL details not retained: %+v", step)
			}
		}
		if step.StepName == "ROLLBACK" && step.Status == "SUCCESS" {
			rolledBack = true
		}
	}
	if failedID == 0 || !rolledBack {
		t.Fatalf("missing original failure or rollback log: failed=%d rollback=%t", failedID, rolledBack)
	}
	var restored int64
	if err := repository.DB.Raw("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='sql_release' AND c.relkind='v' AND c.relname LIKE 'mass_%'").Scan(&restored).Error; err != nil || restored != views {
		t.Fatalf("rollback did not restore all %d views: %d %v", views, restored, err)
	}
	var unchanged int64
	if err := repository.DB.Raw("SELECT count(*) FROM pg_attribute WHERE attrelid='sql_release.mass_columns'::regclass AND format_type(atttypid,atttypmod)='numeric(18,4)'").Scan(&unchanged).Error; err != nil || unchanged != columns-1 {
		t.Fatalf("rolled-back field changes persisted: %d %v", unchanged, err)
	}
	if err := repository.DB.Exec("UPDATE sql_release.mass_columns SET bad='123'").Error; err != nil {
		t.Fatal(err)
	}
	// A separate corrected attempt leaves the original failed task's audit intact.
	next := taskFor()
	analyze(next)
	completed, _, err := s.ExecuteSQLViewDependencyTask(ctx, next.ID, "mass-test")
	if err != nil || completed.Status != "SUCCESS" {
		t.Fatalf("bulk execution failed: %+v %v", completed, err)
	}
	var original model.SQLViewDependencyStep
	if err := repository.DB.First(&original, failedID).Error; err != nil || !strings.Contains(original.ErrorMessage, "22P02") {
		t.Fatalf("original audit overwritten: %+v %v", original, err)
	}
	var value int
	if err := repository.DB.Raw("SELECT bad FROM sql_release.mass_base").Scan(&value).Error; err != nil || value != 123 {
		t.Fatalf("restored view not usable: %d %v", value, err)
	}
	var comment string
	if err := repository.DB.Raw("SELECT obj_description('sql_release.mass_218'::regclass, 'pg_class')").Scan(&comment).Error; err != nil || comment != "preserved 218" {
		t.Fatalf("view comment lost: %q %v", comment, err)
	}
	var granted bool
	if err := repository.DB.Raw("SELECT EXISTS(SELECT 1 FROM pg_class c CROSS JOIN LATERAL aclexplode(c.relacl) a WHERE c.oid='sql_release.mass_218'::regclass AND a.grantee=0 AND a.privilege_type='SELECT')").Scan(&granted).Error; err != nil || !granted {
		t.Fatalf("public view privilege lost: %t %v", granted, err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := s.loadViewDependencySnapshotsCached(canceled, db, "sql_release", "mass_columns", "c0000", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation lost: %v", err)
	}
}

func TestSQLViewBatchGraphMatchesColumnUnion(t *testing.T) {
	s := releaseSQLTestService(t)
	ddl := `CREATE TABLE sql_release.graph_a(id int, changed int);
CREATE TABLE sql_release.graph_b(id int, changed int);
CREATE VIEW sql_release.graph_first AS SELECT changed FROM sql_release.graph_a;
CREATE VIEW sql_release.graph_shared AS SELECT a.changed AS x, b.changed AS y FROM sql_release.graph_first a CROSS JOIN sql_release.graph_b b;
CREATE VIEW sql_release.graph_nested AS SELECT * FROM sql_release.graph_shared;
CREATE VIEW sql_release.graph_unselected AS SELECT id FROM sql_release.graph_a;`
	if err := repository.DB.Exec(ddl).Error; err != nil {
		t.Fatal(err)
	}
	db, err := s.getDB("")
	if err != nil {
		t.Fatal(err)
	}
	changes := []model.SQLViewDependencyChange{{SchemaName: "sql_release", TableName: "graph_a", ColumnName: "changed"}, {SchemaName: "sql_release", TableName: "graph_b", ColumnName: "changed"}}
	ctx := context.Background()
	var single []viewDependencySnapshot
	for _, change := range changes {
		items, err := s.loadViewDependencySnapshots(ctx, db, change.SchemaName, change.TableName, change.ColumnName)
		if err != nil {
			t.Fatal(err)
		}
		single = append(single, items...)
	}
	want := mergeViewDependencySnapshots(single)
	got, err := s.loadViewDependencySnapshotsForChanges(ctx, db, changes)
	if err != nil || len(got) != 3 || len(want) != 3 {
		t.Fatalf("batch graph scope changed: %d %d %v", len(got), len(want), err)
	}
	for i, item := range got {
		if item.Name != want[i].Name || item.Depth != want[i].Depth || item.DropOrder != want[i].DropOrder || item.RestoreOrder != want[i].RestoreOrder || item.Definition != want[i].Definition {
			t.Fatalf("batch changed dependency ordering or definition: got=%+v want=%+v", item, want[i])
		}
	}
}

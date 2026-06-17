package service

import (
	"errors"
	"strings"
	"testing"

	"df-build-server/internal/model"
	"df-build-server/internal/repository"
)

func TestValidateViewDependencyAlterSQLAcceptsExplicitTargetColumn(t *testing.T) {
	req := SQLViewDependencyTaskRequest{
		SchemaName: "his",
		TableName:  "patient",
		ColumnName: "code",
		AlterSQL:   `ALTER TABLE "his"."patient" ALTER COLUMN "code" TYPE varchar(64);`,
	}

	ref, err := validateViewDependencyAlterSQL(req)
	if err != nil {
		t.Fatalf("expected valid ALTER COLUMN SQL, got %v", err)
	}
	if ref.schema != "his" || ref.table != "patient" || ref.column != "code" {
		t.Fatalf("unexpected target ref: %+v", ref)
	}
}

func TestValidateViewDependencyAlterSQLRejectsUnsafeSQL(t *testing.T) {
	tests := []struct {
		name string
		req  SQLViewDependencyTaskRequest
	}{
		{
			name: "multi statement",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE his.patient ALTER COLUMN code TYPE text; DROP TABLE his.x;"},
		},
		{
			name: "missing schema",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE patient ALTER COLUMN code TYPE text;"},
		},
		{
			name: "wrong table",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE his.visit ALTER COLUMN code TYPE text;"},
		},
		{
			name: "wrong column",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE his.patient ALTER COLUMN name TYPE text;"},
		},
		{
			name: "not alter column type",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE his.patient ADD COLUMN memo text;"},
		},
		{
			name: "compound alter drops another column",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE his.patient ALTER COLUMN code TYPE text, DROP COLUMN name;"},
		},
		{
			name: "compound alter changes another column",
			req:  SQLViewDependencyTaskRequest{SchemaName: "his", TableName: "patient", ColumnName: "code", AlterSQL: "ALTER TABLE his.patient ALTER COLUMN code TYPE text, ALTER COLUMN name TYPE text;"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateViewDependencyAlterSQL(tc.req); err == nil {
				t.Fatalf("expected unsafe SQL to be rejected")
			}
		})
	}
}

func TestCreateSQLViewDependencyTaskPersistsValidatedTask(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()

	task, err := svc.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{
		SchemaName: "his",
		TableName:  "patient",
		ColumnName: "code",
		AlterSQL:   "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);",
	}, "tester")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.ID == 0 || task.Status != "CREATED" || task.Operator != "tester" {
		t.Fatalf("unexpected task: %+v", task)
	}
	if task.ExecutionMode != "STEP" {
		t.Fatalf("expected new tasks to default to STEP execution, got %q", task.ExecutionMode)
	}

	var got model.SQLViewDependencyTask
	if err := repository.DB.First(&got, task.ID).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if got.SchemaName != "his" || got.TableName != "patient" || got.ColumnName != "code" {
		t.Fatalf("unexpected persisted task: %+v", got)
	}
}

func TestCreateSQLViewDependencyTaskAcceptsTransactionExecutionMode(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()

	task, err := svc.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{
		SchemaName:    "his",
		TableName:     "patient",
		ColumnName:    "code",
		AlterSQL:      "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);",
		ExecutionMode: "TRANSACTION",
	}, "tester")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.ExecutionMode != "TRANSACTION" {
		t.Fatalf("expected TRANSACTION execution mode, got %q", task.ExecutionMode)
	}
}

func TestExportSQLViewDependencyPlanRequiresAnalyzedItems(t *testing.T) {
	setupSQLChangeServiceTestDB(t)
	svc := NewPostgreSQLService()

	task, err := svc.CreateSQLViewDependencyTask(SQLViewDependencyTaskRequest{
		SchemaName: "his",
		TableName:  "patient",
		ColumnName: "code",
		AlterSQL:   "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);",
	}, "tester")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	if _, err := svc.ExportSQLViewDependencyPlan(task.ID); err == nil {
		t.Fatalf("expected export to require dependency analysis")
	}
}

func TestBuildViewDependencyManualPlanOrdersDropAlterRestoreVerify(t *testing.T) {
	task := model.SQLViewDependencyTask{
		SchemaName: "his",
		TableName:  "patient",
		ColumnName: "code",
		AlterSQL:   "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);",
	}
	items := []model.SQLViewDependencyItem{
		{
			ObjectSchema:       "his",
			ObjectName:         "v_patient_root",
			ObjectKind:         "v",
			DropOrder:          2,
			RestoreOrder:       1,
			DropSQL:            `DROP VIEW IF EXISTS "his"."v_patient_root";`,
			CreateSQL:          `CREATE OR REPLACE VIEW "his"."v_patient_root" AS SELECT code FROM his.patient;`,
			RestoreOwnerSQL:    `ALTER VIEW "his"."v_patient_root" OWNER TO "his_owner";`,
			RestoreGrantsSQL:   `GRANT SELECT ON "his"."v_patient_root" TO "app";`,
			RestoreCommentsSQL: `COMMENT ON VIEW "his"."v_patient_root" IS 'root';`,
			RestoreIndexesSQL:  `CREATE INDEX idx_v_patient_root_code ON "his"."v_patient_root"(code);`,
			VerifySQL:          `SELECT to_regclass('"his"."v_patient_root"');`,
		},
		{
			ObjectSchema: "his",
			ObjectName:   "v_patient_child",
			ObjectKind:   "v",
			DropOrder:    1,
			RestoreOrder: 2,
			DropSQL:      `DROP VIEW IF EXISTS "his"."v_patient_child";`,
			CreateSQL:    `CREATE OR REPLACE VIEW "his"."v_patient_child" AS SELECT code FROM his.v_patient_root;`,
			VerifySQL:    `SELECT to_regclass('"his"."v_patient_child"');`,
		},
	}

	plan := BuildSQLViewDependencyManualPlan(task, items)

	childDrop := strings.Index(plan, `DROP VIEW IF EXISTS "his"."v_patient_child";`)
	rootDrop := strings.Index(plan, `DROP VIEW IF EXISTS "his"."v_patient_root";`)
	alterPos := strings.Index(plan, "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);")
	rootCreate := strings.Index(plan, `CREATE OR REPLACE VIEW "his"."v_patient_root"`)
	childCreate := strings.Index(plan, `CREATE OR REPLACE VIEW "his"."v_patient_child"`)
	verifyPos := strings.Index(plan, "-- Verify")
	if childDrop < 0 || rootDrop < 0 || alterPos < 0 || rootCreate < 0 || childCreate < 0 || verifyPos < 0 {
		t.Fatalf("expected full manual plan, got:\n%s", plan)
	}
	if !(childDrop < rootDrop && rootDrop < alterPos && alterPos < rootCreate && rootCreate < childCreate && childCreate < verifyPos) {
		t.Fatalf("unexpected plan order:\n%s", plan)
	}
	if !strings.Contains(plan, `ALTER VIEW "his"."v_patient_root" OWNER TO "his_owner";`) {
		t.Fatalf("expected owner restore SQL in plan:\n%s", plan)
	}
	if !strings.Contains(plan, `GRANT SELECT ON "his"."v_patient_root" TO "app";`) {
		t.Fatalf("expected grant restore SQL in plan:\n%s", plan)
	}
	if !strings.Contains(plan, `COMMENT ON VIEW "his"."v_patient_root" IS 'root';`) {
		t.Fatalf("expected comment restore SQL in plan:\n%s", plan)
	}
	if !strings.Contains(plan, `CREATE INDEX idx_v_patient_root_code ON "his"."v_patient_root"(code);`) {
		t.Fatalf("expected index restore SQL in plan:\n%s", plan)
	}
}

func TestBuildSQLViewDependencyItemFromSnapshotPreservesBackupSQL(t *testing.T) {
	item := buildSQLViewDependencyItemFromSnapshot(42, viewDependencySnapshot{
		Schema:        "his",
		Name:          "v_patient",
		Kind:          "v",
		Depth:         1,
		Definition:    "SELECT code FROM his.patient",
		Owner:         "his_owner",
		GrantSQL:      []string{`GRANT SELECT ON "his"."v_patient" TO "app";`},
		CommentSQL:    []string{`COMMENT ON VIEW "his"."v_patient" IS 'patient view';`},
		IndexSQL:      []string{`CREATE INDEX idx_v_patient_code ON his.v_patient(code);`},
		RuleSQL:       []string{`CREATE RULE v_patient_log AS ON UPDATE TO "his"."v_patient" DO ALSO NOTIFY patient_changed;`},
		TriggerSQL:    []string{`CREATE TRIGGER v_patient_instead_update INSTEAD OF UPDATE ON "his"."v_patient" FOR EACH ROW EXECUTE FUNCTION his.sync_patient();`},
		OptionsJSON:   `{"security_barrier":true}`,
		DropOrder:     2,
		RestoreOrder:  1,
		Materialized:  false,
		AdditionalSQL: []string{`ALTER VIEW "his"."v_patient" SET (security_barrier=true);`},
	})

	if item.TaskID != 42 || item.ObjectSchema != "his" || item.ObjectName != "v_patient" {
		t.Fatalf("unexpected item identity: %+v", item)
	}
	if item.DropOrder != 2 || item.RestoreOrder != 1 {
		t.Fatalf("unexpected order: %+v", item)
	}
	if !strings.Contains(item.DropSQL, `DROP VIEW IF EXISTS "his"."v_patient";`) {
		t.Fatalf("unexpected drop SQL: %s", item.DropSQL)
	}
	if !strings.Contains(item.CreateSQL, `CREATE OR REPLACE VIEW "his"."v_patient" AS`) {
		t.Fatalf("unexpected create SQL: %s", item.CreateSQL)
	}
	if !strings.Contains(item.RestoreOwnerSQL, `ALTER VIEW "his"."v_patient" OWNER TO "his_owner";`) {
		t.Fatalf("missing owner restore SQL: %s", item.RestoreOwnerSQL)
	}
	if !strings.Contains(item.RestoreGrantsSQL, `GRANT SELECT ON "his"."v_patient" TO "app";`) {
		t.Fatalf("missing grant restore SQL: %s", item.RestoreGrantsSQL)
	}
	if !strings.Contains(item.RestoreCommentsSQL, `COMMENT ON VIEW "his"."v_patient" IS 'patient view';`) {
		t.Fatalf("missing comment restore SQL: %s", item.RestoreCommentsSQL)
	}
	if !strings.Contains(item.IndexesJSON, "idx_v_patient_code") {
		t.Fatalf("missing index backup JSON: %s", item.IndexesJSON)
	}
	if !strings.Contains(item.RulesJSON, "v_patient_log") {
		t.Fatalf("missing rule backup JSON: %s", item.RulesJSON)
	}
	if !strings.Contains(item.TriggersJSON, "v_patient_instead_update") {
		t.Fatalf("missing trigger backup JSON: %s", item.TriggersJSON)
	}
	if !strings.Contains(item.RestoreRulesSQL, "CREATE RULE v_patient_log") {
		t.Fatalf("missing rule restore SQL: %s", item.RestoreRulesSQL)
	}
	if !strings.Contains(item.RestoreTriggersSQL, "CREATE TRIGGER v_patient_instead_update") {
		t.Fatalf("missing trigger restore SQL: %s", item.RestoreTriggersSQL)
	}
	if item.OptionsJSON != `{"security_barrier":true}` {
		t.Fatalf("missing options backup JSON: %s", item.OptionsJSON)
	}
	if item.BackupHash == "" {
		t.Fatalf("expected backup hash")
	}
	if !strings.Contains(item.VerifySQL, `to_regclass('"his"."v_patient"')`) {
		t.Fatalf("unexpected verify SQL: %s", item.VerifySQL)
	}
	if !strings.Contains(item.VerifySQL, `RAISE EXCEPTION`) {
		t.Fatalf("verify SQL must fail when restored object is missing: %s", item.VerifySQL)
	}
}

func TestBuildSQLViewDependencyMaterializedItemRefreshesAfterCreate(t *testing.T) {
	item := buildSQLViewDependencyItemFromSnapshot(42, viewDependencySnapshot{
		Schema:       "his",
		Name:         "mv_patient",
		Kind:         "m",
		Depth:        1,
		Definition:   "SELECT code FROM his.patient",
		DropOrder:    1,
		RestoreOrder: 1,
		Materialized: true,
	})

	if !strings.Contains(item.CreateSQL, "WITH NO DATA") {
		t.Fatalf("expected materialized view to be created without data first: %s", item.CreateSQL)
	}
	if !strings.Contains(item.RestoreRefreshSQL, `REFRESH MATERIALIZED VIEW "his"."mv_patient";`) {
		t.Fatalf("expected materialized view refresh SQL, got %s", item.RestoreRefreshSQL)
	}

	steps := BuildSQLViewDependencyTransactionalSteps(model.SQLViewDependencyTask{
		AlterSQL: "ALTER TABLE his.patient ALTER COLUMN code TYPE text;",
	}, []model.SQLViewDependencyItem{item})
	joined := strings.Join(steps, "\n")
	if strings.Index(joined, item.CreateSQL) > strings.Index(joined, item.RestoreRefreshSQL) {
		t.Fatalf("materialized view refresh must run after create:\n%s", joined)
	}
}

func TestValidateViewDependencyBackupFreshDetectsChangedDefinition(t *testing.T) {
	item := buildSQLViewDependencyItemFromSnapshot(42, viewDependencySnapshot{
		Schema:       "his",
		Name:         "v_patient",
		Kind:         "v",
		Depth:        1,
		Definition:   "SELECT code FROM his.patient",
		DropOrder:    1,
		RestoreOrder: 1,
	})

	err := validateViewDependencyBackupFresh([]model.SQLViewDependencyItem{item}, []viewDependencySnapshot{
		{Schema: "his", Name: "v_patient", Kind: "v", Definition: "SELECT code, name FROM his.patient"},
	})
	if err == nil {
		t.Fatalf("expected changed view definition to make backup stale")
	}
}

func TestEnsureSQLViewDependencyTaskCanExecuteRequiresPrecheck(t *testing.T) {
	if err := ensureSQLViewDependencyTaskCanExecute(model.SQLViewDependencyTask{Status: "ANALYZED"}, []model.SQLViewDependencyItem{{ID: 1}}); err == nil {
		t.Fatalf("expected execution to require PRECHECK_PASSED")
	}
	if err := ensureSQLViewDependencyTaskCanExecute(model.SQLViewDependencyTask{Status: "PRECHECK_PASSED"}, []model.SQLViewDependencyItem{{ID: 1}}); err != nil {
		t.Fatalf("expected prechecked task to execute: %v", err)
	}
}

func TestBuildSQLViewDependencyRestorePlanOnlyRestoresAndVerifies(t *testing.T) {
	task := model.SQLViewDependencyTask{SchemaName: "his", TableName: "patient", ColumnName: "code"}
	items := []model.SQLViewDependencyItem{
		{
			ObjectName:         "v_child",
			RestoreOrder:       2,
			DropSQL:            `DROP VIEW IF EXISTS "his"."v_child";`,
			CreateSQL:          `CREATE OR REPLACE VIEW "his"."v_child" AS SELECT code FROM his.v_root;`,
			RestoreOwnerSQL:    `ALTER VIEW "his"."v_child" OWNER TO "owner";`,
			RestoreGrantsSQL:   `GRANT SELECT ON "his"."v_child" TO "app";`,
			RestoreCommentsSQL: `COMMENT ON VIEW "his"."v_child" IS 'child';`,
			VerifySQL:          `SELECT to_regclass('"his"."v_child"');`,
		},
		{
			ObjectName:   "v_root",
			RestoreOrder: 1,
			DropSQL:      `DROP VIEW IF EXISTS "his"."v_root";`,
			CreateSQL:    `CREATE OR REPLACE VIEW "his"."v_root" AS SELECT code FROM his.patient;`,
			VerifySQL:    `SELECT to_regclass('"his"."v_root"');`,
		},
	}

	plan := BuildSQLViewDependencyRestorePlan(task, items)

	if strings.Contains(plan, "DROP VIEW") || strings.Contains(plan, "ALTER TABLE") {
		t.Fatalf("restore plan must not drop views or run original ALTER: %s", plan)
	}
	rootCreate := strings.Index(plan, `CREATE OR REPLACE VIEW "his"."v_root"`)
	childCreate := strings.Index(plan, `CREATE OR REPLACE VIEW "his"."v_child"`)
	verifyPos := strings.Index(plan, "-- Verify")
	if rootCreate < 0 || childCreate < 0 || verifyPos < 0 || !(rootCreate < childCreate && childCreate < verifyPos) {
		t.Fatalf("unexpected restore plan order:\n%s", plan)
	}
}

func TestBuildSQLViewDependencyTransactionalPlanUsesShortLockSettings(t *testing.T) {
	task := model.SQLViewDependencyTask{
		SchemaName:       "his",
		TableName:        "patient",
		ColumnName:       "code",
		AlterSQL:         "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);",
		LockTimeout:      "3s",
		StatementTimeout: "10min",
	}
	items := []model.SQLViewDependencyItem{
		{DropOrder: 1, RestoreOrder: 1, DropSQL: `DROP VIEW IF EXISTS "his"."v_patient";`, CreateSQL: `CREATE OR REPLACE VIEW "his"."v_patient" AS SELECT code FROM his.patient;`, VerifySQL: `SELECT to_regclass('"his"."v_patient"');`},
	}

	steps := BuildSQLViewDependencyTransactionalSteps(task, items)
	joined := strings.Join(steps, "\n")

	if steps[0] != "BEGIN;" {
		t.Fatalf("transactional steps should start with BEGIN, got %#v", steps)
	}
	if steps[len(steps)-1] != "COMMIT;" {
		t.Fatalf("transactional steps should end with COMMIT, got %#v", steps)
	}
	if !strings.Contains(joined, "SET LOCAL lock_timeout = '3s';") || !strings.Contains(joined, "SET LOCAL statement_timeout = '10min';") {
		t.Fatalf("missing short lock settings:\n%s", joined)
	}
	if strings.Index(joined, `DROP VIEW IF EXISTS "his"."v_patient";`) > strings.Index(joined, task.AlterSQL) {
		t.Fatalf("drop must happen before alter:\n%s", joined)
	}
}

func TestBuildSQLViewDependencyStepExecutionPlanDoesNotWrapInTransaction(t *testing.T) {
	task := model.SQLViewDependencyTask{
		AlterSQL: "ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);",
	}
	items := []model.SQLViewDependencyItem{
		{DropOrder: 1, RestoreOrder: 1, DropSQL: `DROP VIEW IF EXISTS "his"."v_patient";`, CreateSQL: `CREATE OR REPLACE VIEW "his"."v_patient" AS SELECT code FROM his.patient;`, VerifySQL: `SELECT to_regclass('"his"."v_patient"');`},
	}

	plan := BuildSQLViewDependencyStepExecutionPlan(task, items)
	joined := strings.Join(plan.AllSteps(), "\n")

	if strings.Contains(joined, "BEGIN;") || strings.Contains(joined, "COMMIT;") || strings.Contains(joined, "ROLLBACK;") {
		t.Fatalf("step execution plan must not wrap statements in a transaction:\n%s", joined)
	}
	if len(plan.DropSteps) == 0 || len(plan.AlterSteps) != 1 || len(plan.RestoreSteps) == 0 {
		t.Fatalf("expected drop, alter and restore groups, got %+v", plan)
	}
	if plan.AlterSteps[0] != task.AlterSQL {
		t.Fatalf("expected original ALTER SQL as alter step, got %+v", plan.AlterSteps)
	}
}

func TestRunSQLViewDependencyStepExecutionRestoresAfterAlterFailure(t *testing.T) {
	executor := &recordingViewDependencyExecutor{failAt: 2}
	plan := SQLViewDependencyStepExecutionPlan{
		DropSteps:    []string{`DROP VIEW IF EXISTS "his"."v_patient";`},
		AlterSteps:   []string{"ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);"},
		RestoreSteps: []string{`CREATE OR REPLACE VIEW "his"."v_patient" AS SELECT code FROM his.patient;`, `SELECT to_regclass('"his"."v_patient"');`},
	}

	err := runSQLViewDependencyStepExecution(executor, plan)

	if err == nil {
		t.Fatalf("expected ALTER failure")
	}
	if len(executor.steps) != 4 {
		t.Fatalf("expected drop, failed alter and restore steps, got %#v", executor.steps)
	}
	if executor.steps[2] != plan.RestoreSteps[0] || executor.steps[3] != plan.RestoreSteps[1] {
		t.Fatalf("expected automatic restore after alter failure, got %#v", executor.steps)
	}
}

type recordingViewDependencyExecutor struct {
	failAt int
	steps  []string
}

func (e *recordingViewDependencyExecutor) Exec(sqlText string) error {
	e.steps = append(e.steps, sqlText)
	if e.failAt > 0 && len(e.steps) == e.failAt {
		return errors.New("boom")
	}
	return nil
}

func TestRunSQLViewDependencyStepsRollsBackOnFailure(t *testing.T) {
	executor := &recordingViewDependencyExecutor{failAt: 3}
	err := runSQLViewDependencySteps(executor, []string{"BEGIN;", "SET LOCAL lock_timeout = '3s';", "ALTER TABLE his.patient ALTER COLUMN code TYPE text;", "COMMIT;"})

	if err == nil {
		t.Fatalf("expected execution failure")
	}
	if got := executor.steps[len(executor.steps)-1]; got != "ROLLBACK;" {
		t.Fatalf("expected rollback after failure, got steps %#v", executor.steps)
	}
}

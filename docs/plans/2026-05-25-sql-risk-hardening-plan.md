# SQL Risk Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 补齐 SQL 执行中“细粒度风险判断、CREATE OR REPLACE VIEW 兼容性、DROP/TRUNCATE 策略、事务策略、视图依赖处理”的规划能力，并保持实施人员操作入口简单。

**Architecture:** 在现有 `PostgreSQLService` 上拆出 SQL 风险分析、数据库元数据检查、执行策略和视图处理四个内部模块。解析仍以 `pg_query_go` 为主，数据库执行仍以 `pgx` 为主；所有新增能力优先落到 SQL 明细的 `risk_level`、`risk_reason`、`execute_status` 和可导出的 SQL 中。

**Tech Stack:** Go 1.25, Gin, GORM, pgx stdlib, pg_query_go, Vue 3, Element Plus, Vitest/Go test where applicable.

## Scope

本计划只覆盖当前文档中标记为“部分实现或实现深度不足”的五项：

- 细粒度风险判断
- `CREATE OR REPLACE VIEW` 兼容性风险
- `DROP TABLE` / `TRUNCATE` 策略
- SQL 事务策略
- 视图依赖处理

不纳入本轮计划：

- 审批流
- 按业务系统、环境、schema 配置规则
- psql 兼容执行器
- SQL 执行中取消
- 超大 SQL 文件流式处理

## Design Decisions

### 1. 风险判断分层

风险判断分三层执行：

1. **AST 静态判断**：不访问数据库，识别 SQL 类型、对象名、字段类型、是否 `USING`、是否 `CONCURRENTLY`、是否 volatile default。
2. **数据库元数据判断**：需要连接 PostgreSQL，读取表大小、行数、字段现状、视图列定义、依赖关系。
3. **执行策略判断**：根据风险结果决定 `PENDING`、`NOT_EXECUTABLE`、是否允许事务、是否需要导出处理。

风险等级保持当前三档：

- `LOW`：直接执行。
- `WARN`：允许执行，但记录原因。
- `BLOCKED`：不执行，明细状态落为 `NOT_EXECUTABLE`。

### 2. DROP/TRUNCATE 策略

第一版不引入页面配置，使用内置策略：

- `DROP DATABASE`、`DROP SCHEMA` 继续强拦截。
- `DROP TABLE`、`TRUNCATE` 默认 `WARN`。
- 命中临时/备份命名规则时降低提示强度：
  - `temp_`
  - `tmp_`
  - `_tmp`
  - `bak_`
  - `_bak`
  - `backup_`
- 非临时/备份表如果表大小超过阈值，提升为 `BLOCKED`。

默认阈值先写死在 service 内：

```go
const largeTableBytes = 10 * 1024 * 1024 * 1024 // 10GB
const largeTableRows = 10_000_000
```

后续如需要再放到系统设置中。

### 3. 事务策略

默认保持当前“逐条执行”模型，不做文件级全事务。原因：

- 现有 Java 版本就是逐条执行，失败停止后续。
- `CREATE INDEX CONCURRENTLY` 不能在事务块中执行。
- SQL 文件中可能混合 DDL、DML、并发索引、视图重建。

新增的是**策略标记**，不是马上增加事务执行：

- 每条 SQL 增加内部判断：`CanRunInTransaction`。
- `CREATE INDEX CONCURRENTLY`、`VACUUM`、部分 `ALTER TYPE` 等标记为 false。
- 文件级展示可提示“该文件不适合整体事务执行”。

如果未来要增加事务模式，必须作为高级选项，且默认关闭。

### 4. 视图依赖处理

先做“可解释、可导出”的视图依赖处理，不直接自动 drop/recreate：

1. 检测字段类型变更依赖视图。
2. 保存依赖视图定义到 `SQLViewBackup`。
3. 生成建议处理 SQL：
   - `DROP VIEW ...;`
   - 原字段类型变更 SQL
   - `CREATE OR REPLACE VIEW ... AS ...;`
4. 将原 SQL 标记 `NOT_EXECUTABLE` 或 `WARN` 的策略需要谨慎：
   - 如果依赖视图存在，默认 `NOT_EXECUTABLE`，导出处理 SQL 给实施人员。
   - 如果后续增加“自动处理视图”开关，再允许系统自动 drop/recreate。

## Tasks

### Task 1: Extract SQL Risk Analyzer

**Files:**

- Create: `backend/internal/service/sql_risk_analyzer.go`
- Modify: `backend/internal/service/postgresql_service.go`
- Test: `backend/internal/service/sql_risk_analyzer_test.go`

**Step 1: Write failing tests**

Add tests for existing behavior before extraction:

```go
func TestAnalyzeSQLRiskKeepsExistingBlockedRules(t *testing.T) {
	tests := []struct {
		sql string
		typ string
	}{
		{"DROP DATABASE his_prod", "DROP_DATABASE"},
		{"ALTER SYSTEM SET work_mem = '64MB'", "ALTER_SYSTEM"},
		{"VACUUM FULL patient", "VACUUM_FULL"},
	}
	for _, tc := range tests {
		got := AnalyzeSQLRisk(tc.sql)
		if got.SQLType != tc.typ || got.RiskLevel != "BLOCKED" {
			t.Fatalf("expected %s BLOCKED, got %+v", tc.typ, got)
		}
	}
}
```

**Step 2: Run tests**

Run:

```bash
cd backend
go test -count=1 ./internal/service -run TestAnalyzeSQLRiskKeepsExistingBlockedRules
```

Expected: PASS before extraction.

**Step 3: Extract implementation**

Move these functions from `postgresql_service.go` into `sql_risk_analyzer.go` without behavior changes:

- `AnalyzeSQLRisk`
- `analyzeSQLRiskFromAST`
- `analyzeDropStmt`
- `analyzeAlterTableStmt`
- `vacuumHasOption`
- `mergeRisk`
- `riskRank`
- `normalizeSQL`
- `firstKeyword`

**Step 4: Verify**

Run:

```bash
cd backend
go test -count=1 ./internal/service
```

Expected: PASS.

**Step 5: Commit**

```bash
git add backend/internal/service/postgresql_service.go backend/internal/service/sql_risk_analyzer.go backend/internal/service/sql_risk_analyzer_test.go
git commit -m "refactor: extract sql risk analyzer"
```

### Task 2: Add Fine-Grained Column Type Risk

**Files:**

- Modify: `backend/internal/service/sql_risk_analyzer.go`
- Test: `backend/internal/service/sql_risk_analyzer_test.go`

**Step 1: Write failing tests**

Add table-driven tests:

```go
func TestAnalyzeSQLRiskClassifiesColumnTypeChanges(t *testing.T) {
	tests := []struct {
		name       string
		sql        string
		wantLevel  string
		wantReason string
	}{
		{
			name: "varchar expansion is low risk",
			sql: "ALTER TABLE patient ALTER COLUMN code TYPE varchar(20)",
			wantLevel: "LOW",
			wantReason: "varchar 长度扩容",
		},
		{
			name: "varchar shrink warns",
			sql: "ALTER TABLE patient ALTER COLUMN code TYPE varchar(6)",
			wantLevel: "WARN",
			wantReason: "varchar 长度缩容",
		},
		{
			name: "using conversion warns",
			sql: "ALTER TABLE patient ALTER COLUMN id TYPE uuid USING id::uuid",
			wantLevel: "WARN",
			wantReason: "USING",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AnalyzeSQLRisk(tc.sql)
			if got.RiskLevel != tc.wantLevel {
				t.Fatalf("expected %s, got %+v", tc.wantLevel, got)
			}
			if !strings.Contains(got.RiskReason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, got.RiskReason)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run:

```bash
cd backend
go test -count=1 ./internal/service -run TestAnalyzeSQLRiskClassifiesColumnTypeChanges
```

Expected: FAIL because current logic treats all `ALTER_COLUMN_TYPE` as `WARN`.

**Step 3: Implement minimal parser helpers**

Add internal helper:

```go
type columnTypeChangeRisk struct {
	SQLType string
	Level   string
	Reason  string
}
```

Use `pg_query.AlterTableCmd` fields to inspect:

- target type name
- typmods for varchar length when available
- `cmd.GetDef()` / `cmd.GetUsing()` equivalent available in pg_query_go nodes

If pg_query_go does not expose enough detail for typmods, add conservative fallback:

- regex for `varchar(n)`
- if cannot determine old type, classify as `WARN`

Important: without database metadata, true expansion/shrink requires old column type. This task can only classify “new type pattern” and `USING`; actual expansion/shrink must be finalized in Task 3.

**Step 4: Verify**

Run:

```bash
cd backend
go test -count=1 ./internal/service -run TestAnalyzeSQLRiskClassifiesColumnTypeChanges
go test -count=1 ./internal/service
```

Expected: PASS.

**Step 5: Commit**

```bash
git add backend/internal/service/sql_risk_analyzer.go backend/internal/service/sql_risk_analyzer_test.go
git commit -m "feat: classify column type sql risk"
```

### Task 3: Add Database Metadata Inspector

**Files:**

- Create: `backend/internal/service/sql_metadata_inspector.go`
- Test: `backend/internal/service/sql_metadata_inspector_test.go`
- Modify: `backend/internal/service/postgresql_service.go`

**Step 1: Define interfaces**

Create:

```go
type SQLMetadataInspector struct {
	db *sql.DB
}

type TableStats struct {
	Schema string
	Table  string
	Bytes  int64
	Rows   int64
}

type ColumnInfo struct {
	Schema     string
	Table      string
	Column     string
	DataType   string
	UDTName    string
	CharMaxLen *int
	NumericPrecision *int
	NumericScale *int
}
```

**Step 2: Write failing unit tests for decision helpers**

Do not require a real PostgreSQL database in unit tests. Test pure decision functions:

```go
func TestClassifyVarcharResizeWithColumnMetadata(t *testing.T) {
	oldLen := 6
	info := ColumnInfo{DataType: "character varying", CharMaxLen: &oldLen}
	got := classifyTypeChangeWithMetadata(info, "varchar(20)", false)
	if got.RiskLevel != "LOW" {
		t.Fatalf("expected LOW expansion, got %+v", got)
	}

	got = classifyTypeChangeWithMetadata(info, "varchar(4)", false)
	if got.RiskLevel != "WARN" {
		t.Fatalf("expected WARN shrink, got %+v", got)
	}
}
```

**Step 3: Implement metadata queries**

Add methods:

```go
func (i *SQLMetadataInspector) GetTableStats(ctx context.Context, schema, table string) (TableStats, error)
func (i *SQLMetadataInspector) GetColumnInfo(ctx context.Context, schema, table, column string) (ColumnInfo, error)
```

Use:

```sql
SELECT
  pg_total_relation_size(c.oid),
  COALESCE(c.reltuples::bigint, 0)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2;
```

and `information_schema.columns` for column info.

**Step 4: Wire into ParseSQL**

In `ParseSQL`, after static `AnalyzeSQLRisk`, call a new method:

```go
analysis = s.enrichRiskWithDatabaseMetadata(ctx, schemaName, sqlText, analysis)
```

This requires changing `ParseSQL` to accept context:

- Add `ParseSQLWithContext(ctx context.Context, req ParseSQLRequest)`.
- Keep `ParseSQL(req)` as wrapper using `context.Background()` to avoid broad handler churn.

**Step 5: Verify**

Run:

```bash
cd backend
go test -count=1 ./internal/service
go test -count=1 ./...
```

Expected: PASS.

**Step 6: Commit**

```bash
git add backend/internal/service/postgresql_service.go backend/internal/service/sql_metadata_inspector.go backend/internal/service/sql_metadata_inspector_test.go
git commit -m "feat: enrich sql risk with metadata"
```

### Task 4: Implement DROP/TRUNCATE Policy

**Files:**

- Modify: `backend/internal/service/sql_risk_analyzer.go`
- Modify: `backend/internal/service/sql_metadata_inspector.go`
- Test: `backend/internal/service/sql_risk_analyzer_test.go`
- Test: `backend/internal/service/sql_metadata_inspector_test.go`

**Step 1: Write failing tests**

```go
func TestDropAndTruncatePolicyRecognizesTemporaryNames(t *testing.T) {
	tests := []string{
		"DROP TABLE temp_patient",
		"DROP TABLE patient_tmp",
		"TRUNCATE TABLE bak_patient",
		"TRUNCATE TABLE patient_bak",
	}
	for _, sqlText := range tests {
		got := AnalyzeSQLRisk(sqlText)
		if got.RiskLevel != "LOW" {
			t.Fatalf("expected LOW for %s, got %+v", sqlText, got)
		}
	}
}
```

Add pure helper test:

```go
func TestLargeDropOrTruncateIsBlocked(t *testing.T) {
	stats := TableStats{Schema: "public", Table: "patient", Bytes: largeTableBytes + 1, Rows: 1}
	got := classifyDestructiveTableOperation("DROP_TABLE", stats)
	if got.RiskLevel != "BLOCKED" {
		t.Fatalf("expected BLOCKED, got %+v", got)
	}
}
```

**Step 2: Run failing tests**

Run:

```bash
cd backend
go test -count=1 ./internal/service -run 'TestDropAndTruncatePolicyRecognizesTemporaryNames|TestLargeDropOrTruncateIsBlocked'
```

Expected: FAIL.

**Step 3: Implement policy**

Add helpers:

```go
func isTemporaryOrBackupTableName(name string) bool
func classifyDestructiveTableOperation(sqlType string, stats TableStats) RiskAnalysis
```

Rules:

- temp/backup table names: `LOW`, reason “临时/备份表命名规则命中”。
- normal table: `WARN`.
- large table by bytes or rows: `BLOCKED`, reason includes size and rows.

**Step 4: Wire metadata enrichment**

When SQL type is `DROP_TABLE` or `TRUNCATE`, parse table reference from AST and call `GetTableStats`.

If metadata query fails, keep static risk result and append reason:

```text
未能读取表大小信息，按默认风险处理
```

**Step 5: Verify**

Run:

```bash
cd backend
go test -count=1 ./internal/service
go test -count=1 ./...
```

Expected: PASS.

**Step 6: Commit**

```bash
git add backend/internal/service/sql_risk_analyzer.go backend/internal/service/sql_metadata_inspector.go backend/internal/service/*test.go
git commit -m "feat: add drop and truncate risk policy"
```

### Task 5: Plan CREATE OR REPLACE VIEW Compatibility Checks

**Files:**

- Modify: `backend/internal/service/sql_metadata_inspector.go`
- Modify: `backend/internal/service/postgresql_service.go`
- Test: `backend/internal/service/sql_metadata_inspector_test.go`

**Step 1: Define view compatibility types**

```go
type ViewColumn struct {
	Name string
	DataType string
	Ordinal int
}

type ViewCompatibilityResult struct {
	Exists bool
	Compatible bool
	Reason string
}
```

**Step 2: Write failing tests for comparison logic**

```go
func TestCompareViewColumnsDetectsIncompatibleColumnOrder(t *testing.T) {
	oldCols := []ViewColumn{
		{Name: "id", DataType: "integer", Ordinal: 1},
		{Name: "name", DataType: "text", Ordinal: 2},
	}
	newCols := []ViewColumn{
		{Name: "name", DataType: "text", Ordinal: 1},
		{Name: "id", DataType: "integer", Ordinal: 2},
	}
	got := compareViewColumns(oldCols, newCols)
	if got.Compatible {
		t.Fatalf("expected incompatible result")
	}
	if !strings.Contains(got.Reason, "列顺序") {
		t.Fatalf("expected order reason, got %q", got.Reason)
	}
}
```

**Step 3: Implement existing view column query**

Add:

```go
func (i *SQLMetadataInspector) GetViewColumns(ctx context.Context, schema, viewName string) ([]ViewColumn, error)
```

Use `information_schema.columns`:

```sql
SELECT column_name, data_type, ordinal_position
FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2
ORDER BY ordinal_position;
```

**Step 4: Implement new view definition probe**

For PostgreSQL, the most reliable compatibility check is to let the database parse the new view in a rollback-only transaction. First version should avoid executing user SQL directly during parse because `CREATE OR REPLACE VIEW` can still take locks. Instead:

- Static check extracts target view name.
- Metadata check reads existing columns.
- If existing view exists, mark `WARN` with reason “重建视图可能受列名/顺序/类型兼容性限制”.

Do not attempt full new column inference in first implementation unless pg_query_go exposes enough select target information safely.

**Step 5: Wire risk enrichment**

For `CREATE_VIEW` where original SQL starts with `CREATE OR REPLACE VIEW`, if existing view exists, append compatibility warning.

**Step 6: Verify**

Run:

```bash
cd backend
go test -count=1 ./internal/service
```

Expected: PASS.

**Step 7: Commit**

```bash
git add backend/internal/service/postgresql_service.go backend/internal/service/sql_metadata_inspector.go backend/internal/service/sql_metadata_inspector_test.go
git commit -m "feat: warn on replace view compatibility"
```

### Task 6: Add View Dependency Backup and Export Plan

**Files:**

- Modify: `backend/internal/model/postgresql.go`
- Modify: `backend/internal/service/postgresql_service.go`
- Create: `backend/internal/service/sql_view_dependency.go`
- Test: `backend/internal/service/sql_view_dependency_test.go`

**Step 1: Extend view backup model**

Current model:

```go
type SQLViewBackup struct {
	ID           uint
	SchemaName   string
	ViewName     string
	Definition   string
	BackupReason string
	CreatedAt    time.Time
}
```

Add:

```go
FileID      uint   `gorm:"index" json:"fileId"`
StatementID uint   `gorm:"index" json:"statementId"`
DropSQL     string `gorm:"type:text" json:"dropSql"`
CreateSQL   string `gorm:"type:text" json:"createSql"`
```

**Step 2: Write failing tests for SQL generation**

```go
func TestBuildViewRebuildPlanSQL(t *testing.T) {
	plan := BuildViewRebuildPlan(ViewDependency{
		Schema: "public",
		View: "v_patient",
		Definition: " SELECT patient.id FROM patient",
	})
	if !strings.Contains(plan.DropSQL, `DROP VIEW IF EXISTS "public"."v_patient";`) {
		t.Fatalf("unexpected drop sql: %s", plan.DropSQL)
	}
	if !strings.Contains(plan.CreateSQL, `CREATE OR REPLACE VIEW "public"."v_patient" AS`) {
		t.Fatalf("unexpected create sql: %s", plan.CreateSQL)
	}
}
```

**Step 3: Implement dependency lookup with definitions**

Add:

```go
type ViewDependency struct {
	Schema string
	View string
	Definition string
}

func (s *PostgreSQLService) findViewDependenciesWithDefinitions(ctx context.Context, defaultSchema, sqlText string) ([]ViewDependency, error)
```

Query should extend current dependency query and include:

```sql
pg_get_viewdef(dependent_view.oid, true)
```

**Step 4: Save backups during ParseSQL**

When `ALTER_COLUMN_TYPE` dependencies exist:

- Save backups linked to file and statement.
- Set statement risk to `BLOCKED` and status to `NOT_EXECUTABLE`.
- Risk reason should include dependent views and export instruction.

Important: if backup insert fails, parsing should fail. Silent backup failure would make recovery unsafe.

**Step 5: Export rebuild SQL**

Extend `BuildNotExecutableSQLForFile` output:

1. dependency drop SQL
2. original blocked SQL
3. dependency create SQL

Add comments:

```sql
-- View dependency rebuild plan for public.v_patient
DROP VIEW IF EXISTS "public"."v_patient";

-- Original SQL
ALTER TABLE ...

-- Recreate view public.v_patient
CREATE OR REPLACE VIEW ...
```

**Step 6: Verify**

Run:

```bash
cd backend
go test -count=1 ./internal/service
go test -count=1 ./...
```

Expected: PASS.

**Step 7: Commit**

```bash
git add backend/internal/model/postgresql.go backend/internal/service/postgresql_service.go backend/internal/service/sql_view_dependency.go backend/internal/service/sql_view_dependency_test.go
git commit -m "feat: export view dependency rebuild plan"
```

### Task 7: Add Execution Strategy Flags

**Files:**

- Modify: `backend/internal/model/postgresql.go`
- Modify: `backend/internal/service/postgresql_service.go`
- Modify: `frontend/src/api/postgresql.ts`
- Modify: `frontend/src/views/PostgreSQLSQLExecutionView.vue`
- Test: `backend/internal/service/sql_risk_analyzer_test.go`

**Step 1: Extend statement model**

Add to `SQLChangeStatement`:

```go
CanRunInTransaction bool `gorm:"default:true" json:"canRunInTransaction"`
ExecutionStrategy   string `gorm:"size:32" json:"executionStrategy"`
```

Suggested values:

- `DIRECT`
- `DIRECT_NO_TRANSACTION`
- `MANUAL_EXPORT`

**Step 2: Write failing tests**

```go
func TestExecutionStrategyForConcurrentIndex(t *testing.T) {
	strategy := DetermineExecutionStrategy(AnalyzeSQLRisk("CREATE INDEX CONCURRENTLY idx_patient_name ON patient(name)"))
	if strategy.CanRunInTransaction {
		t.Fatalf("concurrent index must not run in transaction")
	}
	if strategy.Name != "DIRECT_NO_TRANSACTION" {
		t.Fatalf("unexpected strategy: %+v", strategy)
	}
}
```

**Step 3: Implement strategy helper**

```go
type SQLExecutionStrategy struct {
	Name string
	CanRunInTransaction bool
}

func DetermineExecutionStrategy(analysis RiskAnalysis) SQLExecutionStrategy
```

Rules:

- `CREATE_INDEX_CONCURRENTLY`, `VACUUM`, `REINDEX`: `DIRECT_NO_TRANSACTION`.
- `BLOCKED`: `MANUAL_EXPORT`.
- default: `DIRECT`.

**Step 4: Persist strategy during parse**

When creating `SQLChangeStatement`, set:

- `CanRunInTransaction`
- `ExecutionStrategy`

**Step 5: Frontend display**

In SQL 明细 table, add compact column:

```vue
<el-table-column prop="executionStrategy" label="策略" width="150" />
```

Do not add new controls yet.

**Step 6: Verify**

Run:

```bash
cd backend
go test -count=1 ./...
cd ../frontend
npm run build
```

Expected: PASS.

**Step 7: Commit**

```bash
git add backend/internal/model/postgresql.go backend/internal/service/postgresql_service.go backend/internal/service/sql_risk_analyzer_test.go frontend/src/api/postgresql.ts frontend/src/views/PostgreSQLSQLExecutionView.vue
git commit -m "feat: record sql execution strategy"
```

### Task 8: Update Requirements Document

**Files:**

- Modify: `docs/sql-change-execution-requirements.md`

**Step 1: Update current implementation table**

Move the planned items from “部分实现” to their new state:

- 细粒度风险判断：部分实现 -> 已实现基础版
- CREATE OR REPLACE VIEW 兼容性：部分实现 -> 已实现提示版
- DROP/TRUNCATE 策略：部分实现 -> 已实现内置策略版
- 事务策略：部分实现 -> 已实现策略标记版
- 视图依赖处理：部分实现 -> 已实现导出版，未自动执行

**Step 2: Update “后续增强”**

Keep these as future work:

- 可配置 DROP/TRUNCATE 策略
- 自动 drop/recreate 依赖视图
- 文件级事务模式
- 更精确的新视图列推断

**Step 3: Commit**

```bash
git add docs/sql-change-execution-requirements.md
git commit -m "docs: update sql risk hardening status"
```

## Verification Checklist

Before final delivery:

```bash
cd backend
go test -count=1 ./...

cd ../frontend
npm run build

cd ..
git diff --check
```

Expected:

- Go tests pass.
- Frontend build passes.
- `git diff --check` has no output.

## Rollout Notes

- These changes add columns to existing GORM models. Existing `AutoMigrate` should create new columns, but it will not backfill old rows.
- Old SQL statements without `execution_strategy` should be treated as `DIRECT` in frontend display.
- View backup rows are new data. If export plan generation is implemented, ensure deleting a SQL file does not hard-delete backups unless explicitly intended.
- Large table thresholds should start as constants. Do not add settings UI until the first real customer case requires tuning.

# PostgreSQL View Dependency Change Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a PostgreSQL management workflow for ALTER COLUMN changes blocked by dependent views, prioritizing complete backups and low impact on production traffic.

**Architecture:** Implement a task-based workflow in the existing PostgreSQL service. Phase 1 creates durable tasks, validates one explicit `ALTER TABLE schema.table ALTER COLUMN column ...` statement, analyzes column-level dependent views, stores backup/rebuild SQL per view, and exports a manual execution plan. Later phases can add short-lock execution and restore automation on top of the same task tables.

**Tech Stack:** Go/Gin/GORM/PostgreSQL catalog queries, pg_query parser, Vue 3/Element Plus frontend.

### Task 1: Backend Models And Migration

**Files:**
- Modify: `backend/internal/model/postgresql.go`
- Modify: `backend/internal/repository/migrate.go`
- Test: `backend/internal/service/sql_view_dependency_change_test.go`

**Steps:**
1. Write failing tests that assert task and item records can be created by the service.
2. Add `SQLViewDependencyTask`, `SQLViewDependencyItem`, and `SQLViewDependencyStep` models.
3. Add the models to `AutoMigrate`.
4. Run the targeted backend test.

### Task 2: ALTER SQL Validation

**Files:**
- Modify: `backend/internal/service/sql_view_dependency.go`
- Test: `backend/internal/service/sql_view_dependency_change_test.go`

**Steps:**
1. Write failing tests for valid and invalid ALTER SQL.
2. Implement parser-backed validation that accepts exactly one explicit `ALTER TABLE schema.table ALTER COLUMN column ...` statement.
3. Reject missing schema, wrong table/column, and multi-statement SQL.
4. Run the targeted backend test.

### Task 3: Dependency Analysis And Backup Plan

**Files:**
- Modify: `backend/internal/service/sql_view_dependency.go`
- Modify: `backend/internal/service/postgresql_service.go`
- Test: `backend/internal/service/sql_view_dependency_change_test.go`

**Steps:**
1. Write failing tests for dependency ordering and full backup SQL generation.
2. Implement dependency item structures with drop/restore order.
3. Generate explicit drop SQL, create SQL, owner restore SQL, grant SQL, and comments placeholder JSON fields.
4. Store analysis results under a task ID.
5. Run the targeted backend test.

### Task 4: API Endpoints

**Files:**
- Modify: `backend/internal/handler/postgresql.go`
- Modify: `frontend/src/api/postgresql.ts`
- Test: backend handler compile via `go test ./...`

**Steps:**
1. Add routes for create task, get task, analyze task, and export plan.
2. Add matching frontend API functions and types.
3. Run backend and frontend type checks.

### Task 5: Frontend Page

**Files:**
- Modify: `frontend/src/router/index.ts`
- Create: `frontend/src/views/PostgreSQLViewDependencyChangeView.vue`
- Modify: `frontend/src/utils/postgresql-sql-execution-view.test.mjs` or add a new static test.

**Steps:**
1. Write failing static test for route/API/page controls.
2. Build a task-first page: create form, task status, dependency table, execution plan export.
3. Keep execution buttons limited to analyze/export in phase 1.
4. Run frontend static test and build.

### Task 6: Verification

**Commands:**
- `cd backend && go test ./...`
- `cd frontend && npm run build`
- `node frontend/src/utils/postgresql-sql-execution-view.test.mjs`


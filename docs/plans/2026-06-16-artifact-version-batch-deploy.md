# Artifact Version Batch Deploy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Persist imported artifact versions and per-file validation/matching results, then make batch deploy consume those versions instead of upload/download workspaces directly.

**Architecture:** Add `artifact_versions` as the version header table and `artifact_version_items` as the file-level result table. Upload/download creates or refreshes a version record; artifact management displays version details; batch deploy selects one available version and creates build/deploy tasks from its deployable items.

**Tech Stack:** Go/Gin/GORM backend, PostgreSQL-backed models, Vue 3 + Element Plus frontend.

### Task 1: Backend Version Persistence

**Files:**
- Create: `backend/internal/model/artifact_version.go`
- Modify: `backend/internal/repository/migrate.go`
- Modify/Test: `backend/internal/handler/batch_deploy.go`
- Test: `backend/internal/handler/batch_deploy_test.go`

**Steps:**
1. Write failing tests for persisting a version from a workspace directory and reading version detail.
2. Add GORM models for `artifact_versions` and `artifact_version_items`.
3. Add AutoMigrate entries.
4. Implement helpers that scan jar/zip files, validate archive readability, match applications, calculate SHA256/size, and store header/item rows.
5. Call the helper after local upload and successful server download.
6. Add list/detail API endpoints.

### Task 2: Batch Deploy Read Model

**Files:**
- Modify/Test: `backend/internal/handler/batch_deploy.go`
- Modify: `frontend/src/api/batch-deploy.ts`

**Steps:**
1. Add API DTOs for version and version items.
2. Make version list/detail come from persisted tables.
3. Keep legacy `source-batches` as compatibility, but frontend should move to the version API.
4. Avoid using `matchArtifacts` for version-driven batch deploy so matching does not trigger latest artifact side effects.

### Task 3: Frontend Refactor

**Files:**
- Modify: `frontend/src/views/ArtifactBatchesView.vue`
- Create: `frontend/src/views/ArtifactVersionDetailView.vue`
- Modify: `frontend/src/views/BatchDeployView.vue`
- Modify: `frontend/src/router/index.ts`
- Modify/Test: `frontend/src/utils/artifact-navigation.test.mjs`

**Steps:**
1. Make artifact management version list show version state and link to a useful detail page.
2. Make detail page show file-level validation and application matching.
3. Make batch deploy show only deploy mode, version selection, matching confirmation, and build submission.
4. Remove upload/download actions from batch deploy.
5. Update static navigation tests.

### Task 4: Verification and Deployment

**Steps:**
1. Run targeted Go tests for batch deploy handler.
2. Run frontend static tests.
3. Build frontend and embed assets.
4. Run backend tests/build.
5. Deploy updated binary to `192.168.199.100:8800`.

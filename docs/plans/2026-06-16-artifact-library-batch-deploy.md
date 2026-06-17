# Artifact Library Batch Deploy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make batch deploy sources automatically maintain the latest artifact library while keeping unmatched files non-blocking.

**Architecture:** Batch upload/download/artifact selections all resolve to a batch workspace under the platform binary directory. The match step validates files, skips SQL packages and unmatched files, and copies matched deployable artifacts into `artifacts/apps/{app}/versions/{batch}/` plus `artifacts/apps/{app}/latest/`.

## Correction: Artifact Library Update Timing

The current implementation updates `artifacts/apps/{app}/latest/` during the match step. This is not the desired rule.

Desired rule:
- Match/validation only reports whether artifacts are valid and which application they map to.
- Artifact library `latest` must be updated only after the selected application has been built and deployed successfully.
- Failed, unselected, invalid, skipped, or unmatched artifacts must not enter the latest artifact library.

This requires splitting the current match side effects out of `POST /api/batch-deploy/match` and moving latest-library updates to the successful pipeline/deployment completion path.

**Tech Stack:** Go/Gin/GORM backend, Vue 3 + Element Plus frontend.

### Task 1: Backend Behavior Tests

**Files:**
- Modify: `backend/internal/handler/batch_deploy_test.go`

**Steps:**
1. Add tests for `isSQLArtifactFile`, artifact library path layout, and latest copy behavior.
2. Run `go test ./internal/handler -run 'TestSQLArtifact|TestStoreLatest|TestBatchUploadRoot'` and verify failure.

### Task 2: Backend Artifact Library

**Files:**
- Modify: `backend/internal/handler/batch_deploy.go`
- Modify: `backend/internal/model/artifact.go`
- Modify: `backend/internal/repository/artifact_repo.go`

**Steps:**
1. Resolve workspace and artifact roots from the platform binary directory.
2. Store local uploads in `workspaces/batch-upload/local/{batch}`.
3. Store remote downloads in `workspaces/batch-upload/download/{batch}`.
4. Add helper to copy matched artifacts into version and latest directories.
5. Update artifact records with source metadata and mark previous records for the app as not latest.

### Task 3: Match Flow

**Files:**
- Modify: `backend/internal/handler/batch_deploy.go`

**Steps:**
1. Skip `*-sql.zip` with a clear message.
2. Keep unmatched files as non-blocking match results.
3. For matched and valid app artifacts, update the artifact library automatically.

### Task 4: Frontend Batch Deploy UI

**Files:**
- Modify: `frontend/src/views/BatchDeployView.vue`

**Steps:**
1. Make remote and local file tables use adaptive columns.
2. Ensure the right panel displays workspace directories.
3. Display skipped/unmatched messages without stopping successful matches.

### Task 5: Verification and Deploy

**Commands:**
- `cd backend && go test ./...`
- `cd frontend && npm run build`
- Deploy current source to `192.168.199.100` and run `/health`.

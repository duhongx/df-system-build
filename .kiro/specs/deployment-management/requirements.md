# Requirements Document

## Introduction

部署管理（Deployment Management）是 DF 构建发布系统中新增的子模块，将原 his-deploy 项目的 HIS 基础设施离线部署能力整体迁入 df-build-system，使一套二进制同时承担"业务应用构建发布"和"基础设施部署管理"两套职责。该模块替换 df-build-system 中已有的"基础设施"占位模块（含 deployer 早期雏形和 4 个空壳前端页面），统一接入 df-build-system 的鉴权、统一响应、SSE、服务器管理、PostgreSQL/GORM 持久化等基础设施。

部署管理模块面向运维工程师，提供 23 个 HIS 基础组件（K8s 体系、中间件、基础设施服务）的离线部署能力：通过 Web UI 管理目标主机绑定（复用服务器管理）、配置全局/组件参数、上传校验离线包、声明式编排部署动作（约 40 种 Action），通过 SSH 直连目标主机执行，实时推送日志，并提供严格的状态机和 rollback 机制保证幂等与可逆。

## Glossary

- **Deployment_Component（部署组件）**: 部署管理中可独立部署的一个基础设施单元，例如 postgresql、etcd、master、calico 等。每个组件有 code、order（执行顺序）、Deploy/Cleanup/Verify 行为及对应的 pipeline YAML。
- **Virtual_Component（虚拟组件）**: 一组真实组件的逻辑集合（如 k8s 体系），用于一次性圈选多个 Deployment_Component 进行部署或回滚。
- **Deployment_Server（部署目标服务器）**: 部署目标主机。复用 df-build-system 已有的 Server（服务器管理）记录，不再单独维护 HostSpec 表。
- **Component_Target（组件目标绑定）**: Deployment_Component 与 Deployment_Server 的多对多绑定关系，决定该组件被部署到哪些主机。
- **Component_Override（组件参数覆盖）**: 针对单个 Deployment_Component 的参数覆盖项，覆盖 Global_Config 与组件默认参数。
- **Global_Config（全局配置）**: 部署管理的集群级公共参数（如 cluster.remote_root、网络段、密码、证书路径），所有组件共享。
- **Offline_Bundle（离线包）**: 包含 23 个组件资源与 manifest.yml 的离线归档，需通过 sha256 + bundle_version 校验后注入到 resource_dir。
- **Resource_Dir（资源目录）**: 部署管理只读的离线资源根目录，存放 templates、二进制、k8s yaml 等。
- **Action（动作）**: 部署引擎执行的最小原子操作，例如 copy_file、render_template、systemd_service、kubectl_apply、record_path_state 等。约 40 种类型。
- **Pipeline_YAML**: 描述某组件部署/回滚的 Action 序列，由部署引擎在运行时从 DB 渲染上下文后产生具体 Action 列表。
- **Deployment_Run（部署运行）**: 一次具体的部署/回滚执行实例，关联组件集合、目标主机、状态、日志、开始/结束时间。
- **Component_Deploy_State（组件部署状态）**: 已部署组件的状态记录，用于状态机硬约束（已部署组件不允许重复部署，必须先 rollback）。
- **State_Dir（状态目录）**: 服务器本地用于存放 rollback marker 的目录（record_path_state、backup_file 写下的可逆性凭据）。**不存入 PostgreSQL**，保留为服务器本地文件。
- **Conflict_Group（冲突组）**: 因 systemd service 名 / 数据目录 / 安装路径冲突而不能同机部署的组件集合，例如 etcd↔postgresql↔dns、redis↔df-ops、postgresql↔df-ops。
- **Deployment_Engine（部署引擎）**: 由 planner（展开 Pipeline_YAML 为 Action 序列）+ runner（按序执行 Action）+ runtime（SSH 调度、并发与冲突检测、日志 pubsub）组成的核心。
- **SSH_Backend**: 通过 golang.org/x/crypto/ssh + pkg/sftp 直连目标主机执行命令与文件操作的执行后端。复用 df-build-system 现有的 WebSSH/SFTP 依赖。
- **SSE（Server-Sent Events）**: 服务端向客户端单向推送实时数据的协议，用于部署日志实时流式传输。
- **Manifest**: resources/manifest.yml，记录每个离线资源文件的 sha256 与 bundle_version，用于完整性校验。

## Requirements

### Requirement 1: Replace Existing Infrastructure Stub Module

**User Story:** As a system maintainer, I want the existing "基础设施" stub module to be cleanly replaced by the new "部署管理" module, so that there is a single, coherent deployment subsystem in df-build-system.

#### Acceptance Criteria

1. THE Deployment_Management_Module SHALL replace the existing `backend/internal/deployer/` package (engine.go / checker.go / registry.go / types.go) and `backend/internal/handler/deploy.go` handler.
2. THE Deployment_Management_Module SHALL replace the existing `backend/internal/model/deploy.go` types (DeployPlan / ComponentAssignment / DeployExecution / DeployLog / EnvironmentInfo) with new GORM models defined under the new module.
3. THE Frontend_Application SHALL replace the existing 4 stub pages (InfraCheckView / InfraPlanView / InfraExecuteView / InfraEnvironmentView) and rename the sidebar menu group from "基础设施" to "部署管理".
4. WHEN the migration is complete, THE Frontend_Router SHALL redirect legacy paths `/infra/*` to the new `/deployment/*` paths so existing bookmarks do not 404.
5. THE Backend_Service SHALL NOT register routes under the legacy `/api/deploy` prefix; all new APIs SHALL be under `/api/deployment`.

---

### Requirement 2: Reuse Server Management for Deployment Targets

**User Story:** As an operator, I want deployment target hosts to come directly from the existing Server Management feature, so that I do not maintain two separate host registries.

#### Acceptance Criteria

1. THE Deployment_Target_Service SHALL reference Server records (existing `model.Server`) by `server_id` instead of maintaining a duplicate HostSpec table.
2. WHEN a user binds Deployment_Component to target hosts, THE Deployment_Target_Service SHALL present the existing Server Management list as the host selection source.
3. THE Deployment_Target_Service SHALL persist `Component_Target` rows as `(component_code, server_id)` pairs in PostgreSQL, with referential integrity to the Server table.
4. WHEN a Server record is deleted, THE Deployment_Target_Service SHALL block the delete IF that server is currently bound to any Deployment_Component, returning a clear error listing the bound components.
5. THE Deployment_Target_Service SHALL reuse the SSH credentials (host, port, user, password/private key) stored on the Server record; deployment management SHALL NOT store a second copy.
6. WHEN the deployment engine connects to a target host, THE SSH_Backend SHALL load credentials from the Server record at execution time so credential updates take effect without re-binding.

---

### Requirement 3: Global Config and Component Parameters

**User Story:** As an operator, I want to manage cluster-level global config and per-component parameter overrides through the Web UI, so that the deployment engine can render runtime templates correctly.

#### Acceptance Criteria

1. THE Global_Config_Service SHALL expose `GET /api/deployment/global-config` returning all cluster-level parameters (cluster.remote_root, network, secrets, etc.) as a single document.
2. THE Global_Config_Service SHALL expose `PUT /api/deployment/global-config` to update the document atomically.
3. THE Component_Override_Service SHALL expose CRUD APIs (`GET/PUT/DELETE /api/deployment/overrides/{component}`) to manage per-component parameter overrides.
4. THE Deployment_Engine SHALL merge parameters in the order: component defaults → Global_Config → Component_Override; later sources SHALL override earlier ones.
5. IF a password parameter contains shell metacharacters (`'`, `"`, `\`, `$`, backtick, space), THEN THE Global_Config_Service SHALL reject the update with a validation error before persistence.
6. WHEN Global_Config or Component_Override is updated, THE Deployment_Engine SHALL pick up the new values on the next deployment run without restart.

---

### Requirement 4: Offline Bundle Management

**User Story:** As an operator, I want to upload and verify offline bundles through the Web UI, so that the deployment engine has all required resources locally and integrity is provable.

#### Acceptance Criteria

1. THE Offline_Bundle_Service SHALL expose `POST /api/deployment/offline/upload` accepting an offline bundle archive.
2. THE Offline_Bundle_Service SHALL expose `POST /api/deployment/offline/install` accepting either an uploaded archive id or a server-local path; either source path SHALL be validated against an allow-list to prevent arbitrary file reads.
3. WHEN a bundle is installed, THE Offline_Bundle_Service SHALL verify every file's sha256 against `manifest.yml` AND verify `bundle_version` matches the platform's expected range.
4. IF any sha256 mismatch is detected, THEN THE Offline_Bundle_Service SHALL abort installation, leave the existing Resource_Dir untouched, and return a list of mismatched files.
5. WHEN sha256 verification passes, THE Offline_Bundle_Service SHALL atomically swap the new resource tree into Resource_Dir so a partial install never exposes a half-written tree.
6. THE Offline_Bundle_Service SHALL expose `GET /api/deployment/offline/status` returning the currently installed bundle_version, install time, and a summary of resource counts.
7. THE Backend_Service SHALL provide a CLI subcommand `dfctl-deploy verify` (or equivalent) that reproduces the same sha256 + manifest reverse check outside the Web UI.
8. THE Backend_Service SHALL provide a CLI subcommand `dfctl-deploy manifest gen` that scans Resource_Dir and writes a fresh `manifest.yml` with sha256 and bundle_version.

---

### Requirement 5: Deployment Execution Engine

**User Story:** As an operator, I want to start, preview, and cancel deployments of one or more components through the Web UI, so that I can roll out infrastructure with full visibility.

#### Acceptance Criteria

1. THE Deployment_Run_Service SHALL expose `POST /api/deployment/runs` accepting a list of component codes (or a Virtual_Component name) and an optional dry-run flag.
2. THE Deployment_Run_Service SHALL expose `POST /api/deployment/runs/preview` returning the planned Action sequence per host without executing any Action.
3. WHEN a deployment run starts, THE Deployment_Engine SHALL expand each Pipeline_YAML into an ordered Action list, group Actions by target host, and execute them via SSH_Backend.
4. THE Deployment_Engine SHALL stop the run immediately when any Action returns an error and SHALL leave the failing Action's component in a FAILED state.
5. THE Deployment_Engine SHALL stream every Action's stdout/stderr line to subscribers in near real time via SSE on `GET /api/deployment/runs/{id}/events`.
6. THE Deployment_Run_Service SHALL expose `GET /api/deployment/runs` (list, with pagination), `GET /api/deployment/runs/{id}` (detail), `GET /api/deployment/runs/{id}/logs` (paged log access), and `GET /api/deployment/runs/{id}/logs.txt` (full plain-text dump).
7. THE Deployment_Run_Service SHALL expose `POST /api/deployment/runs/{id}/cancel`, which SHALL cancel any in-flight Action via context cancellation and mark the run CANCELED.
8. THE Deployment_Engine SHALL persist a Deployment_Run row with start time, end time, triggering user, requested component set, final status, and a summary line per component.

---

### Requirement 6: Action Library Correctness

**User Story:** As a deployment author, I want a stable library of declarative Actions that the engine can execute consistently across hosts, so that pipeline YAML stays portable and reviewable.

#### Acceptance Criteria

1. THE Deployment_Engine SHALL implement at least the following Action types: `copy_file`, `copy_path`, `write_file`, `render_template`, `extract_archive`, `symlink`, `ensure_dir`, `chmod`, `chown`, `yum_package`, `rpm_package`, `systemd_service`, `system_user`, `system_group`, `sysctl_set`, `sysctl_restore`, `cron_line`, `kubectl_apply`, `kubectl_delete`, `kubernetes_artifacts`, `kubernetes_artifacts_check`, `http_check`, `tcp_check`, `resource_preflight`, `record_path_state`, `backup_file`, `restore_file`, `remove_path`, `remove_path_if_created`, `remove_path_if_untracked`, `remove_path_if_created_or_untracked`, `assert_path_absent_if_created`, `slb_config`, `run_command`, `fetch_file`, `copy_dir`.
2. THE `copy_file` Action SHALL read only from Resource_Dir; THE `copy_path` Action SHALL read only from the target host's local filesystem.
3. THE `render_template` Action SHALL render Go templates with the merged parameter context (component defaults → Global_Config → Component_Override) and write the result atomically to the target path.
4. THE `kubectl_apply` and `kubectl_delete` Actions SHALL be executed only on a host bound to the controller-render component and SHALL use the kubeconfig produced by controller-render.
5. WHEN an Action is executed, THE Deployment_Engine SHALL log the action type, target host, target path, exit code, duration, and the first error line on failure.
6. THE Deployment_Engine SHALL preserve at-least-once semantics per Action; an Action that fails after partial side-effects MUST be reversible by the corresponding rollback Action.

---

### Requirement 7: Rollback and State Machine

**User Story:** As an operator, I want each component to track whether it is deployed and to support a clean rollback, so that re-deployment is never destructive and partial state is always recoverable.

#### Acceptance Criteria

1. THE Component_Deploy_State_Service SHALL persist `(component_code, target_scope, status, last_run_id, last_changed_at)` rows in PostgreSQL, where `status ∈ {NOT_DEPLOYED, DEPLOYING, DEPLOYED, FAILED, ROLLING_BACK}`.
2. WHEN a deployment run targets a component whose current status is `DEPLOYED`, THE Deployment_Run_Service SHALL reject the run with a clear error instructing the user to rollback first.
3. THE Deployment_Run_Service SHALL expose `POST /api/deployment/runs/rollback` accepting one or more component codes (or a Virtual_Component name).
4. WHEN a rollback is executed, THE Deployment_Engine SHALL run the component's cleanup pipeline, which SHALL remove every artifact created by the deploy pipeline (rpm, directories, services, cron entries, symlinks).
5. THE rollback Action library (`record_path_state`, `backup_file`, `restore_file`, `remove_path_if_created`, `remove_path_if_untracked`, `remove_path_if_created_or_untracked`, `assert_path_absent_if_created`) SHALL persist its markers under State_Dir on the **target server's local filesystem**, NOT in PostgreSQL.
6. WHEN a rollback finishes successfully for all hosts in scope, THE Component_Deploy_State_Service SHALL transition the component status to `NOT_DEPLOYED`.
7. WHERE a Virtual_Component rollback completes for all real components, THE Component_Deploy_State_Service SHALL synchronize the virtual component's aggregate status accordingly.
8. IF a rollback Action fails partway, THEN THE Component_Deploy_State_Service SHALL leave the component in `FAILED` state with a message pointing to the failing Action; subsequent rollback retries SHALL be allowed.
9. WHEN the df-build-system binary is migrated to a new host, THE migration documentation SHALL state that PostgreSQL schema AND State_Dir MUST both be migrated together; migrating only the database SHALL be considered an unsupported configuration.

---

### Requirement 8: Host Binding Conflict Validation

**User Story:** As an operator, I want the system to enforce known component co-location conflicts at bind time, so that I cannot accidentally schedule conflicting components onto the same host.

#### Acceptance Criteria

1. THE Component_Target_Service SHALL maintain a built-in conflict matrix including at minimum: `etcd ↔ postgresql ↔ dns`, `redis ↔ df-ops`, `postgresql ↔ df-ops`.
2. THE Component_Target_Service SHALL maintain a rule that K8s-system components (`controller-render`, `containerd`, `etcd`, `kube-lb`, `master`, `node`, `calico`, `plugin`) and non-K8s business components (`postgresql`, `redis`, `rabbitmq`, `elasticsearch`, `minio`, `nacos`, `nfs`, `ftp`, `dns`, `skywalking`, `df-ops`, `docker`, `nexus`, `slb`) SHALL NOT share the same target server.
3. WHEN a user attempts to bind a Deployment_Component to a server that already hosts a conflicting component, THE Component_Target_Service SHALL reject the binding with an error message naming the conflict pair and conflict reason (service name / data dir / install path).
4. THE Component_Target_Service SHALL expose `POST /api/deployment/host-checks` to run pre-deploy validation across all current bindings and return a list of conflicts, missing bindings, and unreachable hosts.
5. WHEN a deployment run is created, THE Deployment_Run_Service SHALL re-run the conflict matrix check and refuse to start the run IF any conflict is detected.
6. THE Component_Target_Service SHALL allow operators to view the recommended host topology presets (3-node split, 6+ node split, all-in-one) as read-only references in the UI.

---

### Requirement 9: Deployment Management Frontend in df-build-system Style

**User Story:** As a Web UI user, I want the deployment management pages to look and feel consistent with the rest of df-build-system, so that I do not need to learn two different interfaces.

#### Acceptance Criteria

1. THE Frontend_Application SHALL provide pages under `/deployment/*`: dashboard, components, host bindings, global config, offline, runs (list), run detail.
2. THE Frontend_Application SHALL NOT port his-deploy's existing 8 Vue views verbatim; pages SHALL be rewritten using df-build-system's existing conventions (`page-title` heading, `content-card` containers, `el-table` with the project's standard column patterns, `el-form` layouts, the project's axios `request` wrapper, and the project's `formatTimeStr` utility for all timestamps).
3. THE Frontend_Application SHALL inject the JWT token via the existing axios request wrapper; no separate auth layer SHALL be introduced for deployment management.
4. THE Frontend_Application SHALL render deployment run logs through the existing SSE pattern used elsewhere (e.g., the pipeline detail page), reusing the project's SSE composable rather than introducing a new one.
5. THE Frontend_Application SHALL register the new pages under a sidebar group named "部署管理" with the menu items: 概览 / 组件 / 主机绑定 / 全局配置 / 离线包 / 部署运行.
6. THE Frontend_Application SHALL display all timestamps in the `YYYY-MM-DD HH:mm:ss` format using the existing `frontend/src/utils/time.ts` helper.
7. THE Frontend_Application SHALL surface the conflict matrix violations and offline bundle integrity errors as friendly inline messages, not raw backend payloads.
8. WHEN a deployment run is in `RUNNING` state, THE Frontend_Application SHALL show a live-updating log panel and a Cancel button that calls the cancel API.

---

### Requirement 10: Backend Integration with df-build-system Stack

**User Story:** As a backend engineer, I want the migrated deployment engine to fit cleanly into df-build-system's existing stack, so that we do not maintain two web frameworks or two persistence strategies.

#### Acceptance Criteria

1. THE Backend_Service SHALL expose all deployment management APIs through the existing Gin router and existing JWT middleware; chi router SHALL NOT be introduced.
2. THE Backend_Service SHALL persist all deployment management business data (Deployment_Run, Component_Deploy_State, Component_Target, Component_Override, Global_Config, Deployment_Log, Offline_Bundle metadata) in PostgreSQL through GORM, integrated with the existing `repository` and `migrate.go` / `seed_*.go` mechanisms.
3. THE Backend_Service SHALL package the migrated Go code under `backend/internal/deploy/` with sub-packages mirroring the his-deploy layout (`planner`, `runner`, `executor`, `runtime`, `exec`, `actions`, `virtualcomponents`, `render`, `defaults`, `offline`).
4. THE Backend_Service SHALL change all `dfctl` import paths to `df-build-server/internal/deploy/...` and SHALL NOT keep dual import paths.
5. THE Backend_Service SHALL reuse `golang.org/x/crypto/ssh` and `github.com/pkg/sftp` from the existing dependency graph; no new SSH library SHALL be added.
6. THE Backend_Service SHALL emit deployment SSE events through the existing `handler/sse.go` infrastructure (or a thin wrapper consistent with it).
7. THE Backend_Service SHALL return errors using the project's unified response format; raw chi-style error responses SHALL NOT leak through.
8. WHEN `go build ./...` is run on the backend, THE Backend_Service SHALL compile cleanly with no references to `dfctl`, no references to chi, and no references to SQLite.

---

### Requirement 11: Resource Tree and Pipeline Definitions

**User Story:** As a deployment author, I want the offline resources, component pipelines, and parameter defaults to be migrated as-is, so that the existing tested behavior of all 23 components is preserved.

#### Acceptance Criteria

1. THE Resource_Dir SHALL contain the migrated `resources/offline/` tree from his-deploy with directories for the 23 components: `calico`, `containerd`, `controller-render`, `df-ops`, `dns`, `docker`, `elasticsearch`, `etcd`, `ftp`, `kube-lb`, `master`, `minio`, `nacos`, `nexus`, `nfs`, `node`, `plugin`, `postgresql`, `prepare`, `redis`, `skywalking`, `slb`.
2. THE Resource_Dir SHALL contain the migrated `manifest.yml` reflecting the bundled tree's sha256 + bundle_version.
3. THE Backend_Service SHALL embed the per-component default parameter files (originally `config/components/*.yml` for `containerd`, `df-ops`, `docker`, `elasticsearch`, `ftp`, `minio`, `nacos`, `nexus`, `nfs`, `postgresql`, `rabbitmq`, `redis`, `skywalking`) into the binary so they ship without external files.
4. THE Backend_Service SHALL embed each component's pipeline YAML into the binary so deployment runs do not depend on external YAML files at runtime.
5. THE Resource_Dir layout and read paths SHALL match the contracts described in `docs/dfctl-v2-resource-layout.md` and `docs/offline-resource-layout.md` from his-deploy.
6. WHEN a Resource_Dir is changed by `offline/install`, THE Backend_Service SHALL invalidate any in-memory caches of pipeline YAML or resource paths.

---

### Requirement 12: Migration and Cutover

**User Story:** As a project maintainer, I want a controlled cutover from the old infra stub module to the new deployment management module, so that the migration is reversible and reviewable.

#### Acceptance Criteria

1. THE Backend_Service SHALL deliver the migration in clearly separated commits per phase: engine port (no DB/Web), storage rewrite (GORM + PG), web layer adaptation (Gin + JWT + SSE), frontend rewrite, resources migration, cleanup of legacy code.
2. THE Frontend_Application SHALL deliver the new pages first behind the new `/deployment/*` routes and SHALL only remove the old `/infra/*` views once the new pages are functional.
3. THE Backend_Service SHALL include integration tests covering: action execution against a local backend (no SSH), conflict matrix enforcement, state machine transitions, offline bundle sha256 mismatch handling, and rollback marker file lifecycle.
4. THE migration SHALL document explicitly that his-deploy's chi router, SQLite store, hand-written embedded frontend, and CLI `install` subcommand are dropped in favor of df-build-system's Gin / GORM+PG / Vue rewrite / single-binary deployment.
5. THE migration SHALL document that legacy his-deploy SQLite databases and state_dir contents from existing deployments are NOT automatically migrated; operators with running his-deploy installs require a separate, manual data migration procedure that is out of scope for this feature.
6. AFTER migration is complete, THE Backend_Service SHALL not contain any code, dependency, or documentation referencing the legacy `internal/deployer/` package, the legacy `model/deploy.go` types, or the legacy `/api/deploy` route prefix.

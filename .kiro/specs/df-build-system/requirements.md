# Requirements Document

## Introduction

DF 构建发布系统是一套自研构建发布平台，用于替代 Jenkins + Python 脚本的传统构建方式。系统采用 Go 后端 + Vue 3 前端架构，通过"应用注册 → 任务配置 → 流水线执行"的工作流，为测试工程师提供构建触发、实时日志、批量发布、远端同步/打包等能力。系统具备并发控制、日志归档、凭据加密、通知推送等运维特性。

## Glossary

- **Application（应用）**: 一个可独立构建部署的软件单元，对应一个 Git 仓库。应用有项目类型（Java/Vue）和应用角色（Vue 专属：主应用/子应用/独立应用）。
- **Task（任务）**: 一个可重复执行的构建配置，关联一个应用，指定分支、执行器、远端服务器和远端路径。任务是触发构建的入口。
- **Pipeline（流水线）**: 一次具体的构建执行实例，由 Task 触发产生。包含触发人、Git SHA、各 Stage 状态、日志、耗时、产物路径等元数据。
- **Stage（阶段）**: 流水线中的一个原子执行步骤。Stage 按固定顺序串行执行，前一个 Stage 失败则后续 Stage 标记为 skipped。
- **Executor（执行器）**: 执行编译类 Stage 的 Docker 容器环境，配置有 Docker 镜像、CPU/内存限制。
- **Remote_Server（远端服务器）**: 构建产物上传的目标服务器，以及远端同步/打包操作的执行目标。通过 SSH 连接，凭据加密存储。
- **Build（构建）**: Pipeline 的同义词，指一次流水线执行实例。
- **Notification_Webhook（通知 Webhook）**: 构建事件的通知渠道配置，支持钉钉和企业微信类型。
- **Pipeline_Template（流水线模板）**: 预定义的构建配置模板，包含名称、编码、分类（Java/Vue/工具）、描述和默认配置项（构建命令、产物目录、执行器、默认镜像等）。
- **Artifact（制品）**: 构建成功后产生的可部署文件（.jar 或 .zip），上传到远端服务器的指定路径。
- **SSE（Server-Sent Events）**: 服务端向客户端单向推送实时数据的协议，用于构建日志和远程操作日志的实时流式传输。
- **Vue_Role（Vue 应用角色）**: Vue 类型应用的角色分类，决定产物命名规则。主应用产物名为 `web-main.zip`，子应用产物名为 `{编号}.zip`，独立应用产物名为 `{appName}.zip`。

## Requirements

### Requirement 1: User Authentication

**User Story:** As a test engineer, I want to log in to the system with username and password, so that I can securely access build and release features.

#### Acceptance Criteria

1. WHEN a user submits valid username and password, THE Authentication_Service SHALL issue a JWT token and return it to the client.
2. WHEN a user submits invalid credentials, THE Authentication_Service SHALL return an authentication failure message within 2 seconds without revealing which field is incorrect.
3. WHILE a user session is active, THE Authentication_Service SHALL allow access to all system features based on the valid token.
4. WHEN a session token expires, THE Authentication_Service SHALL return HTTP 401 status, causing the client to redirect to the login page.
5. WHEN a user clicks logout, THE Authentication_Service SHALL invalidate the token and confirm logout success.

---

### Requirement 2: Personal Center

**User Story:** As a test engineer, I want to view and modify my personal information and change my password, so that I can keep my account information up to date.

#### Acceptance Criteria

1. THE User_Service SHALL provide an API to retrieve the current user's profile including username, email, phone number, and department.
2. WHEN a user updates personal information, THE User_Service SHALL allow modification of email, phone number, and department fields.
3. WHEN a user requests a password change, THE User_Service SHALL send a verification code to the user's registered email address.
4. WHEN a user submits a valid verification code and new password, THE User_Service SHALL update the password and invalidate all existing tokens for that user.
5. IF a user submits an invalid or expired verification code, THEN THE User_Service SHALL reject the password change request with an error message.

---

### Requirement 3: Application Management

**User Story:** As a test engineer, I want to manage the application registry, so that I can register applications for building.

#### Acceptance Criteria

1. THE Application_Service SHALL display a list of all registered applications with columns: application name, project type (Java/Vue), application role (Vue only), and Git repository URL.
2. WHEN a user creates a new application, THE Application_Service SHALL require: application name (unique), project type (Java or Vue), and Git repository URL.
3. WHERE the project type is Vue, THE Application_Service SHALL additionally require: application role (主应用/子应用/独立应用).
4. WHERE the application role is 子应用, THE Application_Service SHALL additionally require: application code number (用于产物命名).
5. WHEN a user edits an application, THE Application_Service SHALL allow modification of all fields except application name.
6. WHEN a user deletes an application, THE Application_Service SHALL remove the application record and retain historical build records for the configured log retention period.
7. THE Application_Service SHALL support searching applications by name and filtering by project type.
8. THE Application_Service SHALL derive the artifact name based on application role: 主应用 produces `web-main.zip`, 子应用 produces `{appCode}.zip`, 独立应用 produces `{appName}.zip`, Java applications produce `{appName}.jar`.

---

### Requirement 4: Task Management

**User Story:** As a test engineer, I want to create and manage build tasks, so that I can configure reusable build configurations for applications.

#### Acceptance Criteria

1. THE Task_Service SHALL display a list of all tasks with columns: task name, application name, task branch, last task status, last execution time, and last duration.
2. WHEN a user creates a new task, THE Task_Service SHALL require: task name (unique), associated application (selected from application list), branch, executor (selected from executor list), target remote servers (multi-select from server list), and remote upload path.
3. WHEN a user edits a task, THE Task_Service SHALL allow modification of all fields except task name.
4. WHEN a user deletes a task, THE Task_Service SHALL remove the task record and retain historical build records.
5. THE Task_Service SHALL support searching tasks by name and filtering by application type.
6. THE Task_Service SHALL provide a "history" action per task that navigates to build history filtered by the associated application.

---

### Requirement 5: Build Trigger (Single Execution)

**User Story:** As a test engineer, I want to trigger a build for a single task, so that I can deploy a specific version to the target servers.

#### Acceptance Criteria

1. WHEN a user triggers a single task execution, THE Build_Service SHALL present a dialog requiring: Git branch and remote upload path (pre-filled from task defaults).
2. WHEN a build is triggered, THE Build_Service SHALL create a pipeline record with status "PENDING", recording trigger time, trigger user, application name, branch, remote path, and target servers.
3. WHEN the number of running builds is below the global concurrency limit, THE Build_Scheduler SHALL dequeue the oldest pending build and start execution.
4. WHEN the number of running builds equals the global concurrency limit, THE Build_Scheduler SHALL keep the build in "PENDING" status until a slot becomes available.
5. WHEN a build starts execution, THE Build_Service SHALL update the status to "RUNNING" and record the start time.

---

### Requirement 6: Batch Execution

**User Story:** As a test engineer, I want to trigger builds for multiple tasks at once, so that I can release a coordinated set of services efficiently.

#### Acceptance Criteria

1. WHEN a user selects multiple tasks and triggers batch execution, THE Batch_Build_Service SHALL present a dialog requiring: Git branch and remote upload path (applied to all selected tasks).
2. THE Batch_Build_Service SHALL create independent pipeline records for each selected task.
3. THE Build_Scheduler SHALL execute batch builds subject to the global concurrency limit, processing them in FIFO order.
4. IF one task's build fails in a batch, THEN THE Batch_Build_Service SHALL NOT affect the execution of other tasks in the same batch.

---

### Requirement 7: Pipeline Execution Engine

**User Story:** As a test engineer, I want builds to execute stages sequentially with fail-fast behavior, so that I get quick feedback on failures.

#### Acceptance Criteria

1. WHEN a Java application build starts, THE Pipeline_Engine SHALL execute stages in order: (1) Check Remote Directory, (2) Clean Workspace, (3) Git Clone, (4) Gradle Build, (5) Upload Artifact, (6) Notify.
2. WHEN a Vue application build starts, THE Pipeline_Engine SHALL execute stages in order: (1) Check Remote Directory, (2) Clean Workspace, (3) Git Clone, (4) Yarn Install + Build, (5) Zip Package, (6) Upload Artifact, (7) Notify.
3. WHILE a stage is executing, THE Pipeline_Engine SHALL capture stdout and stderr output in real time.
4. WHEN a stage completes with exit code 0, THE Pipeline_Engine SHALL mark the stage as "success" and proceed to the next stage.
5. WHEN a stage completes with a non-zero exit code, THE Pipeline_Engine SHALL mark the stage as "failed", mark all subsequent stages as "skipped", and mark the pipeline as "FAILED".
6. WHEN all stages complete successfully, THE Pipeline_Engine SHALL mark the pipeline as "SUCCESS".
7. WHEN a compilation stage (Gradle Build or Yarn Install + Build) executes, THE Pipeline_Engine SHALL run the command inside the configured Docker container on the assigned Executor.
8. WHEN a non-compilation stage executes, THE Pipeline_Engine SHALL run the operation directly in the backend service process.
9. THE Pipeline_Engine SHALL record the start time, end time, exit code, and duration for each stage.
10. WHEN a build exceeds the configured build timeout, THE Pipeline_Engine SHALL terminate the running stage, mark the pipeline as "FAILED", and record the timeout as the error reason.

---

### Requirement 8: Vue Artifact Naming

**User Story:** As a test engineer, I want Vue application artifacts to be named according to the application role rules, so that the deployment structure is consistent.

#### Acceptance Criteria

1. WHEN a 主应用 (e.g., web-main) build completes the Zip stage, THE Pipeline_Engine SHALL produce an artifact named `web-main.zip`.
2. WHEN a 子应用 (e.g., web-menzhenysz with code 04) build completes the Zip stage, THE Pipeline_Engine SHALL produce an artifact named `{appCode}.zip` (e.g., `04.zip`).
3. WHEN a 独立应用 (e.g., web-cdr) build completes the Zip stage, THE Pipeline_Engine SHALL produce an artifact named `{appName}.zip` (e.g., `web-cdr.zip`).
4. WHEN a Java application build completes the Gradle Build stage, THE Pipeline_Engine SHALL produce an artifact named `{appName}.jar`.

---

### Requirement 9: Build Queue

**User Story:** As a test engineer, I want to see the current build queue, so that I can understand what is running and what is waiting.

#### Acceptance Criteria

1. THE Build_Queue_Service SHALL provide a list of all running builds with: task number, application name, branch, current stage, elapsed time, trigger user, and start time.
2. THE Build_Queue_Service SHALL provide a list of all pending builds with: queue position, task number, application name, branch, trigger user, and submit time.
3. WHEN a user cancels a pending build, THE Build_Service SHALL remove it from the queue and mark it as "CANCELED".
4. THE Build_Queue_Service SHALL display the current global concurrency limit.
5. WHEN a pending build starts execution, THE Build_Queue_Service SHALL move it from the pending list to the running list.

---

### Requirement 10: Build History

**User Story:** As a test engineer, I want to view build history, so that I can track past deployments and their outcomes.

#### Acceptance Criteria

1. THE Build_History_Service SHALL display a list of all historical builds with columns: task number, application name, branch, status, error stage (if failed), trigger user, duration, and trigger time.
2. THE Build_History_Service SHALL support filtering by application name.
3. THE Build_History_Service SHALL support filtering by status (SUCCESS, FAILED, CANCELED).
4. WHEN a user clicks on a historical build, THE Build_History_Service SHALL navigate to the Pipeline Detail view.
5. WHEN navigating from a task's "history" button, THE Build_History_Service SHALL automatically apply the application name filter.

---

### Requirement 11: Pipeline Detail and Real-Time Logs

**User Story:** As a test engineer, I want to view pipeline execution details with real-time logs, so that I can monitor builds and diagnose failures quickly.

#### Acceptance Criteria

1. THE Pipeline_Detail_Service SHALL provide pipeline metadata: task number, status, application name, branch, Git SHA, trigger user, duration, and timestamps.
2. THE Pipeline_Detail_Service SHALL provide a stage list with each stage's status (pending/running/success/failed/skipped).
3. WHEN a user requests logs for a specific stage, THE Log_Service SHALL return the archived log content for that stage.
4. WHILE a build is running, THE Log_Service SHALL push stage output to the client via SSE in real time.
5. THE Log_Service SHALL store logs segmented by stage, with each segment containing the full stdout and stderr output.
6. THE Log_Service SHALL archive logs per stage upon stage completion.

---

### Requirement 12: Artifact Records

**User Story:** As a test engineer, I want to view all successful build artifacts, so that I can track what was deployed and where.

#### Acceptance Criteria

1. THE Artifact_Service SHALL display a list of all successful build artifacts with columns: task number, application name, artifact name, branch, Git SHA, upload path, target servers, duration, and completion time.
2. WHEN a build completes successfully and uploads an artifact, THE Artifact_Service SHALL create an artifact record with all metadata.
3. THE Artifact_Service SHALL support searching and filtering artifact records.

---

### Requirement 13: Remote Sync

**User Story:** As a test engineer, I want to perform remote file synchronization via the UI, so that I can copy files between directories on remote servers without using Linux commands.

#### Acceptance Criteria

1. WHEN a user submits a remote sync request, THE Remote_Sync_Service SHALL require: source path, destination path, target servers (multi-select), and exclude rules.
2. THE Remote_Sync_Service SHALL execute `rsync -avz --exclude=<rules> <source> <destination>` on each selected target server via SSH.
3. WHILE a remote sync is executing, THE Remote_Sync_Service SHALL stream the rsync output to the UI in real time via SSE.
4. WHEN the rsync command completes with exit code 0, THE Remote_Sync_Service SHALL report success.
5. IF the rsync command fails, THEN THE Remote_Sync_Service SHALL report the error output to the user.

---

### Requirement 14: Remote Package

**User Story:** As a test engineer, I want to create tar.gz archives on remote servers via the UI, so that I can package directories without using Linux commands.

#### Acceptance Criteria

1. WHEN a user submits a remote package request, THE Remote_Package_Service SHALL require: remote directory path and target server.
2. THE Remote_Package_Service SHALL execute `tar -czvf <archive_name>.tar.gz -C <parent_dir> <dir_name>` on the target server via SSH.
3. WHILE a remote package operation is executing, THE Remote_Package_Service SHALL stream the tar output to the UI in real time via SSE.
4. WHEN the tar command completes with exit code 0, THE Remote_Package_Service SHALL report success and display the output archive path.
5. IF the tar command fails, THEN THE Remote_Package_Service SHALL report the error output to the user.

---

### Requirement 15: Global Settings

**User Story:** As a test engineer, I want to configure global system parameters, so that I can tune system behavior to match team needs.

#### Acceptance Criteria

1. THE Settings_Service SHALL provide a configurable global concurrency limit with a default value of 5 and valid range of 1 to 20.
2. THE Settings_Service SHALL provide a configurable build timeout in seconds with a default value of 1800.
3. THE Settings_Service SHALL provide a configurable log retention period in days with a default value of 30.
4. THE Settings_Service SHALL provide a configurable default upload path.
5. THE Settings_Service SHALL provide a configurable "clean workspace after build" toggle.
6. WHEN any setting is changed, THE Settings_Service SHALL persist the change immediately.
7. WHEN the concurrency limit is changed, THE Build_Scheduler SHALL apply the new limit to subsequent scheduling decisions without affecting currently running builds.

---

### Requirement 16: Template Management

**User Story:** As a test engineer, I want to manage pipeline templates, so that I can define reusable build configurations for different project types.

#### Acceptance Criteria

1. THE Template_Service SHALL display a list of all templates in card format showing: name, code, category, and description.
2. WHEN a user creates a new template, THE Template_Service SHALL require: name, code (unique), category (Java/Vue/工具), and description.
3. THE Template_Service SHALL store default configuration items per template including: build command, artifact directory, executor type, and default Docker image.
4. WHEN a user edits a template, THE Template_Service SHALL allow modification of all fields including default configuration items.
5. WHEN a user deletes a template, THE Template_Service SHALL remove the template record.

---

### Requirement 17: Executor Management

**User Story:** As a test engineer, I want to manage Docker executors, so that I can configure where compilation stages run.

#### Acceptance Criteria

1. THE Executor_Service SHALL display a list of configured executors with columns: name, Docker image, type (本地/远程), CPU limit, memory limit, and status (在线/离线).
2. WHEN a user adds an executor, THE Executor_Service SHALL require: name (unique), Docker image, type, CPU limit, and memory limit.
3. WHEN a user edits an executor, THE Executor_Service SHALL allow modification of all fields.
4. WHEN a user deletes an executor, THE Executor_Service SHALL remove the executor record.
5. THE Executor_Service SHALL periodically check executor availability and update the status (在线/离线) accordingly.

---

### Requirement 18: Remote Server Management

**User Story:** As a test engineer, I want to manage remote target servers, so that I can configure where artifacts are uploaded and where remote operations execute.

#### Acceptance Criteria

1. THE Remote_Server_Service SHALL display a list of configured remote servers with columns: name, host address, port, username, authentication method, and connection status.
2. WHEN a user adds a remote server, THE Remote_Server_Service SHALL require: name (unique), host address, SSH port, username, and authentication method (password or SSH key).
3. WHEN a user edits a remote server, THE Remote_Server_Service SHALL allow modification of all connection parameters.
4. WHEN a user deletes a remote server, THE Remote_Server_Service SHALL remove the server record.
5. THE Remote_Server_Service SHALL store credentials securely using encryption at rest.
6. WHEN a user tests a remote server connection, THE Remote_Server_Service SHALL attempt an SSH connection and report success or failure with error details.

---

### Requirement 19: Notification Management

**User Story:** As a test engineer, I want to configure build notification webhooks, so that the team is informed of build events through DingTalk or WeCom.

#### Acceptance Criteria

1. THE Notification_Service SHALL display a list of configured webhooks with columns: name, type (钉钉/企微), URL, success notification toggle, failure notification toggle, and enabled status.
2. WHEN a user creates a webhook, THE Notification_Service SHALL require: name, type (钉钉 or 企微), webhook URL, success notification toggle, and failure notification toggle.
3. WHEN a user edits a webhook, THE Notification_Service SHALL allow modification of all fields.
4. WHEN a user deletes a webhook, THE Notification_Service SHALL remove the webhook record.
5. WHEN a user tests a webhook, THE Notification_Service SHALL send a test message to the configured URL and report success or failure.
6. THE Notification_Service SHALL allow enabling or disabling individual webhooks without deleting them.
7. WHEN a build completes with SUCCESS status and the associated webhook has success notification enabled, THE Notification_Service SHALL send a success message containing: application name, branch, trigger user, and duration.
8. WHEN a build completes with FAILED status and the associated webhook has failure notification enabled, THE Notification_Service SHALL send a failure message containing: application name, branch, trigger user, error stage, and error message.

---

### Requirement 20: Dashboard

**User Story:** As a test engineer, I want to see a dashboard overview, so that I can quickly assess the current state of the build system.

#### Acceptance Criteria

1. THE Dashboard_Service SHALL provide statistics cards showing: registered application count, today's build count, today's success count, and today's failure count.
2. THE Dashboard_Service SHALL provide the most recent 6 completed builds with application name, status, branch, and completion time.
3. THE Dashboard_Service SHALL provide quick-entry links to: new release, application management, batch release, remote sync, remote package, and system settings.

---

### Requirement 21: Concurrency Control and Scheduling

**User Story:** As a test engineer, I want the system to limit concurrent builds, so that the build machine resources are not overwhelmed.

#### Acceptance Criteria

1. THE Build_Scheduler SHALL maintain a count of currently running builds across all tasks.
2. WHILE the running build count equals the global concurrency limit, THE Build_Scheduler SHALL hold new builds in "PENDING" status.
3. WHEN a running build completes (SUCCESS, FAILED, or CANCELED), THE Build_Scheduler SHALL immediately check the queue and start the next build if the queue is non-empty.
4. THE Build_Scheduler SHALL process the queue in FIFO order based on submit time.
5. THE Build_Scheduler SHALL treat each task in a batch execution as an independent build for concurrency counting purposes.

---

### Requirement 22: Log Retention and Cleanup

**User Story:** As a test engineer, I want logs to be automatically cleaned up, so that disk space is managed without manual intervention.

#### Acceptance Criteria

1. THE Log_Retention_Service SHALL delete build logs older than the configured retention period.
2. THE Log_Retention_Service SHALL use the default retention period of 30 days when not explicitly configured.
3. WHEN the retention period is changed in settings, THE Log_Retention_Service SHALL apply the new period to subsequent cleanup cycles.
4. THE Log_Retention_Service SHALL run cleanup automatically on a daily schedule.
5. THE Log_Retention_Service SHALL retain build metadata (status, timestamps, Git SHA) even after logs are deleted.

---

### Requirement 23: Build Workspace Cleanup

**User Story:** As a test engineer, I want the system to clean up build workspaces after completion, so that disk space is not consumed by stale build files.

#### Acceptance Criteria

1. WHILE the "clean workspace after build" setting is enabled, THE Pipeline_Engine SHALL delete the workspace directory after a build completes (regardless of success or failure).
2. WHILE the "clean workspace after build" setting is disabled, THE Pipeline_Engine SHALL retain the workspace directory after build completion.
3. THE Pipeline_Engine SHALL always clean the workspace directory at the start of a new build (Clean Workspace stage) regardless of the cleanup setting.

---

### Requirement 24: Build Timeout

**User Story:** As a test engineer, I want builds to be automatically terminated if they exceed the configured timeout, so that stuck builds do not block the queue indefinitely.

#### Acceptance Criteria

1. WHILE a build is running, THE Pipeline_Engine SHALL track the total elapsed time from build start.
2. WHEN the elapsed time exceeds the configured build timeout, THE Pipeline_Engine SHALL terminate the currently running stage process.
3. WHEN a build is terminated due to timeout, THE Pipeline_Engine SHALL mark the pipeline as "FAILED" with error message "Build timeout exceeded".
4. WHEN a build is terminated due to timeout, THE Build_Scheduler SHALL release the concurrency slot and process the next queued build.

---

### Requirement 25: Credential Security

**User Story:** As a test engineer, I want server credentials to be stored securely, so that sensitive authentication data is protected.

#### Acceptance Criteria

1. THE Credential_Service SHALL encrypt all stored passwords and SSH private keys using AES-256 encryption at rest.
2. THE Credential_Service SHALL decrypt credentials only at the moment of use (SSH connection establishment).
3. THE Credential_Service SHALL NOT return plaintext credentials in any API response.
4. WHEN a user views a remote server configuration, THE Remote_Server_Service SHALL mask the credential value in the response.

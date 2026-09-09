# CloudHIS 平台 — 当前功能与实现状态

核对日期：2026-09-09。依据前端路由、后端注册、业务实现与本地验证整理。本文替代早期按开发任务统计的进度表，不以旧任务勾选率推算完成度。

## 已实现功能

| 模块 | 当前实现 | 主要代码依据 |
|---|---|---|
| 应用与编译配置 | Java／Vue 应用 CRUD、查询与分页；主／子／独立 Vue 角色；Git、制品命名、环境标签、NodePort、Ingress、Nginx 配置；Docker／本机编译模式、执行器、模板及配置项管理 | [应用模型](backend/internal/model/application.go)、[编译配置接口](backend/internal/handler/build_config.go) |
| 流水线 | Git 拉取、Docker 编译、Vue 打包、制品上传、镜像构建／推送、K8s 部署与通知；保留仅上传能力；镜像更新统一先制作并推送，再单独滚动更新 | [阶段解析](backend/internal/pipeline/stage_resolver.go)、[执行引擎](backend/internal/pipeline/engine.go) |
| 调度与日志 | 单次／批量任务、FIFO 队列、并发槽位、取消、超时、启动恢复、构建历史、阶段日志与 SSE、日志保留清理 | [调度器](backend/internal/scheduler/scheduler.go)、[日志接口](backend/internal/handler/sse.go) |
| 制品与版本 | 上传、本地／远程目录导入、远程下载进度及取消；文件识别与应用匹配；最新制品库、版本详情、替换／重下载／删除、工作区版本同步 | [制品与批量更新接口](backend/internal/handler/batch_deploy.go)、[版本模型](backend/internal/model/artifact_version.go) |
| 批量更新 | 先上传并制作镜像，再单独滚动更新；同批 Vue 主／子应用合并到 web-main 流水线；记录更新前后镜像、业务版本和批次；后台批次回滚，业务版本仅采集展示 | [批量更新页面](frontend/src/views/BatchDeployView.vue)、[回滚服务](backend/internal/service/artifact_deploy_rollback.go) |
| Kubernetes 运维 | 概览、命名空间、Nodes、Deployments、Pods、Services、ConfigMaps、Ingress；Watch、日志、指标与 YAML；重启、扩缩容、镜像／资源调整、ConfigMap 编辑、Service 端口修改／删除、运行版本同步 | [K8s 接口](backend/internal/handler/kubernetes.go)、[Watch](backend/internal/handler/k8s_watch.go) |
| 服务器运维 | 服务器 CRUD、连接测试与记录；WebSSH；SFTP 浏览、上传、下载、新建目录、重命名、删除；node_exporter CPU、内存、负载、磁盘、网络等指标 | [服务器](backend/internal/handler/server_mgmt.go)、[终端](backend/internal/handler/webssh.go)、[文件](backend/internal/handler/websftp.go)、[监控](backend/internal/handler/server_monitor.go) |
| PostgreSQL | 配置实例状态及主从信息；SQL 解析、风险分类、schema 补全、编辑、单句／文件／批次执行、取消、跳过、历史、白名单与不可执行 SQL 导出 | [业务服务](backend/internal/service/postgresql_service.go)、[风险分析](backend/internal/service/sql_risk_analyzer.go) |
| 视图依赖变更 | 字段类型修改分析、依赖快照、预检查、变更与恢复脚本、事务／分步执行、单对象恢复与验证、执行步骤记录 | [依赖变更逻辑](backend/internal/service/sql_view_dependency.go)、[页面](frontend/src/views/PostgreSQLViewDependencyChangeView.vue) |
| 基础设施部署 | 组件卡片、全局配置、组件参数覆盖、主机绑定、冲突校验、预览／部署／回滚／阶段运行、取消、日志与 SSE；离线包上传、安装、校验；资源校验与 manifest CLI | [部署接口](backend/internal/deploy/handler/handler.go)、[组件注册](backend/internal/deploy/engine/virtualcomponents/components.go) |
| 平台基础 | JWT 登录、客户端退出、个人邮箱／电话／部门保存、密码哈希、凭据加密；工作台统计、钉钉／企微通知与测试发送、站内消息、标签页、暗色模式、健康检查、SPA 内嵌 | [认证](backend/internal/service/auth_service.go)、[通知](backend/internal/notification/sender.go)、[布局](frontend/src/layouts/DefaultLayout.vue) |

SQL 文件可以随制品导入，但不会进入微服务镜像部署；数据库执行由 PostgreSQL 模块单独处理。批次回滚恢复 Deployment 镜像，不回滚数据库业务数据。

## 已知缺口与实现边界

| 项目 | 当前状态与影响 | 代码依据 |
|---|---|---|
| 邮件验证码 | 有内存存储、过期与校验逻辑；验证码仅写后端日志，未接入 SMTP，接口返回发送成功不代表邮件已送达 | [认证服务](backend/internal/service/auth_service.go) |
| 页面修改密码 | 布局页已调用真实接口，但请求漏传后端必填的 `confirmPassword`，会被参数校验拒绝 | [页面请求](frontend/src/layouts/DefaultLayout.vue)、[请求结构](backend/internal/service/auth_service.go) |
| 首次登录改密 | 后端存在 `mustChangePassword` 字段；前端登录和路由未强制处理，不能承诺首次改密流程完成 | [用户模型](backend/internal/model/user.go)、[登录页](frontend/src/views/LoginView.vue)、[路由](frontend/src/router/index.ts) |
| 认证与权限 | 退出清除客户端 Token，后端未撤销已签发 JWT；未提供 Token 刷新接口、完整角色／项目权限或多级审批 | [认证接口](backend/internal/handler/auth.go)、[鉴权](backend/internal/middleware/auth.go) |
| 远程同步／打包 | SSH 后端接口仍已注册，工作台快捷链接仍存在，但对应前端路由和页面缺失 | [远程接口](backend/internal/handler/remote.go)、[工作台](frontend/src/views/DashboardView.vue)、[路由](frontend/src/router/index.ts) |
| 监控历史 | 当前直接读取 node_exporter 并刷新即时指标，未接入 Prometheus 历史区间查询和完整时间序列页面 | [监控接口](backend/internal/handler/server_monitor.go)、[页面](frontend/src/views/ServerMonitorView.vue) |
| 统计文案 | 工作台核心数据来自接口；部分比较文案和登录页展示数字是静态值，不能当作实际运营统计 | [工作台](frontend/src/views/DashboardView.vue)、[登录页](frontend/src/views/LoginView.vue) |
| 微前端更新 | 单实例按 namespace 串行完成 web-main 构建、推送与滚动部署；清单固定、失败只终止当前任务且其他目标继续、滚动更新前基线校验；部署失败不自动回滚，人工整批回滚用于更新后的业务异常 | [更新流程](docs/vue-app-build-flow.md) |
| 独立前端更新 | web-cdr／web-opm 仍走标准镜像分支，已统一根目录／dist ZIP 展平和入口检查；独立应用和共享前端均以指定镜像滚动完成为准，不再要求上线目录全量校验 | [完整流程及故障处理](docs/vue-app-build-flow.md) |
| 实例与集群配置 | 当前通过全局设置连接目标 PostgreSQL 和 Kubernetes，未实现任意多实例／多集群资产切换体系 | [环境页面](frontend/src/views/SettingsEnvironmentView.vue) |
| 发布产物 | `make build` 不重新生成前端，必须先复制 `frontend/dist` 到 `backend/internal/web/dist`；运行配置与离线资源需要另行提供，仓库没有 Compose 配置 | [构建步骤](README.md#构建单二进制版本) |

## 验证记录

2026-09-09 在本地源码分析时完成：

- 前端 `npm run build` 通过，包括 `vue-tsc -b` 和 Vite 生产构建。
- 后端 `go test ./...` 通过；运行时移除了测试数据库连接变量，依赖 PostgreSQL 的集成用例按 [测试辅助逻辑](backend/internal/testutil/postgres.go) 跳过。
- 未连接实际 Docker、SSH 主机、Kubernetes、业务 PostgreSQL 或通知服务做端到端验收；没有执行线上部署、SQL 变更或回滚。

本次文档对齐仅修改 Markdown，使用文档差异、相对链接和代码配置交叉检查验证，未将历史测试记录视为生产验收结果。

### 前端更新改造验证（2026-09-09）

- 已引入制品清单和 SHA-256、主／子应用按目录替换、未上传子应用保留、Pod 导出失败终止、上线文件内容校验。
- 修复第二步滚动更新时重复构建；使用明确恢复标识复用阶段，补充重复确认及构建 worker 退出前确认的回归测试。
- 后端 `go test ./...` 通过（未配置测试数据库的集成用例跳过）；最后的调度调整后，pipeline、scheduler、handler、repository、k8s 相关包复测通过。
- 临时独立 PostgreSQL 18 验证通过：`TestSchedulerConfirmationKeepsExistingStages`、`TestExecuteRejectsPartialFrontendSelectionBeforeCreatingPipelines`。测试数据库仅监听临时 Unix socket，完成后已停止，未使用业务数据库。
- 前端 `npm run build` 通过；`git diff --check` 通过。
- 未运行真实集群更新／回滚。静态目录的运行时写入、多副本差异、实际镜像拉取和业务可用性仍需环境验收；该轮上线目录校验已在后续简化中移除，业务可用性由业务人员判断。

## 文档维护约定

- 新功能同时更新本文、相关模块说明及页面入口；仅有设计或接口占位时不标为已实现。
- 功能状态以当前源码和实际验证为准；不沿用固定应用数、接口数或历史任务完成百分比。
- 历史需求与计划保留原文并标明性质，入口见 [README](README.md#验证与文档)。

### 更新错误处理修复验证（2026-09-09）

- 独立应用统一 ZIP 根目录／dist 展平、入口检查和本地目录替换；共享 web-main 镜像 COPY 前清理基础静态目录。
- Docker 响应非正常结束、日志读取异常、构建镜像无法确认、推送缺少完成 digest 或仓库 digest 不一致均返回失败；最新调整已移除 latest 标签制作与推送。
- ConfigMap、更新前镜像与记录保存错误停止当前任务并保留原始原因；版本信息采集失败仅作提示。部署进度按准备、提交中、已提交和滚动完成持久化，失败与重启不再把正在部署的记录标为构建失败／部署跳过。
- 滚动检查限定当前 Deployment 所有权和模板匹配的 ReplicaSet，忽略旧版本、无关及正在删除的 Pod；API 查询错误直接返回。
- 后端 `go test ./...` 通过（未提供测试数据库时，依赖数据库的用例按配置跳过）；`go test -race ./internal/docker -count=1` 通过。
- 临时 PostgreSQL 18 独立数据库实测通过：部署进度失败分类、更新前版本读取失败停止、更新后版本比较、阶段日志持久化与 SSE 一致性、重启后的部署状态与错误日志。临时数据库仅监听 Unix socket，测试后已停止。
- 未操作真实 Docker／镜像仓库／Kubernetes。基础镜像实际构建、仓库 Distribution API 权限和真实滚动发布仍需环境验收。
- 使用约束已明确：每个环境独立部署，由单人操作；更新成功后出现业务异常时人工按本次记录整批回滚。浏览器旧资源兼容由业务开发负责，不增加异常旧目录专用恢复入口。

### 制品仅允许更新一次（2026-09-09）

- 按上传版本号与文件名持久化使用记录，提交后禁用再次选择，接口拒绝重复更新。失败、取消、回滚以及工作区重新扫描不清除使用记录；旧部署记录同样视为已使用。
- 同名同内容重新上传生成新版本号，可以再次更新；未选中的制品仍可使用。阻止原地替换、删除或重下载已使用制品来重置状态。
- 临时 PostgreSQL 18 集成测试通过：实际上传与执行接口、重复提交不创建任务、失败回滚后仍拒绝、同内容重新上传可以创建任务、详情接口使用状态、混合选择拒绝的事务完整性、元数据重建后状态保留、旧部署记录兼容。
- 前端生产构建通过。本轮未执行真实 Docker／Kubernetes 部署。

### 批次内失败跳过、其他目标继续（2026-09-09）

- 提交前按实际部署目标隔离制品校验及使用记录错误：独立服务失败只跳过该服务；共享 web-main 组内任一输入失败跳过整组；其他目标继续。来源目录、请求参数错误仍拒绝请求；最新调整将无法识别的应用单独跳过。
- 准备阶段失败保存流水线状态、错误阶段、原始错误和结束时间；页面显示逐项提交错误及已创建数量，仅将实际接受的制品标记为已使用。
- 调度器保持现有行为：某个任务执行失败后释放执行槽并继续后续任务，不触发自动回滚。人工整批回滚用于更新成功后发现业务异常。
- 临时 PostgreSQL 18 集成测试通过：10 个混合制品的后端坏包、子应用坏包、已使用制品及复制失败均不阻止其他部署目标入队；真实调度器和引擎错误路径验证失败后继续下一任务（使用不支持的应用类型触发错误，无真实部署）；一次性制品及共享前端拒绝不完整输入的回归通过。
- 前端生产构建通过，后端 handler／scheduler 测试通过（普通运行未配置数据库的用例跳过，以上用例另用临时数据库实测）。未执行真实 Docker／Kubernetes 更新。

### 更新流程简化与基础错误处理（2026-09-09）

- 移除标准应用与共享 web-main 的 latest 标签制作及推送，只构建、推送并部署本次唯一镜像 tag；不删除仓库已有历史镜像。
- 更新前后及回滚后的版本信息仅采集展示，不比较、不影响执行结果；单次采集最多等待 3 秒，错误保存为 versionWarning 并在详情页独立展示。旧服务没有 Ready Pod 不再因版本读取阻断修复包更新。
- 移除共享前端滚动完成后的全量目录二次导出及摘要检查；保留构建前的输入和目录拼装检查。指定镜像滚动完成、应用就绪即完成部署。
- Java 脚本模板缺失、内容为空及写入失败明确报错，不再误报生成成功。应用查询失败只跳过该项，其他可识别目标继续。
- 流水线最终状态、部署记录及批次汇总保存错误明确返回或报告；当前订阅的流水线详情页展示 state_error，停止普通完成通知，不自动回滚或重新部署。
- 后端 go test ./... 通过（未配置数据库的集成用例跳过），前端 npm run build 通过。另用临时 PostgreSQL 18 独立数据库运行 11 项定向测试通过：版本信息非阻断、回滚采集、汇总写入失败、脚本准备错误、唯一标签构建推送、最终状态保存失败提示、混合批次继续、共享前端输入及部署进度回归。Docker 流程使用模拟 HTTP 接口，确认只构建一个标签并推送一次；没有操作真实 Docker／镜像仓库／Kubernetes。

### 镜像制作与滚动更新拆分（2026-09-09）

- 当前更新统一为两个独立操作：上传并制作、推送镜像；使用已保存镜像滚动更新。删除部署模式选择和源码任务自动／手动部署开关。
- 镜像推送完成即结束第一步，记录状态 IMAGE_READY（页面：镜像制作完成）和耗时。第二步复用已有成功阶段，不重新制作或推送镜像。
- 提供单目标、批次滚动更新接口及页面入口；部分目标制作失败不隐藏已完成镜像，单目标提交失败不阻断其他目标，共享 web-main 只提交一次。
- 已有镜像制作记录保留，历史模式按同样的两步流程执行；制作新镜像不自动取消其他已制作镜像。
- 验证通过：后端 `go test ./...`（未提供数据库时集成用例跳过）、前端生产构建、4 项前端检查；另用临时 PostgreSQL 18 独立数据库完成 8 项集成回归，包括两步边界、失败推送、混合批次、阶段复用、任务创建、一次性制品和共享前端。阶段边界测试使用模拟执行器，未执行真实 Docker／Kubernetes 部署。

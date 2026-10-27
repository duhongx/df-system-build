# CloudHIS 智能管理平台

面向 DF-HIS 系列应用的构建发布与运维管理平台。当前代码覆盖 Java／Vue 构建、制品版本与批量更新、Kubernetes 运维、服务器管理、PostgreSQL 变更以及基础设施离线部署。

本文按 2026-09-09 的源码核对；功能实现范围与已知缺口见 [PROGRESS.md](PROGRESS.md)。代码已实现不代表所有目标环境都已通过部署验收。

## 当前功能

| 模块 | 已实现能力 |
|---|---|
| 应用与编译配置 | 应用管理、Git 分支、Java／Vue 分类、微前端角色、服务器本机编译、安装／构建命令、环境变量、产物目录、配置项模板 |
| 构建发布 | 本机工具编译、打包、SSH/SFTP 上传、镜像构建与推送、K8s 部署；队列、并发控制、取消、超时、历史与实时日志 |
| 制品与批量更新 | 上传／目录导入／远程下载、应用匹配、最新制品库、更新版本、version.json 业务发布版本与说明、文件替换与重下载、制品仅更新一次（再次更新须重新上传）、上传并制作镜像、单独执行滚动更新、人工批次回滚与版本信息展示 |
| Kubernetes | 集群与资源查询、Pod 日志、指标、YAML 查看、重启、扩缩容、镜像调整、受限 Deployment 配置、ConfigMap 编辑、Service 端口维护 |
| 服务器 | 主机与连接管理、WebSSH、SFTP 文件操作、node_exporter 即时指标 |
| PostgreSQL | 实例信息；SQL／ZIP／服务器文件导入、按文件后台执行、遇错停止、失败重试、停止、跳过和历史；独立视图依赖变更、恢复与验证 |
| 基础设施部署 | 组件及主机绑定、参数配置、冲突检查、部署预览、SSH 执行、日志、回滚、离线包管理 |
| 平台基础 | JWT 登录、退出、个人资料、凭据加密、工作台统计、钉钉／企微通知、站内消息、日志清理、多标签页与暗色模式 |

平台使用单一管理员账号，已移除在线改密、邮件验证码及首次登录改密流程；远程同步／打包目前保留后端接口，但缺少对应前端路由。完整限制见 [当前进度与缺口](PROGRESS.md#已知缺口与实现边界)。

## 机房与客户现场

使用同一套代码和二进制，在“系统设置 → 全局参数 → 使用场景”切换：机房支持服务器本机源码编译及制品更新；客户现场只走 JAR／ZIP 制品更新，不依赖 Git、JDK、Gradle、Maven 或 Node。新安装默认客户现场模式，已有安装保留当前设置。

Docker 用于制作和推送应用部署镜像，不再用于运行编译工具。部署环境为“本地”时，镜像推送成功后自动滚动更新；其他环境保留镜像制作和滚动更新两步操作，详见 [使用场景](docs/usage-scenarios.md)。

## Kubernetes 应用配置

管理页面的 namespace 固定读取环境配置。Deployment 操作中的“配置”使用居中弹窗，默认以基本配置、容器资源和健康检查分组展示“可修改配置”；可切换“完整 Deployment”按原有字段层级查看全部内容，切换保留编辑草稿。“展开全部／收起全部”仅出现在完整视图；默认表单同时显示只读更新策略、maxSurge／maxUnavailable 及进度期限；可修改副本数，并按容器修改 CPU／内存的 requests、limits，已有 startupProbe／readinessProbe／livenessProbe 的 initialDelaySeconds，以及 startupProbe.failureThreshold。留空资源值表示移除该项，探针未配置时只展示“未配置”。其余字段置灰只读，包含 env／envFrom、volumeMounts／volumes、metadata、status 等；敏感值脱敏展示。列表不再保留独立“扩缩容”按钮。

默认 Java Deployment 的 startupProbe、readinessProbe、livenessProbe 均使用 TCP 8080，不依赖 `/actuator/health`，保留原有等待周期和失败阈值。未改动过的旧默认模板会自动升级；自定义模板保留。此项仅影响新建 Deployment 的模板，已有 Deployment 的镜像更新继续保留其现有探针配置。TCP 检查仅确认端口可连接，不验证业务健康，也不会关闭应用的 Actuator 接口。

`df-cdc` 固定使用 Recreate，其他已登记的 Java 微服务使用 RollingUpdate；无应用登记的第三方 Deployment 保持已有策略。策略由后端执行规则，页面不提供自由切换；镜像发布与回滚使用同一规则。既有 Deployment 不会在系统启动时批量改写，规则在相应更新提交时生效。

提交前展示变更差异并调用 Kubernetes dry-run，正式提交使用 UID／resourceVersion 检查，防止覆盖期间发生的其他修改。配置记录保存操作者、字段差异和提交结果；保存后跟踪控制器与副本状态，“已提交”与“已就绪”分别显示。配置记录不提供自动恢复功能，已有“回滚”操作仍是镜像回滚。

启动等待变长时自动上调不足的 progressDeadlineSeconds，平台部署等待按探针、进度期限及终止宽限时间计算，取消原固定 300 秒。流水线仍受全局构建超时约束（默认 30 分钟）；超过该范围的慢启动需要同步调整全局超时。

## 技术栈与结构

- 后端：Go 1.25+、Gin、GORM、PostgreSQL、Docker SDK、client-go、SSH/SFTP、JWT、Zap、Viper。SQL 解析依赖 `pg_query_go`，编译需要 CGO 和 C 编译器。
- 前端：Vue 3、Vite 5、TypeScript、Element Plus、Pinia、Vue Router、Axios、xterm。
- 部署：Go 二进制内嵌前端 SPA；运行配置、数据库、工作区和离线资源另行提供。

```text
backend/       Go API、流水线、调度、数据库变更与基础设施部署引擎
frontend/      Vue 页面、API 封装和前端构建
scripts/       指定环境的构建部署脚本
docs/          模块说明及历史需求、计划
```

## 本地开发

准备 Go 1.25+、C 编译器、Node.js/npm 和可连接的 PostgreSQL。执行构建发布等功能还需要 Docker、镜像仓库、SSH 或 Kubernetes 等对应依赖。

先按 [后端配置说明](backend/README.md#运行配置) 创建 `backend/config/config.yaml`，配置数据库、JWT 和 32 字节的 `DF_ENCRYPT_KEY`。配置文件不随仓库分发，即使使用环境变量覆盖也必须存在。

```bash
# 终端一，从项目根目录执行；先配置上述环境变量和文件
cd backend
make run
```

```bash
# 终端二，从项目根目录执行
cd frontend
npm ci
npm run dev
```

前端开发地址为 `http://localhost:3001`，当前 Vite 将 `/api` 代理到 `http://localhost:8080`，后端端口应与之匹配。WebSSH 使用 WebSocket；当前 Vite 代理未配置 `ws: true`，终端联调可使用后端直接提供的 SPA。

全新数据库的初始管理员账号为 `admin / cloudhis@2123`，供少量运维人员共享使用。更新程序不会覆盖已有账号的密码。平台不提供在线改密或邮件验证码功能。

## 构建单二进制版本

以下命令从项目根目录执行，构建本机平台的二进制。必须同步本次前端产物，直接 `make build` 不会更新内嵌页面。

```bash
cd frontend
npm ci
npm run build
cd ..
rm -rf backend/internal/web/dist
mkdir -p backend/internal/web/dist
cp -R frontend/dist/. backend/internal/web/dist/
cd backend
make build
```

产物为 `backend/bin/df-build-server`。运行前按后端说明设置配置路径与环境变量。Linux 发布需要在匹配目标平台、支持 CGO 的环境构建。

仓库未提供 Docker Compose 配置。`scripts/deploy_*.sh` 是绑定特定服务器、分支和路径的环境脚本，执行时会替换程序并重启服务；配置文件需另行准备，不能视为任意环境的开箱即用安装器。脚本中的展示端口为 8800，实际监听端口由配置文件或 `SERVER_PORT` 决定。

## 验证与文档

```bash
cd backend
go test ./...
```

数据库集成测试需要独立测试数据库配置；未设置时会跳过。前端 `npm run build` 包含 TypeScript 检查，`package.json` 当前没有统一的 `npm test` 命令。

- [当前功能、缺口与验证记录](PROGRESS.md)
- [192.168.1.150 机房试点](docs/pilot-192.168.1.150.md)
- [后端开发与配置](backend/README.md)
- [前端页面与开发](frontend/README.md)
- [部署管理](docs/deployment-management.md)
- [Vue 构建与微前端更新](docs/vue-app-build-flow.md)
- [前端主应用、子应用和独立应用更新规则](docs/frontend-update-rules.md)
- [PostgreSQL 当前功能](docs/postgresql.md)
- [机房与客户现场使用场景](docs/usage-scenarios.md)

`docs/plans/` 与需求文档保留设计历史，其中的目标、勾选项和命令不代表当前验收结果；当前使用方式以上述说明为准。

## 许可

内部项目。

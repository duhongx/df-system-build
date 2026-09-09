# CloudHIS 智能管理平台

面向 DF-HIS 系列应用的构建发布与运维管理平台。当前代码覆盖 Java／Vue 构建、制品版本与批量更新、Kubernetes 运维、服务器管理、PostgreSQL 变更以及基础设施离线部署。

本文按 2026-09-09 的源码核对；功能实现范围与已知缺口见 [PROGRESS.md](PROGRESS.md)。代码已实现不代表所有目标环境都已通过部署验收。

## 当前功能

| 模块 | 已实现能力 |
|---|---|
| 应用与编译配置 | 应用管理、Git 分支、Java／Vue 分类、微前端角色、Docker／本机编译模式、构建命令、执行器、缓存与资源限制、配置项模板 |
| 构建发布 | Docker 容器编译、打包、SSH/SFTP 上传、镜像构建与推送、K8s 部署；队列、并发控制、取消、超时、历史与实时日志 |
| 制品与批量更新 | 上传／目录导入／远程下载、应用匹配、最新制品库、更新版本、文件替换与重下载、制品仅更新一次（再次更新须重新上传）、上传并制作镜像、单独执行滚动更新、人工批次回滚与版本信息展示 |
| Kubernetes | 集群与资源查询、Pod 日志、指标、YAML 查看、重启、扩缩容、镜像与资源调整、ConfigMap 编辑、Service 端口维护 |
| 服务器 | 主机与连接管理、WebSSH、SFTP 文件操作、node_exporter 即时指标 |
| PostgreSQL | 实例信息、SQL 解析和风险分析、单句／文件／批次执行、取消、跳过、白名单、SQL 导出；视图依赖变更、恢复与验证 |
| 基础设施部署 | 组件及主机绑定、参数配置、冲突检查、部署预览、SSH 执行、日志、回滚、离线包管理 |
| 平台基础 | JWT 登录、退出、个人资料、凭据加密、工作台统计、钉钉／企微通知、站内消息、日志清理、多标签页与暗色模式 |

账号改密与邮件发送尚未闭环；远程同步／打包目前保留后端接口，但缺少对应前端路由。完整限制见 [当前进度与缺口](PROGRESS.md#已知缺口与实现边界)。

## 技术栈与结构

- 后端：Go 1.25+、Gin、GORM、PostgreSQL、Docker SDK、client-go、SSH/SFTP、JWT、Zap、Viper。SQL 解析依赖 `pg_query_go`，编译需要 CGO 和 C 编译器。
- 前端：Vue 3、Vite 5、TypeScript、Element Plus、Pinia、Vue Router、Axios、xterm。
- 部署：Go 二进制内嵌前端 SPA；运行配置、数据库、工作区和离线资源另行提供。

```text
backend/       Go API、流水线、调度、数据库变更与基础设施部署引擎
frontend/      Vue 页面、API 封装和前端构建
docker/        Java／Node 构建执行器镜像
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

初始管理员种子账号为 `admin / 123456`。目前不能将首次登录强制改密视为已实现能力，详见 [账号缺口](PROGRESS.md#已知缺口与实现边界)。

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
- [后端开发与配置](backend/README.md)
- [前端页面与开发](frontend/README.md)
- [部署管理](docs/deployment-management.md)
- [Vue 构建与微前端更新](docs/vue-app-build-flow.md)
- [PostgreSQL 当前功能](docs/postgresql.md)
- [Java 执行器镜像](docker/gradle-jdk8/README.md)、[Vue 执行器镜像](docker/node18-yarn/README.md)

`docs/plans/` 与需求文档保留设计历史，其中的目标、勾选项和命令不代表当前验收结果；当前使用方式以上述说明为准。

## 许可

内部项目。

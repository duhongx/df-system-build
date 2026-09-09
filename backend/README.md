# CloudHIS 智能管理平台 — 后端

Go 1.25+、Gin、GORM、PostgreSQL。承担构建调度、制品发布、Kubernetes／服务器运维、SQL 变更及基础设施部署。功能状态见 [项目进度](../PROGRESS.md)。

## 运行配置

运行需要可连接的 PostgreSQL。编译需要启用 CGO 并安装 C 编译器（`pg_query_go` 依赖）；Docker／镜像仓库、SSH 和 Kubernetes 按使用的功能配置。

在 `backend/` 下创建 `config/config.yaml`。该文件被 Git 忽略，不随仓库提供。下面是本地配置示例，数据库和密钥需替换为实际值：

```yaml
server:
  port: 8080
  mode: debug
database:
  host: 127.0.0.1
  port: 5432
  name: df_build
  user: df_build
  password: replace-with-database-password
  max_open_conns: 20
  max_idle_conns: 5
jwt:
  secret: replace-with-private-jwt-secret
  expire_hours: 24
docker:
  host: unix:///var/run/docker.sock
workspace:
  base_dir: ./workspaces
log:
  level: debug
  output: stdout
  file_path: ./logs/server.log
```

启动前设置 `DF_ENCRYPT_KEY` 为固定的 32 字节密钥，供凭据加解密使用；已有加密凭据依赖原密钥，重新生成密钥不会自动迁移已有凭据。

```bash
# 在 backend/ 下执行；先设置 DF_ENCRYPT_KEY
make run
```

`make run` 会创建 data、workspaces、logs 目录。启动时连接数据库、自动迁移和写入种子数据，恢复构建状态并将遗留的基础设施运行记录标记为失败。`GET /health` 检查服务和数据库连通性。

初始管理员为 `admin / 123456`。邮件发送、页面改密及首次强制改密仍有缺口，见 [已知限制](../PROGRESS.md#已知缺口与实现边界)。

### 配置加载与覆盖

`CONFIG_PATH` 默认为 `config/config.yaml`；文件不存在时入口还会尝试 `./config.yaml` 和 `../config/config.yaml`。Viper 必须成功读取配置文件，仅设置环境变量不能替代文件。

| 环境变量 | 用途 |
|---|---|
| `CONFIG_PATH` | 指定运行配置文件 |
| `DATABASE_DSN` | 完整 PostgreSQL DSN，优先于分项数据库配置 |
| `DB_HOST`、`DB_PORT`、`DB_NAME`、`DB_USER`、`DB_PASSWORD` | 覆盖数据库连接分项 |
| `SERVER_PORT` | 覆盖监听端口；8080 是上方开发示例，代码没有统一的端口默认值 |
| `JWT_SECRET` | 覆盖 JWT 签名密钥 |
| `LOG_LEVEL` | 覆盖日志级别 |
| `DF_ENCRYPT_KEY` | 凭据加密密钥，由 crypto 包读取，不是 YAML 配置项 |
| `DEPLOY_RESOURCE_DIR` | 离线资源目录，默认 `/opt/his-deploy/resources/offline` |
| `DEPLOY_MANIFEST` | 部署 CLI 的默认清单路径，默认 `/opt/his-deploy/resources/manifest.yml` |

系统自身 PostgreSQL 与界面配置的业务 PostgreSQL 用途不同：前者保存平台状态，后者承接 SQL 变更。业务数据库、Kubernetes、镜像仓库和通知等连接通过平台设置管理。

### 前端嵌入

`internal/web/embed.go` 使用 `//go:embed all:dist`，编译时必须有可嵌入的 dist 文件。仓库保留了一份产物，但它不保证与当前前端源码一致；更新或发布时按 [根目录构建步骤](../README.md#构建单二进制版本) 重新生成并复制。

## 构建与测试

```bash
# 在 backend/ 下执行，前端产物已同步
make build
go test ./...
```

产物：`bin/df-build-server`。本机构建产出本机平台二进制；Linux 发布须使用兼容目标平台的 CGO 构建环境。仓库未提供可直接运行的 Docker Compose 配置。

测试数据库通过 `TEST_DATABASE_DSN`，或 `TEST_DB_HOST`、`TEST_DB_NAME`、`TEST_DB_USER` 加可选的 `TEST_DB_PORT`／`TEST_DB_PASSWORD` 配置。使用独立测试库：集成用例会创建表并改写数据；未配置时跳过相应用例。测试通过范围见 [验证记录](../PROGRESS.md#验证记录)。

## 模块与 API

| 目录 | 职责 |
|---|---|
| `cmd/server/` | 服务入口、路由注册、健康检查和部署 CLI |
| `internal/handler/` | 认证、应用、任务、制品、设置、服务器、K8s、SQL 等 API |
| `internal/service/` | 业务逻辑、SQL 风险与依赖分析、制品版本信息采集和人工回滚 |
| `internal/repository/`、`internal/model/` | 平台数据模型、迁移、种子数据及 GORM 访问 |
| `internal/pipeline/`、`internal/scheduler/` | 顺序流水线、阶段执行、排队并发、取消和日志清理 |
| `internal/deploy/` | 基础设施部署引擎、动作、组件定义、离线资源、存储和运行管理 |
| `internal/docker/`、`internal/k8s/`、`internal/ssh/` | Docker SDK、client-go、SSH/SFTP 封装 |
| `internal/notification/`、`internal/web/` | Webhook 通知、SPA 嵌入 |
| `pkg/` | 配置、凭据加密、响应、日志与 SSE |

业务接口统一在 `/api` 下，以 [入口注册](cmd/server/main.go) 和各 Handler 的 `RegisterRoutes` 为准。基础设施接口为 `/api/deployment/*`，SQL 为 `/api/postgresql/*`，制品批量更新为 `/api/batch-deploy/*`。

[部署管理说明](../docs/deployment-management.md)包含资源目录、组件与 CLI 用法；[PostgreSQL 说明](../docs/postgresql.md)描述 SQL 和视图变更边界。

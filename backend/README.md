# DF 构建平台 - 后端

Go 1.22 + Gin + GORM。详见[项目根 README](../README.md)。

## 运行

```bash
make init      # 首次：创建 data/workspaces/logs 目录
make run       # 启动 :8080
```

默认使用 SQLite（`config/config.yaml` 中 `database.driver: sqlite`），启动时自动迁移表结构和种子数据。

## 构建

```bash
make build     # 产出 bin/df-build-server
make test      # 运行所有单元测试和属性测试
```

## Docker

```bash
docker compose up -d    # 含 PostgreSQL
```

## 配置

主配置：`config/config.yaml`，支持以下环境变量覆盖：

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `CONFIG_PATH` | `config/config.yaml` | 配置文件路径 |
| `SERVER_PORT` | 8080 | 服务端口 |
| `DB_HOST` / `DB_PORT` / `DB_NAME` / `DB_USER` / `DB_PASSWORD` | SQLite | PostgreSQL 连接 |
| `JWT_SECRET` | 见配置 | JWT 签名密钥（生产必改） |
| `DF_ENCRYPT_KEY` | 见代码 | AES-256 凭据加密密钥（生产必改，32 字节） |
| `LOG_LEVEL` | `debug` | debug / info / warn / error |

## 目录结构

```
cmd/server/            程序入口
internal/
├── handler/           HTTP 路由层
├── service/           业务逻辑
├── repository/        数据访问（GORM）
├── model/             GORM 模型
├── pipeline/          流水线引擎（stages + engine）
├── scheduler/         构建调度器（FIFO + 并发控制）
├── docker/            Docker 客户端封装
├── ssh/               SSH/SFTP 客户端
├── notification/      钉钉/企微 Webhook
└── middleware/        JWT / CORS / 日志 / 错误处理
pkg/
├── config/            Viper 配置
├── crypto/            AES-256-GCM
├── sse/               SSE Hub
├── response/          统一响应格式
└── logger/            Zap 日志
```

## 默认账号

`admin / 123456`（首次登录 `mustChangePassword: true`，前端会引导改密）。

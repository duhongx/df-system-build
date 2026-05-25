# DF 构建发布系统

替代 Jenkins + Python 脚本的自研构建发布平台，面向 DF-HIS 系列微服务。支持 Java / Vue 项目的多阶段流水线构建、Docker 容器化编译、SSH/SFTP 产物上传、远程同步与打包、钉钉/企业微信通知。

## 项目结构

```
df-build-system/
├── backend/          Go 后端（Gin + GORM + SQLite/PostgreSQL）
├── frontend/         Vue 3 + Vite + Element Plus 前端
└── .kiro/specs/      功能规划文档（requirements / design / tasks）
```

## 技术栈

**后端**：Go 1.22 · Gin · GORM · JWT · Docker SDK · golang.org/x/crypto/ssh + pkg/sftp · Zap · Viper

**前端**：Vue 3 · Vite · TypeScript · Element Plus · Pinia · Vue Router · Axios

## 快速开始

### 开发模式（前后端分开启动）

```bash
# 后端
cd backend
make init      # 首次：创建 data/workspaces/logs 目录
make run       # 启动服务，监听 :8080

# 前端（新终端）
cd frontend
npm install
npm run dev    # 启动 Vite dev server，监听 :3001
```

浏览器访问 `http://localhost:3001`，用 `admin / 123456` 登录。

### 生产构建

```bash
# 前端
cd frontend && npm run build    # 产出 dist/

# 后端
cd backend && make build        # 产出 bin/df-build-server
```

### Docker

```bash
cd backend
docker compose up -d
```

## 默认账号

- 用户名：`admin`
- 密码：`123456`（首次登录后强制修改）

## 文档

所有 feature spec 文档位于 `.kiro/specs/df-build-system/`：

- `requirements.md` — 需求文档（25 条 EARS 格式）
- `design.md` — 技术设计（架构图、数据模型、API 设计、正确性属性）
- `tasks.md` — 任务拆解（16 个阶段，123 个子任务）

## 许可

内部项目。

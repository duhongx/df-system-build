# DF 构建发布系统 — 项目进度跟踪

## 项目概述

替代原有 Jenkins + Python 脚本方案，将构建配置、执行、监控统一到单一平台。

- **后端**: Go (Gin + GORM + PostgreSQL)
- **前端**: Vue 3 + Vite + Element Plus
- **执行器**: Docker 容器隔离编译
- **调度**: FIFO 队列 + 并发槽位(默认5)
- **日志**: SSE 实时推送
- **通知**: 钉钉/企微 Webhook
- **部署**: 单二进制 + 内嵌前端 SPA

---

## 任务进度总览

| # | 任务 | 状态 | 说明 |
|---|------|------|------|
| 1 | 项目初始化 & 需求收集 | ✅ 完成 | Go 后端 + Vue 前端项目结构 |
| 2 | 前端 UI 实现 | ✅ 完成 | 全部核心页面已完成 |
| 3 | Go 后端实现 (Task 1-16) | ✅ 完成 | 75 Go 文件, 50+ API |
| 4 | 代码审查 & Bug 修复 | ✅ 完成 | 修复 21 个问题 |
| 5 | 项目清理 & Git 初始化 | ✅ 完成 | 推送到 git@192.168.1.206 |
| 6 | 部署脚本 & SPA 内嵌 | ✅ 完成 | scripts/deploy_service.sh |
| 7 | Seed 104 个应用 | ✅ 完成 | 从 _config.yaml 导入 |
| 8 | 分页功能 | ✅ 完成 | 4 个列表页添加分页 |
| 9 | Docker 执行器镜像 | ✅ 完成 | Gradle JDK8 + Node18 Yarn |
| 10 | 流水线核心阶段实现 | ✅ 完成 | Docker CLI + SSH/SFTP + MD5 校验 |
| 11 | 剩余 TODO 实现 | ⚠️ 部分完成 | 见下方详细说明 |
| 12 | 代码质量清理 | ⚠️ 部分完成 | 7 个废弃文件已删除, git push 待重试 |
| 13 | Pipeline 扩展 (镜像+K8s) | ✅ 完成 | BUILD_IMAGE → PUSH_IMAGE → K8S_DEPLOY |

---

## 已完成功能详细清单

### 后端核心功能

- [x] JWT 认证 (登录/登出/Token 刷新)
- [x] 应用管理 CRUD (104 个应用已 seed)
- [x] 任务管理 CRUD (含多服务器关联)
- [x] 流水线引擎 (顺序执行 Stage, fail-fast)
- [x] FIFO 调度器 (并发控制, 启动恢复)
- [x] Docker 编译 (CLI 方式, 缓存挂载, 资源限制)
- [x] Git Clone (ls -al 确认 + 解析 commit 信息)
- [x] SSH/SFTP 上传 (密码/私钥, MD5 校验)
- [x] SSE 实时日志推送
- [x] 钉钉/企微通知 (含测试发送)
- [x] AES-256 凭据加密
- [x] 远程同步 (rsync via SSH + SSE 流)
- [x] 远程打包 (tar via SSH + SSE 流)
- [x] 工作台统计 (应用数/今日构建/成功率/最近构建)
- [x] 制品记录 (构建成功自动创建)
- [x] 日志保留 & 定时清理 (cron)
- [x] 构建超时 (context timeout + docker stop)
- [x] 服务器测试连接 (更新 online/offline 状态)
- [x] SSH 私钥读取 (从服务器 /root/.ssh/id_rsa)
- [x] 配置项管理 (Dockerfile/K8s/脚本模板, ${var} 变量替换)
- [x] 镜像仓库设置 (Nexus Docker Registry)
- [x] K8s 设置 (kubeconfig 路径)
- [x] Pipeline 扩展: BUILD_IMAGE 阶段
- [x] Pipeline 扩展: PUSH_IMAGE 阶段
- [x] Pipeline 扩展: K8S_DEPLOY 阶段
- [x] Task deploy_mode 字段 (upload_only / upload_and_deploy)

### 前端页面

- [x] 登录页
- [x] 工作台 (Dashboard)
- [x] 应用管理 (CRUD + 搜索 + 分页)
- [x] 任务列表 (CRUD + 执行 + 批量执行 + 分页)
- [x] 构建队列
- [x] 构建历史 (分页 + 筛选)
- [x] Pipeline 详情 (Stage Bar + SSE 日志)
- [x] 远程同步 (SSE 输出)
- [x] 远程打包 (SSE 输出)
- [x] 制品记录 (分页)
- [x] 设置 - 全局参数
- [x] 设置 - 镜像仓库
- [x] 设置 - K8s 设置
- [x] 设置 - 配置项管理 (代码编辑器)
- [x] 设置 - 模板管理
- [x] 设置 - 执行器配置 (含缓存挂载)
- [x] 设置 - 远端服务器 (含 SSH Key 读取)
- [x] 设置 - 通知配置 (含测试发送)
- [x] Tab 系统 (右键菜单: 关闭当前/其他/全部)
- [x] 暗色模式
- [x] 侧边栏折叠

### Docker 镜像

- [x] `docker/gradle-jdk8/Dockerfile` — eclipse-temurin:8-jdk + Gradle 6.8.3 + Maven 3.5.4
- [x] `docker/node18-yarn/Dockerfile` — node:18-slim + yarn 1.22 + .npmrc (npmmirror + aliyun token)

### 部署

- [x] `scripts/deploy_service.sh` — 一键部署脚本
- [x] SPA 内嵌 (`backend/internal/web/embed.go`)
- [x] 端口 8800
- [x] PostgreSQL 作为系统自身存储

---

## 未完成 / Mock 功能

### 后端

| # | 文件 | 功能 | 状态 | 优先级 | 说明 |
|---|------|------|------|--------|------|
| 1 | `service/auth_service.go` | SMTP 邮件发送 | 🔶 半实现 | 低 | 验证码逻辑完整(内存存储+5分钟过期+校验)，只是不发邮件，验证码打到后端日志。生产环境需配置 SMTP |

### 前端

| # | 文件 | 功能 | 状态 | 优先级 | 说明 |
|---|------|------|------|--------|------|
| 1 | `DefaultLayout.vue` | 退出登录 | 🔴 Mock | 中 | 用 alert 代替，需调用 `/api/auth/logout` + 清除 token + 跳转登录页 |
| 2 | `DefaultLayout.vue` | 个人信息保存 | 🔴 Mock | 中 | 用 alert 代替，需调用 `PUT /api/auth/profile` |
| 3 | `DefaultLayout.vue` | 修改密码 | 🔴 Mock | 中 | 用 alert 代替，需调用 `POST /api/auth/change-password` |
| 4 | `DefaultLayout.vue` | 发送验证码 | 🔴 Mock | 中 | 只有倒计时动画，需调用 `POST /api/auth/send-code` |

### 其他待办

| # | 说明 | 优先级 |
|---|------|--------|
| 1 | Git push (上次 SSH 连接超时失败) | 中 |
| 2 | SMTP 配置 (config.yaml 新增 smtp 段) | 低 |
| 3 | 监控页面接入 Prometheus — 替换当前仪表盘为折线图（CPU/内存/网络时间序列曲线 + 时间范围选择器） | 中 |
| 4 | web-main 子应用并发更新互斥锁优化（当前用 sync.Mutex，后续考虑分布式锁） | 低 |
| 5 | 进程管理（前端配置应用端口 + 进程状态检查 + 重启） | 中 |

---

## 技术决策记录

| 决策 | 选择 | 原因 |
|------|------|------|
| 数据库 | PostgreSQL | 系统自身存储 |
| Docker 集成 | CLI (exec.Command) | 避免 SDK 依赖问题, arm64/x86_64 兼容 |
| 前端框架 | Vue 3 + Element Plus | gin-vue-admin 风格, 用户熟悉 |
| 认证 | JWT HS256 24h | 简单可靠 |
| 凭据加密 | AES-256-GCM | 环境变量注入密钥 |
| 日志推送 | SSE | 比 WebSocket 简单, 单向足够 |
| 调度 | FIFO + Mutex | 单机场景, 无需 Redis |
| 部署模式 | 单二进制 + 内嵌 SPA | 一个文件搞定 |

---

## 关键配置

- **服务端口**: 8800
- **Git 仓库**: `git@192.168.1.206:duhong/df-build-system.git`
- **部署服务器**: 192.168.1.10 (x86_64)
- **Nexus Registry**: 192.168.199.102:8888
- **默认上传路径**: /root/DFHIS/his-release
- **默认管理员**: admin / 123456

---

## 文件结构

```
df-build-system/
├── backend/                    # Go 后端
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── handler/           # 14 个 handler
│   │   ├── service/           # 业务逻辑
│   │   ├── repository/        # 数据访问
│   │   ├── model/             # 13 个模型
│   │   ├── pipeline/          # 流水线引擎
│   │   │   └── stages/        # 10 个 Stage 实现
│   │   ├── scheduler/         # FIFO 调度器
│   │   ├── docker/            # Docker CLI 封装
│   │   ├── ssh/               # SSH/SFTP 封装
│   │   ├── notification/      # 通知发送
│   │   ├── middleware/        # JWT/CORS/Error
│   │   └── web/               # SPA 内嵌
│   ├── pkg/                   # 公共包
│   ├── config/config.yaml
│   └── go.mod
├── frontend/                   # Vue 3 前端
│   ├── src/
│   │   ├── views/             # 12 个页面
│   │   ├── api/               # API 层
│   │   ├── layouts/
│   │   ├── router/
│   │   └── stores/
│   └── package.json
├── docker/                     # 构建镜像
│   ├── gradle-jdk8/
│   └── node18-yarn/
├── scripts/
│   └── deploy_service.sh
└── README.md
```

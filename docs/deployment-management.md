# 部署管理（Deployment Management）

部署管理是 df-build-system 的子模块，将原 `his-deploy` 项目的 HIS 基础设施离线部署能力整体迁入，替换早期的「基础设施」占位模块。它在同一个二进制内与构建发布子系统共存，负责 23 个基础设施组件（K8s 体系、中间件、基础服务）的离线部署、参数配置、状态管理与回滚。

## 功能概览

- **组件部署 / 回滚**：选组件 → 引擎按 pipeline YAML 展开为 Action 序列 → SSH 直连目标主机执行 → SSE 实时日志。
- **状态机硬约束**：已部署组件不允许重复部署，必须先回滚（状态存 `deployment_component_states`）。
- **主机绑定**：复用「服务器管理」，部署目标通过 `server_id` 引用服务器记录，不维护重复主机表。
- **冲突校验**：硬冲突组（etcd/postgresql/dns、redis/df-ops、postgresql/df-ops）+ K8s↔业务同机校验，绑定与部署前均拦截。
- **全局配置 / 组件参数覆盖**：三层合并 `组件默认 → 全局配置 → 组件覆盖`，密码做 shell 元字符校验。
- **离线包管理**：上传 → sha256 + bundle_version 校验 → 原子替换 Resource_Dir。

## 架构与边界

- 后端：Gin + GORM + PostgreSQL，复用现有 JWT、统一响应、SSE、SSH/SFTP。
- 引擎：`backend/internal/deploy/engine/`（自 his-deploy `dfctlv2` 迁移，import 改为 `df-build-server/internal/deploy/engine`，包名 `engine`）。
- 存储 seam：引擎依赖 `engine/store.Store` 接口，由 `internal/deploy/repository/GormStore` 用 GORM/PostgreSQL 实现。
- 凭据单一来源：SSH 凭据始终从 `model.Server` 在执行时解密读取，部署管理不再存第二份；凭据轮换无需重新绑定。

## 两个关键持久化根

部署管理有**两个**必须一起迁移的持久化根：

1. **PostgreSQL**（`devops` schema）：运行记录、组件状态、目标绑定、覆盖、全局配置、日志、离线包元数据。
2. **State_Dir**（目标主机本地，默认 `/opt/his-deploy/state`）：回滚 marker（`record_path_state` / `backup_file` / `restore_file` 写下的可逆性凭据）。

> ⚠️ 换部署机时，PostgreSQL schema 与各目标主机的 State_Dir 必须同步迁移。仅迁移数据库会让历史部署的 rollback 行为不可预期。

## 离线资源

离线资源树（约 4.5GB，含 docker 镜像等）**不入库、不嵌入二进制**，作为服务器本地只读 `Resource_Dir`（默认 `/opt/his-deploy/resources/offline`，可用环境变量 `DEPLOY_RESOURCE_DIR` 覆盖），由离线包安装填充。只有小文本制品（组件默认参数、pipeline YAML，约 52K）通过 `//go:embed` 嵌入。

## CLI 子命令

```bash
df-build-server deploy verify [manifest.yml]            # 校验资源树与 manifest 一致性
df-build-server deploy manifest gen [resourceDir] [ver] # 扫描资源目录生成 manifest.yml
```

## 迁移决策（与 his-deploy 的差异）

- **丢弃** his-deploy 的 chi 路由 → 改用 df 的 Gin。
- **丢弃** SQLite 手写 store → 改用 GORM + PostgreSQL（保留 `store.Store` 接口为 seam）。
- **丢弃** his-deploy 自带的简陋前端 → 按 df 风格重写 7 个 Vue 页面。
- **丢弃** CLI `install`（写 systemd）→ df 是常驻服务，单二进制部署。
- **不自动迁移**历史 his-deploy SQLite 数据与 state_dir；有存量安装的运维需手动迁移，超出本特性范围。

## API 前缀

所有接口在 `/api/deployment/*`（JWT 保护；SSE `/runs/:id/events` 用 `?token=` 鉴权）。

# 部署管理（Deployment Management）

核对日期：2026-09-09。部署管理已接入同一个 Go 服务，负责 HIS 基础设施离线部署、参数、主机绑定、运行记录和回滚。它与应用构建发布共存，前端入口为“部署管理”。

## 当前页面与流程

| 页面 | 路径 | 能力 |
|---|---|---|
| 组件 | `/deployment/components` | 组件状态、启用配置、目标主机、参数覆盖和任务明细 |
| 全局配置 | `/deployment/global-config` | 部署与网络参数、环境变量、离线资源状态与包管理 |
| 部署运行 | `/deployment/runs` | 新建部署、回滚、单阶段、清理；执行预览、dry-run、历史及运行中任务 |
| 运行详情 | `/deployment/runs/:id` | 状态、SSE 日志、取消与日志下载 |

主流程：维护服务器 → 配置主机绑定及参数 → 校验环境与离线资源 → 预览任务和动作 → 提交运行 → 查看状态与日志。

环境检查执行 hostname、防火墙、SELinux、Swap、ulimit、时间同步等验收，不替代操作系统初始化。已部署组件受状态机约束，通常需先回滚再重新部署。

## 组件与主机映射

UI 组件不是底层 pipeline 文件的一一映射。例如 Kubernetes Master 会展开为证书生成、etcd、containerd、kube-lb、master、node 和 calico；部分动作固定在部署机或所有主机执行。

当前注册的用户组件：

| 类别 | 组件 |
|---|---|
| 准备 | 环境检查、初始化 |
| 集群与网络 | Kubernetes Master、Kubernetes Worker、HAProxy 负载均衡、DNS（CoreDNS）、K8s 业务插件 |
| 存储与基础服务 | Nexus、NFS、FTP、MinIO |
| 数据库与中间件 | PostgreSQL、Redis、RabbitMQ、Elasticsearch、Nacos |
| 运维与监控 | SkyWalking、DF-Ops |

[组件注册](../backend/internal/deploy/engine/virtualcomponents/components.go)是显示名称、顺序和主机映射的依据；[pipeline 定义](../backend/internal/deploy/engine/pipeline/)是实际任务与动作的依据。

当前组件定义包括 PostgreSQL 单机／Patroni 集群、Elasticsearch 单机／集群、MinIO 单机／集群；Redis、RabbitMQ 按单机设计。SLB 当前为一台 HAProxy，不包含 keepalived／VIP。具体主机数量和依赖以注册及预检查规则为准，不能仅凭模板存在认定某拓扑已验收。

## 配置与执行

- 参数按“组件默认值 → 全局配置 → 组件覆盖”合并；密码类参数会做 shell 元字符检查。
- 目标复用“服务器管理”的 `model.Server`，通过服务器 ID 绑定；运行时读取并解密 SSH 凭据。
- 冲突规则包含 etcd／PostgreSQL／DNS 间的硬冲突、Redis／DF-Ops、PostgreSQL／DF-Ops，以及 K8s 体系与业务组件同机限制，见 [冲突矩阵](../backend/internal/deploy/conflict/matrix.go)。
- 引擎按 YAML 展开文件、模板、归档、systemd、命令、Kubernetes 和检查等动作，通过本地／SSH 执行后端运行。
- 部署运行可取消，日志写入数据库并通过 SSE 推送；重启服务后，遗留运行记录会标为失败，不自动续跑。
- 回滚依赖各组件的 rollback／residue 定义以及目标机状态标记，不是任意主机状态的完整快照恢复。

## 持久化与离线资源

需要保留以下数据：

| 数据 | 用途 |
|---|---|
| 平台 PostgreSQL | 运行、日志、组件状态、主机绑定、参数、离线包元数据；schema 由连接配置决定，并非代码固定为 devops |
| 目标机 State_Dir | 路径状态、备份文件及恢复标记；默认配置使用 `/opt/his-deploy/state` |
| 部署机资源目录 | 默认 `/opt/his-deploy/resources/offline`，通过 `DEPLOY_RESOURCE_DIR` 覆盖 |
| 工作区中的 deployment-runs | 运行配置和生成文件，位于 `workspace.base_dir` 下 |

迁移部署环境时需同时保留数据库、凭据密钥和目标机回滚状态；离线资源也需可用。历史 his-deploy SQLite 数据与状态目录不会自动导入。

离线安装包与镜像等大文件不入库、不嵌入二进制；组件默认参数及 pipeline YAML 使用 `go:embed`。离线包流程支持上传暂存、按 manifest 校验 SHA-256／版本、安装及资源目录切换，失败时保留已有资源的逻辑见 [offline 引擎](../backend/internal/deploy/engine/offline/offline.go)。

## API 与 CLI

业务接口为 `/api/deployment/*`，使用 JWT；SSE `/runs/:id/events` 使用查询参数 Token 鉴权。实际接口见 [Handler](../backend/internal/deploy/handler/handler.go)及 [离线接口](../backend/internal/deploy/handler/offline.go)。

```bash
df-build-server deploy verify [manifest.yml]
df-build-server deploy manifest gen [resourceDir] [ver]
```

CLI 子命令在启动 HTTP 服务前分流；默认清单由 `DEPLOY_MANIFEST` 指定，未设置时为 `/opt/his-deploy/resources/manifest.yml`。未提供 CLI `install` 子命令。

## 验证与历史文档

部署引擎、离线包、参数合并、冲突及运行事件等存在测试；本次未执行真实基础设施安装或回滚，详见 [验证记录](../PROGRESS.md#验证记录)。

[早期在线部署需求](infrastructure-deploy.md)和[早期任务清单](infrastructure-deploy-tasks.md)用于了解历史设计；其中旧组件数量、旧页面和旧接口不代表当前实现。

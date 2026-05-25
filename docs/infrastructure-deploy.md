# 基础设施在线部署 — 需求文档

## 概述

将现有 Ansible playbook 的部署能力整合到 DF 构建平台中，实现 Web 在线一键部署整套业务系统基础设施。目标是让实施工程师和客户工程师无需任何 Ansible/Python 知识，通过浏览器界面完成全部部署操作。

## 核心目标

1. **零外部依赖**：不需要 Python/Ansible，部署逻辑用 Go 实现
2. **可视化操作**：Web 界面选择组件、分配服务器、填写参数、一键部署
3. **中文错误提示**：出错时给出明确的原因和建议操作
4. **部署前检测**：自动检查离线包完整性和服务器前置条件
5. **失败可恢复**：每个组件配套清理脚本，失败后还原到部署前状态
6. **环境清单输出**：部署完成后自动生成所有组件的连接信息

---

## 部署组件清单

| 序号 | 组件 | 部署方式 | 说明 |
|------|------|---------|------|
| 00 | Nexus | SSH (虚拟机) | Docker 镜像仓库 |
| 01 | Prepare | SSH (虚拟机) | 导入离线镜像到 Nexus + SSL 证书 + K8s 前置配置 |
| 02 | NFS | SSH (虚拟机) | 为 K8s 提供 StorageClass |
| 03 | etcd | SSH (虚拟机) | K8s 数据存储（3 节点集群） |
| 04 | SLB | SSH (虚拟机) | HAProxy + Keepalived，提供 VIP |
| 05 | Containerd | SSH (虚拟机) | K8s CRI |
| 06 | K8s Master | SSH (虚拟机) | 控制面节点（支持多 Master HA） |
| 07 | K8s Node | SSH (虚拟机) | 工作节点 |
| 08 | Calico | client-go (K8s) | 网络插件，apply YAML |
| 09 | Plugin | client-go (K8s) | CoreDNS + node-local-dns + Ingress |
| 11 | PostgreSQL | SSH (虚拟机) | 业务数据库 |
| 12 | Elasticsearch | SSH (虚拟机) | 日志/搜索 |
| 13 | Redis | SSH (虚拟机) | 缓存 |
| 14 | RabbitMQ | SSH (虚拟机) | 消息队列 |
| 15 | MinIO | SSH (虚拟机) | 对象存储 |
| 16 | FTP | SSH (虚拟机) | 文件传输 |
| 17 | DNS | SSH (虚拟机) | 内部域名解析 |
| 18 | SkyWalking | SSH (虚拟机) | APM 链路追踪 |
| 19 | Nacos | SSH (虚拟机) | 注册中心/配置中心 |

---

## 完整流程

### Step 1: 前置准备

- 部署 df-build-server（一个二进制 + config.yaml）
- 在"服务器管理"中录入所有服务器信息并测试连接
- 将离线部署包提前放到 df-build-server 所在机器的本地目录

### Step 2: 部署前检测

**离线包检测**：
- 读取 manifest.json 确认包含哪些组件
- 逐个检查每个组件的文件是否完整（rpm/二进制/镜像/模板）

**服务器检测**（对所有目标服务器）：
- SSH 连接是否正常
- firewalld 是否关闭
- SELinux 是否关闭
- swap 是否关闭
- 磁盘空间是否充足
- 时间同步是否配置（chrony）
- yum 离线源是否配置好
- 必备软件包是否安装

### Step 3: 规划部署方案

在 Web 界面上为每个组件分配服务器和填写配置参数：
- Nexus → 选择服务器
- etcd → 选择 3 台服务器
- K8s Master → 选择服务器 + 填写 VIP
- K8s Node → 选择服务器
- PostgreSQL → 选择服务器 + 填写端口/密码/数据目录
- ...

### Step 4: 生成部署任务

系统根据规划方案按固定顺序生成部署任务列表（00 → 19）。

### Step 5: 执行部署

按顺序逐个执行：
1. 执行部署步骤（SSH 命令 / SFTP 上传 / 模板渲染）
2. 实时推送日志到前端
3. 部署完成后执行验证
4. 验证通过 → 标记成功，继续下一个
5. 验证失败 → 标记失败，显示错误原因 + 建议操作
6. 用户可选择：重试 / 清理并重试 / 跳过 / 终止

### Step 6: 输出环境清单

部署完成后自动生成文档，包含所有组件的：
- 地址和端口
- 用户名和密码
- 管理界面 URL
- 集群节点信息

支持导出为 Markdown / JSON 格式。

---

## 技术方案

### 部署步骤定义

硬编码在 Go 代码中。每个组件对应一个 Go 文件：

```
backend/internal/deployer/
├── deployer.go          # 部署引擎核心
├── checker.go           # 部署前检测
├── components/
│   ├── nexus.go
│   ├── prepare.go
│   ├── nfs.go
│   ├── etcd.go
│   ├── slb.go
│   ├── containerd.go
│   ├── k8s_master.go
│   ├── k8s_node.go
│   ├── calico.go
│   ├── plugin.go
│   ├── postgresql.go
│   ├── elasticsearch.go
│   ├── redis.go
│   ├── rabbitmq.go
│   ├── minio.go
│   ├── ftp.go
│   ├── dns.go
│   ├── skywalking.go
│   └── nacos.go
└── templates/           # 配置文件模板
    ├── postgresql.conf.tmpl
    ├── redis.conf.tmpl
    ├── haproxy.cfg.tmpl
    └── ...
```

### 每个组件的接口

```go
type Component interface {
    Code() string
    Name() string
    Order() int
    Params() []ParamDef                    // 需要用户填写的参数
    RequiredFiles() []string               // 离线包中需要的文件
    Deploy(ctx context.Context, opts DeployOpts) error
    Verify(ctx context.Context, opts DeployOpts) error
    Cleanup(ctx context.Context, opts DeployOpts) error
    OutputFields() map[string]string       // 输出到环境清单的字段
}
```

### 数据模型

```
DeployPlan        — 部署方案（组件分配 + 参数配置）
DeployExecution   — 一次部署执行记录
DeployTaskLog     — 每个组件的部署日志（永久保留）
EnvironmentInfo   — 环境清单输出
```

### 前端页面

```
侧边栏:
基础设施
  ├── 部署检测        ← 离线包 + 服务器前置条件检测
  ├── 部署规划        ← 分配服务器 + 填写参数
  ├── 执行部署        ← 可视化进度 + 实时日志
  ├── 环境清单        ← 部署结果输出
  └── 扩容节点        ← 新增 Worker 节点
```

---

## 决策记录

| 决策项 | 结论 |
|--------|------|
| 部署步骤定义方式 | 硬编码在 Go 代码中 |
| 离线包管理 | 提前放到本地目录，不支持 Web 删除/更新 |
| 部署顺序 | 固定顺序（00-19），不支持并行 |
| 失败处理 | 每个组件配套清理脚本，还原到部署前状态 |
| 日志保留 | 永久保留 |
| 密码策略 | 统一密码（除 PG 业务用户外） |
| 多环境 | 每个客户现场独立一套 |
| 增量部署 | 支持新增 Worker 节点加入已有集群 |
| 外部依赖 | 零依赖（不需要 Python/Ansible） |

---

## 待确认事项

1. Ansible playbook 适配完成后提供完整脚本
2. 离线包的 manifest.json 格式确认
3. 每个组件的具体验证方式确认
4. 配置模板（.conf.j2）提供后翻译为 Go template

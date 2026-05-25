# 基础设施部署 — 任务清单

## Phase 1: 框架搭建

### 1.1 数据模型 & 数据库
- [ ] 创建 `DeployPlan` 模型（部署方案：组件列表 + 服务器分配 + 参数）
- [ ] 创建 `DeployExecution` 模型（执行记录：状态/开始时间/结束时间）
- [ ] 创建 `DeployTaskLog` 模型（每个组件的执行日志）
- [ ] 创建 `EnvironmentInfo` 模型（环境清单输出）
- [ ] 数据库迁移

### 1.2 部署引擎核心
- [ ] 定义 `Component` 接口
- [ ] 实现部署引擎 `Deployer`（按顺序执行组件、状态管理、日志记录）
- [ ] 实现 SSH 执行器（复用现有 SSH 库，增加步骤级日志）
- [ ] 实现模板渲染（Go text/template）
- [ ] 实现清理机制（失败时调用组件的 Cleanup 方法）
- [ ] 实现验证机制（部署完成后调用 Verify 方法）

### 1.3 部署前检测
- [ ] 实现离线包检测（读取 manifest.json + 检查文件完整性）
- [ ] 实现服务器前置条件检测（firewalld/SELinux/swap/磁盘/chrony/yum）
- [ ] 检测结果 API

### 1.4 后端 API
- [ ] `GET /api/deploy/packages` — 获取离线包信息
- [ ] `POST /api/deploy/check` — 执行部署前检测
- [ ] `GET /api/deploy/components` — 获取可部署组件列表（含参数定义）
- [ ] `POST /api/deploy/plan` — 保存部署方案
- [ ] `GET /api/deploy/plan` — 获取当前部署方案
- [ ] `POST /api/deploy/execute` — 开始执行部署
- [ ] `GET /api/deploy/status` — 获取部署执行状态
- [ ] `POST /api/deploy/retry/:component` — 重试某个组件
- [ ] `POST /api/deploy/cleanup/:component` — 清理某个组件
- [ ] `GET /api/deploy/logs/:component` — 获取组件部署日志
- [ ] `GET /api/deploy/environment` — 获取环境清单
- [ ] `POST /api/deploy/add-node` — 扩容 Worker 节点

### 1.5 前端页面
- [ ] 侧边栏新增"基础设施"菜单组
- [ ] 部署检测页面（离线包检测 + 服务器检测结果展示）
- [ ] 部署规划页面（组件列表 + 服务器选择 + 参数填写表单）
- [ ] 执行部署页面（步骤进度条 + 实时日志 + 操作按钮）
- [ ] 环境清单页面（表格展示 + 导出功能）
- [ ] 扩容节点页面（选择新服务器 + 一键加入集群）

### 1.6 WebSocket 实时日志
- [ ] 部署执行过程通过 WebSocket 推送实时日志到前端
- [ ] 每个组件的日志独立 channel

---

## Phase 2: 组件实现（等 Ansible 适配完成后）

### 2.1 基础设施组件
- [ ] 00.Nexus — 部署 + 验证 + 清理
- [ ] 01.Prepare — 导入镜像 + SSL 证书 + 前置配置
- [ ] 02.NFS — 部署 + 验证 + 清理
- [ ] 03.etcd — 3 节点集群部署 + 验证 + 清理
- [ ] 04.SLB — HAProxy + Keepalived + VIP + 验证 + 清理

### 2.2 Kubernetes 组件
- [ ] 05.Containerd — 部署 + 验证 + 清理
- [ ] 06.K8s Master — 初始化 + 加入其他 Master + 验证 + 清理
- [ ] 07.K8s Node — 加入集群 + 验证 + 清理
- [ ] 08.Calico — client-go apply YAML + 验证
- [ ] 09.Plugin — CoreDNS + node-local-dns + Ingress apply + 验证

### 2.3 中间件组件
- [ ] 11.PostgreSQL — 部署 + 配置 + 创建用户 + 验证 + 清理
- [ ] 12.Elasticsearch — 部署 + 验证 + 清理
- [ ] 13.Redis — 部署 + 配置 + 验证 + 清理
- [ ] 14.RabbitMQ — 部署 + 验证 + 清理
- [ ] 15.MinIO — 部署 + 验证 + 清理
- [ ] 16.FTP — 部署 + 验证 + 清理
- [ ] 17.DNS — 部署 + 验证 + 清理
- [ ] 18.SkyWalking — 部署 + 验证 + 清理
- [ ] 19.Nacos — 部署 + 验证 + 清理

### 2.4 增量操作
- [ ] 新增 Worker 节点（安装 Containerd + kubeadm join）
- [ ] 单独重新部署某个中间件

---

## Phase 3: 测试 & 优化

- [ ] 在测试环境完整走一遍全量部署流程
- [ ] 验证每个组件的清理脚本
- [ ] 验证增量部署（新增 Worker）
- [ ] 验证失败重试机制
- [ ] 性能优化（大文件上传进度）
- [ ] 文档完善

---

## 工作量估算

| Phase | 内容 | 预计工时 |
|-------|------|---------|
| Phase 1 | 框架搭建 | 1-2 周 |
| Phase 2 | 19 个组件实现 | 2-3 周 |
| Phase 3 | 测试优化 | 1 周 |
| **总计** | | **4-6 周** |

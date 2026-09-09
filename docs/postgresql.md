# PostgreSQL 当前功能

核对日期：2026-09-09。本文描述当前源码；[SQL 需求方案](sql-change-execution-requirements.md)和 [视图变更计划](plans/2026-06-17-postgresql-view-dependency-change.md)保留设计历史。

## 配置与页面

业务数据库在“设置 → 环境配置”中配置；“SQL执行配置”维护执行参数及允许强制执行的 SQL 类型。系统自身数据库保存变更记录，业务 PostgreSQL 执行变更，两者用途不同。

| 页面 | 当前能力 |
|---|---|
| 实例管理 `/postgresql/instances` | 当前配置连接的可用性、版本、当前数据库／用户、主从角色、参数和主库复制信息 |
| SQL 执行 `/postgresql/sql-execution` | SQL 文件与批次、待办／已办、语句内容、风险、执行状态、编辑和操作 |
| 视图依赖变更 `/postgresql/view-dependency-change` | 专用变更任务、依赖分析、预检查、执行计划、执行／恢复／验证和步骤结果 |

“实例管理”当前展示全局配置的连接，不是任意多实例资产管理。

## SQL 变更流程

1. 导入服务器 SQL、提交 SQL 内容或批次文件，解析成文件／语句记录。
2. 使用 PostgreSQL 解析器识别语法和对象，结合风险规则与可获取的数据库元数据分析。
3. 展示 `LOW`、`WARN`、`BLOCKED` 风险、原因及执行策略；可编辑文件或语句、补全缺失 schema 后重新判断。
4. 按单句、文件或批次执行，保存状态和错误；文件／批次支持后台执行与取消，支持跳过及历史查询。
5. 被阻止的语句可导出供人工处理；部分类型可按配置白名单进入强制执行路径，不能理解为所有阻止规则都可以绕过。

风险分析覆盖无有效 WHERE 的 UPDATE／DELETE、字段类型变更、默认值、约束、索引、对象删除、TRUNCATE、大表操作及视图兼容性等。具体分类和阈值以 [风险分析](../backend/internal/service/sql_risk_analyzer.go)及 [元数据检查](../backend/internal/service/sql_metadata_inspector.go)为准。

执行策略区分可在事务中执行的语句与并发索引、VACUUM 等不能使用相同事务包装的操作；因此不能将一次文件或批次执行视为整批原子事务。

## 视图依赖变更

面向字段类型修改涉及普通视图／物化视图依赖的场景：

1. 创建变更任务，校验一条或多条字段类型修改语句及目标对象。
2. 分析依赖并保存定义快照，包含恢复需要的 owner、授权、注释及相关索引、规则、触发器等信息。
3. 预检查备份与当前定义是否一致、锁等待等条件。
4. 生成按依赖顺序删除、修改字段、重建与验证的计划；可导出变更 SQL 和恢复 SQL。
5. 支持事务模式和分步模式。事务失败走回滚路径；分步模式失败时有恢复尝试，不能承诺所有步骤都自动撤销。
6. 可执行整体恢复，也可对指定依赖对象单独恢复和验证，保留步骤状态与错误。

恢复计划用于恢复依赖对象，不是任意业务数据或任意 DDL 的通用回滚工具。

## 实现与验证边界

- 接口已注册在 `/api/postgresql/*`，见 [Handler](../backend/internal/handler/postgresql.go)。
- 执行和记录逻辑见 [PostgreSQLService](../backend/internal/service/postgresql_service.go)，依赖计划见 [sql_view_dependency.go](../backend/internal/service/sql_view_dependency.go)。
- SQL 包可存在于制品版本中，但微服务批量部署会排除 SQL；目前不承诺 SQL 和应用部署的统一事务。
- 本地非数据库测试通过；数据库集成测试本次跳过，未在真实业务库执行变更。验证范围见 [进度记录](../PROGRESS.md#验证记录)。

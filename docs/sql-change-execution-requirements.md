# SQL 变更执行功能需求与风险控制方案

## 背景

当前需要在 Go 版构建发布系统中实现类似现有 Java 运维系统的 SQL 变更执行能力。该功能主要面向实施人员，用于执行业务系统的数据库变更脚本，包括：

- 表结构变更
- 业务数据的新增、修改、删除
- 视图的新建和重建
- 常见字段属性调整，例如 `varchar(6)` 扩容到 `varchar(20)`

现有 Java 项目的 SQL 变更功能是上传 SQL 文件后，由后端解析 SQL 文件，再通过 JDBC 连接 PostgreSQL 数据库逐条执行 SQL。Go 项目可以沿用这个模式，使用 PostgreSQL 驱动直连数据库执行。

## 核心约束

该功能的使用场景决定了系统不能设计得太复杂：

- 实施人员技术水平有限，需要尽量简单。
- 客户技术人员通常不参与或不配合。
- 不希望引入审批流、二次确认等复杂流程。
- 系统需要自动兜底，减少明显高危 SQL 导致锁表、重写大表、执行失败的问题。
- 普通 SQL 变更应尽量直接执行，不增加实施负担。

因此本功能不是审批型 SQL 平台，而是一个“低门槛执行 + 自动风险识别 + 自动保护参数”的 SQL 变更工具。

## 执行方式选择

推荐默认使用 Go PostgreSQL 驱动执行，例如 `pgx`。

```text
上传 SQL 文件
-> 保存文件记录
-> 解析 SQL 语句
-> 自动风险扫描
-> 连接 PostgreSQL
-> 设置执行保护参数
-> 逐条执行
-> 记录每条 SQL 的执行状态
```

不建议默认通过 SSH 到数据库服务器后调用 `psql` 执行。`psql` 可以作为特殊兼容入口保留，用于执行包含 `\copy`、`\i`、`\set` 等 psql 元命令的脚本，或执行特别大的 SQL 文件。

驱动执行和 `psql` 执行在性能上通常不是关键差异。真正耗时主要在数据库端，例如锁等待、表扫描、索引构建、DDL 重写、数据写入。驱动执行的优势是更容易做结构化状态记录、错误码识别、超时控制和失败重试。

## 权限建议

不建议继续默认使用 PostgreSQL 超级管理员账号执行所有 SQL。

推荐使用业务专用执行账号：

```text
sql_executor
```

如果业务 schema 中的表结构变更需要 owner 权限，可以采用 role 模型：

```text
biz_owner      拥有业务 schema 和业务对象
sql_executor   允许 SET ROLE biz_owner
```

平台执行 SQL 时先执行：

```sql
SET ROLE biz_owner;
```

这样可以满足业务表结构变更需求，同时避免平台账号拥有 PostgreSQL superuser 权限。

## 默认执行保护参数

每次建立数据库连接后，应先设置会话级保护参数：

```sql
SET lock_timeout = '5s';
SET statement_timeout = '120s';
SET idle_in_transaction_session_timeout = '60s';
```

不同 SQL 类型可以使用不同超时：

| SQL 类型 | 建议超时 |
|---|---:|
| 普通 DML | 120s |
| 普通 DDL | 300s |
| `CREATE INDEX CONCURRENTLY` | 30min 或不设置 statement timeout |
| 视图创建/重建 | 120s |

`lock_timeout` 比 `statement_timeout` 更重要。它可以避免 SQL 长时间等待表锁，导致业务系统卡住。

## SQL 类型风险分类

### 默认允许

以下变更在当前业务场景中可以默认允许执行：

| 类型 | 风险判断 |
|---|---|
| `INSERT` | 默认允许 |
| `UPDATE` | 默认允许，建议记录影响行数 |
| `DELETE` | 默认允许，建议记录影响行数 |
| `CREATE TABLE` | 低风险 |
| `ALTER TABLE ADD COLUMN` 无复杂默认值 | 低风险 |
| `CREATE VIEW` | 低风险 |
| `CREATE OR REPLACE VIEW` | 通常低风险 |
| `ALTER COLUMN SET/DROP DEFAULT` | 低风险 |
| `ALTER COLUMN DROP NOT NULL` | 低风险 |
| `varchar(n)` 扩容 | 低风险 |

### 需要自动标记风险但不阻塞

以下 SQL 不一定需要拦截，但系统应自动记录风险原因，便于排查：

| 类型 | 风险点 |
|---|---|
| `ALTER TABLE ALTER COLUMN TYPE` | 可能重写整表，也可能被视图依赖阻塞 |
| `ALTER COLUMN SET NOT NULL` | 需要扫描全表检查是否存在 NULL |
| `ADD CHECK` | 默认会扫描已有数据 |
| `CREATE INDEX` | 非 `CONCURRENTLY` 会阻塞写入 |
| `varchar(n)` 缩容 | 需要检查已有数据长度，可能失败 |
| `numeric` 精度/范围调整 | 可能需要校验或重写 |

### 建议强拦截

这些 SQL 风险较大，不适合普通实施入口直接执行：

```text
DROP DATABASE
DROP SCHEMA
DROP OWNED
ALTER SYSTEM
COPY ... PROGRAM
REINDEX DATABASE
VACUUM FULL
```

是否拦截 `TRUNCATE`、`DROP TABLE` 可按业务实际决定。如果业务变更中经常有临时表或中间表清理，可以只允许匹配特定命名规则的对象，例如 `_tmp`、`temp_`、`bak_`。

## 视图相关风险

### 新增视图

`CREATE VIEW` 风险较低。它只是创建视图定义，不会复制底层表数据，也不会重写底层表。

### 重建视图

`CREATE OR REPLACE VIEW` 通常风险也不大，但可能失败：

- 新 SQL 语法错误。
- 引用的表或字段不存在。
- 已有视图输出列的名称、顺序、类型发生不兼容变化。
- 其他视图或对象依赖当前视图。

重建视图的主要风险是依赖和结构兼容性，不是大表重写。

## 字段类型变更风险

### `varchar(6)` 扩容到 `varchar(20)`

这类变更通常是低风险。

```sql
ALTER TABLE patient ALTER COLUMN code TYPE varchar(20);
```

原因是放宽长度限制，不需要转换已有数据，通常只是元数据变化，不会重写整表。

但如果该字段被视图引用，PostgreSQL 可能报错：

```text
cannot alter type of a column used by a view or rule
```

这是依赖保护问题，不是性能问题。

处理方式：

```text
查询依赖视图
-> 保存视图定义
-> 先 drop 依赖视图
-> 修改字段类型
-> 按依赖顺序重建视图
```

第一版可以先不自动 drop/recreate 视图，只做执行前依赖检测和更友好的错误提示。

## 常见会导致整表重写或全表扫描的字段变更

### 通常不会重写整表

| 操作 | 说明 |
|---|---|
| `varchar(6) -> varchar(20)` | 放宽长度，通常元数据变更 |
| `varchar(n) -> text` | 通常较轻 |
| `ALTER COLUMN SET DEFAULT` | 只影响后续写入 |
| `ALTER COLUMN DROP DEFAULT` | 元数据变更 |
| `ALTER COLUMN DROP NOT NULL` | 元数据变更 |
| `ADD COLUMN` 无默认值 | 通常不重写 |
| `DROP COLUMN` | 通常不立即重写，但空间不会马上释放 |

### 可能全表扫描

| 操作 | 风险 |
|---|---|
| `ALTER COLUMN SET NOT NULL` | 需要扫描全表确认无 NULL |
| `ADD CHECK` | 默认校验已有数据，会扫描表 |
| `varchar(20) -> varchar(6)` | 需要确认已有值不超长 |
| `text -> varchar(n)` | 需要确认已有值不超长 |

这类不一定重写表，但会读全表。大表上仍可能慢，并持有锁。

### 可能重写整表

| 操作 | 风险 |
|---|---|
| `int -> bigint` | 类型存储格式变化，通常高风险 |
| `numeric -> int` | 需要转换数据，可能失败 |
| `varchar/text -> uuid` | 需要逐行转换，可能失败 |
| `json -> jsonb` | 需要转换存储格式 |
| `timestamp -> timestamptz` | 需要类型转换 |
| `date -> timestamp` | 类型转换 |
| `ALTER COLUMN TYPE ... USING ...` | 大概率逐行转换 |
| `ADD COLUMN DEFAULT volatile表达式` | 可能需要逐行写入 |

### PostgreSQL 版本相关

PostgreSQL 11 之后，`ADD COLUMN DEFAULT 常量` 通常不会像旧版本那样立即重写整表，而是采用更轻量的方式处理。但如果默认值是 volatile 表达式，例如某些逐行变化的函数，则仍可能导致重写或逐行计算。

因此平台应记录数据库版本，风险判断可以按版本调整。

## 视图依赖检测需求

字段类型修改前，系统应尝试查询该字段是否被视图依赖。

示例查询：

```sql
SELECT
  dependent_ns.nspname AS view_schema,
  dependent_view.relname AS view_name
FROM pg_depend d
JOIN pg_rewrite r ON r.oid = d.objid
JOIN pg_class dependent_view ON dependent_view.oid = r.ev_class
JOIN pg_namespace dependent_ns ON dependent_ns.oid = dependent_view.relnamespace
JOIN pg_class source_table ON source_table.oid = d.refobjid
JOIN pg_namespace source_ns ON source_ns.oid = source_table.relnamespace
JOIN pg_attribute a ON a.attrelid = source_table.oid AND a.attnum = d.refobjsubid
WHERE source_ns.nspname = $1
  AND source_table.relname = $2
  AND a.attname = $3
  AND dependent_view.relkind IN ('v', 'm');
```

如果发现依赖，执行结果中应展示：

```text
字段 schema.table.column 被以下视图依赖，直接修改可能失败：
- schema.view_a
- schema.view_b
```

后续可以增强为自动生成视图重建计划。

## 数据模型建议

### SQL 文件表

保存上传文件和整体执行状态：

```text
id
system_code
environment
schema_name
version
file_name
file_content
execute_status
execute_message
execute_user
execute_time
created_at
```

### SQL 语句明细表

保存解析后的每条 SQL：

```text
id
file_id
line_number
sql_content
sql_type
risk_level
risk_reason
execute_status
execute_message
sql_state
affected_rows
duration_ms
execute_time
```

### 视图备份表

用于支持字段变更前后的视图依赖处理：

```text
id
schema_name
view_name
definition
backup_reason
created_at
```

## 第一阶段实现范围

第一阶段不做审批，不做复杂自动编排，优先实现可用和基础兜底：

1. SQL 文件上传。
2. SQL 文件保存。
3. SQL 语句解析。
4. 使用 `pgx` 连接 PostgreSQL 执行。
5. 每条 SQL 记录执行状态、耗时、错误信息、SQLState。
6. 自动设置 `lock_timeout`、`statement_timeout`。
7. 基础风险识别：
   - DML
   - CREATE TABLE
   - ADD COLUMN
   - CREATE VIEW
   - CREATE OR REPLACE VIEW
   - ALTER COLUMN TYPE
   - CREATE INDEX
8. 字段类型修改前检测视图依赖。
9. 对明显高危 SQL 做强拦截。
10. 提供失败后重试和手工跳过能力。

## 后续增强

后续可以逐步增加：

- 自动备份依赖视图定义。
- 自动 drop/recreate 依赖视图。
- 大表识别：查询 `pg_total_relation_size` 和 `pg_class.reltuples`。
- 非 `CONCURRENTLY` 索引创建自动提示或拦截。
- SQL 执行中取消。
- `psql` 兼容执行器。
- 按业务系统、环境、schema 配置不同超时和拦截规则。

## 当前实现对照

本节基于当前 Go 项目的 PostgreSQL 管理与 SQL 执行实现整理，主要对应以下代码：

- 后端入口：`backend/internal/handler/postgresql.go`
- 后端服务：`backend/internal/service/postgresql_service.go`
- 数据模型：`backend/internal/model/postgresql.go`
- 前端页面：`frontend/src/views/PostgreSQLSQLExecutionView.vue`
- 前端实例页：`frontend/src/views/PostgreSQLInstanceView.vue`

### 已实现

| 功能点 | 当前状态 | 说明 |
|---|---|---|
| PostgreSQL 管理一级菜单 | 已实现 | 前端已拆分为“实例管理”和“SQL 执行”两个二级页面。 |
| 实例信息展示 | 已实现 | 从系统设置中的 PostgreSQL 配置读取连接信息，展示连接状态、版本、当前库、当前用户、主从角色、关键参数、复制状态。 |
| SQL 文件上传 | 已实现 | 前端支持选择本地 `.sql` / `.txt` 文件并读取内容。 |
| SQL 内容粘贴执行 | 已实现 | 前端支持直接粘贴 SQL 内容，并调用后端解析或解析后执行。 |
| SQL 文件保存 | 已实现 | 后端保存 `SQLChangeFile`，记录文件名、文件内容、版本、schema、执行状态、执行用户、执行时间等。 |
| 同名文件覆盖 | 已实现 | 支持 `overwrite`，同名未删除文件会软删除旧记录后保存新记录。 |
| Java 风格文件名解析 | 已实现 | 支持从 `version__groupSortNo__schema.sql` 解析版本、排序号、schema。 |
| 从 SQL 推断 schema | 已实现 | 如果页面不传 schema，会尝试从 SQL AST 中首个显式 schema 对象推断。 |
| SQL 语句解析 | 已实现 | 已引入 `pg_query_go`，使用 PostgreSQL parser/scanner 解析和拆分 SQL。 |
| SQL 语法错误识别 | 已实现 | `pg_query.Parse` 失败会标记为 `SQL_PARSE_ERROR` 和 `BLOCKED`，落库状态为 `NOT_EXECUTABLE`。 |
| 使用 PostgreSQL 驱动执行 | 已实现 | 使用 `pgx` stdlib 连接 PostgreSQL 并执行 SQL。 |
| 执行保护参数 | 已实现 | 每条 SQL 执行前设置 `lock_timeout`、`statement_timeout`、`idle_in_transaction_session_timeout`。 |
| 按 SQL 类型设置超时 | 已实现 | 普通 SQL 2 分钟，部分 DDL 5 分钟，`CREATE INDEX CONCURRENTLY` 30 分钟。 |
| 逐条执行并记录状态 | 已实现 | 记录每条 SQL 的状态、耗时、错误信息、SQLState、影响行数、执行时间。 |
| 失败后停止后续执行 | 已实现 | 非可跳过错误会将当前 SQL 标记失败，文件标记 `PARTIAL_FAILED`，并停止继续执行。 |
| 待执行/已执行文件列表 | 已实现 | 后端提供 todo/done 分组列表，前端分别展示。 |
| 失败后重试 | 已实现 | 对非 `SUCCESS` / `SKIPPED` 文件可再次执行，已成功和已跳过语句不会重复执行。 |
| 手工跳过 SQL 明细 | 已实现 | 支持跳过非成功 SQL 明细。 |
| 手工跳过 SQL 文件 | 已实现 | 支持将未成功文件整体标记为 `SKIPPED`。 |
| 删除 SQL 文件 | 已实现 | 支持删除未成功文件，实际为软删除；成功文件不可删除。 |
| 服务器 SQL 文件导入 | 已实现 | 支持从服务器路径导入 `.sql` 文件。 |
| 服务器 ZIP 导入 | 已实现 | 支持导入 `.zip`，自动提取其中 `.sql` 文件。 |
| 不可执行 SQL 导出 | 已实现 | 可导出 `NOT_EXECUTABLE`、`FAILED` 等状态的 SQL，便于线下处理。 |
| Java 版跳过选项 | 已实现 | 支持字段已存在、对象已存在、唯一约束冲突时跳过，分别对应 PostgreSQL SQLState `42701`、`42P07`、`23505`。 |
| 基础风险识别 | 已实现 | 覆盖 DML、建表、建视图、加列、改字段类型、设置 NOT NULL、CHECK、索引、TRUNCATE、DROP TABLE、COPY、REINDEX、VACUUM 等。 |
| 明显高危 SQL 强拦截 | 已实现 | `DROP DATABASE`、`DROP SCHEMA`、`DROP OWNED`、`ALTER SYSTEM`、`COPY PROGRAM`、`REINDEX DATABASE`、`VACUUM FULL` 会标记不可执行。 |
| 字段类型变更视图依赖检测 | 已实现 | 对 `ALTER TABLE ... ALTER COLUMN ... TYPE` 查询依赖视图，并将依赖信息写入风险原因。 |
| 视图备份表模型 | 已实现模型 | 已有 `SQLViewBackup` 模型，但当前还没有自动写入备份和重建流程。 |

### 部分实现或实现深度不足

| 功能点 | 当前状态 | 差距 |
|---|---|---|
| 风险分类细粒度判断 | 部分实现 | 当前按 AST 节点和少量规则识别风险，但未区分 `varchar` 扩容/缩容、`numeric` 精度变化、`USING` 转换、volatile default 等细粒度场景。 |
| `CREATE OR REPLACE VIEW` 兼容性风险 | 部分实现 | 当前统一识别为 `CREATE_VIEW` 低风险，没有检查已有视图列名、顺序、类型兼容性，也没有依赖链分析。 |
| `DROP TABLE` / `TRUNCATE` 策略 | 部分实现 | 当前只标记 WARN，不按 `_tmp`、`temp_`、`bak_` 等命名规则细分允许或拦截。 |
| SQL 事务策略 | 部分实现 | 当前是逐条执行，不启用文件级事务；文档也未明确是否需要整体事务。对于 `CREATE INDEX CONCURRENTLY` 这类不能放事务里的语句，当前策略可执行，但失败回滚能力有限。 |
| schema 推断 | 部分实现 | 当前只从首个显式 schema 对象推断；如果 SQL 全部不带 schema，仍不会自动落到某个默认 schema。 |
| 视图依赖处理 | 部分实现 | 当前只做依赖检测和提示，不自动备份、drop、重建。 |
| 实例管理 | 部分实现 | 当前展示系统设置里的 PostgreSQL 连接和运行状态，不维护独立实例资产、集群拓扑、备份策略等管理信息。根据当前方向，实例管理与 SQL 执行无关。 |

### 未实现但文档已有规划

| 功能点 | 当前状态 | 说明 |
|---|---|---|
| 专用执行账号/SET ROLE 模型 | 未实现 | 当前只使用系统设置中的 PostgreSQL 用户连接执行，没有配置 `sql_executor`、`biz_owner` 或执行前 `SET ROLE`。 |
| 不默认使用 superuser 的强约束 | 未实现 | 文档是权限建议，系统当前没有检测连接用户是否为 superuser，也没有阻止 superuser 执行。 |
| 自动备份依赖视图定义 | 未实现 | `SQLViewBackup` 仅有模型，未在执行前保存 `pg_get_viewdef`。 |
| 自动 drop/recreate 依赖视图 | 未实现 | 当前不生成视图重建计划，也不自动按依赖顺序重建。 |
| 大表识别 | 未实现 | 未查询 `pg_total_relation_size`、`pg_class.reltuples`，也没有基于表大小调整风险级别。 |
| 非 `CONCURRENTLY` 索引创建拦截 | 未实现 | 当前只标记 WARN，不自动改写、不强拦截。 |
| SQL 执行中取消 | 未实现 | 当前没有 cancel API，也没有前端取消按钮；只能等待超时或手工处理进程。 |
| `psql` 兼容执行器 | 未实现 | 不支持 `\copy`、`\i`、`\set` 等 psql 元命令，也没有 SSH/psql 执行入口。 |
| 超大 SQL 文件处理 | 未实现 | 当前会将文件内容整体读入内存并保存到数据库，未做流式解析、分片导入或后台任务化。 |
| 按业务系统/环境/schema 配置超时和规则 | 暂不规划 | 当前产品方向已调整为不在 SQL 执行页面关注业务系统、环境、schema；schema 优先从 SQL 或文件名解析。 |
| 审批流/二次确认 | 不规划 | 文档明确第一阶段不做审批，当前也未实现。 |

### 建议后续优先级

1. **执行账号和权限校验**：先补 `SET ROLE`、superuser 检测、只读/高危权限边界。这是 SQL 执行风险控制的基础。
2. **大表风险识别**：对 `ALTER COLUMN TYPE`、`SET NOT NULL`、`ADD CHECK`、`CREATE INDEX`、`DROP TABLE`、`TRUNCATE` 查询表大小和预估行数，给出更可信的风险提示。
3. **细化字段类型变更风险**：区分 `varchar` 扩容、缩容、`USING` 转换、存储格式变化、默认值表达式等场景。
4. **执行取消能力**：增加执行中的 cancel API 和前端按钮，避免长 SQL 只能等超时。
5. **psql 兼容执行器**：作为特殊入口处理包含 psql 元命令或超大文件的脚本，不替代默认 pgx 执行。
6. **视图自动备份和重建计划**：先生成可人工确认的重建 SQL，再考虑自动 drop/recreate。

## 总体结论

本功能应定位为面向实施人员的 SQL 变更执行工具，而不是审批平台。

推荐方案：

```text
Go + pgx 驱动执行
不引入审批
不默认使用 PostgreSQL superuser
自动设置锁等待和语句超时
默认允许常规 DML、建表、加列、建视图、重建视图
自动识别字段类型变更、视图依赖、大表风险
强拦截极少数明显危险 SQL
完整记录执行状态和错误原因
```

这样可以保持操作简单，同时比现有 Java 项目增加必要的自动风险兜底。

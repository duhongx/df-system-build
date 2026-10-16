# 192.168.1.150 机房试点

试点日期：2026-10-14。CloudHIS 与现有 Jenkins 并行运行，先验证平台，再逐个迁移构建任务。

## 部署信息

| 项目 | 配置 |
|---|---|
| 平台地址 | `http://192.168.1.150:8800` |
| 平台账号 | `admin / 123456`，不提供在线改密或邮件验证码 |
| 操作系统 | openEuler 22.03 LTS，x86_64 |
| 程序与配置 | `/opt/df-build-server/df-build-server`、`config.yaml` |
| 服务 | `df-build-server.service`，由 systemd 管理 |
| 平台数据库 | 独立 Docker 容器 `df-build-postgres`，使用服务器已有的 `postgres:14` 镜像 |
| 数据库监听 | `127.0.0.1:55432`，库和账号为 `df_build` |
| 数据库文件 | `/opt/df-build-server/postgres` |
| 工作区 | `/opt/df-build-server/workspaces` |
| 凭据文件 | `runtime.env`、`config.yaml`、`secrets/db-password`，仅服务器 root 可读 |
| 构建源码快照 | `/opt/df-build-server/source-20261014/backend`，包含本地工作区改动，排除本地运行配置 |
| Go 工具链 | `/opt/df-build-server/toolchain/go`，Go 1.25.8，CGO 开启 |

数据库密码、JWT 密钥及 32 字节加密密钥在服务器生成。备份和迁移时需保留数据库、工作区以及这些凭据文件；不要重新生成加密密钥替换已有密钥。

## 与 Jenkins 的关系

- 保留 Jenkins 服务、任务、凭据、工作区及发布脚本。
- 平台使用独立目录和数据库；编译平台程序时限制为两个并发构建进程，平台任务并发已设置为 1。
- 目标机已有 Docker 20.10.16、Java 8、Gradle 6.8.3、Maven 和 Node 18.20.7。
- 已确认 kubeconfig 可读取，集群 API 为 `https://192.168.1.151:6443`，存在 `prod` namespace；只做资源查询。
- 现有镜像仓库为 `192.168.1.154:8888`，匿名访问 `/v2/` 返回 401；部署后已由用户配置有效凭据，真实推送验证结果见下文。
- 平台初始化的应用、编译配置和模板来自仓库种子数据，不代表已经与现有 Jenkins 任务对齐。当前业务编译已统一使用本机工具，旧 Docker 编译配置已停用；试点应用需选择匹配的本机编译配置。

## 运维命令

在目标服务器执行：

```bash
systemctl status df-build-server
journalctl -u df-build-server -n 100 --no-pager
curl -fsS http://127.0.0.1:8800/health
docker exec df-build-postgres pg_isready -U df_build -d df_build
```

暂停试点平台可执行 `systemctl stop df-build-server`，不会停止 Jenkins；平台数据库独立运行。不要删除 PostgreSQL 数据目录或工作区来重置试点。

## 首个应用的试点步骤

1. 选择一个 Jenkins 任务，核对 Git 地址和分支、JDK/Node 版本、安装及编译命令、制品目录、Dockerfile、ConfigMap 和目标 Deployment。
2. 将该应用的配置录入平台；平台构建并发建议先设为 1，与 Jenkins 同机时避免资源竞争。
3. 先执行编译和镜像制作，确认制品、镜像内容及推送结果；镜像制作完成不会自动滚动更新。
4. 确认更新窗口并停止同一应用在 Jenkins 中的并行发布，再单独执行滚动更新。
5. 检查 Pod 就绪、入口和业务功能，再验证人工回滚。业务数据和 SQL 不随镜像回滚恢复。

安装和平台可访问性验证不代表业务应用构建、更新或回滚已验收。

## 本次验证结果

- Linux/amd64 CGO 编译通过，程序已安装并设置为开机启动；`df-build-server` 和 Jenkins 均保持 active。部署前后 Jenkins 主进程均为 PID 1081。
- 从服务器和本机访问 `/health` 均返回 `status=ok`、`database=healthy`。
- 管理员登录、个人资料读取和退出通过；数据库只有一个用户，登录响应不再包含首次改密字段。
- 工作台、应用列表、编译配置、日志来源/收集记录、SQL 文件列表及 Kubernetes namespace 查询均返回业务成功状态。
- 改密和验证码接口均返回 HTTP 404；已部署的前端入口资源不包含这两个接口或改密入口。
- 使用本机 Chrome 查看真实工作台与日志收集页面，账号菜单只显示个人信息和退出登录。
- 本地前端生产构建、后端 `go test ./...` 和 `git diff --check` 通过；后端测试未提供测试数据库，数据库集成用例按规则跳过。
- 程序 SHA-256：`a80ebaedc59ce6a157c652d3b69b30cd99225abbc732a6a5619700c449eb8477`。

以上为首次安装验证范围。后续已完成一个 Java 应用的真实编译和镜像测试，见下文；Jenkins 任务仍未修改。

## 首个 Java 应用真实测试（2026-10-14）

| 项目 | 实际结果 |
|---|---|
| Jenkins 对照任务 | `yb-yibaogl-test` |
| 平台试点任务 | `机房试点-yb-yibaogl-RC_2.16.1_250514`，任务 ID 1 |
| 流水线 | `yb-yibaogl-0001`，ID 1，状态 `IMAGE_READY` |
| 详情 | `http://192.168.1.150:8800/release/1` |
| 分支 | `RC_2.16.1_250514` |
| Git 提交 | `67b5cf6e9db2bb3fad6ee07126e62b36cd365820` |
| 编译配置 | 新建本机 Java 8 / Gradle 6.8.3 配置，ID 8 |
| 编译命令 | `/usr/local/src/gradle/bin/gradle --no-daemon --max-workers=2 clean build -x test` |
| 实际阶段耗时 | Git 2 秒、编译 51 秒、镜像制作 7 秒、推送 11 秒；流水线共 72 秒 |
| 推送镜像 | `192.168.1.154:8888/yb-yibaogl:RC_2.16.1_250514-20261014142613-p1` |
| 仓库 digest | `sha256:e150169ef96cab5c8dbed9103edf463a26390e3fe30f7acdcee82932b51f52c4` |
| 制品 | JAR 193724665 字节；镜像内与构建目录中的 JAR SHA-256 一致 |
| JAR SHA-256 | `b980b3172fd7c64a57738bd41decbd02e55cf32464fdcb7206bda01441e2553f` |

平台使用服务器现有 SSH 密钥读取 GitLab，将该应用的仓库地址对齐为 SSH 地址。Java Dockerfile、启动及停机模板对齐 Jenkins 现用模板，基础镜像为 `192.168.1.154:8888/base/java:v2`。修改前的应用配置和三个模板保存在服务器 `/opt/df-build-server/pilot-tests/20261014/before-config.json`，文件仅 root 可读。三个 Java 模板属于平台公共配置，后续新任务会使用本轮对齐后的模板。

浏览器从任务列表执行，确认框明确展示“先编译并制作、推送镜像，完成后单独执行滚动更新”。任务队列和详情页状态正常，Gradle 阶段日志显示 `BUILD SUCCESSFUL`。镜像通过创建未启动的临时容器读取文件验证，临时容器已清理；没有启动业务进程。

镜像内的启动和停机脚本与 Jenkins 模板按应用名渲染后的内容逐字一致。推送阶段确认了仓库 digest，镜像和编译产物验证结果保存在服务器 `pilot-tests/20261014/verification.json`。

### 环境连接测试

镜像仓库认证、Kubernetes、Nacos 和业务 PostgreSQL 连接测试通过，仅做连接与查询验证，没有执行 SQL 变更。

SkyWalking 原配置为本机 `192.168.1.150:11800`，测试返回端口未开放。根据 Jenkins 启动脚本和实际网络验证，修正平台 OAP 为 `192.168.1.154:11800`、GraphQL 为 `http://192.168.1.154:12800/graphql`。OAP TCP、平台连接测试和 GraphQL `{ __typename }` 查询均通过，旧配置保存在服务器 `skywalking-settings-before.json`。

### 当前边界与后续事项

- 没有触发 Jenkins 构建或修改 Jenkins 任务。
- 没有执行平台第二步滚动更新、业务 SQL 或回滚；编译按 Jenkins 原方式使用 `-x test`，未运行该业务应用的单元测试。
- 测试前后业务 Deployment UID、generation 和镜像完全一致：`prod/yb-yibaogl` 的 generation 为 307，仍使用 `192.168.1.154:8888/prod/yb-yibaogl:2026.10.14.13.59.00`。
- 首轮平台镜像命名为 `/yb-yibaogl`，Jenkins 为 `/prod/yb-yibaogl`；该差异已在下述镜像命名修复中解决。首轮镜像及其记录保留原名称。
- 镜像已推送，但新版本实际启动、滚动更新、业务功能及回滚仍待更新窗口内验证。Vue 构建和共享 web-main 更新尚未进行真实环境测试。

## 镜像命名修复与再次实测（2026-10-14）

按用户要求，应用镜像统一添加 `/prod`；源码标签改为“分支-8 位真实 commitid-北京时间年月日时分秒”，去掉标签中的流水线 ID。Java、独立 Vue 和共享 web-main 共用命名函数，基础镜像路径和仓库认证地址不变。

本地后端全量测试通过（未配置数据库的集成用例跳过）。服务器另建独立临时测试数据库，命名边界用例和 Java 镜像制作/推送的模拟 Docker API 链路测试通过；临时库已删除，未使用平台库或业务库作为测试库。

新程序已部署，保留旧二进制 `/opt/df-build-server/df-build-server.pre-naming-20261014`；新程序 SHA-256 为 `e7d0c7032622f367f8b83edfa4d0ef56a949baf7ee7c7227f1cb54458430a6d3`。

实际执行同一试点任务生成流水线 `yb-yibaogl-0002`：

- 详情：`http://192.168.1.150:8800/release/2`。
- 结果：`IMAGE_READY`，耗时 68 秒，Git 拉取、编译、镜像制作和推送均成功。
- 镜像：`192.168.1.154:8888/prod/yb-yibaogl:RC_2.16.1_250514-67b5cf6e-20261014144737`。
- digest：`sha256:baf39dbd44e8855d79ee54e7d3e76bf4b5d252c86ed496904153c0db7365f492`。
- Chrome 详情页显示正确的 `/prod` 路径与标签，保留独立的第二步滚动更新入口。

本轮没有执行流水线 2 的滚动更新；测试前后 Deployment 均为 generation 308，镜像均为首轮流水线 1 的镜像。该状态是本轮开始时实际读取的基线，与首轮测试时 generation 307 的历史结果不同。新测试与基线记录保存在服务器 `pilot-tests/20261014/naming-test.json`。

上传制品可能没有 Git 提交号，多个前端制品也可能来自不同仓库；其标签暂保留 `manual-upload-时间-p流水线ID`，路径同样增加 `/prod`。这类制品的最终标签规则待明确，不用伪造提交号代替真实 commitid。

## 本机编译与客户现场模式实测（2026-10-14）

移除 Docker 编译代码后，同一程序通过全局参数“使用场景”切换机房与客户现场。150 保持机房模式（`deploy_source=both`），客户现场使用 `artifact`。两个场景均保留部署镜像制作／推送和独立滚动更新；现场无需 Git 和业务编译工具。详见 [使用场景说明](usage-scenarios.md)。

旧容器编译配置 1–5 已停用，名称和历史记录保留；本机配置 6–8 保持启用。接口不再输出 Docker 编译参数，旧执行器接口返回 404。Chrome 确认新增编译配置表单仅有本机参数，现场模式隐藏源码及编译配置入口，直接访问编译配置地址重定向到制品更新，最后已恢复机房模式。

| 验证 | 实际结果 |
|---|---|
| 机房源码流程 | [yb-yibaogl-0003](http://192.168.1.150:8800/release/3)，`IMAGE_READY`，88 秒；真实 Git、Java 8／Gradle 6.8.3 本机编译、镜像制作和推送通过 |
| 源码镜像 | `192.168.1.154:8888/prod/yb-yibaogl:RC_2.16.1_250514-67b5cf6e-20261014153326` |
| 现场制品流程 | 上传上述编译得到的 JAR（193721240 字节），版本 `20261014153349-5af983da`；[yb-yibaogl-0004](http://192.168.1.150:8800/release/4)，`IMAGE_READY`，1 秒（Docker 缓存命中） |
| 制品镜像 | `192.168.1.154:8888/prod/yb-yibaogl:manual-upload-20261014153350-p4` |
| 现场执行边界 | 制品流水线只有 `BUILD_IMAGE`、`PUSH_IMAGE`、待执行的 `K8S_DEPLOY` 和 `NOTIFY`，无 Git／编译；源码执行、批量执行、编译配置创建均返回 HTTP 403 |
| 业务 Deployment | 测试前后 UID、generation 309 和镜像完全一致；仍为源码流水线 2 镜像，两个新流水线均未滚动更新 |

本地前端生产构建和后端全量测试通过（未提供数据库配置的集成用例跳过）。服务器另建独立临时测试数据库，验证旧编译表重复迁移、原 Docker 配置停用、旧 NOT NULL 列下新配置写入、现场 HTTP 边界、本机命令和环境变量、Maven 产物目录、Vue 自定义输出目录与 ZIP 根目录、模拟镜像制作和推送；测试数据库均已删除。

部署前数据库及二进制备份位于 `/opt/df-build-server/backups/local-only-20261014/`，目录仅 root 可读。新源码快照为 `/opt/df-build-server/source-local-only-20261014/backend`，实测记录为 `/opt/df-build-server/pilot-tests/local-only-20261014/result.json`。平台与 Jenkins 服务均保持运行。

现场模式在 150 上验证，尚未安装到真实客户现场服务器。没有触发 Jenkins 构建、业务滚动更新、业务 SQL 或回滚；业务编译沿用 `-x test`，实际业务启动和 Vue 更新仍需相应环境验收。

## PostgreSQL 业务账号自动发现与密码覆盖（2026-10-14）

已部署“统一业务密码 + 账号专用密码覆盖”，环境配置不再填写单个业务用户名。管理员固定为 `postgres`，普通 SQL 根据文件名中的 schema 选择同名账号；选择和测试规则见 [PostgreSQL 说明](postgresql.md#业务账号与密码)。

- 部署前确认编译队列为空、无普通 SQL 执行任务，备份平台数据库及程序到 `/opt/df-build-server/backups/pg-accounts-20261014/`，目录 root 700、数据库备份 600。
- 旧管理员及统一业务密码启动时加密，迁移前后各配置值 SHA-256 摘要完全一致；未改连接地址、数据库名及密码原值。
- 程序源码快照为 `/opt/df-build-server/source-pg-accounts-20261014/backend`，验证记录为 `/opt/df-build-server/pilot-tests/pg-accounts-20261014/verification.json`；记录不含密码原文。备份恢复旧程序时，应同步恢复其平台数据库备份及原加密密钥。
- 管理员连接、实例查询通过，实际用户为 `postgres`；自动发现 27 个字面 `df_` 开头且可登录的业务账号，含 `df_zhushuju`、`df_lc_menzhen`、`df_lc_zhuyuan`、`df_yibao`、`df_yaokufang`。
- 业务测试对 27 个账号均报告密码认证失败（SQLSTATE 28P01），需要修正已保存的统一业务密码；此前管理员测试成功不能代表这些业务账号可用。本轮没有更改业务密码、添加实际账号覆盖或执行业务 SQL。
- Chrome 确认业务用户名输入框移除、统一业务密码和自动发现账号表格正常、各账号有设置专用密码入口。没有在真实环境保存测试密码。
- 本地独立临时 PostgreSQL 18 实例完成不同密码登录和专用密码真实 SQL 执行、密码覆盖错误／清除／解密失败、加密迁移、API、普通 SQL 失败重试回归；这些测试只操作临时测试数据库，未使用平台或业务数据库执行测试 SQL。

## 两种密码方式及弹窗界面修正（2026-10-14）

上一节的混合密码方式已替换为“所有业务用户同一个密码”和“业务用户密码不同”两种明确选择。150 默认保持 `postgresql_password_mode=shared`，地址、数据库名、管理员及业务密码原值不变；独立模式不会使用统一密码补缺。

业务账号密码配置改为弹窗，选择不同密码时自动打开并加载账号，支持行内输入、保存、清除和测试；连接测试明细也在弹窗中显示。Chrome 已实际验证这两种弹窗、27 个账号加载、关闭窗口和返回同一密码方式；没有在试点保存独立密码或改变实际密码方式，未执行业务 SQL。

修正版源码目录为 `/opt/df-build-server/source-pg-modes-20261014/backend`，完整源码包为 `source-pg-modes-20261014.tar.gz`；原平台程序及数据库备份为 `/opt/df-build-server/backups/pg-password-modes-20261014/`，配置摘要与接口验证记录为 `/opt/df-build-server/pilot-tests/pg-password-modes-20261014/verification.json`。后端测试、前端构建和独立临时 PostgreSQL 密码模式及 SQL 回归通过，测试实例已停止。

## 批量与单账号密码操作优化（2026-10-14）

已部署批量设置交互：同一密码模式可一次“保存并应用全部账号”；不同密码弹窗支持搜索、勾选／全选／仅选未配置、批量填入同一密码、单独修改某行，最后“保存并关闭”。账号密码与 PostgreSQL 连接设置、密码方式在同一事务提交，失败整批回滚。

源码目录为 `/opt/df-build-server/source-pg-bulk-20261014/backend`，部署前平台数据库和原程序备份为 `/opt/df-build-server/backups/pg-bulk-20261014/`，原配置摘要和账号密码元数据保存在 `/opt/df-build-server/pilot-tests/pg-bulk-20261014/before-config-hashes.json`。本轮没有保存真实账号密码或修改目标数据库账号；批量保存及失败回滚另用本机独立 PostgreSQL 测试实例验证通过，实例已停止。

## 构建历史自适应与本地自动更新（2026-10-14）

已部署到 150。构建历史填满主内容区，表格内部滚动、分页保持底部，列宽随可用空间分配，窄窗口使用横向滚动及文本提示。Chrome 实际检查了表格高度、触发时间列、操作列和底部分页。

当前 `customer_env=本地`，新提交任务镜像推送和结果保存成功后，在同一个 worker 中自动执行 Kubernetes 滚动更新及通知；源码与上传制品均适用。其他环境保留手动两步，已有 `IMAGE_READY` 历史记录保持手动入口。共享 web-main 自动流程保持原互斥锁，失败停止后续阶段，不自动回滚。

- 源码快照：`/opt/df-build-server/source-auto-layout-20261014/backend`。
- 部署前数据库与原程序备份：`/opt/df-build-server/backups/auto-layout-20261014/`（root 700，数据库备份 600）。
- 当前程序 SHA-256：`a6e9858888c6ce1e85ff36748ae29849a5445a1cdded1761c0a734cb0f9c1b15`。
- 检查记录：`/opt/df-build-server/pilot-tests/auto-layout-20261014/verification.json`。
- 后端测试、前端生产构建及确认文案测试通过；独立临时 PostgreSQL 验证 Java／Vue 的源码／制品自动阶段、非本地两步、推送及滚动失败停止，测试实例已停止。
- 部署前确认构建队列及 SQL 执行任务为空；部署后平台健康检查及 Jenkins 服务正常。本轮没有主动触发业务构建、Kubernetes 更新或业务 SQL；真实业务的自动发布链路尚未实测。

## 统一命名空间与保存并测试（2026-10-14）

已部署。创建／编辑任务和制品更新不再填写或展示命名空间，新流水线统一采用环境配置；历史流水线保留原目标。六个环境配置区块均改为“保存并测试”，只保存本区块后测试，保存失败停止，测试失败保留设置并提示。K8s 按当前选择的路径或内容生效，PostgreSQL 保留完整结果弹窗。

源码为 `/opt/df-build-server/source-env-save-20261014/backend`，部署前程序及平台数据库备份为 `/opt/df-build-server/backups/env-save-20261014/`（root 700，数据库备份 600），验证记录为 `/opt/df-build-server/pilot-tests/env-save-20261014/verification.json`。程序 SHA-256 为 `6b8dc781a7f2ff58dd944e78dccab619a8acd002221047242a69394b2ae6b30b`。

后端测试、前端构建和实际脚本的顺序／失败／部分保存验证通过，命名空间规则另在独立临时 PostgreSQL 验证。Chrome 实测创建任务字段移除、六个按钮及 K8s 保存后连接测试成功；其他五个区块本轮未对实际服务器连接测试。部署前构建和 SQL 任务为空，平台健康检查及 Jenkins 正常。没有触发业务构建、滚动更新、业务 SQL 或修改业务密码。

## 本地任务自动选择 Gradle／Node（2026-10-14）

已部署。创建／编辑任务按应用类型只读带出默认编译配置，无须用户填写配置 ID。150 当前默认 Java 为“本地 Gradle”（配置 6），Web 为“本地 Node”（配置 7）；原配置及已有任务保留。后端创建／编辑自动选择匹配配置，切换应用同步切换。非本地隐藏源码及编译入口并阻止源码执行；只上传制品的流程保持。

程序 SHA-256 为 `ef349b7d11e2903a3484c5bf0e41ce0259902583bb093fa3182d07f3a1db658e`，源码为 `/opt/df-build-server/source-defaults-20261014/backend`，部署前程序及平台数据库备份为 `/opt/df-build-server/backups/defaults-20261014/`（root 700，数据库备份 600），验证记录为 `/opt/df-build-server/pilot-tests/defaults-20261014/verification.json`。

后端测试、前端构建、独立临时 PostgreSQL 的自动默认与切换应用测试，以及前端本地／非本地入口状态测试通过。Chrome 实测 agg-service 自动带出 Gradle，切换 web-cdr 自动带出 Node，字段只读。非本地入口规则通过状态测试验证，本轮未改变 150 的真实部署环境；没有提交真实任务、业务构建、滚动更新或 SQL。平台健康检查及 Jenkins 正常。

## 前端逐页审核（2026-10-14）

修正版已部署，覆盖全部 30 个组件和 37 个路由的审核结果见 [前端逐页报告](frontend-audit-20261014.md)。重点修正记录取消语义及终态保护、跨页筛选、latest 分页、自适应列表与操作栏、失败诊断折叠、终端和公共弹窗布局，以及不生效／虚构／易误解的内容。

源码目录为 `/opt/df-build-server/source-ui-audit-20261014/backend`，部署前平台数据库及程序备份为 `/opt/df-build-server/backups/ui-audit-20261014/`（root 700，数据库备份 600），验证记录为 `/opt/df-build-server/pilot-tests/ui-audit-20261014/verification.json`。程序 SHA-256 为 `4324fd6edfe44d231b48e404a4b0542fb051da454347a53d9d83355238c4bfcd`。

部署前确认构建／SQL 无运行任务；部署后平台健康检查及 Jenkins 正常。Chrome 复核修正后的历史、失败详情、服务器和 K8s 列表及窄视口，缩放已恢复。前后端构建／测试和独立临时 PostgreSQL 验证通过，临时库实例已停止。真实业务操作与全量实际深色页面未测试，限制在报告中逐项说明；本轮未发起业务构建、滚动更新、SQL、通知测试、资源删除或修改业务凭据。

## Deployment 配置及前端目录更新修正上线（2026-10-15 15:06）

已按授权更新 df-build-server，版本标识 `deployment-config-20261015`，来自 `5593e2b` 基础上的已验证工作区快照，未创建 Git 提交。二进制 SHA256 为 `9c4c9d183cc0484833b9907a90965e5ce2689a835dda13b0524f6ed6a24fc11f`，源码和程序在 `/opt/df-build-server/pilot-tests/deployment-config-final-1792047381/`，运行记录为 `/opt/df-build-server/release.json`。

- 备份目录 `/opt/df-build-server/backups/deployment-config-20261015T150632/` 包含原程序、配置、发布记录和停止服务后的平台数据库备份。
- 服务与数据库健康检查通过；97 个任务、108 个应用数量一致，配置变更记录表已迁移。
- Chrome 直接检查线上页面：namespace 无切换入口，配置抽屉加载正常，现有 startupProbe 延迟 60 秒、失败阈值 30；env 与卷配置只读。页面检查未提交配置更新，无脚本错误。
- 发布前后所有生产 Deployment 的 UID 与 spec 摘要一致；Jenkins 始终 active 且 PID 未变。
- 此版本同时包含此前验证通过的前端所选目录完整替换、本地环境持久完整站点目录及 Nginx v2 基础镜像选择修正；本次更新平台程序，没有触发业务镜像构建、滚动更新，也没有修改生产 Nginx、Python 脚本或其站点目录。现有 Nginx 的迁移事项仍需单独处理。

部署及页面证据见 [output/deployment-config-20261015](../output/deployment-config-20261015/)。

## Deployment 完整配置弹窗与扩缩容整合（2026-10-15 15:19）

已部署 `deployment-dialog-20261015`。配置改为居中弹窗，按完整 Deployment 层级展示字段，允许修改项直接编辑，其余字段置灰；managedFields 默认折叠但可展开查看，环境敏感值与历史完整配置注解脱敏。副本数并入 spec.replicas，移除列表独立“扩缩容”按钮。顶部显示实际策略，与规则不符时提供“预览策略修正”。

隔离真实集群验证副本从 1 到 2、CPU／内存和探针配置联合更新通过，env 和卷保持原值；接口回归验证缩容到 0 保留 Pod 模板，并正确标记为扩缩容而非模板替换。浏览器模拟确认在旧副本数仍为 1 时不会提前提示扩容成功。

二进制 SHA256 为 `10fc6f6edaf9f7750c36cc079801f8fbba376b07a7412b5388d23000767f1ce0`，发布备份位于 `/opt/df-build-server/backups/deployment-dialog-20261015T151907/`。平台健康正常，97 个任务和 108 个应用数量一致，生产 Deployment UID 与 spec 摘要未变，Jenkins PID 未变。未在生产执行扩缩容或配置保存。证据见 [output/deployment-dialog-20261015](../output/deployment-dialog-20261015/)。

## Deployment 默认显示可修改配置（2026-10-15 15:29）

已部署 `deployment-editable-20261015`。参考 ACK 官方工作负载配置的基本信息、资源和健康检查分组，默认显示可修改配置表单；通过“完整 Deployment”切换查看完整层级，展开／收起按钮仅显示在完整视图。视图切换保留当前编辑草稿；后端字段白名单未改变。官方参考：https://help.aliyun.com/zh/ack/ack-managed-and-ack-dedicated/user-guide/create-a-stateless-application-by-using-a-deployment 。

前端类型检查、构建与浏览器模拟交互验证通过，Linux 二进制构建成功。发布备份为 `/opt/df-build-server/backups/deployment-editable-20261015T152956/`，二进制 SHA256 为 `e0e723ecc23e5454305169e914a04970e7cc023d5746e0c25b5cf879a1e47cf2`。服务健康正常，任务和应用仍分别为 97／108，生产 Deployment spec 未变、Jenkins PID 未变。证据见 [output/deployment-editable-20261015](../output/deployment-editable-20261015/)。

## 默认表单恢复更新策略参数（2026-10-15 15:36）

已部署 `deployment-strategy-20261015`，默认表单恢复只读策略、maxSurge／maxUnavailable 与进度期限，完整视图继续保留。此次仅恢复展示，没有修改业务探针或滚动更新参数，也未增加后端编辑字段。前端构建和浏览器交互验证通过；发布备份为 `/opt/df-build-server/backups/deployment-strategy-20261015T153559/`。服务与数据库健康，任务／应用数量为 97／108，生产 Deployment spec 和 Jenkins PID 未变。证据见 [output/deployment-strategy-20261015](../output/deployment-strategy-20261015/)。

## 默认 Java Deployment 使用 TCP 探针（2026-10-15 15:42）

已部署 `deployment-probes-20261015`。默认 `deployment-java` 模板的启动、就绪、存活检查统一为 TCP 8080；启动探针保留 initialDelaySeconds=60、periodSeconds=10、failureThreshold=30、timeoutSeconds=5，其余检查周期及失败阈值保持原值。150 保存的旧模板与上一版默认内容指纹相同，启动时已升级；只有完全匹配旧默认内容的模板才会自动升级，自定义模板不会被覆盖。Web 模板、Service 模板和版本采集逻辑未改。

真实隔离 PostgreSQL 验证首次生成、存量默认升级、重复启动不改写、自定义模板保留；YAML 严格解析及流水线阶段回归通过。发布后核对实际模板摘要 `f7732922669af89b4a7308461659a73dfac49fd51d6a70400119f829d0ab727e`，三项 tcpSocket、无 httpGet／actuator 健康检查，其余模板摘要一致。生产 Deployment spec、Jenkins PID 未变，服务及平台数据库健康。备份 `/opt/df-build-server/backups/deployment-probes-20261015T154211/`，证据 [output/deployment-probes-20261015](../output/deployment-probes-20261015/)。

此次更新仅修改生成模板；已有 Deployment 不会自动切换探针，后续只更新镜像时也仍保留既有探针。TCP 检查不等于应用业务就绪；探针改为 TCP 不会禁用或限制 Actuator 的 HTTP 暴露。

### 2026-10-15 SQL 文件列表与执行弹窗

已发布 `sql-alignment-20261015`，二进制 SHA256 `a93e94ba4a71e566dba334885ff097637aebdb259424e665d7204201f031b68f`。文件列表／全屏执行弹窗按 df-ops-service 流程调整，支持所选文件后台顺序执行、逐句结果及手工跳过。按 SQL 类型限定重复错误跳过；真实失败或停止后释放后续等待文件。

前端构建、浏览器模拟交互、隔离 PostgreSQL 的普通文件／队列测试及 Linux 构建通过。发布前确认无活动平台任务并备份平台库；发布后健康检查、SQL 记录校验、Kubernetes Deployment 规格校验均通过，Jenkins 进程保持运行。未执行业务 SQL。回退备份：`/opt/df-build-server/backups/sql-alignment-20261015T174854`。验证文件位于 `output/sql-platform-alignment-20261015/`。

### 2026-10-15 SQL 上传不再受命名规则拦截

已发布 `sql-upload-20261015`，二进制 SHA256 `e0f646877ea764f53b43f9feed77aafb1a5bebf37a75bafcd1a2dad42d455dc9`。SQL 文件先上传，非标准名称、空内容和无法拆分的内容保留原文并展示待处理原因；执行时过滤不满足条件的文件，正常文件继续。过滤文件仍可手工标记不执行或删除，完整 SQL 可直接查看。

18 文件浏览器模拟上传、执行过滤／标记／删除检查通过；隔离 PostgreSQL 普通 SQL、队列、审核回归及 HTTP 上传测试通过，前端及 Linux 构建通过。发布后平台健康，既有 SQL 记录与业务 Deployment 规格保持原样，Jenkins 持续运行。未执行业务 SQL。备份：`/opt/df-build-server/backups/sql-upload-20261015T181315`。验证记录位于 `output/sql-upload-20261015/`。

### 2026-10-15 SQL 详情支持修正语句

已发布 `sql-edit-20261015`，二进制 SHA256 `72dfb7ed0f78b81ae21928580ae9c478d63898ba95b1d9c08d15c341a8705fee`。SQL 详情新增编辑、取消、保存，允许修正未执行或失败的单条 SQL（含完整 DO 块）。保存同步完整文件及后续行号，保留其他成功／已跳过记录，并保存修改前后内容、修改人及原错误；执行中、排队中、结果待核实的文件不可编辑，并发旧版本覆盖被拦截。保存不自动执行，需手动点击“执行”重试。

隔离 PostgreSQL 已验证 DO 块修正及续执行、成功／跳过结果保留、原文件及行号同步、编辑状态限制和并发修改冲突；前端构建、浏览器模拟交互、Linux 构建通过。150 实际页面只读检查确认失败 DO 块可进入编辑，并取消退出，未保存或重试业务 SQL。更新后平台健康，原 SQL 记录和 Kubernetes Deployment 规格校验未变，Jenkins 进程持续运行。备份：`/opt/df-build-server/backups/sql-edit-20261015T183122`。验证记录位于 `output/sql-edit-20261015/`。

### 2026-10-15 SQL 执行错误定位

已发布 `sql-error-focus-20261015`，二进制 SHA256 `9f9e3420874a376dd7d660487dcdb9fe8edba3371bf9211a1d8b2b233170be24`。语句错误直接在对应执行信息列标红并展开，失败行高亮；打开失败文件或执行队列失败时自动定位具体文件及语句，提供“定位错误”再次跳转。取消语句报错在顶部重复展示的整段 SQL；没有语句结果的连接等文件级错误保留提示。手动滚动后刷新不重复抢占位置。SQL 编辑及执行规则保持原有行为。

前端构建、错误定位／多文件失败／文件级错误／SQL 编辑浏览器交互检查、Linux 构建通过。150 实际页面只读验证第 209 行 UPDATE 错误可见并高亮，顶部无重复报错。只读核对业务库 `df_zhushuju.gy_ribaomb` 主键为 `zhujianid`、不可延迟校验，6 条记录主键互不重复且没有自定义触发器；本次未修改或执行任何业务 SQL。发布后平台健康、SQL 记录和业务 Deployment 规格校验保持原样，Jenkins 进程未变。备份：`/opt/df-build-server/backups/sql-error-focus-20261015T185610`。验证记录位于 `output/sql-error-focus-20261015/`。

### 2026-10-16 SQL 执行控制与逐次记录

已发布 `sql-history-20261016`，二进制 SHA256 `76457319d98c43e61b826fe00ee537640418021d06e4ae7b5b66f1720a3998d9`。跳过保存及刷新期间锁定执行；停止按整个执行批次持久保存，在文件／语句边界检查；列表、工作台及后台使用自然版本顺序；上传后可补填并只读校验执行账号/schema，同名覆盖及删除后重传保留配置和已完成进度。

新增 `sql_statement_records`，记录每次执行尝试、自动／手动跳过、编辑、配置和沿用旧结果，保留原错误、操作人、时间及来源。新增批次文件快照，历史查询不再复用当前状态；工作台使用显式 current=true 查询续跑状态。页面增加“SQL记录”、单句“执行记录”、当次批次文件与 SQL 查看。

33 项服务、7 项 HTTP 顶层测试及前端构建通过；浏览器验证跳过保存期间执行锁、原错误保留、批次停止、手动账号配置、历史查询和旧批次快照，上传／编辑／错误定位回归通过。150 实际页面只读验证 SQL 关键字查询、手动跳过筛选及配置入口。升级迁移 193 条旧版当前状态快照，其中 2 条记录显示手动跳过；旧版已覆盖的历次尝试无法恢复，不伪造成完整日志。当前原 SQL 文件／语句状态校验未变，新增已提交数据库执行的记录为 0。Jenkins 及生产 Kubernetes Deployment 规格保持原样。

备份：`/opt/df-build-server/backups/sql-history-20261016T104645`。验证记录位于 `output/sql-history-20261016/`。本次没有执行或重试线上业务 SQL。

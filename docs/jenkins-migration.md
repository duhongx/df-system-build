# Jenkins 应用任务迁移

迁移目标是同机 df-build-system 的本地源码任务。迁移只保存配置，不触发源码构建、镜像推送或集群更新，也不修改 Jenkins 服务、任务、凭据和历史。

`scripts/migrate_jenkins_tasks.py` 使用受保护的盘点目录：Jenkins 配置快照、`/opt/deploy` 应用清单与前端路径映射、平台同一 SSH 密钥的仓库分支校验结果、现有部署与服务端口。脚本默认生成预览，`--apply` 先备份平台 PostgreSQL，再在单个事务中写入应用与任务，并校验导入结果。重复迁移复用相同应用、分支和执行方式的现有任务，保留任务 ID 和构建历史。

任务名称默认“应用-命名空间”；同一应用的其他分支追加分支名。源码仓库转换为 SSH 地址，不将 Jenkins 中的 HTTP 内嵌凭据写入新系统。

日常 Java/Vue 任务沿用本地公共 Gradle/Node 工具配置。`web-cdr`、`web-opm` 保留 `npm run build`，其他前端使用原有 `build:new`。已有应用的自定义字段与部署模板不覆盖；仓库地址、缺失的端口、默认分支及必要的编译命令差异写入迁移清单。

仅制作镜像的验收任务使用 `image_only`：完整拉取源码、编译并推送镜像后结束，不因本地环境触发滚动更新。任务列表显示此执行方式。停用任务或停用应用不能从单次执行接口或批量执行接口创建流水线。

原仓库分支不存在、应用配置缺失或前端部署映射不完整的任务导入后保持停用，原因写入清单。不会猜测替代分支或子应用目录。多应用打包及固定历史提交号的专项任务保留完整 Jenkins 配置归档，单独列出，不转换成普通应用构建。

在目标机执行：

```sh
python3 /path/to/migrate_jenkins_tasks.py --snapshot-dir /root/protected-jenkins-inventory
python3 /path/to/migrate_jenkins_tasks.py --snapshot-dir /root/protected-jenkins-inventory --apply
```

盘点目录的原始 XML、应用配置和数据库备份可能包含凭据，应只供 root 读取。对外迁移报告仅列任务名、分支、映射和异常原因。

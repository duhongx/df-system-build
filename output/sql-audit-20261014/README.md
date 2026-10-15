# SQL 审核复现材料

`sql_audit_proof_test.go` 是缺陷复现程序，断言的是当前错误行为。PASS 表示问题已复现，不代表修复完成。

只在隔离的 PostgreSQL 数据库中运行。运行前把文件临时复制到 `backend/internal/service/`，设置 `TEST_DATABASE_DSN` 指向独立、空白测试数据库，执行：

```sh
go test ./internal/service -run '^TestSQLAuditProof20261014$' -count=1 -v
```

测试依赖现有 `releaseSQLTestService`，会创建平台表、`sql_release` 角色和 schema 及专用表/视图。需要隔离库的管理员权限；密码配置使用非空测试密码。每次运行应使用新的空白数据库。结束后移除临时测试文件。切勿指向机房或客户业务库。

`proof.log` 保存 6 个缺陷复现结果，4 个独立日志保存既有集成测试结果，`verification.json` 说明覆盖和限制，`source-sha256.json` 标识本次审核的源码版本。

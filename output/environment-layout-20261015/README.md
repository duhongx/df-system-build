# 环境配置页面布局修复

已部署代码：`47324bc`；地址：`http://192.168.1.150:8800/settings/environment`。

原页面最大宽度被限制为 1080px，六组配置卡片始终单列排列。移除宽度上限，按页面实际可用宽度自适应：达到 1120px 时两列，其他尺寸一列。卡片间距统一为 20px，表单标签调整为 128px；输入框与按钮组合允许自然换行，手机上标签放在输入框上方。

保存、连接测试、密码管理和 kubeconfig 数据处理代码未修改。

验证：

- TypeScript 检查与生产构建通过；现有 settings store、SQL 设置页面检查通过。
- 预览模拟 API 验证六组保存并测试、总保存及 kubeconfig 来源字段；没有请求真实环境保存或连接测试接口。
- 线上真实页面在 2048、1440、1280、1024、768、390 六种宽度检查通过。六组卡片使用完整可用宽度，kubeconfig 两种录入方式均未出现控件越界或页面横向滚动，无浏览器异常、无业务写请求。截图已遮盖所有 textarea。
- 78 个线上静态文件与本地构建逐字节一致。服务器使用独立 PostgreSQL 14 恢复备份进行候选程序启动验证，再更新服务；健康检查通过。线上浏览器检查前后 settings 数据指纹一致。Jenkins 仍为 inactive / disabled。

程序、配置与数据库备份：`/opt/df-build-server/backups/devops-47324bc-20261015T103541`。

[宽屏](environment-2048.png) · [桌面](environment-1440.png) · [手机](environment-390.png) · [线上验证](live-verification.json) · [部署验证](deployment-verification.json)

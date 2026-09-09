# CloudHIS 智能管理平台 — 前端

Vue 3、Vite 5、TypeScript、Element Plus、Pinia、Vue Router、Axios 和 xterm。当前功能与缺口见 [项目进度](../PROGRESS.md)。

## 开发与构建

在 `frontend/` 下执行：

```bash
npm ci
npm run dev
```

开发地址为 `http://localhost:3001`；`vite.config.ts` 将 `/api` 代理到 `http://localhost:8080`，后端端口需匹配。WebSSH 使用 WebSocket，当前 Vite 代理没有 `ws: true` 配置，终端联调可用后端直接提供的 SPA。

```bash
npm run build
npm run preview
```

`build` 执行 `vue-tsc -b && vite build`，生成 `dist/`。`preview` 预览构建产物；当前 Vite 版本默认继承 `server.proxy`，API 仍需本地 8080 后端。单二进制发布还需复制 dist 到后端，见 [生产构建步骤](../README.md#构建单二进制版本)。

`package.json` 没有统一的 `npm test` 脚本。`src/utils/` 有独立的 `.test.mjs` 检查，其中部分依赖预编译的 `compiled/*.js`；前端构建通过不代表这些检查或浏览器端到端测试已执行。

## 当前页面

以下为页面入口，具体 API 以 `src/api/` 和后端路由为准。

| 功能 | 路径 |
|---|---|
| 登录、工作台 | `/login`、`/dashboard` |
| 构建任务、队列 | `/tasks`、`/build-queue` |
| 构建历史、流水线详情 | `/release`、`/release/:id` |
| 制品导入、更新版本、最新制品库 | `/artifacts/import`、`/artifacts/versions`、`/artifacts/latest` |
| 更新版本详情、新增更新任务 | `/artifacts/versions/:versionNo`、`/batch-deploy` |
| 服务器、终端、文件、监控 | `/servers`、`/servers/:id/terminal`、`/servers/:id/files`、`/servers/:id/monitor` |
| Kubernetes | `/kubernetes/overview`、`nodes`、`deployments`、`pods`、`services`、`configmaps`、`ingresses`（均在 `/kubernetes/` 下） |
| PostgreSQL | `/postgresql/instances`、`/postgresql/sql-execution`、`/postgresql/view-dependency-change` |
| 部署管理 | `/deployment/components`、`/deployment/global-config`、`/deployment/runs`、`/deployment/runs/:id` |
| 系统设置 | `/settings/general`、`apps`、`environment`、`build-configs`、`sql-execution`、`notifications`、`config-items`（均在 `/settings/` 下） |

环境配置统一容纳镜像仓库、Kubernetes、包下载及相关服务连接配置；原模板／执行器设置入口重定向到编译配置。`/artifact-collect`、`/artifact-batches` 和早期基础设施路由保留兼容重定向。

## 页面能力与边界

- 构建和部署运行使用 SSE 查看实时状态／日志，WebSSH 使用 xterm 和 WebSocket。
- 批量更新支持立即部署、构建镜像后等待卡点部署；版本页面展示部署记录及业务版本，SQL 包不会进入镜像构建。
- 布局支持菜单折叠、多标签页、暗色模式、站内消息、个人信息与账号操作。
- 退出和个人信息保存已接真实 API；邮件发送未实现，改密请求漏传确认密码，首次登录改密未强制处理。
- 工作台的远程同步／打包快捷链接没有对应路由；部分统计比较文案和登录页展示数字为静态内容。
- 登录守卫仅检查 Token 存在，实际鉴权依赖后端；没有完整的角色／项目权限页面。

## 目录

```text
public/        品牌图标与登录插图
src/
├── api/       按模块划分的 Axios 请求和类型
├── views/     功能页面
├── layouts/   侧边栏、标签页、消息和个人信息布局
├── router/    页面路由、旧入口重定向、登录守卫
├── stores/    用户和设置状态
├── styles/    全局样式
└── utils/     时间、制品选择、批量部署展示等辅助逻辑及独立检查
```

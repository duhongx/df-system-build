# CloudHIS智能管理平台 - 前端

Vue 3 + Vite + TypeScript + Element Plus。详见[项目根 README](../README.md)。

## 开发

```bash
npm install
npm run dev      # http://localhost:3001
```

Vite 会将 `/api/*` 代理到 `http://localhost:8080`（后端），确保后端已启动。

## 构建

```bash
npm run build    # 产出 dist/
npm run preview  # 本地预览构建结果
```

## 目录结构

```
src/
├── api/           API 客户端（axios 封装，按模块分）
├── assets/        静态资源
├── components/    可复用组件（PipelineStageBar / StageLogDrawer）
├── layouts/       布局（DefaultLayout：侧边栏 + Tab + 主区域）
├── router/        Vue Router 配置 + 登录守卫
├── stores/        Pinia stores（user）
├── styles/        全局样式（含深色模式）
├── types/         共享 TS 类型
└── views/         页面组件
```

## 主要页面

- 工作台 · 应用管理 · 任务列表 · 构建队列 · 构建历史 · Pipeline 详情
- 远程同步 · 远程打包 · 制品记录
- 系统设置（全局参数 · 模板 · 执行器 · 远端服务器 · 通知）

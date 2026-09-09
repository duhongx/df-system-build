# Vue 构建执行器镜像

核对日期：2026-09-09。镜像示例名为 `ops-builder-node:18-yarn1`，实际构建以同目录 [Dockerfile](Dockerfile) 为准。

## 构建上下文

当前 Dockerfile 使用镜像仓库中的 `node:18-slim`，安装 Yarn 1.22.22、zip 和 git，复制名为 `.npmrc` 的配置文件：

```text
构建目录/
├── Dockerfile
└── .npmrc
```

需要自行准备可用于构建环境的 npm 配置。文件必须保留前导点，不能使用旧说明中的 `npmrc`；当前指令为 `COPY .npmrc /root/.npmrc`。该配置会进入镜像层，内容应按构建镜像的使用范围管理。

## 构建与验证

在准备好的上下文目录执行：

```bash
docker build -t ops-builder-node:18-yarn1 .
docker run --rm ops-builder-node:18-yarn1 node --version
docker run --rm ops-builder-node:18-yarn1 yarn --version
docker run --rm ops-builder-node:18-yarn1 npm config get registry
```

Yarn registry 在 Dockerfile 中设为 `https://registry.npmmirror.com`；npm 配置由提供的 `.npmrc` 决定。构建依赖基础镜像、apt 和 npm 源可达。

## 平台使用

在“设置 → 编译配置”选择 Vue、Docker 模式，填写镜像、依赖安装命令、构建命令、制品目录及缓存挂载。平台通过 Docker SDK 启动容器，以下仅为手动运行示例：

```bash
docker run --rm \
  -v /path/to/source:/workspace \
  -v /opt/build-cache/yarn:/usr/local/share/.cache/yarn \
  -v /opt/build-cache/npm:/root/.npm \
  -w /workspace \
  ops-builder-node:18-yarn1 \
  sh -c "yarn install && npm run build:new"
```

`build:new` 是业务应用命令示例，需按目标项目 package.json 调整。它与本仓库管理界面的 `npm run build` 不同。微前端打包与更新规则见 [Vue 流程](../../docs/vue-app-build-flow.md)。

本次文档更新未重新构建该镜像。

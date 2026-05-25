# ops-builder-node:18-yarn1 执行器镜像

## 构建步骤（在 192.168.1.10 服务器上执行）

```bash
# 1. 创建构建目录
mkdir -p /tmp/ops-builder-node && cd /tmp/ops-builder-node

# 2. 拷贝 Dockerfile（从 git 或手动上传）
cp /path/to/df-build-system/docker/node18-yarn/Dockerfile .

# 3. 拷贝 .npmrc（注意文件名改成 npmrc，没有点号前缀）
cp /root/.npmrc ./npmrc

# 4. 构建镜像
docker build -t ops-builder-node:18-yarn1 .

# 5. 验证
docker run --rm ops-builder-node:18-yarn1 node --version
docker run --rm ops-builder-node:18-yarn1 yarn --version
docker run --rm ops-builder-node:18-yarn1 npm config get registry

# 6. 清理
rm -rf /tmp/ops-builder-node
```

## 目录结构

构建前确保当前目录有这 2 个文件：
```
/tmp/ops-builder-node/
├── Dockerfile    ← 本文件同目录
└── npmrc         ← 从 /root/.npmrc 拷贝（注意去掉点号前缀）
```

> 为什么文件名是 `npmrc` 不是 `.npmrc`？
> Docker COPY 命令对 `.` 开头的文件有时会被 .dockerignore 忽略，用 `npmrc` 更安全。

## 镜像内容

| 组件 | 版本 | 说明 |
|---|---|---|
| Node.js | 18 LTS | 基础运行时 |
| Yarn | 1.22.x | 包管理器 |
| npm | 随 Node 18 自带 | 用于 `npm run build` |
| zip | 系统包 | 打包 dist 目录 |
| .npmrc | - | 淘宝镜像 + 阿里云私服 token |

## 使用方式

系统构建时自动执行：
```bash
docker run --rm \
  -v /path/to/source:/workspace \
  -w /workspace \
  ops-builder-node:18-yarn1 \
  sh -c "yarn install && npm run build:new"
```

## 缓存加速（可选）

挂载 yarn/npm 缓存目录：
```bash
docker run --rm \
  -v /path/to/source:/workspace \
  -v /opt/build-cache/yarn:/usr/local/share/.cache/yarn \
  -v /opt/build-cache/npm:/root/.npm \
  -w /workspace \
  ops-builder-node:18-yarn1 \
  sh -c "yarn install && npm run build:new"
```

首次构建后依赖缓存留在宿主机，后续构建直接复用。

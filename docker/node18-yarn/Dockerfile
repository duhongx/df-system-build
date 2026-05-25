# ============================================================
# DF Build System - Node 18 + Yarn 执行器镜像
# 镜像名: ops-builder-node:18-yarn1
#
# 包含: Node.js 18 LTS + Yarn 1.22.x + zip
# 用途: Vue 前端项目编译（yarn install && npm run build:new）
#
# 构建方式（在服务器上执行）:
#   1. 将此目录上传到服务器
#   2. 将服务器上的 .npmrc 拷贝进来:
#      cp /root/.npmrc ./npmrc
#   3. docker build -t ops-builder-node:18-yarn1 .
# ============================================================

FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/node:18-slim


# 基础工具
RUN apt-get update && apt-get install -y --no-install-recommends \
    zip \
    git \
    && rm -rf /var/lib/apt/lists/*

# 安装 Yarn 1.x（node:18-slim 自带 corepack，但版本可能不对）
RUN npm install -g yarn@1.22.22

# 拷贝 .npmrc（registry + 私服 token）
COPY .npmrc /root/.npmrc

# Yarn 也读 .npmrc 的 registry，但显式设置一下更保险
RUN yarn config set registry https://registry.npmmirror.com

# 工作目录
WORKDIR /workspace

# 验证
RUN node --version && yarn --version && npm --version && zip --version | head -1

CMD ["bash"]

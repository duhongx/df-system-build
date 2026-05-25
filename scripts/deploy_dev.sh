#!/bin/bash
set -e

# ============================================================
# DF 构建平台 - DevOps 分支部署脚本
# 功能：拉取代码 → 编译前端 → 嵌入后端 → 编译后端 → 上传并启动
# 目标：192.168.1.56 (Dev 开发测试服务器)
# ============================================================

# ---------- 配置 ----------
GIT_REPO="git@192.168.1.206:duhong/df-build-system.git"
GIT_BRANCH="${1:-devops}"
WORK_DIR="/tmp/df-build-deploy-dev"
REMOTE_HOST="192.168.1.56"
REMOTE_USER="root"
REMOTE_DIR="/opt/df-build-server"
SERVICE_NAME="df-build-server"
BINARY_NAME="df-build-server"
SERVICE_PORT="8800"

# ---------- 颜色输出 ----------
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[$(date '+%H:%M:%S')]${NC} $1"; }
warn() { echo -e "${YELLOW}[$(date '+%H:%M:%S')] WARN:${NC} $1"; }
err() { echo -e "${RED}[$(date '+%H:%M:%S')] ERROR:${NC} $1"; exit 1; }

# ---------- Step 1: 准备工作目录 ----------
log "清理工作目录: ${WORK_DIR}"
rm -rf "${WORK_DIR}"
mkdir -p "${WORK_DIR}"

# ---------- Step 2: 拉取代码 ----------
log "拉取代码: ${GIT_REPO} (分支: ${GIT_BRANCH})"
git clone -b "${GIT_BRANCH}" "${GIT_REPO}" "${WORK_DIR}" || err "代码拉取失败"
log "代码拉取完成"

# ---------- Step 3: 编译前端 ----------
log "开始编译前端..."
cd "${WORK_DIR}/frontend"

if ! command -v node &> /dev/null; then
    err "Node.js 未安装，请先安装 Node.js 18+"
fi

if ! command -v npm &> /dev/null; then
    err "npm 未安装"
fi

npm install --registry=https://registry.npmmirror.com || err "前端依赖安装失败"
npm run build || err "前端编译失败"
log "前端编译完成: ${WORK_DIR}/frontend/dist/"

# ---------- Step 4: 将前端制品拷贝到后端 ----------
log "将前端制品拷贝到后端项目..."
rm -rf "${WORK_DIR}/backend/internal/web/dist"
mkdir -p "${WORK_DIR}/backend/internal/web/dist"
cp -r "${WORK_DIR}/frontend/dist/"* "${WORK_DIR}/backend/internal/web/dist/"
log "前端制品已拷贝到 backend/internal/web/dist/"

# ---------- Step 5: 确认 embed.go 存在 ----------
log "检查 embed.go..."
if [ ! -f "${WORK_DIR}/backend/internal/web/embed.go" ]; then
    err "embed.go 不存在，请确认代码完整"
fi
log "embed.go 已就绪"

# ---------- Step 6: 编译后端 ----------
log "开始编译后端..."
cd "${WORK_DIR}/backend"

# pg_query_go 依赖 cgo，使用 Docker 在 Linux 环境内构建目标二进制，避免 macOS 交叉编译 cgo 失败。
if ! command -v docker &> /dev/null; then
    err "Docker 未安装，无法构建包含 pg_query_go/cgo 的 Linux 目标二进制"
fi
docker run --rm \
    -v "${WORK_DIR}/backend:/src" \
    -w /src \
    -e GOPROXY=https://goproxy.cn,direct \
    -e CGO_ENABLED=1 \
    -e GOOS=linux \
    -e GOARCH=amd64 \
    golang:1.25-bookworm \
    go build -o "${BINARY_NAME}" ./cmd/server || err "后端编译失败"
log "后端编译完成: ${WORK_DIR}/backend/${BINARY_NAME}"

# ---------- Step 7: 上传到远程服务器 ----------
log "上传到远程服务器: ${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_DIR}"

# 确保远程目录存在
ssh "${REMOTE_USER}@${REMOTE_HOST}" "mkdir -p ${REMOTE_DIR}" || err "创建远程目录失败"

# 上传二进制文件
scp "${WORK_DIR}/backend/${BINARY_NAME}" "${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_DIR}/${BINARY_NAME}.new" || err "上传失败"

# 上传配置文件（每次都更新，确保和代码同步）
log "上传配置文件..."
scp "${WORK_DIR}/backend/config/config.yaml" "${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_DIR}/config.yaml"

log "上传完成"

# ---------- Step 8: 远程停止旧服务、替换、启动新服务 ----------
log "远程部署并启动服务..."
ssh "${REMOTE_USER}@${REMOTE_HOST}" << ENDSSH
set -e

cd ${REMOTE_DIR}

# 停止旧服务
if pgrep -f "${BINARY_NAME}" > /dev/null 2>&1; then
    echo "停止旧服务..."
    pkill -f "${BINARY_NAME}" || true
    sleep 2
fi

# 替换二进制
if [ -f "${BINARY_NAME}" ]; then
    rm -f "${BINARY_NAME}.bak"
    mv "${BINARY_NAME}" "${BINARY_NAME}.bak"
fi
mv "${BINARY_NAME}.new" "${BINARY_NAME}"
chmod +x "${BINARY_NAME}"

# 创建必要目录
mkdir -p data workspaces logs

# 启动服务（后台运行，日志输出到文件）
export CONFIG_PATH="${REMOTE_DIR}/config.yaml"
nohup ./${BINARY_NAME} > logs/server.log 2>&1 &

# 等待启动
sleep 3

# 检查是否启动成功
if pgrep -f "${BINARY_NAME}" > /dev/null 2>&1; then
    echo "服务启动成功 (PID: \$(pgrep -f ${BINARY_NAME}))"
else
    echo "服务启动失败，查看日志:"
    tail -20 logs/server.log
    exit 1
fi
ENDSSH

log "=========================================="
log "Dev 部署完成！"
log "服务地址: http://${REMOTE_HOST}:${SERVICE_PORT}"
log "健康检查: http://${REMOTE_HOST}:${SERVICE_PORT}/health"
log "=========================================="

# ---------- 清理 ----------
log "清理工作目录..."
rm -rf "${WORK_DIR}"
log "Done."

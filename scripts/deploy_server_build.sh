#!/bin/bash
set -e

# ============================================================
# DF 构建平台 - 服务器本地编译部署脚本
# 功能：在目标服务器上拉取代码 → 编译前端 → 编译后端 → 重启服务
# 目标：192.168.199.100 (DevOps 服务器，本地编译)
# 前提：服务器已安装 Go 1.22+、Node.js 18+、GCC
# ============================================================

# ---------- 配置 ----------
GIT_REPO="git@192.168.1.206:duhong/df-build-system.git"
GIT_BRANCH="${1:-devops}"
REMOTE_HOST="192.168.199.100"
REMOTE_USER="root"
REMOTE_DIR="/opt/df-build-server"
SERVICE_NAME="df-build-server"
BINARY_NAME="df-build-server"
SERVICE_PORT="8800"
WORK_DIR="/tmp/df-build-deploy"

# ---------- 颜色输出 ----------
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

log() { echo -e "${GREEN}[$(date '+%H:%M:%S')]${NC} $1"; }
err() { echo -e "${RED}[$(date '+%H:%M:%S')] ERROR:${NC} $1"; exit 1; }

log "开始在服务器上编译部署..."

# ---------- 在远程服务器上执行全部操作 ----------
ssh "${REMOTE_USER}@${REMOTE_HOST}" << ENDSSH
set -e

echo "[$(date '+%H:%M:%S')] === Step 1: 准备工作目录 ==="
rm -rf ${WORK_DIR}
mkdir -p ${WORK_DIR}

echo "[$(date '+%H:%M:%S')] === Step 2: 拉取代码 ==="
git clone -b ${GIT_BRANCH} ${GIT_REPO} ${WORK_DIR}

echo "[$(date '+%H:%M:%S')] === Step 3: 编译前端 ==="
cd ${WORK_DIR}/frontend
npm install --registry=https://registry.npmmirror.com
npm run build

echo "[$(date '+%H:%M:%S')] === Step 4: 拷贝前端制品到后端 ==="
rm -rf ${WORK_DIR}/backend/internal/web/dist
mkdir -p ${WORK_DIR}/backend/internal/web/dist
cp -r ${WORK_DIR}/frontend/dist/* ${WORK_DIR}/backend/internal/web/dist/

echo "[$(date '+%H:%M:%S')] === Step 5: 编译后端 ==="
cd ${WORK_DIR}/backend
source /etc/profile
export GOPROXY=https://goproxy.cn,direct
go build -o ${BINARY_NAME} ./cmd/server

echo "[$(date '+%H:%M:%S')] === Step 6: 停止旧服务 ==="
mkdir -p ${REMOTE_DIR}/logs
if pgrep -f "${BINARY_NAME}" > /dev/null 2>&1; then
    echo "停止旧服务..."
    pkill -f "${BINARY_NAME}" || true
    sleep 2
fi

echo "[$(date '+%H:%M:%S')] === Step 7: 替换二进制和配置 ==="
cd ${REMOTE_DIR}
if [ -f "${BINARY_NAME}" ]; then
    mv "${BINARY_NAME}" "${BINARY_NAME}.bak"
fi
cp ${WORK_DIR}/backend/${BINARY_NAME} ${REMOTE_DIR}/${BINARY_NAME}
chmod +x ${REMOTE_DIR}/${BINARY_NAME}
cp ${WORK_DIR}/backend/config/config.yaml ${REMOTE_DIR}/config.yaml

echo "[$(date '+%H:%M:%S')] === Step 8: 启动服务 ==="
mkdir -p data workspaces logs
export CONFIG_PATH="${REMOTE_DIR}/config.yaml"
nohup ./${BINARY_NAME} > logs/server.log 2>&1 &
sleep 3

if pgrep -f "${BINARY_NAME}" > /dev/null 2>&1; then
    echo "[$(date '+%H:%M:%S')] 服务启动成功 (PID: \$(pgrep -f ${BINARY_NAME}))"
else
    echo "[$(date '+%H:%M:%S')] 服务启动失败:"
    tail -20 logs/server.log
    exit 1
fi

echo "[$(date '+%H:%M:%S')] === Step 9: 清理 ==="
rm -rf ${WORK_DIR}

echo "=========================================="
echo "部署完成！"
echo "服务地址: http://${REMOTE_HOST}:${SERVICE_PORT}"
echo "=========================================="
ENDSSH

log "部署完成！访问 http://${REMOTE_HOST}:${SERVICE_PORT}"

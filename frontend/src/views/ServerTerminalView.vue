<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getManagedServer, getTerminalWsUrl, type ManagedServer } from '../api/server-mgmt'
import 'xterm/css/xterm.css'

const route = useRoute()
const router = useRouter()
const serverId = Number(route.params.id)
const server = ref<ManagedServer | null>(null)
const terminalRef = ref<HTMLDivElement>()
const connected = ref(false)
const statusText = ref('连接中...')

let ws: WebSocket | null = null
let term: any = null

onMounted(async () => {
  try {
    server.value = await getManagedServer(serverId)
  } catch (e) {
    statusText.value = '服务器不存在'
    return
  }

  // Dynamically import xterm (loaded from CDN or node_modules)
  await initTerminal()
})

onUnmounted(() => {
  if (ws) { ws.close(); ws = null }
  if (term) { term.dispose(); term = null }
})

async function initTerminal() {
  // @ts-ignore - xterm loaded dynamically
  const { Terminal } = await import('xterm')
  // @ts-ignore
  const { FitAddon } = await import('xterm-addon-fit')

  term = new Terminal({
    cursorBlink: true,
    fontSize: 14,
    fontFamily: "'JetBrains Mono', 'Fira Code', 'Consolas', monospace",
    theme: {
      background: '#1e1e1e',
      foreground: '#d4d4d4',
      cursor: '#d4d4d4',
    },
    allowProposedApi: true,
    rightClickSelectsWord: true,
  })

  const fitAddon = new FitAddon()
  term.loadAddon(fitAddon)
  term.open(terminalRef.value!)
  fitAddon.fit()

  // Handle window resize
  const resizeHandler = () => {
    fitAddon.fit()
    // Send resize event to server
    if (ws && ws.readyState === WebSocket.OPEN) {
      const msg = JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows })
      ws.send(msg)
    }
  }
  window.addEventListener('resize', resizeHandler)

  // Connect WebSocket
  const wsUrl = getTerminalWsUrl(serverId)
  ws = new WebSocket(wsUrl)

  ws.onopen = () => {
    connected.value = true
    statusText.value = `已连接 ${server.value?.username}@${server.value?.host}`
    // Send initial terminal size
    const msg = JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows })
    ws?.send(msg)
  }

  ws.onmessage = (event) => {
    term.write(event.data)
  }

  ws.onclose = () => {
    connected.value = false
    statusText.value = '连接已断开'
    term.write('\r\n\x1b[31m--- 连接已断开 ---\x1b[0m\r\n')
  }

  ws.onerror = () => {
    statusText.value = '连接错误'
  }

  // Send terminal input to WebSocket
  term.onData((data: string) => {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data)
    }
  })

  // Right-click paste support
  terminalRef.value!.addEventListener('contextmenu', async (e: MouseEvent) => {
    e.preventDefault()
    try {
      const text = await navigator.clipboard.readText()
      if (text && ws && ws.readyState === WebSocket.OPEN) {
        ws.send(text)
      }
    } catch (err) {
      // Clipboard API not available or permission denied
    }
  })

  // Copy on selection (auto-copy)
  term.onSelectionChange(() => {
    const selection = term.getSelection()
    if (selection) {
      navigator.clipboard.writeText(selection).catch(() => {})
    }
  })
}

function handleBack() {
  router.push('/servers')
}

function handleReconnect() {
  if (ws) { ws.close() }
  if (term) { term.dispose() }
  connected.value = false
  statusText.value = '重新连接中...'
  initTerminal()
}
</script>

<template>
  <div class="terminal-page">
    <div class="terminal-header">
      <div class="header-left">
        <el-button size="small" @click="handleBack"><el-icon><ArrowLeft /></el-icon>返回</el-button>
        <span class="server-info" v-if="server">
          <el-tag :type="connected ? 'success' : 'danger'" size="small" effect="dark">
            {{ connected ? '已连接' : '未连接' }}
          </el-tag>
          <span style="margin-left: 8px;">{{ server.username }}@{{ server.host }}:{{ server.port }}</span>
          <span v-if="server.remark" style="color: #909399; margin-left: 8px;">({{ server.remark }})</span>
        </span>
      </div>
      <div class="header-right">
        <el-button size="small" @click="handleReconnect" :disabled="connected">重新连接</el-button>
      </div>
    </div>
    <div class="terminal-container" ref="terminalRef"></div>
  </div>
</template>

<style scoped>
.terminal-page {
  display: flex;
  flex-direction: column;
  height: calc(100vh - 130px);
}

.terminal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 16px;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 6px 6px 0 0;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.server-info {
  font-size: 13px;
  color: #303133;
}

.terminal-container {
  flex: 1;
  background: #1e1e1e;
  border-radius: 0 0 6px 6px;
  padding: 4px;
  overflow: hidden;
}

:deep(.xterm) {
  height: 100%;
}
</style>

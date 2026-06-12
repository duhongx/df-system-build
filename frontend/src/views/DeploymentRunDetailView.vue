<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  cancelRun, getRun, getRunLogs, createRun, runEventSource,
  type DeploymentRun, type DeploymentLog,
} from '../api/deployment'
import { formatTime } from '../utils/time'

const route = useRoute()
const router = useRouter()
const runId = Number(route.params.id)
const run = ref<DeploymentRun | null>(null)
const logs = ref<DeploymentLog[]>([])
const logBox = ref<HTMLElement | null>(null)
const stick = ref(true)
let es: EventSource | null = null

function statusTag(s: string) {
  const v = (s || '').toLowerCase()
  if (v === 'success' || v === 'deployed') return 'success'
  if (v === 'failed') return 'danger'
  if (v === 'running') return 'primary'
  if (v === 'canceled') return 'warning'
  return 'info'
}

const isRunning = () => (run.value?.status || '').toLowerCase() === 'running'

async function loadRun() {
  run.value = await getRun(runId)
}

async function loadHistory() {
  logs.value = await getRunLogs(runId, 0, 1000)
  scrollToBottom()
}

function scrollToBottom() {
  if (!stick.value) return
  nextTick(() => {
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  })
}

function subscribe() {
  es = runEventSource(runId)
  es.addEventListener('log', (e: MessageEvent) => {
    try {
      const entry = JSON.parse(e.data) as DeploymentLog
      logs.value.push(entry)
      scrollToBottom()
    } catch { /* ignore malformed frame */ }
  })
  es.addEventListener('done', () => {
    es?.close()
    loadRun()
  })
  es.onerror = () => { es?.close() }
}

async function handleCancel() {
  await cancelRun(runId)
  ElMessage.warning('已提交取消请求')
  loadRun()
}

async function handleRollback() {
  if (!run.value) return
  const body = run.value.scope_kind === 'all'
    ? { mode: 'cleanup' }
    : { mode: 'rollback', component: run.value.target_component }
  const res = await createRun(body)
  ElMessage.success(`已发起回滚，运行 #${res.id}`)
  router.push(`/deployment/runs/${res.id}`)
}

onMounted(async () => {
  await loadRun()
  await loadHistory()
  subscribe()
})
onBeforeUnmount(() => es?.close())
</script>

<template>
  <div class="page">
    <h4 class="page-title">运行 #{{ runId }} {{ run ? `(${run.target_component})` : '' }}</h4>

    <div class="content-card" v-if="run">
      <div class="overview">
        <span>状态 <el-tag :type="statusTag(run.status)" size="small">{{ run.status }}</el-tag></span>
        <span>类型 <strong>{{ run.task_type }}</strong></span>
        <span>范围 <strong>{{ run.scope_kind }}</strong></span>
        <span>开始 <strong>{{ formatTime(run.started_at) }}</strong></span>
        <div class="spacer" />
        <el-button v-if="isRunning()" size="small" type="danger" @click="handleCancel">取消</el-button>
        <el-button v-else size="small" type="warning" @click="handleRollback">回滚</el-button>
      </div>
      <p v-if="run.error_summary" class="err">{{ run.error_summary }}</p>
    </div>

    <div class="content-card log-card">
      <div class="log-head">
        <span>实时日志</span>
        <el-checkbox v-model="stick" size="small">跟随底部</el-checkbox>
      </div>
      <div class="log-box" ref="logBox">
        <div v-for="(l, i) in logs" :key="i" class="log-line" :class="{ err: l.is_error }">
          <span class="ts">{{ formatTime(l.timestamp)?.slice(11) }}</span>
          <span class="src">[{{ l.component }}/{{ l.host }}]</span>
          <span class="act">{{ l.action_name }}</span>
          <span class="st">{{ l.status }}</span>
          <span v-if="l.detail" class="dt">- {{ l.detail }}</span>
        </div>
        <el-empty v-if="!logs.length" :image-size="50" description="暂无日志" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; margin-bottom: 12px; }
.overview { display: flex; align-items: center; gap: 20px; font-size: 13px; color: #606266; }
.overview strong { color: #303133; margin-left: 4px; }
.spacer { flex: 1; }
.err { color: #f56c6c; font-size: 13px; margin: 10px 0 0; }
.log-card { display: flex; flex-direction: column; }
.log-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; font-size: 13px; font-weight: 500; }
.log-box { height: 460px; overflow-y: auto; background: #1e1e1e; border-radius: 6px; padding: 10px; font-family: 'JetBrains Mono', Consolas, monospace; font-size: 12px; line-height: 1.6; }
.log-line { color: #d4d4d4; white-space: pre-wrap; word-break: break-all; }
.log-line.err { color: #f48771; }
.ts { color: #6a9955; margin-right: 6px; }
.src { color: #569cd6; margin-right: 6px; }
.act { color: #dcdcaa; margin-right: 6px; }
.st { color: #c586c0; margin-right: 6px; }
</style>

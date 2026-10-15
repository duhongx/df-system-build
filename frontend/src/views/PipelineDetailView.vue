<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage } from 'element-plus'
import { rolloutPipeline, getPipeline, getStageLogs, streamLogsUrl, type Pipeline, type PipelineStage, type StageLog } from '../api/pipeline'
import { hasCompletedDeploymentError } from '../utils/pipeline-result'
import { formatTime } from '../utils/time'

const route = useRoute()
const task = ref<Pipeline | null>(null)
const loading = ref(true)
const rolling = ref(false)
const activeStageId = ref<number | null>(null)
const logLines = ref<StageLog[]>([])
const logLoading = ref(false)
let eventSource: EventSource | null = null
let pollTimer: ReturnType<typeof setInterval> | null = null

let logRequest = 0
const activeStatuses = new Set(['PENDING', 'RUNNING', 'DEPLOYING'])
const pipelineId = Number(route.params.id)

onMounted(async () => {
  loading.value = true
  try {
    task.value = await getPipeline(pipelineId)
    if (task.value && task.value.stages && task.value.stages.length) {
      const failed = task.value.stages.find(s => s.status === 'FAILED')
      const running = task.value.stages.find(s => s.status === 'RUNNING')
      const target = failed || running || task.value.stages[task.value.stages.length - 1]
      await selectStage(target)
    }
    if (task.value && activeStatuses.has(task.value.status)) {
      subscribeToLogs()
      startPolling()
    }
  } catch (e) {
    task.value = null
  } finally {
    loading.value = false
  }
})

onUnmounted(() => {
  if (eventSource) eventSource.close()
  if (pollTimer) clearInterval(pollTimer)
})

function subscribeToLogs() {
  eventSource?.close()
  eventSource = new EventSource(streamLogsUrl(pipelineId))
  eventSource.addEventListener('stage_log', (e: MessageEvent) => {
    const entry = JSON.parse(e.data) as StageLog
    if (entry.stageId === activeStageId.value) {
      logLines.value = mergeLogs(logLines.value, [entry])
    }
  })
  eventSource.addEventListener('done', () => {
    eventSource?.close()
    if (pollTimer) clearInterval(pollTimer)
    // Final refresh to get completed state
    refreshPipeline()
  })
}

function startPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = setInterval(async () => {
    await refreshPipeline()
    // Stop polling when pipeline is no longer running
    if (task.value && !activeStatuses.has(task.value.status)) {
      if (pollTimer) clearInterval(pollTimer)
      pollTimer = null
    }
  }, 3000)
}

async function refreshPipeline() {
  try {
    const updated = await getPipeline(pipelineId)
    if (updated) {
      const previousRunning = task.value?.stages?.find(s => s.status === 'RUNNING')
      task.value = { ...task.value!, ...updated, stages: updated.stages }
      const running = updated.stages?.find(s => s.status === 'RUNNING')
      if (running && (activeStageId.value === null || activeStageId.value === previousRunning?.id)) {
        if (activeStageId.value !== running.id) await selectStage(running)
        else await loadStageLogs(running.id)
      } else if (activeStageId.value !== null) {
        await loadStageLogs(activeStageId.value)
      } else if (updated.stages?.length) {
        const target = updated.stages.find(s => s.status === 'FAILED') || updated.stages[updated.stages.length - 1]!
        await selectStage(target)
      }
    }
  } catch (_) {}
}

function mergeLogs(saved: StageLog[], live: StageLog[]): StageLog[] {
  const entries = new Map<string, StageLog>()
  for (const row of [...saved, ...live]) {
    const key = row.id ? String(row.id) : `${row.stageId}:${row.timestamp}:${row.content}`
    entries.set(key, row)
  }
  return [...entries.values()].sort((a, b) => a.lineNumber - b.lineNumber || a.id - b.id)
}

async function loadStageLogs(stageId: number) {
  const request = ++logRequest
  try {
    const saved = await getStageLogs(pipelineId, stageId)
    if (request === logRequest && activeStageId.value === stageId) {
      logLines.value = mergeLogs(saved, logLines.value)
    }
  } finally {
    if (request === logRequest) logLoading.value = false
  }
}

async function selectStage(stage: PipelineStage) {
  activeStageId.value = stage.id
  logLines.value = []
  logLoading.value = true
  try {
    await loadStageLogs(stage.id)
  } catch (_) {
    // Keep live entries received while the history request was in flight.
  }
}

async function handleRollout() {
  if (rolling.value || task.value?.status !== 'IMAGE_READY') return
  rolling.value = true
  try {
    await rolloutPipeline(pipelineId)
    ElMessage.success('滚动更新已提交')
    await refreshPipeline()
    subscribeToLogs()
    startPolling()
  } finally { rolling.value = false }
}

function stageStatusColor(status: string) {
  const map: Record<string, string> = {
    SUCCESS: '#67c23a', FAILED: '#f56c6c', RUNNING: '#409eff',
    PENDING: '#dcdfe6', SKIPPED: '#c0c4cc', WARNING: '#e6a23c',
  }
  return map[status] || '#dcdfe6'
}

function statusType(status: string) {
  const map: Record<string, string> = {
    SUCCESS: 'success', FAILED: 'danger', RUNNING: 'primary',
    PENDING: 'info', CANCELED: 'warning', IMAGE_READY: 'success', DEPLOYING: 'primary',
  }
  return map[status] || 'info'
}

function statusLabel(status: string) {
  const map: Record<string, string> = {
    SUCCESS: '成功', FAILED: '失败', RUNNING: '运行中',
    PENDING: '排队中', CANCELED: '已取消', IMAGE_READY: '镜像制作完成', DEPLOYING: '滚动更新排队中',
  }
  return map[status] || status
}

const deploymentRecordError = computed(() => !!task.value && hasCompletedDeploymentError(task.value))

const frontendPhase = computed(() => ({
  preparing: '发布预检', assets_submitting: '提交资源镜像', assets_submitted: '资源镜像滚动中',
  assets_ready: '资源已就绪', submitting: '提交正式镜像', submitted: '正式镜像滚动中',
  verifying: '验证全部副本', completed: '技术验证完成',
} as Record<string, string>)[task.value?.deploymentProgress || ''] || '镜像准备')

const activeStage = computed(() => {
  if (!task.value) return null
  return (task.value.stages || []).find(s => s.id === activeStageId.value) || null
})
</script>

<template>
  <div class="page" v-loading="loading">
    <template v-if="task">
      <div class="info-bar">
        <div class="info-bar-left">
          <el-tag :type="deploymentRecordError ? 'warning' : statusType(task.status)" effect="dark" size="default">{{ deploymentRecordError ? '部署成功，结果记录异常' : statusLabel(task.status) }}</el-tag>
          <span class="info-task-no">{{ task.pipelineNo }}</span>
          <span class="info-sep">|</span>
          <span class="info-app">{{ task.appName }}</span>
          <span class="info-sep">|</span>
          <span class="info-branch">{{ task.gitBranch }}</span>
        </div>
        <div class="info-bar-right">
          <el-button v-if="task.status === 'IMAGE_READY'" type="primary" :loading="rolling" @click="handleRollout">第二步：滚动更新</el-button>
          <span class="info-meta">触发人: {{ task.triggerUser }}</span>
          <span class="info-meta">耗时: {{ task.durationSeconds != null ? task.durationSeconds + 's' : '-' }}</span>
          <span class="info-meta">{{ formatTime(task.startTime) }}</span>
        </div>
      </div>

      <div class="pipeline-bar">
        <div
          v-for="(stage, idx) in (task.stages || [])"
          :key="stage.id"
          class="pipeline-stage"
          :class="{ active: stage.id === activeStageId }"
          @click="selectStage(stage)"
        >
          <div class="stage-dot" :style="{ background: stageStatusColor(stage.status) }">
            <el-icon v-if="stage.status === 'RUNNING'" :size="10" color="#fff"><Loading /></el-icon>
            <el-icon v-else-if="stage.status === 'SUCCESS'" :size="10" color="#fff"><Check /></el-icon>
            <el-icon v-else-if="stage.status === 'FAILED'" :size="10" color="#fff"><Close /></el-icon>
          </div>
          <div class="stage-label">{{ stage.stageName }}</div>
          <div class="stage-dur" v-if="stage.durationSeconds">{{ stage.durationSeconds }}s</div>
          <div class="stage-connector" v-if="idx < (task.stages?.length || 0) - 1" :style="{ background: stageStatusColor(stage.status) }"></div>
        </div>
      </div>

      <el-collapse v-if="task.frontendRelease" style="margin-bottom: 12px; background: var(--el-bg-color); padding: 0 16px;">
        <el-collapse-item name="scope">
          <template #title>
            <span>{{ task.frontendRelease.namespace }}/{{ task.frontendRelease.deploymentName }}</span>
            <el-tag size="small" style="margin-left: 12px;">{{ frontendPhase }}</el-tag>
          </template>
          <el-table :data="task.frontendRelease.regions" size="small">
            <el-table-column prop="appName" label="应用" min-width="180" />
            <el-table-column prop="path" label="更新目录" min-width="160" />
            <el-table-column label="本次文件" width="100"><template #default="{ row }">{{ Object.keys(row.current || {}).length }}</template></el-table-column>
            <el-table-column label="保留旧文件" width="110"><template #default="{ row }">{{ Object.keys(row.retained || {}).length }}</template></el-table-column>
          </el-table>
        </el-collapse-item>
      </el-collapse>

      <div class="log-panel">
        <div class="log-panel-header">
          <div class="log-panel-title" v-if="activeStage">
            <span class="log-dot" :style="{ background: stageStatusColor(activeStage.status) }"></span>
            {{ activeStage.stageName }}
            <el-tag v-if="activeStage.durationSeconds" size="small" type="info" effect="plain" style="margin-left: 8px;">
              {{ activeStage.durationSeconds }}s
            </el-tag>
          </div>
        </div>
        <div class="log-body" v-loading="logLoading">
          <div class="log-content" v-if="logLines.length">
            <div
              v-for="line in logLines"
              :key="line.id"
              class="log-line"
              :class="{ 'log-stderr': line.stream === 'stderr' }"
            >
              <span class="ln">{{ line.lineNumber }}</span>
              <span class="lc">{{ line.content }}</span>
            </div>
          </div>
          <div class="log-empty" v-else>
            <el-empty description="暂无日志输出" :image-size="48" />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.info-bar {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  padding: 12px 20px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.info-bar-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.info-task-no {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
}

.info-sep { color: #dcdfe6; }

.info-app {
  font-size: 13px;
  color: #409eff;
  font-weight: 500;
}

.info-branch {
  font-size: 13px;
  color: #606266;
}

.info-bar-right {
  display: flex;
  align-items: center;
  gap: 16px;
}

.info-meta {
  font-size: 12px;
  color: #909399;
}

.pipeline-bar {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  padding: 16px 20px;
  display: flex;
  align-items: flex-start;
  overflow-x: auto;
  margin-bottom: 12px;
}

.pipeline-stage {
  display: flex;
  flex-direction: column;
  align-items: center;
  position: relative;
  min-width: 72px;
  padding: 4px 6px;
  cursor: pointer;
  border-radius: 6px;
  transition: background 0.15s;
}

.pipeline-stage:hover { background: #f5f7fa; }
.pipeline-stage.active { background: #ecf5ff; }

.stage-dot {
  width: 20px;
  height: 20px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-bottom: 6px;
  flex-shrink: 0;
}

.stage-label {
  font-size: 11px;
  color: #606266;
  text-align: center;
  line-height: 1.3;
  max-width: 64px;
  word-break: keep-all;
}

.stage-dur {
  font-size: 10px;
  color: #909399;
  margin-top: 2px;
}

.stage-connector {
  position: absolute;
  top: 14px;
  left: calc(50% + 14px);
  width: calc(100% - 20px);
  height: 2px;
  opacity: 0.5;
}

.log-panel {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow: hidden;
}

.log-panel-header {
  padding: 10px 20px;
  border-bottom: 1px solid #f0f0f0;
}

.log-panel-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
  color: #303133;
}

.log-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.log-body {
  min-height: 280px;
  max-height: 500px;
  overflow-y: auto;
}

.log-content {
  font-family: 'JetBrains Mono', 'Fira Code', 'Menlo', 'Consolas', monospace;
  font-size: 12px;
  line-height: 1.7;
  background: #1e1e2e;
  color: #cdd6f4;
  padding: 14px 16px;
  min-height: 280px;
}

.log-line {
  display: flex;
  gap: 14px;
}

.log-line.log-stderr { color: #f38ba8; }

.ln {
  color: #585b70;
  min-width: 24px;
  text-align: right;
  user-select: none;
  flex-shrink: 0;
}

.lc {
  white-space: pre-wrap;
  word-break: break-all;
}

.log-empty {
  padding: 40px;
  display: flex;
  align-items: center;
  justify-content: center;
}
</style>

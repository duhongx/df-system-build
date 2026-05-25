<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getBuildQueue, cancelQueued, deployPipeline, listPipelines, type Pipeline } from '../api/pipeline'
import { formatTime } from '../utils/time'

const router = useRouter()
const runningTasks = ref<Pipeline[]>([])
const queuedTasks = ref<Pipeline[]>([])
const imageReadyTasks = ref<Pipeline[]>([])
const loading = ref(true)
let refreshTimer: any = null

async function load() {
  try {
    const [queueData, imageReadyData] = await Promise.all([
      getBuildQueue(),
      listPipelines({ status: 'IMAGE_READY', pageSize: 50 }),
    ])
    runningTasks.value = queueData.running || []
    queuedTasks.value = queueData.pending || []

    // Deduplicate: only keep the latest IMAGE_READY pipeline per app
    const allReady = imageReadyData.list || []
    const latestByApp = new Map<string, any>()
    for (const p of allReady) {
      const existing = latestByApp.get(p.appName)
      if (!existing || p.id > existing.id) {
        latestByApp.set(p.appName, p)
      }
    }
    imageReadyTasks.value = Array.from(latestByApp.values())
  } catch (e) {
    runningTasks.value = []
    queuedTasks.value = []
    imageReadyTasks.value = []
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  loading.value = true
  await load()
  refreshTimer = setInterval(load, 5000)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})

function currentStage(task: Pipeline) {
  const running = (task.stages || []).find(s => s.status === 'RUNNING')
  return running ? running.stageName : '等待中'
}

function elapsed(task: Pipeline) {
  if (!task.startTime) return '-'
  const start = new Date(task.startTime).getTime()
  const sec = Math.floor((Date.now() - start) / 1000)
  if (sec < 60) return `${sec}s`
  return `${Math.floor(sec / 60)}m ${sec % 60}s`
}

function goToDetail(task: Pipeline) {
  router.push(`/release/${task.id}`)
}

async function handleCancel(task: Pipeline) {
  await ElMessageBox.confirm(`确定取消 "${task.pipelineNo}" 吗？`, '确认取消', { type: 'warning' })
  try {
    await cancelQueued(task.id)
    ElMessage.success('已取消')
    await load()
  } catch (e) {
    // handled
  }
}

async function handleDeploy(task: Pipeline) {
  await ElMessageBox.confirm(`确定开始部署 "${task.pipelineNo}" 吗？`, '确认部署', { type: 'info' })
  try {
    await deployPipeline(task.id)
    ElMessage.success('部署已触发')
    await load()
  } catch (e) {
    // handled
  }
}
</script>

<template>
  <div class="build-queue-page" v-loading="loading">
    <el-card class="section-card" shadow="never">
      <template #header>
        <div class="section-header">
          <span class="section-title">
            <el-icon color="#409eff"><Loading /></el-icon>
            运行中 ({{ runningTasks.length }})
          </span>
        </div>
      </template>

      <el-table :data="runningTasks" @row-click="goToDetail" style="cursor: pointer;" v-if="runningTasks.length">
        <el-table-column prop="pipelineNo" label="任务编号" width="180" />
        <el-table-column prop="appName" label="应用" width="160" />
        <el-table-column prop="gitBranch" label="分支" width="200" show-overflow-tooltip />
        <el-table-column label="当前阶段" width="140">
          <template #default="{ row }">
            <el-tag type="primary" size="small" effect="plain">{{ currentStage(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="已耗时" width="100">
          <template #default="{ row }">{{ elapsed(row) }}</template>
        </el-table-column>
        <el-table-column prop="triggerUser" label="触发人" width="100" />
        <el-table-column label="开始时间" width="180">
          <template #default="{ row }">{{ formatTime(row.startTime) }}</template>
        </el-table-column>
      </el-table>
      <el-empty v-else description="当前没有运行中的任务" :image-size="60" />
    </el-card>

    <el-card class="section-card" shadow="never">
      <template #header>
        <div class="section-header">
          <span class="section-title">
            <el-icon color="#e6a23c"><Clock /></el-icon>
            排队中 ({{ queuedTasks.length }})
          </span>
        </div>
      </template>

      <el-table :data="queuedTasks" v-if="queuedTasks.length">
        <el-table-column label="#" width="60">
          <template #default="{ $index }">{{ $index + 1 }}</template>
        </el-table-column>
        <el-table-column prop="pipelineNo" label="任务编号" width="180" />
        <el-table-column prop="appName" label="应用" width="160" />
        <el-table-column prop="gitBranch" label="分支" width="200" show-overflow-tooltip />
        <el-table-column prop="triggerUser" label="触发人" width="100" />
        <el-table-column label="提交时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="100">
          <template #default="{ row }">
            <el-button type="danger" link size="small" @click="handleCancel(row)">取消</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-else description="队列为空" :image-size="60" />
    </el-card>

    <el-card class="section-card" shadow="never">
      <template #header>
        <div class="section-header">
          <span class="section-title">
            <el-icon color="#e6a23c"><WarningFilled /></el-icon>
            等待部署 ({{ imageReadyTasks.length }})
          </span>
        </div>
      </template>

      <el-table :data="imageReadyTasks" @row-click="goToDetail" style="cursor: pointer;" v-if="imageReadyTasks.length">
        <el-table-column prop="pipelineNo" label="任务编号" width="180" />
        <el-table-column prop="appName" label="应用" width="160" />
        <el-table-column prop="gitBranch" label="分支" width="200" show-overflow-tooltip />
        <el-table-column prop="triggerUser" label="触发人" width="100" />
        <el-table-column label="构建时间" width="180"><template #default="{ row }">{{ formatTime(row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button type="warning" link size="small" @click.stop="handleDeploy(row)">开始部署</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-else description="暂无等待部署的任务" :image-size="60" />
    </el-card>
  </div>
</template>

<style scoped>
.section-card {
  border-radius: 8px;
  margin-bottom: 16px;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.section-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 15px;
  font-weight: 600;
  color: #303133;
}
</style>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getBuildQueue, cancelQueued, deployPipeline, type Pipeline } from '../api/pipeline'
import {
  getArtifactDeployBatch,
  listArtifactDeployBatches,
  type ArtifactDeployBatch,
  type ArtifactDeployRecord,
} from '../api/batch-deploy'
import { formatTime } from '../utils/time'

const router = useRouter()
const runningTasks = ref<Pipeline[]>([])
const queuedTasks = ref<Pipeline[]>([])
const deployBatches = ref<ArtifactDeployBatch[]>([])
const selectedBatch = ref<ArtifactDeployBatch | null>(null)
const selectedRecords = ref<ArtifactDeployRecord[]>([])
const batchDrawerVisible = ref(false)
const loading = ref(true)
let refreshTimer: any = null
const imageReadyBatches = computed(() => deployBatches.value.filter(batch => batch.deployMode === 'cutover' && batch.status === 'image_ready'))

async function load() {
  try {
    const [queueData, batchData] = await Promise.all([
      getBuildQueue(),
      listArtifactDeployBatches('image_ready'),
    ])
    runningTasks.value = queueData.running || []
    queuedTasks.value = queueData.pending || []
    deployBatches.value = batchData.batches || []
  } catch (e) {
    runningTasks.value = []
    queuedTasks.value = []
    deployBatches.value = []
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

async function showBatchDetail(batch: ArtifactDeployBatch) {
  selectedBatch.value = batch
  selectedRecords.value = []
  batchDrawerVisible.value = true
  try {
    const result = await getArtifactDeployBatch(batch.deployBatchNo)
    selectedBatch.value = result.batch
    selectedRecords.value = result.records || []
  } catch (e) {
    // handled
  }
}

async function handleDeployBatch(batch: ArtifactDeployBatch) {
  await ElMessageBox.confirm(`确定开始部署版本 "${batch.versionNo}" 吗？将触发该版本中已构建镜像的 Deployment 更新。`, '确认批次部署', { type: 'info' })
  try {
    const result = await getArtifactDeployBatch(batch.deployBatchNo)
    const pipelineIds = Array.from(new Set((result.records || []).map(record => record.pipelineId).filter(Boolean)))
    for (const pipelineId of pipelineIds) {
      await deployPipeline(pipelineId)
    }
    ElMessage.success(`已触发 ${pipelineIds.length} 个部署任务`)
    await load()
  } catch (e) {
    // handled
  }
}

function batchStatusText(status: string) {
  if (status === 'image_ready') return '镜像已就绪'
  if (status === 'deployed') return '已部署'
  if (status === 'failed') return '失败'
  return status || '-'
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
            等待部署批次 ({{ imageReadyBatches.length }})
          </span>
        </div>
      </template>

      <el-table :data="imageReadyBatches" v-if="imageReadyBatches.length">
        <el-table-column prop="versionNo" label="版本号" min-width="220" show-overflow-tooltip />
        <el-table-column prop="namespace" label="命名空间" width="110" />
        <el-table-column prop="totalCount" label="应用数" width="90" />
        <el-table-column label="镜像状态" width="120">
          <template #default="{ row }">
            <el-tag type="success" size="small">{{ batchStatusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="triggerUser" label="创建人" width="100" />
        <el-table-column label="创建时间" width="180">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button type="primary" link size="small" @click="showBatchDetail(row)">详情</el-button>
            <el-button type="warning" link size="small" @click="handleDeployBatch(row)">开始部署</el-button>
          </template>
        </el-table-column>
      </el-table>
      <el-empty v-else description="暂无等待卡点部署的批次" :image-size="60" />
    </el-card>

    <el-drawer v-model="batchDrawerVisible" size="56%" title="等待部署批次详情">
      <template v-if="selectedBatch">
        <div class="batch-summary">
          <div><span>版本号</span>{{ selectedBatch.versionNo }}</div>
          <div><span>应用数</span>{{ selectedBatch.totalCount }}</div>
          <div><span>状态</span>{{ batchStatusText(selectedBatch.status) }}</div>
          <div><span>创建人</span>{{ selectedBatch.triggerUser || '-' }}</div>
        </div>
        <el-table :data="selectedRecords" border stripe size="small">
          <el-table-column prop="appName" label="应用" min-width="140" show-overflow-tooltip />
          <el-table-column prop="fileName" label="制品" min-width="150" show-overflow-tooltip />
          <el-table-column prop="deploymentName" label="Deployment" min-width="130" show-overflow-tooltip />
          <el-table-column prop="afterImage" label="新镜像" min-width="220" show-overflow-tooltip />
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-tag type="warning" size="small">{{ row.status === 'image_ready' ? '等待部署' : row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="100">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="goToDetail({ id: row.pipelineId } as Pipeline)">详情</el-button>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-drawer>
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

.batch-summary {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}

.batch-summary div {
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  background: #f8fafc;
  font-size: 12px;
  color: #303133;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.batch-summary span {
  color: #909399;
  margin-right: 8px;
}

.batch-alert {
  margin-bottom: 12px;
}
</style>

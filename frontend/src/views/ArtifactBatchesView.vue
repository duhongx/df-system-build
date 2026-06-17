<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listArtifactVersions, type ArtifactVersion } from '../api/batch-deploy'
import { formatTime } from '../utils/time'

const router = useRouter()
const loading = ref(false)
const batches = ref<ArtifactVersion[]>([])

const readyBatches = computed(() => batches.value.filter(batch => isReady(batch)))
const failedBatches = computed(() => batches.value.filter(batch => batch.status === 'failed'))

onMounted(loadBatches)

async function loadBatches() {
  loading.value = true
  try {
    const result = await listArtifactVersions()
    batches.value = result.versions || []
  } finally {
    loading.value = false
  }
}

function isReady(batch: ArtifactVersion) {
  return batch.status === 'available' || batch.status === 'ready' || batch.status === 'success'
}

function statusType(batch: ArtifactVersion) {
  if (isReady(batch)) return 'success'
  if (batch.status === 'failed') return 'danger'
  return 'warning'
}

function statusText(batch: ArtifactVersion) {
  if (isReady(batch)) return '可用'
  if (batch.status === 'failed') return '失败'
  if (batch.status === 'collecting' || batch.status === 'running') return '采集中'
  if (batch.status === 'pending') return '等待中'
  return batch.status || '未知'
}

function sourceTypeText(type: string) {
  if (type === 'upload') return '本地上传'
  if (type === 'download') return '服务器下载'
  if (type === 'artifact') return '制品库选择'
  return type || '-'
}

function showDetail(batch: ArtifactVersion) {
  router.push(`/artifacts/versions/${encodeURIComponent(batch.versionNo)}`)
}
</script>

<template>
  <div class="artifact-batches-page">
    <div class="page-header">
      <div>
        <h4>更新版本</h4>
        <p>本地上传、服务器下载、制品库选择都会先形成一个更新版本；这里仅查看版本和制品详情。</p>
      </div>
      <div class="page-actions">
        <el-button type="primary" :loading="loading" @click="loadBatches">刷新</el-button>
      </div>
    </div>

    <div class="summary-row">
      <div class="summary-card">
        <span>全部版本</span>
        <strong>{{ batches.length }}</strong>
      </div>
      <div class="summary-card">
        <span>可用版本</span>
        <strong>{{ readyBatches.length }}</strong>
      </div>
      <div class="summary-card">
        <span>异常版本</span>
        <strong>{{ failedBatches.length }}</strong>
      </div>
    </div>

    <el-card shadow="never" class="table-card">
      <el-table :data="batches" v-loading="loading" border stripe height="620">
        <el-table-column prop="versionNo" label="版本号" min-width="220" show-overflow-tooltip />
        <el-table-column label="来源" width="120">
          <template #default="{ row }">{{ sourceTypeText(row.sourceType) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="statusType(row)" size="small">{{ statusText(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="count" label="制品数" width="90" />
        <el-table-column prop="deployableCount" label="可部署" width="90" />
        <el-table-column prop="invalidCount" label="不可读" width="90" />
        <el-table-column prop="unmatchedCount" label="未匹配" width="90" />
        <el-table-column label="本机目录" min-width="300" show-overflow-tooltip>
          <template #default="{ row }">{{ row.targetPath || row.localDir || '-' }}</template>
        </el-table-column>
        <el-table-column label="更新时间" width="180">
          <template #default="{ row }">{{ formatTime(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="showDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.artifact-batches-page { display: flex; flex-direction: column; gap: 12px; }
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #fff;
  border: 1px solid #e5e7eb;
  padding: 16px 18px;
}
.page-header h4 { margin: 0 0 6px; font-size: 16px; color: #1f2937; }
.page-header p { margin: 0; color: #8a9099; }
.page-actions { display: flex; gap: 8px; }
.summary-row { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; }
.summary-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #fff;
  border: 1px solid #e5e7eb;
  padding: 14px 18px;
}
.summary-card span { color: #8a9099; }
.summary-card strong { font-size: 22px; color: #1f2937; }
.table-card { border-radius: 4px; }
</style>

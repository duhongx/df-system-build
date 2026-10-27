<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteArtifactVersion,
  listArtifactVersions,
  type ArtifactVersion,
} from '../api/batch-deploy'
import { formatBeijingTime } from '../utils/time'
import { matchesReleaseVersion } from '../utils/release-version'

const router = useRouter()
const loading = ref(false)
const batches = ref<ArtifactVersion[]>([])
const search = ref('')
const visibleBatches = computed(() => batches.value.filter(batch => matchesReleaseVersion(batch, search.value)))

const readyBatches = computed(() => batches.value.filter(batch => isReady(batch)))
const failedBatches = computed(() => batches.value.filter(batch => batch.status === 'failed'))

onMounted(loadBatches)

async function loadBatches() {
  loading.value = true
  try {
    const result = await listArtifactVersions()
    batches.value = sortArtifactVersions(result.versions || [])
  } finally {
    loading.value = false
  }
}

function sortArtifactVersions(versions: ArtifactVersion[]) {
  return [...versions].sort((a, b) => {
    const updatedDiff = new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime()
    if (updatedDiff !== 0) return updatedDiff
    return String(b.versionNo || '').localeCompare(String(a.versionNo || ''))
  })
}

function isReady(batch: ArtifactVersion) {
  return ['available', 'ready', 'success'].includes(batch.status) && (batch.remainingDeployableCount ?? batch.deployableCount) > 0
}

function statusType(batch: ArtifactVersion) {
  if (isReady(batch)) return 'success'
  if (batch.status === 'failed') return 'danger'
  if (batch.status === 'available' && batch.deployableCount === 0) return 'warning'
  return 'warning'
}

function statusText(batch: ArtifactVersion) {
  if (batch.deployableCount > 0 && batch.remainingDeployableCount === 0) return '已提交更新'
  if (isReady(batch)) return '可用'
  if (batch.status === 'failed') return '失败'
  if (batch.status === 'collecting' || batch.status === 'running') return '采集中'
  if (batch.status === 'pending') return '等待中'
  if (batch.status === 'available' && batch.deployableCount === 0) return '无可更新文件'
  if (batch.status === 'available') return '可用'
  return batch.status || '未知'
}

function sourceTypeText(type: string) {
  if (type === 'upload') return '本地上传'
  if (type === 'download') return '服务器下载'
  if (type === 'artifact') return '历史制品导入'
  return type || '-'
}

function showDetail(batch: ArtifactVersion) {
  router.push(`/artifacts/versions/${encodeURIComponent(batch.versionNo)}`)
}

async function deleteVersion(batch: ArtifactVersion) {
  try {
    await ElMessageBox.confirm(
        `确认删除更新记录 ${batch.versionNo}？这会清理工作区文件和记录，已有部署批次或正在下载的记录不能删除。`,
      '删除更新记录',
      {
        type: 'warning',
        confirmButtonText: '删除',
        cancelButtonText: '取消',
      },
    )
    await deleteArtifactVersion(batch.versionNo)
    ElMessage.success('已删除更新记录')
    await loadBatches()
  } catch (err) {
    if (err === 'cancel' || err === 'close') return
    throw err
  }
}
</script>

<template>
  <div class="artifact-batches-page viewport-table-page">
    <div class="page-header">
      <div>
        <h4>更新记录</h4>
      </div>
      <div class="page-actions">
        <el-input v-model="search" placeholder="搜索业务版本、记录编号或更新说明" clearable style="width: 310px" />
        <el-button type="primary" :loading="loading" @click="loadBatches">刷新</el-button>
      </div>
    </div>

    <div class="summary-row">
      <div class="summary-card">
        <span>全部记录</span>
        <strong>{{ batches.length }}</strong>
      </div>
      <div class="summary-card">
        <span>可更新记录</span>
        <strong>{{ readyBatches.length }}</strong>
      </div>
      <div class="summary-card">
        <span>异常记录</span>
        <strong>{{ failedBatches.length }}</strong>
      </div>
    </div>

    <el-card shadow="never" class="table-card">
      <el-table height="100%" :data="visibleBatches" v-loading="loading" border stripe>
        <el-table-column label="业务版本" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <el-tag v-if="row.releaseManifestError" type="warning">版本信息异常</el-tag>
            <strong v-else>{{ row.appVersion || '未提供' }}</strong>
          </template>
        </el-table-column>
        <el-table-column prop="releaseReadme" label="更新说明" min-width="240" show-overflow-tooltip />
        <el-table-column prop="releaseTime" label="发布时间" width="180" />
        <el-table-column prop="versionNo" label="记录编号" min-width="220" show-overflow-tooltip />
        <el-table-column label="文件来源" width="120">
          <template #default="{ row }">{{ sourceTypeText(row.sourceType) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="statusType(row)" size="small">{{ statusText(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="count" label="文件数" width="90" />
        <el-table-column prop="remainingDeployableCount" label="剩余可更新" width="110" />
        <el-table-column prop="invalidCount" label="不可读" width="90" />
        <el-table-column prop="unmatchedCount" label="未匹配" width="90" />
        <el-table-column label="本机目录" min-width="300" show-overflow-tooltip>
          <template #default="{ row }">{{ row.targetPath || row.localDir || '-' }}</template>
        </el-table-column>
        <el-table-column label="更新时间" width="180">
          <template #default="{ row }">{{ formatBeijingTime(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="170" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="showDetail(row)">详情</el-button>
            <el-button link type="danger" @click="deleteVersion(row)">删除</el-button>
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

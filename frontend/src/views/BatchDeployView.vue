<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type TableInstance } from 'element-plus'
import {
  executeBatchDeploy,
  getArtifactVersion,
  listArtifactVersions,
  type ArtifactVersion,
  type ArtifactVersionItem,
  type MatchResult,
} from '../api/batch-deploy'
import { useSettingsStore } from '../stores/settings'
import { isDeployableMatch, selectedDeployableFileNames } from '../utils/batch-deploy-selection'
import type { DeployMode } from '../utils/batch-deploy-flow'

const route = useRoute()
const router = useRouter()
const settingsStore = useSettingsStore()

const loading = ref(false)
const executing = ref(false)
const batchId = ref('')
const sourceType = ref('')
const sourceDir = ref('')
const namespace = ref('')
const deployMode = ref<DeployMode>('immediate')
const matchResults = ref<MatchResult[]>([])
const selectedFiles = ref<string[]>([])
const matchTableRef = ref<TableInstance>()
const versions = ref<ArtifactVersion[]>([])
const currentVersion = ref<ArtifactVersion | null>(null)

const deployableResults = computed(() => {
  const selected = new Set(selectedFiles.value)
  return matchResults.value.filter(result => selected.has(result.fileName) && isDeployableMatch(result))
})
const matchedCount = computed(() => matchResults.value.filter(result => result.matched && result.valid && !result.skipped).length)
const invalidCount = computed(() => matchResults.value.filter(result => !result.valid).length)
const unmatchedCount = computed(() => matchResults.value.filter(result => !result.matched && !result.skipped).length)
const skippedCount = computed(() => matchResults.value.filter(result => result.skipped).length)
const hasVersion = computed(() => Boolean(currentVersion.value || batchId.value))
const readyVersions = computed(() => versions.value.filter(isVersionReady))

onMounted(async () => {
  if (!settingsStore.loaded) {
    await settingsStore.fetchSettings()
  }
  namespace.value = settingsStore.k8sNamespace
  batchId.value = String(route.query.batchId || '')
  sourceType.value = String(route.query.sourceType || '')
  sourceDir.value = String(route.query.sourceDir || '')

  await loadVersions()
  if (batchId.value) {
    await loadVersionDetail(batchId.value)
  }
})

async function loadVersions() {
  loading.value = true
  try {
    const result = await listArtifactVersions(true)
    versions.value = result.versions || []
  } finally {
    loading.value = false
  }
}

function isVersionReady(version: ArtifactVersion) {
  return (version.status === 'available' || version.status === 'ready' || version.status === 'success') && version.deployableCount > 0
}

function versionStatusType(version: ArtifactVersion) {
  if (isVersionReady(version)) return 'success'
  if (version.status === 'failed') return 'danger'
  return 'warning'
}

function versionStatusText(version: ArtifactVersion) {
  if (isVersionReady(version)) return '可部署'
  if (version.status === 'failed') return '失败'
  if (version.status === 'collecting' || version.status === 'running') return '导入中'
  if (version.deployableCount === 0) return '无可部署制品'
  return version.status || '未知'
}

async function selectVersion(version: ArtifactVersion) {
  if (!isVersionReady(version)) {
    ElMessage.warning('请选择可部署的更新版本')
    return
  }
  const dir = version.targetPath || version.localDir
  if (!dir) {
    ElMessage.error('更新版本缺少本机目录，无法部署')
    return
  }
  batchId.value = version.versionNo
  sourceType.value = version.sourceType
  sourceDir.value = dir
  await router.replace({
    path: '/batch-deploy',
    query: { batchId: version.versionNo, sourceType: version.sourceType, sourceDir: dir },
  })
  await loadVersionDetail(version.versionNo)
}

async function loadVersionDetail(versionNo: string) {
  loading.value = true
  try {
    const result = await getArtifactVersion(versionNo)
    currentVersion.value = result.version
    batchId.value = result.version.versionNo
    sourceType.value = result.version.sourceType
    sourceDir.value = result.version.targetPath || result.version.localDir || sourceDir.value
    matchResults.value = (result.items || []).map(versionItemToMatchResult)
    selectedFiles.value = selectedDeployableFileNames(matchResults.value)
    await nextTick()
    for (const row of matchResults.value) {
      if (isDeployableMatch(row)) {
        matchTableRef.value?.toggleRowSelection(row, true)
      }
    }
    if (selectedFiles.value.length === 0) {
      ElMessage.warning('当前版本没有可部署制品')
    }
  } finally {
    loading.value = false
  }
}

function versionItemToMatchResult(item: ArtifactVersionItem): MatchResult {
  return {
    fileName: item.fileName,
    appName: item.appName,
    appType: item.appType,
    appId: item.appId,
    matched: item.matchStatus === 'matched',
    valid: item.validateStatus === 'valid',
    skipped: item.matchStatus === 'skipped',
    matchReason: item.statusReason,
  }
}

function sourceTypeText(type: string) {
  if (type === 'upload') return '本地上传'
  if (type === 'download') return '服务器下载'
  if (type === 'artifact') return '制品库选择'
  return type || '-'
}

function resultStatus(result: MatchResult) {
  if (result.skipped) return { type: 'info', text: '忽略' }
  if (!result.valid) return { type: 'danger', text: '不可读' }
  if (!result.matched) return { type: 'warning', text: '未匹配' }
  return { type: 'success', text: '可部署' }
}

function resetSelection() {
  currentVersion.value = null
  batchId.value = ''
  sourceDir.value = ''
  sourceType.value = ''
  matchResults.value = []
  selectedFiles.value = []
  router.replace('/batch-deploy')
}

async function executeDeploy() {
  if (deployableResults.value.length === 0) {
    ElMessage.warning('没有可部署制品')
    return
  }
  const items = deployableResults.value.map(result => ({ fileName: result.fileName, appId: result.appId }))
  executing.value = true
  try {
    const result = await executeBatchDeploy(sourceDir.value, items, namespace.value.trim(), batchId.value, deployMode.value)
    const errors = result.errors || []
    if (errors.length > 0) {
      ElMessage.warning(`已创建 ${result.pipelines?.length || 0} 个任务，${errors.length} 个失败`)
    } else {
      ElMessage.success(`已创建 ${result.pipelines?.length || 0} 个构建任务`)
    }
    router.push(deployMode.value === 'cutover' ? '/build-queue' : '/release')
  } finally {
    executing.value = false
  }
}
</script>

<template>
  <div class="batch-deploy-page">
    <div class="page-header">
      <div>
        <h4>批量部署</h4>
        <p>从已导入的更新版本创建构建任务；制品上传和服务器下载在制品管理中处理。</p>
      </div>
      <el-button @click="router.push('/artifacts/import')">进入制品管理</el-button>
    </div>

    <el-card v-if="!hasVersion" shadow="never" class="table-card" v-loading="loading">
      <template #header>
        <div class="table-header">
          <div>
            <strong>选择更新版本</strong>
            <span>{{ readyVersions.length }} 个可部署版本</span>
          </div>
          <el-button :loading="loading" @click="loadVersions">刷新</el-button>
        </div>
      </template>
      <el-table :data="readyVersions" border stripe height="620" table-layout="auto">
        <el-table-column prop="versionNo" label="版本号" min-width="210" show-overflow-tooltip />
        <el-table-column label="来源" width="110">
          <template #default="{ row }">{{ sourceTypeText(row.sourceType) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="versionStatusType(row)" size="small">{{ versionStatusText(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="count" label="制品数" width="86" />
        <el-table-column prop="deployableCount" label="可部署" width="86" />
        <el-table-column prop="invalidCount" label="不可读" width="86" />
        <el-table-column prop="unmatchedCount" label="未匹配" width="86" />
        <el-table-column label="本机目录" min-width="260" show-overflow-tooltip>
          <template #default="{ row }">{{ row.targetPath || row.localDir || '-' }}</template>
        </el-table-column>
        <el-table-column label="更新时间" width="170">
          <template #default="{ row }">{{ row.updatedAt ? new Date(row.updatedAt).toLocaleString() : '-' }}</template>
        </el-table-column>
        <el-table-column label="操作" width="96" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="selectVersion(row)">选择</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <template v-else>
      <el-card shadow="never" class="selected-card">
        <div class="selected-grid">
          <div><span>版本号</span><strong>{{ batchId || '-' }}</strong></div>
          <div><span>来源</span><strong>{{ sourceTypeText(sourceType) }}</strong></div>
          <div><span>可部署</span><strong>{{ matchedCount }}</strong></div>
          <div><span>异常</span><strong>{{ invalidCount + unmatchedCount }}</strong></div>
          <div class="path-cell"><span>目录</span><strong>{{ sourceDir || '-' }}</strong></div>
        </div>
        <el-button @click="resetSelection">更换版本</el-button>
      </el-card>

      <el-card shadow="never" class="table-card" v-loading="loading">
        <template #header>
          <div class="table-header">
            <div>
              <strong>应用匹配</strong>
              <span>默认勾选可部署制品；不可读、未匹配和 SQL 包不会进入部署。</span>
            </div>
            <div class="status-tags">
              <el-tag type="success">可部署 {{ matchedCount }}</el-tag>
              <el-tag type="danger">不可读 {{ invalidCount }}</el-tag>
              <el-tag type="warning">未匹配 {{ unmatchedCount }}</el-tag>
              <el-tag type="info">忽略 {{ skippedCount }}</el-tag>
            </div>
          </div>
        </template>
        <el-table
          ref="matchTableRef"
          :data="matchResults"
          border
          stripe
          height="520"
          table-layout="auto"
          @selection-change="(rows: MatchResult[]) => selectedFiles = rows.map(row => row.fileName)"
        >
          <el-table-column type="selection" width="46" :selectable="isDeployableMatch" />
          <el-table-column prop="fileName" label="制品文件" min-width="210" show-overflow-tooltip />
          <el-table-column prop="appName" label="匹配应用" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">{{ row.appName || '-' }}</template>
          </el-table-column>
          <el-table-column prop="appType" label="类型" width="90">
            <template #default="{ row }">{{ row.appType || '-' }}</template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="resultStatus(row).type" size="small">{{ resultStatus(row).text }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="matchReason" label="说明" min-width="230" show-overflow-tooltip />
        </el-table>
      </el-card>

      <div class="action-bar">
        <div class="deploy-options">
          <el-input v-model="namespace" placeholder="Namespace" class="namespace-input" />
          <el-segmented v-model="deployMode" :options="[
            { label: '立即部署', value: 'immediate' },
            { label: '卡点部署', value: 'cutover' },
          ]" />
          <span>{{ deployMode === 'cutover' ? '只构建并推送镜像，维护窗口再更新 Deployment。' : '镜像构建成功后自动更新 Deployment。' }}</span>
        </div>
        <el-button type="primary" :disabled="deployableResults.length === 0" :loading="executing" @click="executeDeploy">
          创建 {{ deployableResults.length }} 个构建任务
        </el-button>
      </div>
    </template>
  </div>
</template>

<style scoped>
.batch-deploy-page { display: flex; flex-direction: column; gap: 12px; }
.page-header,
.selected-card,
.table-card,
.action-bar {
  background: #fff;
  border: 1px solid #e5e7eb;
  border-radius: 4px;
}
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 18px;
}
.page-header h4 { margin: 0 0 6px; font-size: 16px; color: #1f2937; }
.page-header p { margin: 0; color: #8a9099; }
.table-card { border-radius: 4px; }
.table-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.table-header strong { display: block; color: #1f2937; margin-bottom: 4px; }
.table-header span { color: #8a9099; }
.status-tags { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
.selected-card :deep(.el-card__body) {
  display: flex;
  align-items: center;
  gap: 12px;
}
.selected-grid {
  flex: 1;
  min-width: 0;
  display: grid;
  grid-template-columns: 220px 120px 90px 90px minmax(260px, 1fr);
  gap: 8px;
}
.selected-grid > div {
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid #e5e7eb;
  background: #f8fafc;
}
.selected-grid span {
  display: block;
  font-size: 12px;
  color: #8a9099;
  margin-bottom: 4px;
}
.selected-grid strong {
  display: block;
  color: #1f2937;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.action-bar {
  position: sticky;
  bottom: 0;
  z-index: 5;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
  box-shadow: 0 -6px 18px rgba(15, 23, 42, 0.05);
}
.deploy-options {
  display: flex;
  align-items: center;
  gap: 10px;
  color: #606266;
}
.namespace-input { width: 180px; }
@media (max-width: 1280px) {
  .selected-grid { grid-template-columns: 1fr 1fr; }
  .path-cell { grid-column: 1 / -1; }
  .action-bar,
  .deploy-options { align-items: stretch; flex-direction: column; }
  .namespace-input { width: 100%; }
}
</style>

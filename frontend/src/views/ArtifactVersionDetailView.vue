<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteArtifactVersionItem,
  getArtifactDeployBatch,
  getArtifactVersion,
  redownloadArtifactVersionItem,
  replaceArtifactVersionItem,
  rollbackArtifactDeployBatch,
  rolloutArtifactDeployBatch,
  type ArtifactDeployBatch,
  type ArtifactDeployRecord,
  type ArtifactVersion,
  type ArtifactVersionItem,
} from '../api/batch-deploy'
import { formatTime } from '../utils/time'
import ReleaseVersionInfo from '../components/ReleaseVersionInfo.vue'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const deployLoading = ref(false)
const version = ref<ArtifactVersion | null>(null)
const items = ref<ArtifactVersionItem[]>([])
const deployBatch = ref<ArtifactDeployBatch | null>(null)
const deployRecords = ref<ArtifactDeployRecord[]>([])
const rolloutSubmitting = ref(false)
const rollbackSubmitting = ref(false)
const rollbacking = computed(() => rollbackSubmitting.value || deployBatch.value?.status === 'rolling_back')
const canRollback = computed(() => !rollbacking.value && deployRecords.value.some(record =>
  Boolean(record.beforeImage && record.afterImage)
  && !['pending', 'waiting', 'skipped', 'not_started'].includes(record.deployStatus)
  && !['success', 'skipped'].includes(record.rollbackStatus),
))
let rollbackPoll: ReturnType<typeof setTimeout> | undefined
let disposed = false
const itemOperatingId = ref<number | null>(null)
const replaceInput = ref<HTMLInputElement>()
const replacingItem = ref<ArtifactVersionItem | null>(null)
const versionJsonDialogVisible = ref(false)
const versionJsonDialogTitle = ref('')
const versionJsonDialogContent = ref('')

const versionNo = computed(() => String(route.params.versionNo || ''))
const deployableItems = computed(() => items.value.filter(item => item.deployable && !item.updateUsed))
const invalidItems = computed(() => items.value.filter(item => item.validateStatus === 'invalid'))
const unmatchedItems = computed(() => items.value.filter(item => item.matchStatus === 'unmatched' && item.validateStatus === 'valid'))
const versionEditable = computed(() => !deployBatch.value && !items.value.some(item => item.updateUsed))
const canContinueUpdate = computed(() => versionEditable.value && deployableItems.value.length > 0 && ['available', 'ready', 'success'].includes(version.value?.status || ''))
function continueUpdate() {
  if (!canContinueUpdate.value || !version.value) return
  router.push({ path: '/batch-deploy', query: { batchId: version.value.versionNo } })
}
const versionRows = computed(() => buildVersionRows(items.value))
const deployRecordRows = computed(() => buildDeployRecordRows(deployRecords.value))

onMounted(loadDetail)
onUnmounted(() => {
  disposed = true
  clearTimeout(rollbackPoll)
})

async function loadDetail() {
  loading.value = true
  try {
    const result = await getArtifactVersion(versionNo.value)
    version.value = result.version
    items.value = result.items || []
    void loadDeployBatch()
  } finally {
    loading.value = false
  }
}

async function loadDeployBatch() {
  clearTimeout(rollbackPoll)
  if (deployLoading.value || disposed) return
  deployLoading.value = true
  try {
    const wasRollingBack = deployBatch.value?.status === 'rolling_back'
    const result = await getArtifactDeployBatch(versionNo.value)
    if (disposed) return
    deployBatch.value = result.batch
    deployRecords.value = result.records || []
    if (wasRollingBack && result.batch?.status === 'rolled_back') {
      ElMessage.success('整体回滚完成，需恢复的旧镜像已恢复并就绪')
    } else if (wasRollingBack && result.batch?.status === 'rollback_failed') {
      ElMessage.error('回滚未全部完成，请查看各目标的详细错误')
    }
  } catch {
    // Retain the last known progress after a transient request failure.
  } finally {
    deployLoading.value = false
    if (!disposed && (deployBatch.value?.status === 'rolling_back' || deployRecords.value.some(record => ['building', 'deploying'].includes(record.status)))) {
      rollbackPoll = setTimeout(() => { void loadDeployBatch() }, 3000)
    }
  }
}

function sourceTypeText(type?: string) {
  if (type === 'upload') return '本地上传'
  if (type === 'download') return '服务器下载'
  if (type === 'artifact') return '历史制品导入'
  return type || '-'
}

function statusText(status?: string) {
  if (status === 'available' || status === 'ready' || status === 'success') return '可用'
  if (status === 'failed') return '失败'
  if (status === 'collecting' || status === 'running') return '采集中'
  return status || '-'
}

function statusType(status?: string) {
  if (status === 'available' || status === 'ready' || status === 'success') return 'success'
  if (status === 'failed') return 'danger'
  return 'warning'
}

function versionStatusText(current: ArtifactVersion) {
  if (current.deployableCount > 0 && current.remainingDeployableCount === 0) return '已提交更新'
  if (current.status === 'available' && current.deployableCount === 0) return '无可更新文件'
  return statusText(current.status)
}

function versionStatusType(current: ArtifactVersion) {
  if (current.deployableCount > 0 && current.remainingDeployableCount === 0) return 'warning'
  if (current.status === 'available' && current.deployableCount === 0) return 'warning'
  return statusType(current.status)
}

function validateTag(item: ArtifactVersionItem) {
  return item.validateStatus === 'valid'
    ? { type: 'success', text: '通过' }
    : { type: 'danger', text: '不可读' }
}

function matchTag(item: ArtifactVersionItem) {
  if (item.matchStatus === 'matched') return { type: 'success', text: '已匹配' }
  if (item.matchStatus === 'skipped') return { type: 'info', text: '已忽略' }
  return { type: 'warning', text: '未匹配' }
}

function formatBytes(size?: number) {
  if (!size) return '-'
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function deploymentRecordError(record: ArtifactDeployRecord) {
  return record.status === 'failed' && record.deployStatus === 'success' && record.rollbackStatus !== 'failed'
}

function deployRecordStatusType(status?: string) {
  if (status === 'deployed' || status === 'rolled_back') return 'success'
  if (status === 'failed' || status === 'rollback_failed') return 'danger'
  if (status === 'image_ready') return 'warning'
  if (status === 'current') return 'info'
  return 'info'
}

function deployRecordStatusText(status?: string) {
  if (status === 'current') return '当前状态'
  if (status === 'image_ready') return '镜像制作完成'
  if (status === 'deployed') return '已部署'
  if (status === 'rolled_back') return '已回滚'
  if (status === 'failed') return '失败'
  if (status === 'rolling_back') return '回滚中'
  if (status === 'rollback_failed') return '回滚未全部完成'
  if (status === 'deploying') return '部署中'
  if (status === 'pending') return '等待部署'
  return status || '-'
}

function rollbackStatusText(status?: string) {
  return ({ pending: '等待回滚', running: '回滚中', success: '回滚成功', failed: '回滚失败', skipped: '无需回滚（未提交部署）' } as Record<string, string>)[status || ''] || '-'
}

function formatRawJson(value?: string) {
  if (!value) return '-'
  try {
    const data = JSON.parse(value)
    return JSON.stringify(data, null, 2)
  } catch (e) {
    return value
  }
}

function parseVersionJson(value?: string) {
  if (!value) return null
  try {
    return JSON.parse(value)
  } catch (e) {
    return null
  }
}

function versionJsonText(value: unknown) {
  if (value === undefined || value === null || value === '') return '-'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function versionCommitId(data: any) {
  const idValue = data?.git?.commit?.id
  if (typeof idValue === 'object') return idValue.abbrev || idValue.full || '-'
  return idValue || data?.git?.commit?.id?.abbrev || data?.commit || '-'
}

function versionJsonSummaryFields(value?: string) {
  const data = parseVersionJson(value)
  if (!data || typeof data !== 'object') {
    return value ? [{ label: '原始值', value }] : []
  }
  if (data.git) {
    return [
      { label: 'branch', value: versionJsonText(data.git.branch) },
      { label: 'commit', value: versionJsonText(versionCommitId(data)) },
      { label: 'time', value: versionJsonText(data.git.commit?.time) },
    ]
  }
  return [
    { label: 'version', value: versionJsonText(data.version) },
    { label: 'branch', value: versionJsonText(data.branch) },
    { label: 'commit', value: versionJsonText(data.commit) },
    { label: 'date', value: versionJsonText(data.date) },
  ]
}

function openVersionJsonDialog(title: string, value?: string) {
  versionJsonDialogTitle.value = title
  versionJsonDialogContent.value = formatRawJson(value)
  versionJsonDialogVisible.value = true
}

type VersionTableRow = ArtifactVersionItem & {
  rowKey: string
  deploymentTarget: string
  containerPath: string
}

type DeployRecordTableRow = ArtifactDeployRecord & {
  rowKey: string
  deploymentTarget: string
}

function deploymentTargetForItem(item: ArtifactVersionItem) {
  if (item.appType === 'vue') {
    if (item.appName === 'web-main') return 'web-main'
    if (item.appName === 'web-cdr' || item.appName === 'web-opm') return item.appName
    return 'web-main'
  }
  return item.appName || '-'
}

function deploymentTargetForRecord(record: ArtifactDeployRecord) {
  if (record.deploymentName) return record.deploymentName
  if (record.appType === 'vue') {
    if (record.appName === 'web-main') return 'web-main'
    if (record.appName === 'web-cdr' || record.appName === 'web-opm') return record.appName
    return 'web-main'
  }
  return record.appName || '-'
}

function buildVersionRows(sourceItems: ArtifactVersionItem[]): VersionTableRow[] {
  return sourceItems
    .map(item => ({
      ...item,
      rowKey: `item-${item.id}`,
      deploymentTarget: deploymentTargetForItem(item),
      containerPath: containerPathForItem(item),
      statusReason: item.appName || item.statusReason || '-',
    }))
    .sort((a, b) => {
      const targetCompare = a.deploymentTarget.localeCompare(b.deploymentTarget)
      if (targetCompare !== 0) {
        if (a.deploymentTarget === 'web-main') return -1
        if (b.deploymentTarget === 'web-main') return 1
        return targetCompare
      }
      if (a.appName === 'web-main') return -1
      if (b.appName === 'web-main') return 1
      return a.fileName.localeCompare(b.fileName)
    })
}

function buildDeployRecordRows(sourceRecords: ArtifactDeployRecord[]): DeployRecordTableRow[] {
  return sourceRecords
    .map(record => ({
      ...record,
      rowKey: record.id ? `record-${record.id}` : `record-${record.deployBatchNo}-${record.artifactVersionItemId}-${record.fileName}`,
      deploymentTarget: deploymentTargetForRecord(record),
    }))
    .sort((a, b) => {
      const targetCompare = a.deploymentTarget.localeCompare(b.deploymentTarget)
      if (targetCompare !== 0) {
        if (a.deploymentTarget === 'web-main') return -1
        if (b.deploymentTarget === 'web-main') return 1
        return targetCompare
      }
      if (a.appName === 'web-main') return -1
      if (b.appName === 'web-main') return 1
      return a.fileName.localeCompare(b.fileName)
    })
}

function containerPathForItem(item: ArtifactVersionItem) {
  if (item.fileType === 'jar' || item.appType === 'java') return '/opt/app.jar'
  if (item.appType === 'vue') {
    if (item.appName === 'web-main' || item.appName === 'web-cdr' || item.appName === 'web-opm') {
      return '/usr/share/nginx/html'
    }
    const appCode = item.fileName.replace(/\.zip$/i, '')
    if (/^[A-Za-z0-9_-]+$/.test(appCode)) return `/usr/share/nginx/html/apps/${appCode}`
  }
  return '-'
}

function artifactSpanMethod({ rowIndex, columnIndex }: { rowIndex: number; columnIndex: number }) {
  if (columnIndex !== 0) return { rowspan: 1, colspan: 1 }
  const rows = versionRows.value
  const current = rows[rowIndex]
  if (!current) return { rowspan: 1, colspan: 1 }
  if (rowIndex > 0 && rows[rowIndex - 1]?.deploymentTarget === current.deploymentTarget) {
    return { rowspan: 0, colspan: 0 }
  }
  let rowspan = 1
  for (let i = rowIndex + 1; i < rows.length; i++) {
    if (rows[i].deploymentTarget !== current.deploymentTarget) break
    rowspan++
  }
  return { rowspan, colspan: 1 }
}

function deployRecordSpanMethod({ rowIndex, columnIndex }: { rowIndex: number; columnIndex: number }) {
  if (columnIndex !== 0) return { rowspan: 1, colspan: 1 }
  const rows = deployRecordRows.value
  const current = rows[rowIndex]
  if (!current) return { rowspan: 1, colspan: 1 }
  if (rowIndex > 0 && rows[rowIndex - 1]?.deploymentTarget === current.deploymentTarget) {
    return { rowspan: 0, colspan: 0 }
  }
  let rowspan = 1
  for (let i = rowIndex + 1; i < rows.length; i++) {
    if (rows[i].deploymentTarget !== current.deploymentTarget) break
    rowspan++
  }
  return { rowspan, colspan: 1 }
}

function applyVersionResult(result: { version: ArtifactVersion; items: ArtifactVersionItem[] }) {
  version.value = result.version
  items.value = result.items || []
}

async function handleDeleteItem(item: ArtifactVersionItem) {
  await ElMessageBox.confirm(`确定删除文件 "${item.fileName}" 吗？该操作只影响当前更新记录。`, '删除文件', { type: 'warning' })
  itemOperatingId.value = item.id
  try {
    applyVersionResult(await deleteArtifactVersionItem(versionNo.value, item.id))
    ElMessage.success('制品已删除')
  } finally {
    itemOperatingId.value = null
  }
}

function handleReplaceItem(item: ArtifactVersionItem) {
  replacingItem.value = item
  replaceInput.value?.click()
}

async function handleReplaceInput(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  const item = replacingItem.value
  input.value = ''
  if (!file || !item) return
  itemOperatingId.value = item.id
  try {
    applyVersionResult(await replaceArtifactVersionItem(versionNo.value, item.id, file))
    ElMessage.success('制品已重新上传并校验')
  } finally {
    itemOperatingId.value = null
    replacingItem.value = null
  }
}

async function handleRedownloadItem(item: ArtifactVersionItem) {
  itemOperatingId.value = item.id
  try {
    applyVersionResult(await redownloadArtifactVersionItem(versionNo.value, item.id))
    ElMessage.success('制品已重新下载并校验')
  } finally {
    itemOperatingId.value = null
  }
}

async function handleRollout() {
  if (!deployBatch.value || rolloutSubmitting.value) return
  rolloutSubmitting.value = true
  try {
    const result = await rolloutArtifactDeployBatch(deployBatch.value.deployBatchNo)
    if (result.errors.length) {
      await ElMessageBox.alert(result.errors.join('\n'), `已提交 ${result.pipelines.length} 个滚动更新任务，部分目标未提交`, { type: 'warning' })
    } else if (result.pipelines.length) {
      ElMessage.success(`已提交 ${result.pipelines.length} 个滚动更新任务`)
    } else {
      ElMessage.info('没有新的镜像可执行滚动更新')
    }
    void loadDeployBatch()
  } finally {
    rolloutSubmitting.value = false
  }
}

async function handleRollback() {
  if (!deployBatch.value || rollbacking.value) return
  await ElMessageBox.confirm(`确定整体回滚版本 "${deployBatch.value.versionNo}" 吗？系统会按部署目标恢复到本次更新前记录的镜像；尚未开始部署的目标会跳过。`, '整体回滚', { type: 'warning' })
  rollbackSubmitting.value = true
  try {
    const result = await rollbackArtifactDeployBatch(deployBatch.value.deployBatchNo)
    deployBatch.value = result.batch
    ElMessage.success(result.batch.status === 'rolled_back' ? '该批次已完成回滚' : '后台回滚已提交，可在此页面查看进度')
    void loadDeployBatch()
  } finally {
    rollbackSubmitting.value = false
  }
}
</script>

<template>
  <div class="version-detail-page" v-loading="loading">
    <div class="page-header">
      <div>
        <h4>更新记录详情</h4>
      </div>
      <div><el-button v-if="canContinueUpdate" type="primary" @click="continueUpdate">继续更新</el-button><el-button @click="router.push('/artifacts/versions')">返回更新记录</el-button></div>
    </div>

    <el-card v-if="version" shadow="never" class="summary-card">
      <div class="summary-top">
        <div>
          <span>记录编号</span>
          <strong>{{ version.versionNo }}</strong>
        </div>
        <el-tag :type="versionStatusType(version)" size="large">{{ versionStatusText(version) }}</el-tag>
      </div>
      <div class="summary-grid">
        <div><span>来源</span><strong>{{ sourceTypeText(version.sourceType) }}</strong></div>
        <div><span>制品数</span><strong>{{ version.count }}</strong></div>
        <div><span>可更新</span><strong>{{ deployableItems.length }}</strong></div>
        <div><span>不可读</span><strong>{{ invalidItems.length }}</strong></div>
        <div><span>未匹配</span><strong>{{ unmatchedItems.length }}</strong></div>
        <div><span>更新时间</span><strong>{{ formatTime(version.updatedAt) }}</strong></div>
      </div>
      <div class="path-grid">
        <div><span>本机目录</span><strong>{{ version.targetPath || version.localDir || '-' }}</strong></div>
        <div v-if="version.remotePath"><span>远程目录</span><strong>{{ version.remotePath }}</strong></div>
        <div v-if="version.error"><span>错误</span><strong>{{ version.error }}</strong></div>
      </div>
    </el-card>

    <ReleaseVersionInfo v-if="version" :version="version" />

    <el-card shadow="never" class="table-card">
      <template #header>
        <div class="table-header">
          <strong>更新文件</strong>
          <el-button :loading="loading" @click="loadDetail">刷新</el-button>
        </div>
      </template>
      <input ref="replaceInput" type="file" accept=".jar,.zip" hidden @change="handleReplaceInput" />
      <el-table
        :data="versionRows"
        border
        stripe
        max-height="60vh"
        table-layout="auto"
        row-key="rowKey"
        :span-method="artifactSpanMethod"
      >
        <el-table-column label="部署目标" width="140" align="center">
          <template #default="{ row }">
            <strong class="deployment-target">{{ row.deploymentTarget }}</strong>
          </template>
        </el-table-column>
        <el-table-column prop="fileName" label="文件名" min-width="220" show-overflow-tooltip />
        <el-table-column label="容器内路径" min-width="220" show-overflow-tooltip>
          <template #default="{ row }">{{ row.containerPath }}</template>
        </el-table-column>
        <el-table-column prop="fileType" label="类型" width="90" />
        <el-table-column label="大小" width="110">
          <template #default="{ row }">{{ formatBytes(row.fileSizeBytes) }}</template>
        </el-table-column>
        <el-table-column label="校验状态" width="110">
          <template #default="{ row }">
            <el-tag :type="validateTag(row).type" size="small">{{ validateTag(row).text }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="应用匹配" width="110">
          <template #default="{ row }">
            <el-tag :type="matchTag(row).type" size="small">{{ matchTag(row).text }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="appType" label="应用类型" width="100">
          <template #default="{ row }">{{ row.appType || '-' }}</template>
        </el-table-column>
        <el-table-column label="更新状态" width="120">
          <template #default="{ row }">
            <el-tag :type="row.deployable && !row.updateUsed ? 'success' : 'info'" size="small">{{ row.updateUsed ? '已提交更新' : row.deployable ? '可更新' : '不可更新' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="sha256" label="SHA256" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">{{ row.sha256?.slice(0, 12) || '-' }}</template>
        </el-table-column>
        <el-table-column prop="statusReason" label="说明" min-width="260" show-overflow-tooltip />
        <el-table-column label="操作" width="230" fixed="right">
          <template #default="{ row }">
            <template v-if="versionEditable">
              <el-button
                v-if="version?.sourceType === 'download'"
                link
                type="primary"
                :loading="itemOperatingId === row.id"
                @click="handleRedownloadItem(row)"
              >
                重新下载
              </el-button>
              <el-button link type="primary" :loading="itemOperatingId === row.id" @click="handleReplaceItem(row)">重新上传</el-button>
              <el-button link type="danger" :loading="itemOperatingId === row.id" @click="handleDeleteItem(row)">删除制品</el-button>
            </template>
            <span v-else>-</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <el-card shadow="never" class="table-card">
      <template #header>
        <div class="table-header">
          <div>
            <strong>部署记录与回滚</strong>
            <span v-if="deployBatch">部署批次 {{ deployBatch.deployBatchNo }}，状态 {{ deployRecordStatusText(deployBatch.status) }}</span>
            <span v-else-if="deployRecords.length">待部署</span>
            <span v-else>暂无部署目标</span>
            <span class="section-hint">镜像准备完成后，滚动更新会逐步替换 Deployment 的 Pod。</span>
            <span class="section-hint">整体回滚按 Deployment 恢复更新前镜像；共享的 web-main 只回滚一次。</span>
          </div>
          <div class="header-actions">
            <el-button :loading="deployLoading" @click="loadDeployBatch">刷新状态</el-button>
            <el-button v-if="deployBatch && deployRecords.some(record => record.status === 'image_ready')" type="primary" :loading="rolloutSubmitting" @click="handleRollout">第二步：开始滚动更新</el-button>
            <el-button
              v-if="deployBatch && canRollback"
              type="warning"
              :loading="rollbacking"
              @click="handleRollback"
            >
              整体回滚
            </el-button>
          </div>
        </div>
      </template>
      <el-alert v-if="deployBatch?.status === 'rolling_back'" title="正在回滚" type="info" :closable="false" />
      <el-alert v-if="deployBatch?.errorMessage" :title="deployBatch.errorMessage" type="error" :closable="false" />
      <el-table
        :data="deployRecordRows"
        border
        stripe
        max-height="50vh"
        table-layout="auto"
        row-key="rowKey"
        :span-method="deployRecordSpanMethod"
        v-loading="deployLoading"
      >
        <el-table-column label="部署目标" width="140" align="center">
          <template #default="{ row }">
            <strong class="deployment-target">{{ row.deploymentTarget }}</strong>
          </template>
        </el-table-column>
        <el-table-column prop="fileName" label="更新文件" min-width="150" show-overflow-tooltip />
        <el-table-column prop="appName" label="应用" min-width="150" show-overflow-tooltip />
        <el-table-column label="状态" width="200">
          <template #default="{ row }">
            <el-tag :type="deploymentRecordError(row) ? 'warning' : deployRecordStatusType(row.status)" size="small">{{ deploymentRecordError(row) ? '部署成功，结果记录异常' : deployRecordStatusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="回滚进度" min-width="200">
          <template #default="{ row }">{{ rollbackStatusText(row.rollbackStatus) }}</template>
        </el-table-column>
        <el-table-column prop="beforeDesiredImage" label="更新前目标镜像" min-width="230" show-overflow-tooltip />
        <el-table-column prop="beforeImage" label="可回滚镜像" min-width="230" show-overflow-tooltip />
        <el-table-column prop="afterImage" label="更新后镜像" min-width="230" show-overflow-tooltip />
        <el-table-column label="更新包版本" min-width="320">
          <template #default="{ row }">
            <div v-if="row.packageVersionJson" class="version-json-cell">
              <div v-for="field in versionJsonSummaryFields(row.packageVersionJson)" :key="field.label">
                <span>{{ field.label }}</span>
                <strong>{{ field.value }}</strong>
              </div>
              <el-button link type="primary" @click="openVersionJsonDialog(`${row.fileName} 更新包版本`, row.packageVersionJson)">完整数据</el-button>
            </div>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column label="更新前版本" min-width="320">
          <template #default="{ row }">
            <div v-if="row.beforeBusinessVersionJson" class="version-json-cell">
              <div v-for="field in versionJsonSummaryFields(row.beforeBusinessVersionJson)" :key="field.label">
                <span>{{ field.label }}</span>
                <strong>{{ field.value }}</strong>
              </div>
              <el-button link type="primary" @click="openVersionJsonDialog(`${row.fileName} 更新前版本`, row.beforeBusinessVersionJson)">完整数据</el-button>
            </div>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column label="更新后版本" min-width="320">
          <template #default="{ row }">
            <div v-if="row.afterBusinessVersionJson" class="version-json-cell">
              <div v-for="field in versionJsonSummaryFields(row.afterBusinessVersionJson)" :key="field.label">
                <span>{{ field.label }}</span>
                <strong>{{ field.value }}</strong>
              </div>
              <el-button link type="primary" @click="openVersionJsonDialog(`${row.fileName} 更新后版本`, row.afterBusinessVersionJson)">完整数据</el-button>
            </div>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column label="回滚后版本" min-width="320">
          <template #default="{ row }">
            <div v-if="row.restoredBusinessVersionJson" class="version-json-cell">
              <div v-for="field in versionJsonSummaryFields(row.restoredBusinessVersionJson)" :key="field.label">
                <span>{{ field.label }}</span>
                <strong>{{ field.value }}</strong>
              </div>
              <el-button link type="primary" @click="openVersionJsonDialog(`${row.fileName} 回滚后版本`, row.restoredBusinessVersionJson)">完整数据</el-button>
            </div>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column prop="versionWarning" label="版本信息提示（不影响结果）" min-width="240" show-overflow-tooltip />
        <el-table-column prop="errorMessage" label="错误" min-width="220" show-overflow-tooltip />
      </el-table>
    </el-card>

    <el-dialog v-model="versionJsonDialogVisible" :title="versionJsonDialogTitle || '完整版本数据'" width="760px">
      <div class="json-dialog-label">完整版本数据</div>
      <pre class="json-dialog-content">{{ versionJsonDialogContent }}</pre>
    </el-dialog>
  </div>
</template>

<style scoped>
.version-detail-page { display: flex; flex-direction: column; gap: 12px; }
.page-header,
.summary-card,
.table-card {
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
.summary-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.summary-top span,
.summary-grid span,
.path-grid span { color: #8a9099; }
.summary-top strong {
  display: block;
  margin-top: 4px;
  font-size: 18px;
  color: #1f2937;
}
.summary-grid {
  display: grid;
  grid-template-columns: repeat(6, minmax(0, 1fr));
  border: 1px solid #ebeef5;
  border-bottom: 0;
}
.summary-grid > div,
.path-grid > div {
  min-width: 0;
  padding: 10px 12px;
  border-bottom: 1px solid #ebeef5;
  border-right: 1px solid #ebeef5;
}
.summary-grid strong,
.path-grid strong {
  display: block;
  margin-top: 4px;
  color: #1f2937;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.path-grid {
  display: grid;
  grid-template-columns: 1fr;
  border-left: 1px solid #ebeef5;
}
.table-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.table-header strong { display: block; margin-bottom: 4px; }
.table-header span { color: #8a9099; font-size: 12px; }
.table-header .section-hint { color: #6b7280; }
.header-actions { display: flex; gap: 8px; }
.deployment-target {
  color: #1f2937;
  font-weight: 600;
}
.json-cell {
  margin: 0;
  max-height: 160px;
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-word;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.45;
  color: #374151;
}
.version-json-cell {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 4px 10px;
  align-items: start;
  color: #606266;
  font-size: 12px;
}
.version-json-cell > div {
  min-width: 0;
}
.version-json-cell span {
  display: block;
  color: #8a9099;
  line-height: 1.25;
}
.version-json-cell strong {
  display: block;
  margin-top: 2px;
  color: #303133;
  font-weight: 500;
  line-height: 1.35;
  word-break: break-all;
}
.version-json-cell .el-button {
  justify-self: start;
  padding: 0;
  min-height: 20px;
}
.json-dialog-label {
  margin-bottom: 8px;
  color: #606266;
  font-size: 13px;
  font-weight: 600;
}
.json-dialog-content {
  margin: 0;
  max-height: 62vh;
  overflow: auto;
  padding: 12px;
  background: #f7f8fa;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  color: #374151;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
@media (max-width: 1280px) {
  .summary-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
</style>

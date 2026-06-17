<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  deleteArtifactVersionItem,
  getArtifactDeployBatch,
  getArtifactVersion,
  redownloadArtifactVersionItem,
  replaceArtifactVersionItem,
  rollbackArtifactDeployBatch,
  type ArtifactDeployBatch,
  type ArtifactDeployRecord,
  type ArtifactVersion,
  type ArtifactVersionItem,
} from '../api/batch-deploy'
import { formatTime } from '../utils/time'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const version = ref<ArtifactVersion | null>(null)
const items = ref<ArtifactVersionItem[]>([])
const deployBatch = ref<ArtifactDeployBatch | null>(null)
const deployRecords = ref<ArtifactDeployRecord[]>([])
const rollbacking = ref(false)
const itemOperatingId = ref<number | null>(null)
const replaceInput = ref<HTMLInputElement>()
const replacingItem = ref<ArtifactVersionItem | null>(null)

const versionNo = computed(() => String(route.params.versionNo || ''))
const deployableItems = computed(() => items.value.filter(item => item.deployable))
const invalidItems = computed(() => items.value.filter(item => item.validateStatus === 'invalid'))
const unmatchedItems = computed(() => items.value.filter(item => item.matchStatus === 'unmatched' && item.validateStatus === 'valid'))
const versionEditable = computed(() => !deployBatch.value)
const versionRows = computed(() => buildVersionRows(items.value))

onMounted(loadDetail)

async function loadDetail() {
  loading.value = true
  try {
    const result = await getArtifactVersion(versionNo.value)
    version.value = result.version
    items.value = result.items || []
    await loadDeployBatch()
  } finally {
    loading.value = false
  }
}

async function loadDeployBatch() {
  try {
    const result = await getArtifactDeployBatch(versionNo.value)
    deployBatch.value = result.batch
    deployRecords.value = result.records || []
  } catch (e) {
    deployBatch.value = null
    deployRecords.value = []
  }
}

function sourceTypeText(type?: string) {
  if (type === 'upload') return '本地上传'
  if (type === 'download') return '服务器下载'
  if (type === 'artifact') return '制品库选择'
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

function deployRecordStatusType(status?: string) {
  if (status === 'deployed' || status === 'rolled_back') return 'success'
  if (status === 'failed' || status === 'rollback_failed') return 'danger'
  if (status === 'image_ready') return 'warning'
  return 'info'
}

function deployRecordStatusText(status?: string) {
  if (status === 'image_ready') return '等待卡点'
  if (status === 'deployed') return '已部署'
  if (status === 'rolled_back') return '已回滚'
  if (status === 'failed') return '失败'
  return status || '-'
}

function formatBusinessVersion(value?: string) {
  if (!value) return '-'
  try {
    const data = JSON.parse(value)
    if (data.git) {
      const commit = data.git.commit || {}
      return [data.git.branch, commit.id || commit.idAbbrev, commit.time].filter(Boolean).join(' / ')
    }
    return [data.xiTongId, data.version, data.branch, data.commit, data.date].filter(Boolean).join(' / ')
  } catch (e) {
    return value
  }
}

type VersionTableRow = ArtifactVersionItem & {
  rowKey: string
  deploymentTarget: string
  containerPath: string
}

function deploymentTargetForItem(item: ArtifactVersionItem) {
  if (item.appType === 'vue') {
    if (item.appName === 'web-main') return 'web-main'
    if (item.appName === 'web-cdr' || item.appName === 'web-opm') return item.appName
    return 'web-main'
  }
  return item.appName || '-'
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

function currentImage(row: ArtifactDeployRecord) {
  if (row.status === 'rolled_back') return row.beforeImage || '-'
  return row.afterImage || row.beforeImage || '-'
}

function currentBusinessVersion(row: ArtifactDeployRecord) {
  if (row.status === 'rolled_back') {
    return formatBusinessVersion(row.restoredBusinessVersionJson || row.beforeBusinessVersionJson)
  }
  return formatBusinessVersion(row.afterBusinessVersionJson || row.beforeBusinessVersionJson)
}

function applyVersionResult(result: { version: ArtifactVersion; items: ArtifactVersionItem[] }) {
  version.value = result.version
  items.value = result.items || []
}

async function handleDeleteItem(item: ArtifactVersionItem) {
  await ElMessageBox.confirm(`确定删除制品 "${item.fileName}" 吗？该操作只影响当前更新版本。`, '删除制品', { type: 'warning' })
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

async function handleRollback() {
  if (!deployBatch.value) return
  await ElMessageBox.confirm(`确定整体回滚版本 "${deployBatch.value.versionNo}" 吗？会把本版本涉及的 Deployment 镜像恢复到更新前镜像。`, '整体回滚', { type: 'warning' })
  rollbacking.value = true
  try {
    const result = await rollbackArtifactDeployBatch(deployBatch.value.deployBatchNo)
    deployBatch.value = result.batch
    deployRecords.value = result.records || []
    ElMessage.success('已触发整体回滚并完成版本校验')
  } finally {
    rollbacking.value = false
  }
}
</script>

<template>
  <div class="version-detail-page" v-loading="loading">
    <div class="page-header">
      <div>
        <h4>更新版本详情</h4>
        <p>查看该版本包含的制品、应用匹配结果和 jar/zip 可读性校验。</p>
      </div>
      <el-button @click="router.push('/artifacts/versions')">返回更新版本</el-button>
    </div>

    <el-card v-if="version" shadow="never" class="summary-card">
      <div class="summary-top">
        <div>
          <span>版本号</span>
          <strong>{{ version.versionNo }}</strong>
        </div>
        <el-tag :type="statusType(version.status)" size="large">{{ statusText(version.status) }}</el-tag>
      </div>
      <div class="summary-grid">
        <div><span>来源</span><strong>{{ sourceTypeText(version.sourceType) }}</strong></div>
        <div><span>制品数</span><strong>{{ version.count }}</strong></div>
        <div><span>可部署</span><strong>{{ deployableItems.length }}</strong></div>
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

    <el-card shadow="never" class="table-card">
      <template #header>
        <div class="table-header">
          <strong>制品文件</strong>
          <el-button :loading="loading" @click="loadDetail">刷新</el-button>
        </div>
      </template>
      <input ref="replaceInput" type="file" accept=".jar,.zip" hidden @change="handleReplaceInput" />
      <el-table
        :data="versionRows"
        border
        stripe
        height="620"
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
        <el-table-column label="可部署" width="90">
          <template #default="{ row }">
            <el-tag :type="row.deployable ? 'success' : 'info'" size="small">{{ row.deployable ? '是' : '否' }}</el-tag>
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
            <span v-else>当前版本还没有部署记录</span>
          </div>
          <div class="header-actions">
            <el-button :loading="loading" @click="loadDeployBatch">刷新</el-button>
            <el-button
              v-if="deployBatch && deployRecords.length"
              type="warning"
              :loading="rollbacking"
              @click="handleRollback"
            >
              整体回滚
            </el-button>
          </div>
        </div>
      </template>
      <el-table :data="deployRecords" border stripe height="420" table-layout="auto">
        <el-table-column prop="appName" label="应用" min-width="150" show-overflow-tooltip />
        <el-table-column prop="fileName" label="制品" min-width="150" show-overflow-tooltip />
        <el-table-column prop="deploymentName" label="Deployment" min-width="140" show-overflow-tooltip />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="deployRecordStatusType(row.status)" size="small">{{ deployRecordStatusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="beforeImage" label="更新前镜像" min-width="230" show-overflow-tooltip />
        <el-table-column prop="afterImage" label="更新后镜像" min-width="230" show-overflow-tooltip />
        <el-table-column label="当前镜像" min-width="230" show-overflow-tooltip>
          <template #default="{ row }">{{ currentImage(row) }}</template>
        </el-table-column>
        <el-table-column label="更新包版本" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ formatBusinessVersion(row.packageVersionJson) }}</template>
        </el-table-column>
        <el-table-column label="更新前版本" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ formatBusinessVersion(row.beforeBusinessVersionJson) }}</template>
        </el-table-column>
        <el-table-column label="更新后版本" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ formatBusinessVersion(row.afterBusinessVersionJson) }}</template>
        </el-table-column>
        <el-table-column label="回滚后版本" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ formatBusinessVersion(row.restoredBusinessVersionJson) }}</template>
        </el-table-column>
        <el-table-column label="当前版本" min-width="240" show-overflow-tooltip>
          <template #default="{ row }">{{ currentBusinessVersion(row) }}</template>
        </el-table-column>
        <el-table-column prop="errorMessage" label="错误" min-width="220" show-overflow-tooltip />
      </el-table>
    </el-card>
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
.page-header p { margin: 0; color: #8a9099; }
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
.header-actions { display: flex; gap: 8px; }
.deployment-target {
  color: #1f2937;
  font-weight: 600;
}
@media (max-width: 1280px) {
  .summary-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
</style>

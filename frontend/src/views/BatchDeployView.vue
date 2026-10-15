<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox, type TableInstance } from 'element-plus'
import { Document, Folder, Upload } from '@element-plus/icons-vue'
import {
  cancelDownloadJob,
  executeBatchDeploy,
  getActiveDownloadJob,
  getArtifactVersion,
  getDownloadProgress,
  listArtifactVersions,
  listPackageDownloadDir,
  listPackageDownloadLocalDir,
  startDownloadRemoteDir,
  type ArtifactVersion,
  type ArtifactVersionItem,
  type DownloadJob,
  type DownloadJobItem,
  type MatchResult,
} from '../api/batch-deploy'
import { getSettings } from '../api/settings'
import { useSettingsStore } from '../stores/settings'
import { buildDeployConfirmation, isDeployableMatch, selectedDeployableFileNames } from '../utils/batch-deploy-selection'
import type { FileItem } from '../api/server-mgmt'

type UploadEntry = { file: File; path: string }
type SourceMode = 'upload' | 'download' | 'existing'

const route = useRoute()
const router = useRouter()
const settingsStore = useSettingsStore()

const fileInput = ref<HTMLInputElement>()
const dirInput = ref<HTMLInputElement>()
const sourceMode = ref<SourceMode>('upload')
const uploadLoading = ref(false)
const loading = ref(false)
const executing = ref(false)
const batchId = ref('')
const sourceType = ref('')
const sourceDir = ref('')
const matchResults = ref<MatchResult[]>([])
const selectedFiles = ref<string[]>([])
const matchTableRef = ref<TableInstance>()
const versions = ref<ArtifactVersion[]>([])
const currentVersion = ref<ArtifactVersion | null>(null)

const remoteLoading = ref(false)
const remoteBasePath = ref('/')
const remotePath = ref('/')
const remoteFiles = ref<FileItem[]>([])
const selectedRemoteRows = ref<FileItem[]>([])
const localBasePath = ref('')
const localPath = ref('')
const localFiles = ref<FileItem[]>([])
const localLoading = ref(false)
const packageConfigured = ref(false)
const activeDownloadJob = ref<DownloadJob | null>(null)
let pollTimer: number | undefined

const deployableResults = computed(() => {
  const selected = new Set(selectedFiles.value)
  return matchResults.value.filter(result => selected.has(result.fileName) && isDeployableMatch(result))
})
const matchedCount = computed(() => matchResults.value.filter(result => result.matched && result.valid && !result.skipped).length)
const invalidCount = computed(() => matchResults.value.filter(result => !result.valid).length)
const unmatchedCount = computed(() => matchResults.value.filter(result => !result.matched && !result.skipped).length)
const skippedCount = computed(() => matchResults.value.filter(result => result.skipped).length)
const readyVersions = computed(() => versions.value.filter(isVersionReady))
const hasVersion = computed(() => Boolean(currentVersion.value || batchId.value))
const selectedRemotePaths = computed(() => selectedRemoteRows.value.map(row => row.path))
const isDownloadRunning = computed(() => {
  const status = activeDownloadJob.value?.status
  return status === 'pending' || status === 'running'
})
const canDownloadSelected = computed(() => selectedRemotePaths.value.length > 0 && !isDownloadRunning.value)
const sortedRemoteFiles = computed(() => sortFiles(remoteFiles.value))
const sortedLocalFiles = computed(() => sortFiles(localFiles.value))
const remotePathRelativeParts = computed(() => relativePathParts(remoteBasePath.value, remotePath.value))
const localPathRelativeParts = computed(() => relativePathParts(localBasePath.value, localPath.value))
const downloadPercent = computed(() => {
  const job = activeDownloadJob.value
  if (!job?.totalFiles) return 0
  return Math.min(100, Math.round((job.completedFiles / job.totalFiles) * 100))
})
const downloadFileRows = computed<DownloadJobItem[]>(() => {
  const job = activeDownloadJob.value
  if (!job) return []
  if (job.items?.length) return job.items
  return (job.files || []).map(name => ({
    name,
    remotePath: '',
    localPath: '',
    size: 0,
    status: 'success',
    artifact: true,
  }))
})
const downloadTargetPath = computed(() => {
  const job = activeDownloadJob.value
  if (!job) return '下载后自动生成本地工作区目录'
  return job.targetPath || job.localDir || '下载后自动生成本地工作区目录'
})

onMounted(async () => {
  if (!settingsStore.loaded) {
    await settingsStore.fetchSettings()
  }
  batchId.value = String(route.query.batchId || '')
  sourceType.value = String(route.query.sourceType || '')
  sourceDir.value = String(route.query.sourceDir || '')

  await Promise.all([loadVersions(), loadSettingsAndRemoteRoot()])
  await restoreActiveDownloadJob()
  if (batchId.value) {
    await loadVersionDetail(batchId.value)
    sourceMode.value = 'existing'
  }
})

onUnmounted(() => {
  if (pollTimer) window.clearTimeout(pollTimer)
})

watch(sourceMode, async mode => {
  if (mode === 'download' && packageConfigured.value) {
    if (remoteFiles.value.length === 0) await loadRemoteDir(remotePath.value)
    await loadLocalDownloadDir()
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
  return (version.status === 'available' || version.status === 'ready' || version.status === 'success') && (version.remainingDeployableCount ?? version.deployableCount) > 0
}

function versionStatusType(version: ArtifactVersion) {
  if (isVersionReady(version)) return 'success'
  if (version.status === 'failed') return 'danger'
  return 'warning'
}

function versionStatusText(version: ArtifactVersion) {
  if (isVersionReady(version)) return '可部署'
  if (version.deployableCount > 0 && version.remainingDeployableCount === 0) return '已提交更新'
  if (version.status === 'failed') return '失败'
  if (version.status === 'collecting' || version.status === 'running') return '导入中'
  if (version.deployableCount === 0) return '无可部署制品'
  return version.status || '未知'
}

async function activateVersion(versionNo: string) {
  await loadVersionDetail(versionNo)
  sourceMode.value = 'existing'
  await router.replace({
    path: '/batch-deploy',
    query: { batchId: batchId.value, sourceType: sourceType.value, sourceDir: sourceDir.value },
  })
}

async function selectVersion(version: ArtifactVersion) {
  if (!isVersionReady(version)) {
    ElMessage.warning('请选择可部署的更新版本')
    return
  }
  await activateVersion(version.versionNo)
}

async function loadVersionDetail(versionNo: string, notifyEmpty = true) {
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
    matchTableRef.value?.clearSelection()
    for (const row of matchResults.value) {
      if (isDeployableMatch(row)) {
        matchTableRef.value?.toggleRowSelection(row, true)
      }
    }
    if (notifyEmpty && selectedFiles.value.length === 0) {
      ElMessage.warning('当前版本没有可部署制品')
    }
  } finally {
    loading.value = false
  }
}

function versionItemToMatchResult(item: ArtifactVersionItem): MatchResult {
  return {
    fileName: item.fileName,
    updateUsed: item.updateUsed,
    appName: item.appName,
    appType: item.appType,
    vueRole: item.vueRole,
    appCode: item.appCode,
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
  if (result.updateUsed) return { type: 'info', text: '已提交更新' }
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
  if (executing.value) return
  if (deployableResults.value.length === 0) {
    ElMessage.warning('没有可部署制品')
    return
  }
  const items = deployableResults.value.map(result => ({ fileName: result.fileName, appId: result.appId }))
  executing.value = true
  try {
    await settingsStore.fetchSettings()
    if (deployableResults.value.some(item => item.appType === 'vue')) {
      try {
        await ElMessageBox.confirm(
          buildDeployConfirmation(deployableResults.value, settingsStore.autoRollout),
          settingsStore.autoRollout ? '确认构建与滚动更新范围' : '确认镜像制作范围', { customClass: 'deploy-scope-confirm', type: 'warning', confirmButtonText: settingsStore.autoRollout ? '构建并滚动更新' : '制作镜像', cancelButtonText: '取消' },
        )
      } catch { return }
    }
    const result = await executeBatchDeploy(sourceDir.value, items, batchId.value)
    // Once accepted, inputs stay disabled even if execution fails or is rolled back.
    const submittedFiles = new Set(result.consumedFiles || [])
    matchResults.value.forEach(item => { if (submittedFiles.has(item.fileName)) item.updateUsed = true })
    selectedFiles.value = selectedFiles.value.filter(file => !submittedFiles.has(file))
    // Refresh failures must not hide accepted tasks or re-enable their inputs.
    await Promise.allSettled([loadVersionDetail(batchId.value, false), loadVersions()])
    const errors = result.errors || []
    if (errors.length > 0) {
      await ElMessageBox.alert(
        errors.join('\n\n'),
        `已创建 ${result.pipelines?.length || 0} 个任务，其他任务已跳过`,
        { type: 'warning', confirmButtonText: '查看已创建任务' },
      ).catch(() => {})
    } else {
      ElMessage.success(`已创建 ${result.pipelines?.length || 0} 个构建任务`)
    }
    if (result.pipelines?.length) router.push('/build-queue')
  } finally {
    executing.value = false
  }
}

function chooseFiles() {
  fileInput.value?.click()
}

function chooseDirectory() {
  dirInput.value?.click()
}

async function handleUploadInput(event: Event) {
  const input = event.target as HTMLInputElement
  if (!input.files?.length) return
  const entries: UploadEntry[] = []
  for (let i = 0; i < input.files.length; i++) {
    const file = input.files[i] as File & { webkitRelativePath?: string }
    entries.push({ file, path: file.webkitRelativePath || file.name })
  }
  await uploadEntries(entries)
  input.value = ''
}

async function handleDrop(event: DragEvent) {
  const files = Array.from(event.dataTransfer?.files || [])
  await uploadEntries(files.map(file => ({ file, path: file.name })))
}

async function uploadEntries(entries: UploadEntry[]) {
  const artifacts = entries.filter(entry => {
    const name = entry.path.toLowerCase()
    return name.endsWith('.jar') || name.endsWith('.zip')
  })
  if (artifacts.length === 0) {
    ElMessage.warning('未找到 .jar / .zip 制品文件')
    return
  }

  const formData = new FormData()
  for (const entry of artifacts) {
    formData.append('files', entry.file, entry.path)
  }

  uploadLoading.value = true
  try {
    const resp = await fetch('/api/batch-deploy/upload', {
      method: 'POST',
      headers: { Authorization: `Bearer ${localStorage.getItem('df-token') || ''}` },
      body: formData,
    })
    const data = await resp.json()
    if (data.code !== 0) {
      ElMessage.error(data.message || '上传失败')
      return
    }
    ElMessage.success(`已上传 ${data.data.count} 个制品文件，生成版本 ${data.data.batchId}`)
    await loadVersions()
    await activateVersion(data.data.batchId)
  } finally {
    uploadLoading.value = false
  }
}

async function loadSettingsAndRemoteRoot() {
  try {
    const settings = await getSettings()
    packageConfigured.value = Boolean(settings.package_download_host && settings.package_download_user)
    remoteBasePath.value = settings.package_download_path || '/'
    remotePath.value = remoteBasePath.value
    if (packageConfigured.value && sourceMode.value === 'download') {
      await loadRemoteDir(remoteBasePath.value)
      await loadLocalDownloadDir()
    }
  } catch {
    packageConfigured.value = false
  }
}

async function restoreActiveDownloadJob() {
  try {
    const job = await getActiveDownloadJob()
    if (job) {
      activeDownloadJob.value = job
      sourceMode.value = 'download'
      pollDownload(job.id)
      await loadLocalDownloadDir(job.targetPath || job.localDir || undefined)
      return
    }
    activeDownloadJob.value = null
    await loadLocalDownloadDir()
  } catch {
    // Manual refresh still works if active job restoration fails.
  }
}

async function loadRemoteDir(path?: string) {
  if (!packageConfigured.value) {
    ElMessage.warning('请先在系统设置 / 环境配置中配置软件包下载服务器')
    return
  }
  remoteLoading.value = true
  try {
    const result = await listPackageDownloadDir(path || remotePath.value)
    remotePath.value = result.path
    remoteBasePath.value = result.basePath || remoteBasePath.value
    remoteFiles.value = result.files
    selectedRemoteRows.value = []
  } finally {
    remoteLoading.value = false
  }
}

async function loadLocalDownloadDir(path?: string) {
  if (!packageConfigured.value) return
  localLoading.value = true
  try {
    const result = await listPackageDownloadLocalDir(path)
    localBasePath.value = result.root
    localPath.value = result.path
    localFiles.value = result.files
  } finally {
    localLoading.value = false
  }
}

function getRemoteCrumbPath(index: number) {
  const suffix = remotePathRelativeParts.value.slice(0, index + 1).join('/')
  return `${remoteBasePath.value.replace(/\/+$/, '')}/${suffix}`
}

function getLocalCrumbPath(index: number) {
  const suffix = localPathRelativeParts.value.slice(0, index + 1).join('/')
  return `${localBasePath.value.replace(/\/+$/, '')}/${suffix}`
}

function remoteGoUp() {
  if (remotePath.value === remoteBasePath.value) return
  const parts = remotePath.value.split('/').filter(Boolean)
  parts.pop()
  loadRemoteDir('/' + parts.join('/'))
}

function localGoUp() {
  if (localPath.value === localBasePath.value) return
  const parts = localPath.value.split('/').filter(Boolean)
  parts.pop()
  loadLocalDownloadDir('/' + parts.join('/'))
}

function handleRemoteNavigate(row: FileItem) {
  if (row.isDir) loadRemoteDir(row.path)
}

function handleLocalNavigate(row: FileItem) {
  if (row.isDir) loadLocalDownloadDir(row.path)
}

function handleRemoteSelectionChange(rows: FileItem[]) {
  selectedRemoteRows.value = rows
}

async function handleRemoteDownload(row: FileItem) {
  const selectedPaths = row.isDir ? undefined : [row.path]
  try {
    const job = await startDownloadRemoteDir(row.isDir ? row.path : remotePath.value, undefined, selectedPaths)
    activeDownloadJob.value = job
    sourceMode.value = 'download'
    ElMessage.info(row.isDir ? '目录下载任务已创建' : '文件下载任务已创建')
    pollDownload(job.id)
  } catch {
    // handled globally
  }
}

async function handleDownloadSelectedRemoteItems() {
  if (!canDownloadSelected.value) return
  const selectedCount = selectedRemotePaths.value.length
  try {
    const job = await startDownloadRemoteDir(remotePath.value, undefined, selectedRemotePaths.value)
    activeDownloadJob.value = job
    sourceMode.value = 'download'
    ElMessage.info(`已创建 ${selectedCount} 项下载任务`)
    pollDownload(job.id)
  } catch {
    // handled globally
  }
}

async function pollDownload(jobId: string) {
  if (pollTimer) window.clearTimeout(pollTimer)
  const job = await getDownloadProgress(jobId)
  activeDownloadJob.value = job
  if (job.status === 'success') {
    ElMessage.success('下载完成，已生成更新版本')
    await loadLocalDownloadDir(job.targetPath || job.localDir)
    activeDownloadJob.value = null
    await loadVersions()
    if (job.batchId) await activateVersion(job.batchId)
    return
  }
  if (job.status === 'failed') {
    ElMessage.error(job.error || '下载失败')
    if (job.targetPath || job.localDir) await loadLocalDownloadDir(job.targetPath || job.localDir)
    activeDownloadJob.value = null
    return
  }
  if (job.status === 'canceled') {
    ElMessage.warning(job.error || '下载任务已取消')
    if (job.targetPath || job.localDir) await loadLocalDownloadDir(job.targetPath || job.localDir)
    activeDownloadJob.value = null
    return
  }
  pollTimer = window.setTimeout(() => pollDownload(jobId), 1000)
}

async function handleCancelDownload() {
  const job = activeDownloadJob.value
  if (!job || (job.status !== 'pending' && job.status !== 'running')) return
  try {
    const canceled = await cancelDownloadJob(job.id)
    activeDownloadJob.value = canceled
    if (pollTimer) window.clearTimeout(pollTimer)
    ElMessage.warning('已取消下载任务')
    await loadLocalDownloadDir(canceled.targetPath || canceled.localDir)
    activeDownloadJob.value = null
  } catch {
    // handled globally
  }
}

function sortFiles(files: FileItem[]) {
  const dirs = files.filter(f => f.isDir).sort((a, b) => a.name.localeCompare(b.name))
  const plainFiles = files.filter(f => !f.isDir).sort((a, b) => a.name.localeCompare(b.name))
  return [...dirs, ...plainFiles]
}

function relativePathParts(basePath: string, path: string) {
  if (!path || !basePath || path === basePath) return []
  const relative = path.startsWith(basePath) ? path.slice(basePath.length) : path
  return relative.split('/').filter(Boolean)
}

function downloadStatusText(status: DownloadJob['status']) {
  if (status === 'pending') return '等待下载'
  if (status === 'running') return '正在下载'
  if (status === 'success') return '下载完成'
  if (status === 'failed') return '下载失败'
  if (status === 'canceled') return '已取消'
  return status
}

function downloadItemStatusText(status: string) {
  if (status === 'pending') return '等待下载'
  if (status === 'running') return '正在下载'
  if (status === 'success') return '已完成'
  if (status === 'skipped') return '未变化'
  if (status === 'failed') return '失败'
  if (status === 'canceled') return '已取消'
  return status || '-'
}

function downloadItemTagType(status: string) {
  if (status === 'success' || status === 'skipped') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'running') return 'warning'
  return 'info'
}

function downloadProgressStatus(status: DownloadJob['status']) {
  if (status === 'success') return 'success'
  if (status === 'failed' || status === 'canceled') return 'exception'
  return undefined
}

function formatDownloadSize(size?: number) {
  if (!size || size < 0) return '-'
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  if (size < 1024 * 1024 * 1024) return `${(size / 1024 / 1024).toFixed(1)} MB`
  return `${(size / 1024 / 1024 / 1024).toFixed(1)} GB`
}
</script>

<template>
  <div class="batch-deploy-page">
    <div class="page-header">
      <div>
        <h4>新增更新任务</h4>
      </div>
      <div class="header-actions">
        <el-button @click="router.push('/artifacts/versions')">更新版本库</el-button>
        <el-button @click="router.push('/build-queue')">查看任务队列</el-button>
      </div>
    </div>

    <section class="step-strip">
      <div class="step-item active"><strong>1</strong><span>上传并制作镜像</span></div>
      <div class="step-line"></div>
      <div class="step-item"><strong>2</strong><span>{{ settingsStore.autoRollout ? '自动滚动更新' : '滚动更新' }}</span></div>
    </section>

    <section class="workflow-section">
      <div class="section-title">
        <div>
          <strong>制品来源</strong>
        </div>
        <el-segmented
          v-model="sourceMode"
          :options="[
            { label: '本地上传', value: 'upload' },
            { label: '服务器下载', value: 'download' },
            { label: '选择更新版本', value: 'existing' },
          ]"
        />
      </div>

      <div v-if="sourceMode === 'upload'" class="upload-zone" v-loading="uploadLoading" @dragover.prevent @drop.prevent="handleDrop">
        <el-icon :size="40"><Upload /></el-icon>
        <strong>拖拽 JAR / ZIP 到此处</strong>

        <div class="upload-actions">
          <el-button type="primary" @click="chooseFiles">选择文件</el-button>
          <el-button @click="chooseDirectory">选择目录</el-button>
        </div>
        <input ref="fileInput" type="file" multiple accept=".jar,.zip" hidden @change="handleUploadInput" />
        <input ref="dirInput" type="file" multiple webkitdirectory directory hidden @change="handleUploadInput" />
      </div>

      <div v-if="sourceMode === 'download'" class="download-layout">
        <div class="download-column">
          <div class="column-title">远程目录</div>
          <div class="path-bar">
            <span class="crumb" @click="loadRemoteDir(remoteBasePath)">软件包路径</span>
            <template v-for="(part, idx) in remotePathRelativeParts" :key="idx">
              <span class="crumb-sep">/</span>
              <span class="crumb" @click="loadRemoteDir(getRemoteCrumbPath(idx))">{{ part }}</span>
            </template>
            <el-button link size="small" :disabled="remotePath === remoteBasePath" @click="remoteGoUp">上级</el-button>
            <el-button link size="small" :loading="remoteLoading" @click="loadRemoteDir(remotePath)">刷新</el-button>
            <span class="path-spacer"></span>
            <el-button link size="small" type="primary" :disabled="!canDownloadSelected" @click="handleDownloadSelectedRemoteItems">
              下载选中<span v-if="selectedRemotePaths.length">({{ selectedRemotePaths.length }})</span>
            </el-button>
          </div>
          <el-table
            :data="sortedRemoteFiles"
            v-loading="remoteLoading"
            border
            stripe
            size="small"
            height="320"
            @row-dblclick="handleRemoteNavigate"
            @selection-change="handleRemoteSelectionChange"
          >
            <el-table-column type="selection" width="44" />
            <el-table-column label="名称" min-width="220" show-overflow-tooltip>
              <template #default="{ row }">
                <span class="file-name" @click="handleRemoteNavigate(row)">
                  <el-icon :color="row.isDir ? '#e6a23c' : '#909399'"><Folder v-if="row.isDir" /><Document v-else /></el-icon>
                  {{ row.name }}
                </span>
              </template>
            </el-table-column>
            <el-table-column label="大小" width="90">
              <template #default="{ row }">{{ row.isDir ? '-' : row.size }}</template>
            </el-table-column>
            <el-table-column prop="modTime" label="修改时间" width="170" />
            <el-table-column label="操作" width="90" fixed="right">
              <template #default="{ row }">
                <el-button link type="primary" :disabled="isDownloadRunning" @click.stop="handleRemoteDownload(row)">下载</el-button>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <div class="download-column">
          <div class="column-title">本地目录</div>
          <div v-if="activeDownloadJob" class="download-status">
            <div class="download-head">
              <span>{{ downloadStatusText(activeDownloadJob.status) }}</span>
              <div class="download-head-actions">
                <span>{{ activeDownloadJob.completedFiles }} / {{ activeDownloadJob.totalFiles || '-' }}</span>
                <el-button
                  v-if="activeDownloadJob.status === 'pending' || activeDownloadJob.status === 'running'"
                  link
                  type="danger"
                  size="small"
                  @click="handleCancelDownload"
                >
                  取消下载
                </el-button>
              </div>
            </div>
            <el-progress
              :percentage="downloadPercent"
              :indeterminate="activeDownloadJob.status === 'running' && !activeDownloadJob.totalFiles"
              :status="downloadProgressStatus(activeDownloadJob.status)"
            />
            <div class="download-sub">本地目录：{{ downloadTargetPath }}</div>
            <div class="download-sub">当前文件：{{ activeDownloadJob.currentPath || activeDownloadJob.remotePath }}</div>
            <div v-if="activeDownloadJob.error" class="download-error">{{ activeDownloadJob.error }}</div>
          </div>
          <div v-else class="local-empty">当前没有正在下载的任务。当前目录内容如下：</div>
          <div class="path-bar">
            <span class="crumb" @click="loadLocalDownloadDir(localBasePath)">本地下载目录</span>
            <template v-for="(part, idx) in localPathRelativeParts" :key="idx">
              <span class="crumb-sep">/</span>
              <span class="crumb" @click="loadLocalDownloadDir(getLocalCrumbPath(idx))">{{ part }}</span>
            </template>
            <el-button link size="small" :disabled="localPath === localBasePath" @click="localGoUp">上级</el-button>
            <el-button link size="small" :loading="localLoading" @click="loadLocalDownloadDir(localPath)">刷新</el-button>
          </div>
          <el-table
            :data="activeDownloadJob ? downloadFileRows : sortedLocalFiles"
            v-loading="localLoading"
            border
            stripe
            size="small"
            height="220"
            :empty-text="activeDownloadJob ? '当前任务暂无文件' : '当前目录暂无文件'"
            @row-dblclick="activeDownloadJob ? undefined : handleLocalNavigate"
          >
            <el-table-column label="名称" min-width="200" show-overflow-tooltip>
              <template #default="{ row }">
                <span class="file-name" @click="activeDownloadJob ? undefined : handleLocalNavigate(row)">
                  <el-icon :color="row.isDir ? '#e6a23c' : '#909399'"><Folder v-if="row.isDir" /><Document v-else /></el-icon>
                  {{ row.name }}
                </span>
              </template>
            </el-table-column>
            <el-table-column v-if="activeDownloadJob" label="下载状态" width="96">
              <template #default="{ row }">
                <el-tag size="small" :type="downloadItemTagType(row.status)">{{ downloadItemStatusText(row.status) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="大小" width="90">
              <template #default="{ row }">{{ row.isDir ? '-' : formatDownloadSize(row.size) }}</template>
            </el-table-column>
            <el-table-column v-if="activeDownloadJob" label="错误" min-width="160" show-overflow-tooltip>
              <template #default="{ row }">
                <span :class="{ 'download-error-inline': row.error }">{{ row.error || '-' }}</span>
              </template>
            </el-table-column>
          </el-table>
        </div>
      </div>

      <div v-if="sourceMode === 'existing'" class="existing-layout" v-loading="loading">
        <el-table :data="readyVersions" border stripe height="320" table-layout="auto">
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
          <el-table-column prop="remainingDeployableCount" label="可更新" width="86" />
          <el-table-column prop="invalidCount" label="不可读" width="86" />
          <el-table-column prop="unmatchedCount" label="未匹配" width="86" />
          <el-table-column label="更新时间" width="170">
            <template #default="{ row }">{{ row.updatedAt ? new Date(row.updatedAt).toLocaleString() : '-' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="96" fixed="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="selectVersion(row)">选择</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </section>

    <section class="summary-section" v-if="hasVersion">
      <div><span>版本号</span><strong>{{ batchId || '-' }}</strong></div>
      <div><span>来源</span><strong>{{ sourceTypeText(sourceType) }}</strong></div>
      <div><span>可部署</span><strong>{{ matchedCount }}</strong></div>
      <div><span>异常</span><strong>{{ invalidCount + unmatchedCount }}</strong></div>
      <div class="path-cell"><span>目录</span><strong>{{ sourceDir || '-' }}</strong></div>
      <el-button @click="resetSelection">更换制品</el-button>
    </section>

    <section class="workflow-section">
      <div class="section-title">
        <div>
          <strong>应用匹配</strong>
        </div>
        <div class="status-tags">
          <el-tag type="success">可部署 {{ matchedCount }}</el-tag>
          <el-tag type="danger">不可读 {{ invalidCount }}</el-tag>
          <el-tag type="warning">未匹配 {{ unmatchedCount }}</el-tag>
          <el-tag type="info">忽略 {{ skippedCount }}</el-tag>
        </div>
      </div>
      <el-table
        ref="matchTableRef"
        :data="matchResults"
        v-loading="loading"
        border
        stripe
        height="420"
        table-layout="auto"
        empty-text="暂无制品"
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
    </section>

    <section class="action-bar">
      <div class="deploy-options">
        <strong>{{ settingsStore.autoRollout ? '构建与滚动更新' : '第一步：上传并制作镜像' }}</strong>
      </div>
      <el-button type="primary" :disabled="deployableResults.length === 0" :loading="executing" @click="executeDeploy">
        {{ matchResults.length > 0 && matchResults.every(item => item.updateUsed || !item.matched || !item.valid || item.skipped) ? '无可用制品' : `${settingsStore.autoRollout ? '构建并滚动更新' : '制作镜像'}（${deployableResults.length} 个制品）` }}
      </el-button>
    </section>
  </div>
</template>

<style scoped>
:global(.deploy-scope-confirm .el-message-box__message) {
  white-space: pre-line;
  overflow-wrap: anywhere;
}

.batch-deploy-page { display: flex; flex-direction: column; gap: 12px; }
.page-header,
.step-strip,
.workflow-section,
.summary-section,
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
.header-actions { display: flex; gap: 8px; }
.step-strip { display: grid; grid-template-columns: 150px 1fr 150px 1fr 150px 1fr 150px; align-items: center; padding: 14px 18px; }
.step-item { display: flex; align-items: center; gap: 8px; color: #9ca3af; font-weight: 600; }
.step-item strong {
  width: 24px;
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  border: 1px solid #d1d5db;
  font-size: 12px;
}
.step-item.active { color: #2563eb; }
.step-item.active strong { color: #fff; background: #2563eb; border-color: #2563eb; }
.step-line { height: 1px; background: #d1d5db; }
.workflow-section { padding: 14px 16px 16px; }
.section-title {
  min-height: 40px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.section-title strong { display: block; color: #1f2937; margin-bottom: 4px; }
.section-title span { color: #8a9099; }
.status-tags { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
.upload-zone {
  min-height: 260px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 10px;
  border: 2px dashed #dcdfe6;
  border-radius: 4px;
  background: #fbfcff;
  color: #606266;
}
.upload-zone span { max-width: 520px; text-align: center; font-size: 12px; color: #909399; }
.upload-actions { margin-top: 8px; display: flex; gap: 10px; }
.download-layout { display: grid; grid-template-columns: minmax(0, 1.08fr) minmax(360px, 0.92fr); gap: 12px; }
.download-column { min-width: 0; }
.column-title { height: 30px; display: flex; align-items: center; font-size: 14px; font-weight: 600; color: #303133; }
.path-bar { min-height: 38px; display: flex; align-items: center; flex-wrap: wrap; gap: 4px; font-size: 12px; border: 1px solid #ebeef5; border-bottom: 0; padding: 7px 10px; }
.path-spacer { flex: 1; min-width: 12px; }
.crumb { cursor: pointer; color: #409eff; }
.crumb-sep { color: #c0c4cc; }
.file-name { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
.download-status { border: 1px solid #d9ecff; background: #f8fbff; border-radius: 4px; padding: 10px; margin-bottom: 10px; }
.download-head { display: flex; justify-content: space-between; align-items: center; gap: 10px; font-size: 12px; color: #606266; margin-bottom: 6px; }
.download-head-actions { display: inline-flex; align-items: center; gap: 10px; }
.download-sub { margin-top: 6px; font-size: 12px; color: #909399; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.download-error,
.download-error-inline { color: #f56c6c; }
.local-empty { min-height: 82px; display: flex; align-items: center; justify-content: center; text-align: center; color: #909399; font-size: 12px; border: 1px dashed #dcdfe6; border-radius: 4px; background: #fbfcff; padding: 12px; margin-bottom: 10px; }
.summary-section {
  display: grid;
  grid-template-columns: 220px 120px 90px 90px minmax(260px, 1fr) auto;
  gap: 8px;
  align-items: stretch;
  padding: 12px;
}
.summary-section > div {
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid #e5e7eb;
  background: #f8fafc;
}
.summary-section span {
  display: block;
  font-size: 12px;
  color: #8a9099;
  margin-bottom: 4px;
}
.summary-section strong {
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
.deploy-options > strong { color: #1f2937; }
@media (max-width: 1280px) {
  .step-strip { grid-template-columns: 1fr; gap: 8px; }
  .step-line { display: none; }
  .download-layout { grid-template-columns: 1fr; }
  .summary-section { grid-template-columns: 1fr 1fr; }
  .path-cell { grid-column: 1 / -1; }
  .action-bar,
  .deploy-options { align-items: stretch; flex-direction: column; }
}
</style>

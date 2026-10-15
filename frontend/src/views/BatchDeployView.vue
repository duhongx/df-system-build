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
import { getPipeline, rolloutPipeline, type Pipeline } from '../api/pipeline'
import { updateTaskSteps, imageState, imagesSettled, rolloutState, needsExecutionPolling, parseSubmittedPipelineIds } from '../utils/update-task-workflow'
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
const workflowStep = ref(0)
const submittedIds = ref<number[]>([])
const submittedPipelines = ref<Pipeline[]>([])
const executionLoading = ref(false)
const executionError = ref(false)
const rolling = ref(false)
let executionTimer: number | undefined
let disposed = false
let restoringSelection = false
const hasSubmitted = computed(() => submittedIds.value.length > 0)
const imagesComplete = computed(() => !executionError.value && submittedPipelines.value.length === submittedIds.value.length && imagesSettled(submittedPipelines.value))
const hasReadyImages = computed(() => submittedPipelines.value.some(pipeline => imageState(pipeline) === 'ready'))
const readyToRollout = computed(() => submittedPipelines.value.filter(pipeline => pipeline.status === 'IMAGE_READY'))
const allUpdated = computed(() => !executionError.value && submittedPipelines.value.length === submittedIds.value.length && submittedPipelines.value.length > 0 && submittedPipelines.value.every(pipeline => rolloutState(pipeline) === 'success'))
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
const hasVersion = computed(() => Boolean(batchId.value))
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
  submittedIds.value = parseSubmittedPipelineIds(route.query.pipelineIds)

  await Promise.allSettled([loadVersions(), loadSettingsAndRemoteRoot()])
  if (!hasSubmitted.value) await restoreActiveDownloadJob()
  if (batchId.value) {
    try {
      await loadVersionDetail(batchId.value, !hasSubmitted.value)
      workflowStep.value = 1
    } catch { /* Accepted task IDs remain available even if version loading fails. */ }
    sourceMode.value = sourceType.value === 'upload' ? 'upload' : sourceType.value === 'download' ? 'download' : 'existing'
  }
  if (hasSubmitted.value) {
    workflowStep.value = 2
    await refreshExecution()
  }
})

onUnmounted(() => {
  disposed = true
  if (executionTimer) window.clearTimeout(executionTimer)
  if (pollTimer) window.clearTimeout(pollTimer)
})

watch(sourceMode, async mode => {
  if (mode === 'download' && packageConfigured.value) {
    if (remoteFiles.value.length === 0) await loadRemoteDir(remotePath.value)
    await loadLocalDownloadDir()
  }
})

watch(workflowStep, async step => {
  if (step === 1) await restoreMatchSelection()
})

async function restoreMatchSelection() {
  restoringSelection = true
  const selected = new Set(selectedFiles.value)
  await nextTick()
  matchTableRef.value?.clearSelection()
  for (const row of matchResults.value) {
    if (selected.has(row.fileName) && isDeployableMatch(row)) matchTableRef.value?.toggleRowSelection(row, true)
  }
  restoringSelection = false
}

function handleMatchSelection(rows: MatchResult[]) {
  if (!restoringSelection) selectedFiles.value = rows.map(row => row.fileName)
}

async function refreshExecution() {
  if (executionLoading.value || !hasSubmitted.value || disposed) return
  if (executionTimer) window.clearTimeout(executionTimer)
  executionLoading.value = true
  try {
    const results = await Promise.allSettled(submittedIds.value.map(async id => {
      const pipeline = await getPipeline(id)
      if (pipeline.batchId !== batchId.value) throw new Error('任务不属于当前更新版本')
      return pipeline
    }))
    if (disposed) return
    executionError.value = results.some(result => result.status === 'rejected')
    submittedPipelines.value = results.flatMap((result, index) => {
      if (result.status === 'fulfilled') return [result.value]
      const previous = submittedPipelines.value.find(pipeline => pipeline.id === submittedIds.value[index])
      return previous ? [previous] : []
    })
    if (imagesComplete.value && hasReadyImages.value) workflowStep.value = 3
  } finally {
    executionLoading.value = false
    if (!disposed && (executionError.value || needsExecutionPolling(submittedPipelines.value))) executionTimer = window.setTimeout(refreshExecution, 3000)
  }
}

async function startRollout() {
  if (rolling.value || executionLoading.value || executionError.value || !readyToRollout.value.length) return
  rolling.value = true
  try {
    const results = await Promise.allSettled(readyToRollout.value.map(pipeline => rolloutPipeline(pipeline.id)))
    const failed = results.filter(result => result.status === 'rejected').length
    if (failed) ElMessage.warning(`${failed} 个任务未提交，请查看对应任务`)
    else ElMessage.success('滚动更新已提交')
    await refreshExecution()
  } finally {
    rolling.value = false
  }
}

function executionStatus(pipeline: Pipeline) {
  if (workflowStep.value === 2) {
    const state = imageState(pipeline)
    if (state === 'ready') return { type: 'success' as const, text: '镜像已就绪' }
    if (state === 'failed') return { type: 'danger' as const, text: pipeline.status === 'CANCELED' ? '已取消' : '制作失败' }
    return { type: 'primary' as const, text: pipeline.status === 'PENDING' ? '排队中' : '制作中' }
  }
  const state = rolloutState(pipeline)
  const text = { success: '更新完成', record_error: '更新完成，记录异常', failed: '更新失败', image_failed: '镜像制作失败', canceled: '已取消', ready: '待滚动更新', running: '滚动更新中', pending: '等待中' }[state]
  const type = state === 'success' ? 'success' : state === 'record_error' ? 'warning' : ['failed', 'image_failed'].includes(state) ? 'danger' : 'info'
  return { type: type as 'success' | 'warning' | 'danger' | 'info', text }
}

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
  workflowStep.value = 1
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
  sourceMode.value = 'existing'
  await activateVersion(version.versionNo)
}

async function loadVersionDetail(versionNo: string, notifyEmpty = true) {
  loading.value = true
  try {
    const result = await getArtifactVersion(versionNo)
    batchId.value = result.version.versionNo
    sourceType.value = result.version.sourceType
    sourceDir.value = result.version.targetPath || result.version.localDir || sourceDir.value
    matchResults.value = (result.items || []).map(versionItemToMatchResult)
    selectedFiles.value = selectedDeployableFileNames(matchResults.value)
    if (workflowStep.value === 1) await restoreMatchSelection()
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

function startNewTask() {
  if (executing.value || rolling.value || executionLoading.value) return
  if (executionTimer) window.clearTimeout(executionTimer)
  submittedIds.value = []
  submittedPipelines.value = []
  executionError.value = false
  sourceMode.value = 'upload'
  resetSelection()
}

function resetSelection() {
  if (hasSubmitted.value) return
  workflowStep.value = 0
  batchId.value = ''
  sourceDir.value = ''
  sourceType.value = ''
  matchResults.value = []
  selectedFiles.value = []
  router.replace('/batch-deploy')
}

async function executeDeploy() {
  if (executing.value || hasSubmitted.value) return
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
          settingsStore.autoRollout ? '确认镜像制作与更新范围' : '确认镜像制作范围', { customClass: 'deploy-scope-confirm', type: 'warning', confirmButtonText: settingsStore.autoRollout ? '制作镜像并滚动更新' : '制作镜像', cancelButtonText: '取消' },
        )
      } catch { return }
    }
    const result = await executeBatchDeploy(sourceDir.value, items, batchId.value)
    submittedIds.value = (result.pipelines || []).map(pipeline => Number(pipeline.id)).filter(id => Number.isSafeInteger(id) && id > 0)
    if (hasSubmitted.value) {
      // Persist accepted task IDs before any follow-up reads can fail.
      await router.replace({ path: '/batch-deploy', query: { batchId: batchId.value, pipelineIds: submittedIds.value.join(',') } })
      await refreshExecution()
    }
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
        { type: 'warning', confirmButtonText: '知道了' },
      ).catch(() => {})
    } else {
      ElMessage.success(`已创建 ${result.pipelines?.length || 0} 个制品更新任务`)
    }
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
  if (uploadLoading.value || hasSubmitted.value) return
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
    if (job.batchId && workflowStep.value === 0 && !hasSubmitted.value) await activateVersion(job.batchId)
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
  <div class="batch-deploy-page viewport-table-page">
    <div class="page-header">
      <div>
        <h4>新增更新任务</h4>
      </div>
      <div class="header-actions">
        <el-button v-if="hasSubmitted" :disabled="executing || rolling || executionLoading" type="primary" @click="startNewTask">新建任务</el-button>
        <el-button @click="router.push('/artifacts/versions')">更新版本库</el-button>
        <el-button @click="router.push('/build-queue')">查看任务队列</el-button>
      </div>
    </div>

    <section class="step-strip">
      <el-steps :active="allUpdated ? 4 : workflowStep" finish-status="success" align-center>
        <el-step v-for="title in updateTaskSteps" :key="title" :title="title" />
      </el-steps>
    </section>

    <section v-if="workflowStep === 0" class="workflow-section">
      <div class="section-title">
        <div>
          <strong>制品来源</strong>
        </div>
        <el-segmented
          v-model="sourceMode"
          :options="[
            { label: '离线上传', value: 'upload' },
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
          <div v-else class="local-empty">暂无下载任务</div>
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

    <section v-if="workflowStep === 1" class="workflow-section matching-section">
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
        height="100%"
        table-layout="auto"
        empty-text="暂无制品"
        @selection-change="handleMatchSelection"
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

    <section v-if="workflowStep === 2 && !hasSubmitted" class="workflow-section">
      <div class="section-title">
        <strong>制作镜像</strong>
        <el-tag>{{ settingsStore.autoRollout ? '自动滚动更新' : '手动滚动更新' }}</el-tag>
      </div>
      <el-table :data="deployableResults" border stripe height="100%">
        <el-table-column prop="fileName" label="制品文件" min-width="220" show-overflow-tooltip />
        <el-table-column prop="appName" label="应用" min-width="160" />
        <el-table-column prop="appType" label="类型" width="100" />
      </el-table>
    </section>

    <section v-if="hasSubmitted && workflowStep >= 2" class="workflow-section">
      <div class="section-title">
        <strong>{{ workflowStep === 2 ? '镜像制作状态' : '滚动更新状态' }}</strong>
        <el-button :disabled="rolling || executing" :loading="executionLoading" @click="refreshExecution">刷新状态</el-button>
      </div>
      <el-alert v-if="executionError" title="任务状态读取失败，请刷新重试" type="error" :closable="false" show-icon />
      <el-table :data="submittedPipelines" border stripe height="100%" empty-text="正在读取任务状态">
        <el-table-column prop="appName" label="应用" min-width="150" />
        <el-table-column prop="pipelineNo" label="任务" min-width="180" />
        <el-table-column prop="imageName" label="镜像" min-width="220" show-overflow-tooltip />
        <el-table-column label="状态" min-width="160">
          <template #default="{ row }"><el-tag :type="executionStatus(row).type">{{ executionStatus(row).text }}</el-tag></template>
        </el-table-column>
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }"><el-button link type="primary" @click="router.push(`/release/${row.id}`)">查看日志</el-button></template>
        </el-table-column>
      </el-table>
    </section>

    <section v-if="workflowStep > 0 || hasVersion" class="action-bar">
      <span v-if="hasVersion" class="version-label">{{ batchId }}</span>
      <div class="step-actions">
        <template v-if="workflowStep === 0">
          <el-button @click="resetSelection">清除选择</el-button>
          <el-button type="primary" @click="workflowStep = 1">下一步：应用匹配</el-button>
        </template>
        <template v-else-if="!hasSubmitted">
          <el-button :disabled="executing" @click="workflowStep--">上一步</el-button>
          <el-button v-if="workflowStep === 1" type="primary" :disabled="!deployableResults.length" @click="workflowStep = 2">下一步：制作镜像（{{ deployableResults.length }}）</el-button>
          <el-button v-else type="primary" :disabled="!deployableResults.length" :loading="executing" @click="executeDeploy">{{ settingsStore.autoRollout ? '开始更新' : '开始制作镜像' }}</el-button>
        </template>
        <template v-else-if="workflowStep === 2">
          <el-button type="primary" :disabled="!imagesComplete || !hasReadyImages" @click="workflowStep = 3">下一步：滚动更新</el-button>
        </template>
        <template v-else>
          <el-button :disabled="executionLoading || rolling" @click="workflowStep = 2">镜像状态</el-button>
          <el-button v-if="readyToRollout.length" type="primary" :disabled="executionError || executionLoading" :loading="rolling" @click="startRollout">开始滚动更新（{{ readyToRollout.length }}）</el-button>
          <el-tag v-else-if="allUpdated" type="success">更新完成</el-tag>
        </template>
      </div>
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
.page-header h4 { margin: 0; font-size: 16px; color: #1f2937; }
.header-actions { display: flex; gap: 8px; }
.step-strip { padding: 20px 18px; }
.workflow-section { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: auto; padding: 14px 16px 16px; }
.workflow-section > .el-table { flex: 1; min-height: 0; }
.page-header, .step-strip, .section-title, .action-bar { flex-shrink: 0; }
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
  flex: 1;
  min-height: 180px;
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
.action-bar {
  z-index: 5;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 16px;
  box-shadow: 0 -6px 18px rgba(15, 23, 42, 0.05);
}
.step-actions { display: flex; align-items: center; gap: 10px; }
.version-label { color: #606266; overflow-wrap: anywhere; }
@media (max-width: 1000px) {
  .download-layout { grid-template-columns: 1fr; }
  .page-header { gap: 12px; flex-wrap: wrap; }
  .step-strip { padding: 16px 4px; }
  .action-bar { align-items: stretch; flex-direction: column; }
  .step-actions { justify-content: flex-end; flex-wrap: wrap; }
}
</style>

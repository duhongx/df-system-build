<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  getActiveDownloadJob,
  getDownloadProgress,
  listPackageDownloadDir,
  startDownloadRemoteDir,
  type DownloadJob,
} from '../api/batch-deploy'
import { getSettings } from '../api/settings'
import type { FileItem } from '../api/server-mgmt'

type UploadEntry = { file: File; path: string }
type ImportMode = 'upload' | 'download'

const router = useRouter()
const fileInput = ref<HTMLInputElement>()
const dirInput = ref<HTMLInputElement>()
const importMode = ref<ImportMode>('upload')
const uploadLoading = ref(false)
const remoteLoading = ref(false)
const remoteBasePath = ref('/')
const remotePath = ref('/')
const remoteFiles = ref<FileItem[]>([])
const packageConfigured = ref(false)
const activeDownloadJob = ref<DownloadJob | null>(null)
let pollTimer: number | undefined

const sortedRemoteFiles = computed(() => {
  const dirs = remoteFiles.value.filter(f => f.isDir).sort((a, b) => a.name.localeCompare(b.name))
  const files = remoteFiles.value.filter(f => !f.isDir).sort((a, b) => a.name.localeCompare(b.name))
  return [...dirs, ...files]
})

const remotePathRelativeParts = computed(() => {
  if (!remotePath.value || !remoteBasePath.value || remotePath.value === remoteBasePath.value) return []
  const relative = remotePath.value.startsWith(remoteBasePath.value)
    ? remotePath.value.slice(remoteBasePath.value.length)
    : remotePath.value
  return relative.split('/').filter(Boolean)
})

const downloadPercent = computed(() => {
  const job = activeDownloadJob.value
  if (!job?.totalFiles) return 0
  return Math.min(100, Math.round((job.completedFiles / job.totalFiles) * 100))
})

const downloadedFiles = computed(() => activeDownloadJob.value?.files || [])

const localTargetPath = computed(() => {
  const job = activeDownloadJob.value
  if (!job) return '下载后自动生成本地工作区目录'
  return job.targetPath || job.localDir || '下载后自动生成本地工作区目录'
})

onMounted(async () => {
  await loadSettingsAndRemoteRoot()
  await restoreActiveDownloadJob()
})

onUnmounted(() => {
  if (pollTimer) window.clearTimeout(pollTimer)
})

watch(importMode, async mode => {
  if (mode === 'download' && packageConfigured.value && remoteFiles.value.length === 0) {
    await loadRemoteDir(remotePath.value)
  }
})

async function loadSettingsAndRemoteRoot() {
  try {
    const settings = await getSettings()
    packageConfigured.value = Boolean(settings.package_download_host && settings.package_download_user)
    remoteBasePath.value = settings.package_download_path || '/'
    remotePath.value = remoteBasePath.value
    if (packageConfigured.value && importMode.value === 'download') {
      await loadRemoteDir(remoteBasePath.value)
    }
  } catch {
    packageConfigured.value = false
  }
}

async function restoreActiveDownloadJob() {
  try {
    const job = await getActiveDownloadJob()
    if (!job) return
    activeDownloadJob.value = job
    importMode.value = 'download'
    pollDownload(job.id)
  } catch {
    // Ignore restore failures; manual refresh still works.
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
  } finally {
    remoteLoading.value = false
  }
}

function getRemoteCrumbPath(index: number) {
  const suffix = remotePathRelativeParts.value.slice(0, index + 1).join('/')
  return `${remoteBasePath.value.replace(/\/+$/, '')}/${suffix}`
}

function remoteGoUp() {
  if (remotePath.value === remoteBasePath.value) return
  const parts = remotePath.value.split('/').filter(Boolean)
  parts.pop()
  loadRemoteDir('/' + parts.join('/'))
}

function handleRemoteNavigate(row: FileItem) {
  if (row.isDir) loadRemoteDir(row.path)
}

async function handleRemoteDownload(row: FileItem) {
  if (!row.isDir) return
  try {
    const job = await startDownloadRemoteDir(row.path)
    activeDownloadJob.value = job
    ElMessage.info('下载任务已创建，可在本地目录查看进度')
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
    return
  }
  if (job.status === 'failed') {
    ElMessage.error(job.error || '下载失败')
    return
  }
  pollTimer = window.setTimeout(() => pollDownload(jobId), 1000)
}

function downloadStatusText(status: DownloadJob['status']) {
  if (status === 'pending') return '等待下载'
  if (status === 'running') return '正在下载'
  if (status === 'success') return '下载完成'
  if (status === 'failed') return '下载失败'
  return status
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
    router.push('/artifacts/versions')
  } finally {
    uploadLoading.value = false
  }
}
</script>

<template>
  <div class="artifact-collect-page">
    <div class="page-header">
      <h4>制品导入</h4>
      <el-button type="primary" plain @click="router.push('/artifacts/versions')">查看更新版本</el-button>
    </div>

    <div class="source-switch">
      <el-segmented
        v-model="importMode"
        :options="[
          { label: '本地上传', value: 'upload' },
          { label: '服务器下载', value: 'download' },
        ]"
      />
    </div>

    <div class="collect-panel">
      <section v-if="importMode === 'upload'" class="collect-card">
        <div class="card-title">本地上传</div>
        <div class="card-desc">从浏览器上传 jar/zip 文件或目录，系统会生成一个更新版本。</div>
        <div class="upload-zone" v-loading="uploadLoading" @dragover.prevent @drop.prevent="handleDrop">
          <el-icon :size="42"><Upload /></el-icon>
          <strong>点击或拖拽上传制品</strong>
          <span>支持 .jar / .zip；选择目录时会递归上传目录内制品</span>
          <div class="upload-actions">
            <el-button type="primary" @click="chooseFiles">选择文件</el-button>
            <el-button @click="chooseDirectory">选择目录</el-button>
          </div>
        </div>
        <input ref="fileInput" type="file" multiple accept=".jar,.zip" hidden @change="handleUploadInput" />
        <input ref="dirInput" type="file" multiple webkitdirectory directory hidden @change="handleUploadInput" />
      </section>

      <section v-if="importMode === 'download'" class="collect-card">
        <div class="card-title">服务器下载</div>
        <div class="card-desc">从软件包下载服务器选择目录，下载到平台工作区并生成更新版本。</div>
        <div class="download-layout">
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
            </div>
            <el-table :data="sortedRemoteFiles" v-loading="remoteLoading" border stripe size="small" height="430" @row-dblclick="handleRemoteNavigate">
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
                  <el-button v-if="row.isDir" link type="primary" @click.stop="handleRemoteDownload(row)">下载</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>

          <div class="download-column local-column">
            <div class="column-title local-title">
              <span>本地目录</span>
              <el-button
                v-if="activeDownloadJob?.status === 'success'"
                link
                type="primary"
                @click="router.push('/artifacts/versions')"
              >
                查看更新版本
              </el-button>
            </div>
            <div v-if="activeDownloadJob" class="download-status">
              <div class="download-head">
                <span>{{ downloadStatusText(activeDownloadJob.status) }}</span>
                <span>{{ activeDownloadJob.completedFiles }} / {{ activeDownloadJob.totalFiles || '-' }}</span>
              </div>
              <el-progress
                :percentage="downloadPercent"
                :indeterminate="activeDownloadJob.status === 'running' && !activeDownloadJob.totalFiles"
                :status="activeDownloadJob.status === 'success' ? 'success' : activeDownloadJob.status === 'failed' ? 'exception' : undefined"
              />
              <div class="download-sub">本地目录：{{ localTargetPath }}</div>
              <div class="download-sub">当前文件：{{ activeDownloadJob.currentPath || activeDownloadJob.remotePath }}</div>
              <div v-if="activeDownloadJob.error" class="download-error">{{ activeDownloadJob.error }}</div>
            </div>
            <div v-else class="local-empty">
              从左侧选择远程目录并点击下载后，这里会显示本地目标目录、下载进度和已下载文件。
            </div>
            <el-table :data="downloadedFiles" border stripe size="small" height="330" empty-text="暂无已下载文件">
              <el-table-column label="已下载文件" min-width="220" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="file-name">
                    <el-icon color="#909399"><Document /></el-icon>
                    {{ row }}
                  </span>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.artifact-collect-page { display: flex; flex-direction: column; gap: 12px; }
.page-header { display: flex; align-items: center; justify-content: space-between; }
.page-header h4 { margin: 0; font-size: 16px; }
.source-switch { display: flex; align-items: center; background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 12px 16px; }
.collect-panel { min-width: 0; }
.collect-card { background: #fff; border: 1px solid #ebeef5; border-radius: 8px; padding: 18px; min-width: 0; }
.card-title { font-size: 15px; font-weight: 600; color: #303133; }
.card-desc { margin-top: 6px; margin-bottom: 14px; font-size: 12px; color: #909399; }
.upload-zone {
  min-height: 430px; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px;
  border: 2px dashed #dcdfe6; border-radius: 8px; background: #fbfcff; color: #606266;
}
.upload-zone span { font-size: 12px; color: #909399; }
.upload-actions { margin-top: 10px; display: flex; gap: 10px; }
.download-layout { display: grid; grid-template-columns: minmax(0, 1.1fr) minmax(360px, 0.9fr); gap: 12px; }
.download-column { min-width: 0; }
.column-title { height: 36px; display: flex; align-items: center; font-size: 14px; font-weight: 600; color: #303133; }
.local-title { justify-content: space-between; }
.download-status { border: 1px solid #d9ecff; background: #f8fbff; border-radius: 6px; padding: 10px; margin-bottom: 10px; }
.download-head { display: flex; justify-content: space-between; font-size: 12px; color: #606266; margin-bottom: 6px; }
.download-sub { margin-top: 6px; font-size: 12px; color: #909399; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.download-error { margin-top: 6px; font-size: 12px; color: #f56c6c; }
.local-empty { min-height: 86px; display: flex; align-items: center; justify-content: center; text-align: center; color: #909399; font-size: 12px; border: 1px dashed #dcdfe6; border-radius: 6px; background: #fbfcff; padding: 12px; margin-bottom: 10px; }
.path-bar { min-height: 38px; display: flex; align-items: center; flex-wrap: wrap; gap: 4px; font-size: 12px; border: 1px solid #ebeef5; border-bottom: 0; padding: 7px 10px; }
.crumb { cursor: pointer; color: #409eff; }
.crumb-sep { color: #c0c4cc; }
.file-name { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
@media (max-width: 1200px) {
  .download-layout { grid-template-columns: 1fr; }
}
</style>

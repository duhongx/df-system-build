<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getManagedServer, listSftpFiles, mkdirSftp, renameSftp,
  type ManagedServer, type FileItem
} from '../api/server-mgmt'

const route = useRoute()
const router = useRouter()
const serverId = Number(route.params.id)
const server = ref<ManagedServer | null>(null)
const currentPath = ref('/')
const files = ref<FileItem[]>([])
const loading = ref(false)
const uploadInput = ref<HTMLInputElement>()

const sortedFiles = computed(() => {
  const dirs = files.value.filter(f => f.isDir).sort((a, b) => a.name.localeCompare(b.name))
  const regularFiles = files.value.filter(f => !f.isDir).sort((a, b) => a.name.localeCompare(b.name))
  return [...dirs, ...regularFiles]
})

onMounted(async () => {
  try {
    server.value = await getManagedServer(serverId)
  } catch (e) { /* handled */ }
  await loadFiles('/')
})

async function loadFiles(path: string) {
  loading.value = true
  try {
    const result = await listSftpFiles(serverId, path)
    currentPath.value = result.path
    files.value = result.files
  } catch (e) {
    files.value = []
  } finally {
    loading.value = false
  }
}

function handleNavigate(item: FileItem) {
  if (item.isDir) {
    loadFiles(item.path)
  }
}

function handleGoUp() {
  const parts = currentPath.value.split('/').filter(Boolean)
  parts.pop()
  const parent = '/' + parts.join('/')
  loadFiles(parent || '/')
}

function handlePathClick(idx: number) {
  const parts = currentPath.value.split('/').filter(Boolean)
  const path = '/' + parts.slice(0, idx + 1).join('/')
  loadFiles(path)
}

async function handleMkdir() {
  const { value } = await ElMessageBox.prompt('请输入目录名称', '新建目录', { confirmButtonText: '创建' })
  if (!value?.trim()) return
  const newPath = currentPath.value === '/' ? `/${value}` : `${currentPath.value}/${value}`
  try {
    await mkdirSftp(serverId, newPath)
    ElMessage.success('目录已创建')
    await loadFiles(currentPath.value)
  } catch (e) { /* handled */ }
}

async function handleRename(item: FileItem) {
  const { value } = await ElMessageBox.prompt('请输入新名称', '重命名', {
    confirmButtonText: '确定', inputValue: item.name
  })
  if (!value?.trim() || value === item.name) return
  const dir = currentPath.value === '/' ? '/' : currentPath.value
  const newPath = dir + '/' + value
  try {
    await renameSftp(serverId, item.path, newPath)
    ElMessage.success('重命名成功')
    await loadFiles(currentPath.value)
  } catch (e) { /* handled */ }
}

function handleDownload(item: FileItem) {
  const url = `/api/server-mgmt/${serverId}/sftp/download?path=${encodeURIComponent(item.path)}`
  window.open(url, '_blank')
}

async function handleUpload(event: Event) {
  const input = event.target as HTMLInputElement
  if (!input.files?.length) return
  const file = input.files[0]

  const formData = new FormData()
  formData.append('file', file)
  formData.append('path', currentPath.value)

  try {
    const resp = await fetch(`/api/server-mgmt/${serverId}/sftp/upload`, {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${localStorage.getItem('df-token') || ''}` },
      body: formData,
    })
    if (resp.ok) {
      ElMessage.success(`上传成功: ${file.name}`)
      await loadFiles(currentPath.value)
    } else {
      const data = await resp.json()
      ElMessage.error(data.message || '上传失败')
    }
  } catch (e) {
    ElMessage.error('上传失败')
  }
  input.value = ''
}

function formatSize(bytes: number) {
  if (bytes === 0) return '-'
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  if (bytes < 1024 * 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + ' MB'
  return (bytes / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

function handleBack() {
  router.push('/servers')
}

function triggerUpload() {
  uploadInput.value?.click()
}

const pathParts = computed(() => currentPath.value.split('/').filter(Boolean))
</script>

<template>
  <div class="files-page">
    <div class="files-header">
      <div class="header-left">
        <el-button size="small" @click="handleBack"><el-icon><ArrowLeft /></el-icon>返回</el-button>
        <span class="server-info" v-if="server">{{ server.username }}@{{ server.host }} ({{ server.remark || server.host }})</span>
      </div>
      <div class="header-right">
        <el-button size="small" @click="handleMkdir"><el-icon><FolderAdd /></el-icon>新建目录</el-button>
        <el-button size="small" type="primary" @click="triggerUpload"><el-icon><Upload /></el-icon>上传文件</el-button>
        <input ref="uploadInput" type="file" style="display: none;" @change="handleUpload" />
      </div>
    </div>

    <!-- Breadcrumb -->
    <div class="breadcrumb">
      <span class="crumb" @click="loadFiles('/')">/</span>
      <template v-for="(part, idx) in pathParts" :key="idx">
        <span class="crumb-sep">/</span>
        <span class="crumb" @click="handlePathClick(idx)">{{ part }}</span>
      </template>
      <el-button link size="small" style="margin-left: 8px;" @click="handleGoUp" :disabled="currentPath === '/'">
        <el-icon><Top /></el-icon>上级
      </el-button>
    </div>

    <!-- File Table -->
    <div class="table-wrapper">
      <el-table :data="sortedFiles" v-loading="loading" stripe size="small" @row-dblclick="handleNavigate">
        <el-table-column label="名称" min-width="300">
          <template #default="{ row }">
            <div class="file-name" @click="handleNavigate(row)">
              <el-icon :size="16" :color="row.isDir ? '#409eff' : '#909399'">
                <Folder v-if="row.isDir" />
                <Document v-else />
              </el-icon>
              <span :class="{ 'is-dir': row.isDir }">{{ row.name }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ row.isDir ? '-' : formatSize(row.size) }}</template>
        </el-table-column>
        <el-table-column prop="mode" label="权限" width="120" />
        <el-table-column prop="modTime" label="修改时间" width="170" />
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button type="primary" link size="small" @click.stop="handleDownload(row)">下载</el-button>
            <el-button type="primary" link size="small" @click.stop="handleRename(row)">重命名</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<style scoped>
.files-page { display: flex; flex-direction: column; gap: 12px; }
.files-header {
  display: flex; align-items: center; justify-content: space-between;
  background: #fff; padding: 12px 16px; border-radius: 6px; border: 1px solid #ebeef5;
}
.header-left { display: flex; align-items: center; gap: 12px; }
.header-right { display: flex; align-items: center; gap: 8px; }
.server-info { font-size: 13px; color: #303133; }
.breadcrumb {
  background: #fff; padding: 8px 16px; border-radius: 6px; border: 1px solid #ebeef5;
  font-size: 13px; display: flex; align-items: center; flex-wrap: wrap;
}
.crumb { cursor: pointer; color: #409eff; padding: 2px 4px; border-radius: 3px; }
.crumb:hover { background: #ecf5ff; }
.crumb-sep { color: #c0c4cc; margin: 0 2px; }
.table-wrapper { background: #fff; border-radius: 6px; border: 1px solid #ebeef5; overflow: hidden; }
.file-name { display: flex; align-items: center; gap: 6px; cursor: pointer; }
.file-name .is-dir { color: #409eff; font-weight: 500; }
</style>

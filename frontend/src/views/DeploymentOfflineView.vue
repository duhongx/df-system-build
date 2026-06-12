<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getOfflineStatus, installOffline, type OfflineStatus } from '../api/deployment'
import { formatTime } from '../utils/time'

const loading = ref(false)
const installing = ref(false)
const status = ref<OfflineStatus | null>(null)
const installPath = ref('')
const bundleVersion = ref('')

async function load() {
  loading.value = true
  try { status.value = await getOfflineStatus() }
  finally { loading.value = false }
}

async function install() {
  if (!installPath.value.trim()) { ElMessage.warning('请输入服务器上的离线包路径'); return }
  installing.value = true
  try {
    const res = await installOffline({ path: installPath.value, bundleVersion: bundleVersion.value || undefined, clean: true })
    ElMessage.success(`安装成功：版本 ${res.bundleVersion}，${res.fileCount} 个文件`)
    await load()
  } finally {
    installing.value = false
  }
}

function scanRows() {
  const s = status.value?.scan || {}
  return Object.keys(s).sort().map(k => ({ component: k, count: s[k] }))
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h4 class="page-title">离线包</h4>
    <div class="content-card" v-loading="loading">
      <div class="cur" v-if="status?.current">
        <span>当前版本 <strong>{{ status.current.bundleVersion || '-' }}</strong></span>
        <span>文件数 <strong>{{ status.current.fileCount }}</strong></span>
        <span>安装时间 <strong>{{ formatTime(status.current.installedAt) }}</strong></span>
        <span>安装人 <strong>{{ status.current.installedBy || '-' }}</strong></span>
      </div>
      <el-empty v-else :image-size="50" description="尚未安装离线包" />
      <p class="hint">资源目录：{{ status?.resourceDir }}。离线资源树（约 4.5GB）保存在服务器本地，由离线包安装填充；不入库、不嵌入二进制。</p>
    </div>

    <div class="content-card">
      <div class="sec-title">安装离线包</div>
      <p class="hint">先将离线包上传至服务器，再填写服务器上的包路径进行校验安装（sha256 + bundle_version + 原子替换）。</p>
      <div class="install-row">
        <el-input v-model="installPath" placeholder="服务器离线包路径，如 /tmp/offline-v1.2.tar.gz" style="flex: 1;" />
        <el-input v-model="bundleVersion" placeholder="期望版本(可选)" style="width: 200px;" />
        <el-button type="primary" :loading="installing" @click="install">校验并安装</el-button>
      </div>
    </div>

    <div class="content-card" v-if="scanRows().length">
      <div class="sec-title">资源扫描</div>
      <el-table :data="scanRows()" size="small" border stripe>
        <el-table-column prop="component" label="组件" min-width="160" />
        <el-table-column prop="count" label="文件数" width="120" />
      </el-table>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; margin-bottom: 12px; }
.cur { display: flex; gap: 24px; font-size: 13px; color: #606266; }
.cur strong { color: #303133; margin-left: 4px; }
.sec-title { font-size: 13px; font-weight: 600; color: #303133; margin-bottom: 10px; }
.hint { font-size: 12px; color: #909399; margin: 0 0 12px; }
.install-row { display: flex; gap: 8px; }
</style>

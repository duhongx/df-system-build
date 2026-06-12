<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  getGlobalConfig, putGlobalConfig, getOfflineStatus, installOffline,
  type GlobalConfig, type OfflineStatus,
} from '../api/deployment'
import { formatTime } from '../utils/time'

const loading = ref(false)
const saving = ref(false)
const cfg = ref<GlobalConfig>({
  deployment: { sshUser: '', sshPrivateKeyPath: '', sshPort: 22, remoteRoot: '', retainDeployments: 20, defaultTimeoutSeconds: 1800 },
  network: { vip: '', serviceCidr: '', clusterCidr: '', nodeCidrMaskSize: 24 },
  env: [],
})

// offline bundle
const offlineLoading = ref(false)
const installing = ref(false)
const status = ref<OfflineStatus | null>(null)
const installPath = ref('')
const bundleVersion = ref('')

async function load() {
  loading.value = true
  try { cfg.value = await getGlobalConfig() }
  finally { loading.value = false }
}

async function loadOffline() {
  offlineLoading.value = true
  try { status.value = await getOfflineStatus() }
  finally { offlineLoading.value = false }
}

async function save() {
  saving.value = true
  try {
    await putGlobalConfig({
      deployment: cfg.value.deployment,
      network: cfg.value.network,
      envReplace: false,
    })
    ElMessage.success('已保存全局配置')
  } finally {
    saving.value = false
  }
}

async function install() {
  if (!installPath.value.trim()) { ElMessage.warning('请输入服务器上的离线包路径'); return }
  installing.value = true
  try {
    const res = await installOffline({ path: installPath.value, bundleVersion: bundleVersion.value || undefined, clean: true })
    ElMessage.success(`安装成功：版本 ${res.bundleVersion}，${res.fileCount} 个文件`)
    await loadOffline()
  } finally {
    installing.value = false
  }
}

function scanRows() {
  const s = status.value?.scan || {}
  return Object.keys(s).sort().map(k => ({ component: k, count: s[k] }))
}

onMounted(() => { load(); loadOffline() })
</script>

<template>
  <div class="page">
    <h4 class="page-title">全局配置</h4>

    <div class="content-card" v-loading="loading">
      <el-form label-width="150px" style="max-width: 640px;">
        <el-divider content-position="left">部署</el-divider>
        <el-form-item label="远端根目录"><el-input v-model="cfg.deployment.remoteRoot" placeholder="/opt/his-deploy" /></el-form-item>
        <el-form-item label="保留部署记录数"><el-input-number v-model="cfg.deployment.retainDeployments" :min="0" /></el-form-item>
        <el-form-item label="默认超时(秒)"><el-input-number v-model="cfg.deployment.defaultTimeoutSeconds" :min="60" /></el-form-item>

        <el-divider content-position="left">网络</el-divider>
        <el-form-item label="Service CIDR"><el-input v-model="cfg.network.serviceCidr" /></el-form-item>
        <el-form-item label="Cluster CIDR"><el-input v-model="cfg.network.clusterCidr" /></el-form-item>
        <el-form-item label="Node CIDR 掩码"><el-input-number v-model="cfg.network.nodeCidrMaskSize" :min="8" :max="32" /></el-form-item>

        <el-form-item style="margin-top: 20px;">
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 离线包 -->
    <div class="content-card" v-loading="offlineLoading">
      <div class="sec-title">离线包</div>
      <div class="cur" v-if="status?.current">
        <span>当前版本 <strong>{{ status.current.bundleVersion || '-' }}</strong></span>
        <span>文件数 <strong>{{ status.current.fileCount }}</strong></span>
        <span>安装时间 <strong>{{ formatTime(status.current.installedAt) }}</strong></span>
        <span>安装人 <strong>{{ status.current.installedBy || '-' }}</strong></span>
      </div>
      <el-empty v-else :image-size="50" description="尚未安装离线包" />
      <p class="hint">资源目录：{{ status?.resourceDir }}。离线资源树（约 4.5GB）保存在服务器本地，由离线包安装填充；不入库、不嵌入二进制。</p>
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
.sec-title { font-size: 13px; font-weight: 600; color: #303133; margin-bottom: 10px; }
.cur { display: flex; gap: 24px; font-size: 13px; color: #606266; flex-wrap: wrap; }
.cur strong { color: #303133; margin-left: 4px; }
.hint { font-size: 12px; color: #909399; margin: 0 0 12px; }
.install-row { display: flex; gap: 8px; }
</style>

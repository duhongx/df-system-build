<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getGlobalConfig, putGlobalConfig, getOfflineStatus, installOffline, verifyOffline,
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
const initializing = ref(false)
const verifying = ref(false)
const status = ref<OfflineStatus | null>(null)
const offlinePath = ref('')

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

// 初始化：解压离线包并转移到部署的资源目录下
async function initialize() {
  if (!offlinePath.value.trim()) { ElMessage.warning('请输入离线包路径'); return }
  initializing.value = true
  try {
    const res = await installOffline({ path: offlinePath.value, clean: true })
    ElMessage.success(`初始化成功：版本 ${res.bundleVersion || '-'}，${res.fileCount} 个文件`)
    await loadOffline()
  } finally {
    initializing.value = false
  }
}

// 校验：检查已安装的离线包是否有缺失
async function verify() {
  verifying.value = true
  try {
    const res = await verifyOffline()
    if (res.ok) {
      ElMessage.success('校验通过：离线资源完整')
    } else {
      await ElMessageBox.alert(
        `缺失 ${res.missing.length} 项资源：\n` + res.missing.slice(0, 50).join('\n'),
        '校验未通过', { type: 'warning' })
    }
  } finally {
    verifying.value = false
  }
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

        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 离线包 -->
    <div class="content-card" v-loading="offlineLoading">
      <el-form label-width="150px" style="max-width: 760px;">
        <el-divider content-position="left">离线包</el-divider>
        <el-form-item label="当前版本">
          <span v-if="status?.current">
            {{ status.current.bundleVersion || '-' }} ·
            {{ status.current.fileCount }} 个文件 ·
            {{ formatTime(status.current.installedAt) }}
          </span>
          <span v-else class="muted">尚未初始化</span>
        </el-form-item>
        <el-form-item label="离线包路径">
          <el-input v-model="offlinePath" placeholder="服务器上的离线包路径，如 /tmp/offline-v1.2.tar.gz" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="initializing" @click="initialize">初始化</el-button>
          <el-button :loading="verifying" @click="verify">校验</el-button>
        </el-form-item>
        <el-form-item>
          <span class="hint">初始化：解压离线包并转移到资源目录 {{ status?.resourceDir }}。校验：检查资源是否有缺失。</span>
        </el-form-item>
      </el-form>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; margin-bottom: 12px; }
.muted { color: #909399; }
.hint { font-size: 12px; color: #909399; }
</style>

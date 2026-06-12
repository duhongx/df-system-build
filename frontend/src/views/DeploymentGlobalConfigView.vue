<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getGlobalConfig, putGlobalConfig, type GlobalConfig } from '../api/deployment'

const loading = ref(false)
const saving = ref(false)
const cfg = ref<GlobalConfig>({
  deployment: { sshUser: '', sshPrivateKeyPath: '', sshPort: 22, remoteRoot: '', retainDeployments: 20, defaultTimeoutSeconds: 1800 },
  network: { vip: '', serviceCidr: '', clusterCidr: '', nodeCidrMaskSize: 24 },
  env: [],
})

async function load() {
  loading.value = true
  try { cfg.value = await getGlobalConfig() }
  finally { loading.value = false }
}

async function save() {
  saving.value = true
  try {
    // env is left untouched (envReplace=false); host SSH credentials come from
    // 服务器管理, so only remote root / retention / timeout / network CIDRs here.
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

onMounted(load)
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
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
</style>

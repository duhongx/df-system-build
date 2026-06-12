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

function addEnv() { cfg.value.env.push({ key: '', value: '' }) }
function removeEnv(i: number) { cfg.value.env.splice(i, 1) }

async function save() {
  saving.value = true
  try {
    await putGlobalConfig({
      deployment: cfg.value.deployment,
      network: cfg.value.network,
      env: cfg.value.env.filter(e => e.key),
      envReplace: true,
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
        <el-divider content-position="left">连接 / 部署</el-divider>
        <el-form-item label="SSH 用户(默认)"><el-input v-model="cfg.deployment.sshUser" /></el-form-item>
        <el-form-item label="SSH 私钥路径"><el-input v-model="cfg.deployment.sshPrivateKeyPath" /></el-form-item>
        <el-form-item label="SSH 端口"><el-input-number v-model="cfg.deployment.sshPort" :min="1" :max="65535" /></el-form-item>
        <el-form-item label="远端根目录"><el-input v-model="cfg.deployment.remoteRoot" placeholder="/opt/his-deploy" /></el-form-item>
        <el-form-item label="保留部署记录数"><el-input-number v-model="cfg.deployment.retainDeployments" :min="0" /></el-form-item>
        <el-form-item label="默认超时(秒)"><el-input-number v-model="cfg.deployment.defaultTimeoutSeconds" :min="60" /></el-form-item>

        <el-divider content-position="left">网络</el-divider>
        <el-form-item label="VIP"><el-input v-model="cfg.network.vip" /></el-form-item>
        <el-form-item label="Service CIDR"><el-input v-model="cfg.network.serviceCidr" /></el-form-item>
        <el-form-item label="Cluster CIDR"><el-input v-model="cfg.network.clusterCidr" /></el-form-item>
        <el-form-item label="Node CIDR 掩码"><el-input-number v-model="cfg.network.nodeCidrMaskSize" :min="8" :max="32" /></el-form-item>

        <el-divider content-position="left">环境变量（密码不能含 shell 元字符）</el-divider>
        <div v-for="(e, i) in cfg.env" :key="i" class="env-row">
          <el-input v-model="e.key" placeholder="键" style="width: 220px;" />
          <el-input v-model="e.value" placeholder="值" style="flex: 1;" />
          <el-button link type="danger" @click="removeEnv(i)">删除</el-button>
        </div>
        <el-button size="small" @click="addEnv">+ 添加变量</el-button>

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
.env-row { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
</style>

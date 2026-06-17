<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import request from '../api/request'
import { useSettingsStore } from '../stores/settings'

const loading = ref(false)
const saving = ref(false)
const testingType = ref('')
const k8sInputMode = ref<'path' | 'content'>('path')
const k8sNamespaces = ref<string[]>([])
const k8sLoadingNs = ref(false)
const k8sReadingConfig = ref(false)
const settingsStore = useSettingsStore()

const form = ref<Record<string, string>>({
  docker_registry_url: '',
  docker_registry_user: '',
  docker_registry_password: '',
  docker_registry_repo: '',
  k8s_kubeconfig_path: '',
  k8s_kubeconfig_content: '',
  k8s_namespace: '',
  nacos_url: '',
  nacos_user: '',
  nacos_password: '',
  skywalking_oap_url: '',
  skywalking_graphql_url: '',
  package_download_host: '',
  package_download_user: 'root',
  package_download_password: '',
  package_download_key: '',
  package_download_path: '/',
  postgresql_host: '',
  postgresql_port: '5432',
  postgresql_admin_password: '',
  postgresql_user: '',
  postgresql_password: '',
  postgresql_database: '',
})

async function loadSettings() {
  loading.value = true
  try {
    const res: any = await request.get('/settings')
    if (res && typeof res === 'object') {
      for (const key of Object.keys(form.value)) {
        if (res[key] !== undefined) {
          form.value[key] = res[key]
        }
      }
    }
    // Determine k8s input mode
    if (form.value.k8s_kubeconfig_content) {
      k8sInputMode.value = 'content'
    }
  } catch (e: any) {
    ElMessage.error('加载设置失败: ' + (e.message || '未知错误'))
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  saving.value = true
  try {
    await request.put('/settings', form.value)
    await settingsStore.fetchSettings()
    ElMessage.success('保存成功')
  } catch (e: any) {
    ElMessage.error('保存失败: ' + (e.message || '未知错误'))
  } finally {
    saving.value = false
  }
}

async function testConnection(type: string) {
  testingType.value = type
  let config: Record<string, string> = {}

  switch (type) {
    case 'registry':
      config = {
        docker_registry_url: form.value.docker_registry_url,
        docker_registry_user: form.value.docker_registry_user,
        docker_registry_password: form.value.docker_registry_password,
        docker_registry_repo: form.value.docker_registry_repo,
      }
      break
    case 'k8s':
      config = {
        k8s_kubeconfig_path: form.value.k8s_kubeconfig_path,
        k8s_kubeconfig_content: form.value.k8s_kubeconfig_content,
        k8s_namespace: form.value.k8s_namespace,
      }
      break
    case 'nacos':
      config = {
        nacos_url: form.value.nacos_url,
        nacos_user: form.value.nacos_user,
        nacos_password: form.value.nacos_password,
      }
      break
    case 'skywalking':
      config = {
        skywalking_oap_url: form.value.skywalking_oap_url,
        skywalking_graphql_url: form.value.skywalking_graphql_url,
      }
      break
    case 'package-download':
      config = {
        package_download_host: form.value.package_download_host,
        package_download_user: form.value.package_download_user,
        package_download_password: form.value.package_download_password,
        package_download_key: form.value.package_download_key,
        package_download_path: form.value.package_download_path,
      }
      break
    case 'postgresql':
      config = {
        postgresql_host: form.value.postgresql_host,
        postgresql_port: form.value.postgresql_port,
        postgresql_admin_password: form.value.postgresql_admin_password,
        postgresql_user: form.value.postgresql_user,
        postgresql_password: form.value.postgresql_password,
        postgresql_database: form.value.postgresql_database,
      }
      break
  }

  try {
    const res: any = await request.post('/settings/test-connection', { type, config })
    ElMessage.success(res.message || '连接成功')
  } catch (e: any) {
    // Error already shown by interceptor, no need to show again
  } finally {
    testingType.value = ''
  }
}

async function handleReadKubeconfig() {
  const path = form.value.k8s_kubeconfig_path || '/root/.kube/config'
  k8sReadingConfig.value = true
  try {
    const res: any = await request.get('/settings/read-kubeconfig', { params: { path } })
    if (res?.content) {
      form.value.k8s_kubeconfig_content = res.content
      if (res.namespace) {
        form.value.k8s_namespace = res.namespace
      }
      k8sInputMode.value = 'content'
      ElMessage.success('读取成功')
    } else {
      ElMessage.warning('未读取到内容')
    }
  } catch (e: any) {
    // Error already shown by interceptor
  } finally {
    k8sReadingConfig.value = false
  }
}

async function handleLoadNamespaces() {
  k8sLoadingNs.value = true
  try {
    const res: any = await request.post('/settings/k8s-namespaces', {
      config: {
        k8s_kubeconfig_path: form.value.k8s_kubeconfig_path,
        k8s_kubeconfig_content: form.value.k8s_kubeconfig_content,
        k8s_namespace: form.value.k8s_namespace,
      },
    })
    if (Array.isArray(res)) {
      k8sNamespaces.value = res
      if (!form.value.k8s_namespace && res.length > 0) {
        form.value.k8s_namespace = res[0]
      }
      if (res.length > 0) {
        ElMessage.success(`获取到 ${res.length} 个命名空间`)
      } else {
        ElMessage.warning('未获取到命名空间，请检查 kubeconfig 配置')
      }
    }
  } catch (e: any) {
    // Error already shown by interceptor
  } finally {
    k8sLoadingNs.value = false
  }
}

onMounted(() => {
  loadSettings()
})
</script>

<template>
  <div class="settings-environment" v-loading="loading">
    <el-alert
      type="warning"
      :closable="false"
      show-icon
      style="margin-bottom: 20px;"
    >
      以下账号权限极大，请务必保管好。所有具备管理员权限的人员均可查看账号信息。
    </el-alert>

    <!-- 镜像仓库设置 -->
    <el-card shadow="never" style="margin-bottom: 20px;">
      <template #header>
        <div class="section-header">
          <span class="section-title">镜像仓库设置</span>
          <el-button
            size="small"
            :loading="testingType === 'registry'"
            @click="testConnection('registry')"
          >测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="仓库地址">
          <el-input v-model="form.docker_registry_url" placeholder="如 192.168.199.102:8888" />
        </el-form-item>
        <el-form-item label="仓库名称">
          <el-input v-model="form.docker_registry_repo" placeholder="cloudhis" />
          <div class="form-hint">Nexus 中 Docker (hosted) 仓库的名称</div>
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.docker_registry_user" placeholder="仓库用户名" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.docker_registry_password" type="password" show-password placeholder="仓库密码" />
        </el-form-item>
      </el-form>
    </el-card>

    <!-- K8s 设置 -->
    <el-card shadow="never" style="margin-bottom: 20px;">
      <template #header>
        <div class="section-header">
          <span class="section-title">K8s 设置</span>
          <el-button
            size="small"
            :loading="testingType === 'k8s'"
            @click="testConnection('k8s')"
          >测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="配置方式">
          <el-radio-group v-model="k8sInputMode">
            <el-radio value="path">服务器路径</el-radio>
            <el-radio value="content">手动录入</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="kubeconfig 路径" v-if="k8sInputMode === 'path'">
          <div style="display: flex; gap: 8px; width: 100%;">
            <el-input v-model="form.k8s_kubeconfig_path" placeholder="/root/.kube/config" style="flex: 1;" />
            <el-button :loading="k8sReadingConfig" @click="handleReadKubeconfig">读取</el-button>
          </div>
          <div class="form-hint">服务器上 kubeconfig 文件的绝对路径</div>
        </el-form-item>
        <el-form-item label="kubeconfig 内容" v-if="k8sInputMode === 'content'">
          <el-input
            v-model="form.k8s_kubeconfig_content"
            type="textarea"
            :rows="10"
            placeholder="粘贴 kubeconfig YAML 内容..."
            class="code-editor"
          />
        </el-form-item>
        <el-form-item label="命名空间">
          <div style="display: flex; gap: 8px; width: 100%;">
            <el-select v-model="form.k8s_namespace" filterable allow-create style="flex: 1;" placeholder="选择或输入命名空间">
              <el-option v-for="ns in k8sNamespaces" :key="ns" :label="ns" :value="ns" />
            </el-select>
            <el-button :loading="k8sLoadingNs" @click="handleLoadNamespaces">获取命名空间</el-button>
          </div>
          <div class="form-hint">点击"获取命名空间"从集群读取可用的 namespace 列表</div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- Nacos -->
    <el-card shadow="never" style="margin-bottom: 20px;">
      <template #header>
        <div class="section-header">
          <span class="section-title">Nacos</span>
          <el-button
            size="small"
            :loading="testingType === 'nacos'"
            @click="testConnection('nacos')"
          >测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="地址">
          <el-input v-model="form.nacos_url" placeholder="如 http://192.168.199.101:8848/nacos" />
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.nacos_user" placeholder="nacos" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.nacos_password" type="password" show-password placeholder="Nacos 密码" />
        </el-form-item>
      </el-form>
    </el-card>

    <!-- SkyWalking -->
    <el-card shadow="never" style="margin-bottom: 20px;">
      <template #header>
        <div class="section-header">
          <span class="section-title">SkyWalking</span>
          <el-button
            size="small"
            :loading="testingType === 'skywalking'"
            @click="testConnection('skywalking')"
          >测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="OAP 地址">
          <el-input v-model="form.skywalking_oap_url" placeholder="192.168.1.150:11800" />
          <div class="form-hint">多个示例: 192.168.1.150:11800,192.168.1.151:11800</div>
        </el-form-item>
        <el-form-item label="GraphQL 地址">
          <el-input v-model="form.skywalking_graphql_url" placeholder="如 http://192.168.1.154:28080/graphql" />
        </el-form-item>
      </el-form>
    </el-card>

    <!-- 软件包下载服务器 -->
    <el-card shadow="never" style="margin-bottom: 20px;">
      <template #header>
        <div class="section-header">
          <span class="section-title">软件包下载服务器</span>
          <el-button
            size="small"
            :loading="testingType === 'package-download'"
            @click="testConnection('package-download')"
          >测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="服务器地址">
          <el-input v-model="form.package_download_host" placeholder="如 192.168.199.100 或 192.168.199.100:22" />
          <div class="form-hint">不填写端口时默认使用 22</div>
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.package_download_user" placeholder="root" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.package_download_password" type="password" show-password placeholder="服务器登录密码" />
        </el-form-item>
        <el-form-item label="Key">
          <el-input
            v-model="form.package_download_key"
            type="textarea"
            :rows="6"
            placeholder="可选，粘贴 SSH 私钥内容"
          />
          <div class="form-hint">填写 Key 时优先使用 Key 登录；不填则使用密码登录</div>
        </el-form-item>
        <el-form-item label="软件包路径">
          <el-input v-model="form.package_download_path" placeholder="/root/DFHIS/his-release" />
          <div class="form-hint">批量部署的「服务器下载」会从该目录开始浏览</div>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- PostgreSQL -->
    <el-card shadow="never" style="margin-bottom: 20px;">
      <template #header>
        <div class="section-header">
          <span class="section-title">PostgreSQL</span>
          <el-button
            size="small"
            :loading="testingType === 'postgresql'"
            @click="testConnection('postgresql')"
          >测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="主机">
          <el-input v-model="form.postgresql_host" placeholder="如 192.168.1.100" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input v-model="form.postgresql_port" placeholder="5432" />
        </el-form-item>
        <el-form-item label="管理员密码">
          <el-input v-model="form.postgresql_admin_password" type="password" show-password placeholder="PostgreSQL 管理员密码" />
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="form.postgresql_user" placeholder="PostgreSQL 用户名" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.postgresql_password" type="password" show-password placeholder="PostgreSQL 密码" />
        </el-form-item>
        <el-form-item label="数据库名">
          <el-input v-model="form.postgresql_database" placeholder="数据库名" />
        </el-form-item>
      </el-form>
    </el-card>

    <!-- Save Button -->
    <div class="save-bar">
      <el-button type="primary" :loading="saving" @click="saveSettings">保存</el-button>
    </div>
  </div>
</template>

<style scoped>
.settings-environment {
  width: 100%;
  max-width: 1080px;
}

.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.section-title {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
}

.form-hint {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
}

.save-bar {
  padding: 16px 0;
  text-align: left;
}

:deep(.code-editor textarea) {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 13px;
  line-height: 1.5;
}
</style>

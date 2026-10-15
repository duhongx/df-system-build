<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { ElMessage, type TableInstance } from 'element-plus'
import request from '../api/request'
import { useSettingsStore } from '../stores/settings'
import { listPostgreSQLPasswordOverrides, discoverPostgreSQLAccounts, setPostgreSQLAccountPassword, savePostgreSQLAccountPasswords, clearPostgreSQLAccountPassword, testPostgreSQLAccount, testPostgreSQLConnections, type PostgreSQLBusinessAccount, type PostgreSQLAccountTestResult, type PostgreSQLConnectionTestReport } from '../api/settings'

const loading = ref(false)
const saving = ref(false)
const testingType = ref('')
const k8sInputMode = ref<'path' | 'content'>('path')
const k8sNamespaces = ref<string[]>([])
const k8sLoadingNs = ref(false)
const k8sReadingConfig = ref(false)
const settingsStore = useSettingsStore()
const pgAccounts = ref<PostgreSQLBusinessAccount[]>([])
const pgDiscovering = ref(false)
const pgAccountsDiscovered = ref(false)
const pgTestReport = ref<PostgreSQLConnectionTestReport | null>(null)
const pgAccountResults = ref<Record<string, PostgreSQLAccountTestResult>>({})
const pgAccountBusy = ref('')
const pgPasswordDialog = ref(false)
const pgPasswordDrafts = ref<Record<string, string>>({})
const pgTestDialog = ref(false)
const pgAccountTable = ref<TableInstance>()
const pgSelectedAccounts = ref<PostgreSQLBusinessAccount[]>([])
const pgBulkPassword = ref('')
const pgAccountSearch = ref('')
const pgVisibleAccounts = computed(() => pgAccounts.value.filter(account => account.username.toLowerCase().includes(pgAccountSearch.value.trim().toLowerCase())))
const pgPendingCount = computed(() => Object.values(pgPasswordDrafts.value).filter(Boolean).length)

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
  postgresql_password: '',
  postgresql_password_mode: 'shared',
  postgresql_database: '',
})

function postgresqlConfig(): Record<string, string> {
  const keys = ['postgresql_host', 'postgresql_port', 'postgresql_database', 'postgresql_admin_password', 'postgresql_password', 'postgresql_password_mode']
  return Object.fromEntries(keys.map(key => [key, form.value[key] || '']))
}

watch(() => Object.values(postgresqlConfig()).join('\u0000'), () => {
  pgTestReport.value = null
  pgTestDialog.value = false
  pgAccountResults.value = {}
  pgAccountsDiscovered.value = false
})

async function loadPostgreSQLAccounts() {
  pgDiscovering.value = true
  pgAccountTable.value?.clearSelection()
  pgSelectedAccounts.value = []
  try {
    pgAccounts.value = await discoverPostgreSQLAccounts(postgresqlConfig())
    pgAccountsDiscovered.value = true
  } catch { /* Request interceptor displays the error. */ }
  finally { pgDiscovering.value = false }
}

async function openPostgreSQLPasswords() {
  pgPasswordDialog.value = true
  pgPasswordDrafts.value = {}
  pgBulkPassword.value = ''
  pgAccountSearch.value = ''
  pgSelectedAccounts.value = []
  await loadPostgreSQLAccounts()
}

function selectPostgreSQLAccounts(unconfiguredOnly = false) {
  pgAccountTable.value?.clearSelection()
  for (const account of pgVisibleAccounts.value) {
    if (account.discovered && (!unconfiguredOnly || (!account.hasOverride && !pgPasswordDrafts.value[account.username]))) pgAccountTable.value?.toggleRowSelection(account, true)
  }
}

function applyPostgreSQLBulkPassword() {
  if (!pgBulkPassword.value || !pgSelectedAccounts.value.length) return
  for (const account of pgSelectedAccounts.value) pgPasswordDrafts.value[account.username] = pgBulkPassword.value
  const count = pgSelectedAccounts.value.length
  pgBulkPassword.value = ''
  pgAccountTable.value?.clearSelection()
  ElMessage.success(`已填入 ${count} 个账号，点击“保存并关闭”生效；个别账号可继续单独修改`)
}

function closePostgreSQLPasswords() {
  pgPasswordDrafts.value = {}
  pgBulkPassword.value = ''
  pgSelectedAccounts.value = []
}

async function savePostgreSQLPasswordDialog() {
  pgAccountBusy.value = '__save_all__'
  const items = Object.entries(pgPasswordDrafts.value).filter(([, password]) => password).map(([username, password]) => ({ username, password }))
  try {
    await savePostgreSQLAccountPasswords(items, postgresqlConfig())
    for (const item of items) {
      const account = pgAccounts.value.find(account => account.username === item.username)
      if (account) account.hasOverride = true
      delete pgAccountResults.value[item.username]
    }
    pgTestReport.value = null
    pgPasswordDialog.value = false
    ElMessage.success(`设置已保存${items.length ? `，更新 ${items.length} 个账号密码` : ''}`)
  } catch { /* Request interceptor displays the error. */ }
  finally { pgAccountBusy.value = '' }
}

async function savePostgreSQLSharedPassword() {
  if (!form.value.postgresql_password) { ElMessage.warning('请输入业务密码'); return }
  pgAccountBusy.value = '__shared__'
  try {
    await savePostgreSQLAccountPasswords([], postgresqlConfig())
    ElMessage.success('统一业务密码已保存，全部业务账号使用此密码')
  } catch { /* Request interceptor displays the error. */ }
  finally { pgAccountBusy.value = '' }
}

async function handlePostgreSQLPasswordMode(value: string | number | boolean | undefined) {
  if (value === 'individual') await openPostgreSQLPasswords()
}

async function savePostgreSQLPassword(account: PostgreSQLBusinessAccount) {
  const password = pgPasswordDrafts.value[account.username]
  if (!password) { ElMessage.warning('请输入该账号的密码'); return }
  pgAccountBusy.value = account.username
  try {
    await setPostgreSQLAccountPassword(account.username, password)
    account.hasOverride = true
    delete pgPasswordDrafts.value[account.username]
    delete pgAccountResults.value[account.username]
    pgTestReport.value = null
    ElMessage.success(`${account.username} 密码已保存`)
  } catch { /* Request interceptor displays the error. */ }
  finally { pgAccountBusy.value = '' }
}

async function clearPostgreSQLPassword(account: PostgreSQLBusinessAccount) {
  pgAccountBusy.value = account.username
  try {
    await clearPostgreSQLAccountPassword(account.username)
    account.hasOverride = false
    delete pgPasswordDrafts.value[account.username]
    delete pgAccountResults.value[account.username]
    pgTestReport.value = null
    ElMessage.success('账号密码已清除')
  } catch { /* Request interceptor displays the error. */ }
  finally { pgAccountBusy.value = '' }
}

async function testPostgreSQLBusinessAccount(account: PostgreSQLBusinessAccount) {
  pgAccountBusy.value = account.username
  try {
    const result = await testPostgreSQLAccount(account.username, postgresqlConfig())
    pgAccountResults.value[account.username] = result
    if (result.success) ElMessage.success(`${account.username} 连接成功`)
    else ElMessage.error(result.message)
  } catch { /* Request interceptor displays the error. */ }
  finally { pgAccountBusy.value = '' }
}

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
    const overrides = await listPostgreSQLPasswordOverrides()
    pgAccounts.value = overrides.map(row => ({ username: row.username, hasOverride: true, discovered: false }))
  } catch (e: any) {
    ElMessage.error('加载设置失败: ' + (e.message || '未知错误'))
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (testingType.value || saving.value || pgAccountBusy.value) return
  saving.value = true
  try {
    await request.put('/settings', { ...form.value, ...connectionConfig('k8s') })
    await settingsStore.fetchSettings()
    ElMessage.success('保存成功')
  } catch (e: any) {
    ElMessage.error('保存失败: ' + (e.message || '未知错误'))
  } finally {
    saving.value = false
  }
}

function connectionConfig(type: string): Record<string, string> {
  if (type === 'postgresql') return postgresqlConfig()
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
  }

  if (type === 'k8s') {
    // Persist only the selected kubeconfig source so a previously entered
    // content value cannot override the server path selected on this page.
    if (k8sInputMode.value === 'path') config.k8s_kubeconfig_content = ''
    else config.k8s_kubeconfig_path = ''
  }
  return config
}

async function saveAndTestConnection(type: string) {
  if (saving.value || testingType.value || pgAccountBusy.value) return
  testingType.value = type
  const config = connectionConfig(type)
  let saved = false
  try {
    await request.put('/settings', config)
    saved = true
    await settingsStore.fetchSettings()
    ElMessage.success('设置已保存，正在测试连接')
    if (type === 'postgresql') {
      const report = await testPostgreSQLConnections(config)
      pgTestReport.value = report
      pgTestDialog.value = true
      pgAccountResults.value = Object.fromEntries(report.results.filter(r => r.username !== 'postgres').map(r => [r.username, r]))
      if (report.results[0]?.success) {
        pgAccounts.value = report.accounts
        pgAccountsDiscovered.value = true
      }
      if (report.success && report.accounts.length > 0) ElMessage.success(report.message)
      else ElMessage.warning(report.message)
    } else {
      const res: any = await request.post('/settings/test-connection', { type, config })
      ElMessage.success(res.message || '连接成功')
    }
  } catch {
    // The interceptor displays the specific error; distinguish persisted
    // settings from a save failure without claiming the connection worked.
    if (saved) ElMessage.warning('设置已保存，连接测试未通过，请根据错误信息调整')
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
            :disabled="saving || !!testingType || !!pgAccountBusy"
            :loading="testingType === 'registry'"
            @click="saveAndTestConnection('registry')"
          >保存并测试</el-button>
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
            :disabled="saving || !!testingType || !!pgAccountBusy"
            :loading="testingType === 'k8s'"
            @click="saveAndTestConnection('k8s')"
          >保存并测试</el-button>
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
            :disabled="saving || !!testingType || !!pgAccountBusy"
            :loading="testingType === 'nacos'"
            @click="saveAndTestConnection('nacos')"
          >保存并测试</el-button>
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
            :disabled="saving || !!testingType || !!pgAccountBusy"
            :loading="testingType === 'skywalking'"
            @click="saveAndTestConnection('skywalking')"
          >保存并测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="OAP 地址">
          <el-input v-model="form.skywalking_oap_url" placeholder="192.168.1.150:11800" />
          <div class="form-hint">Java Agent 上报地址，多个地址用逗号分隔；测试仅检查第一个 OAP 地址。</div>
        </el-form-item>
        <el-form-item label="GraphQL 地址">
          <el-input v-model="form.skywalking_graphql_url" placeholder="如 http://192.168.1.154:28080/graphql" />
          <div class="form-hint">链路查询地址，供部署模板使用；当前连接测试不校验此地址。</div>
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
            :disabled="saving || !!testingType || !!pgAccountBusy"
            :loading="testingType === 'package-download'"
            @click="saveAndTestConnection('package-download')"
          >保存并测试</el-button>
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
            :disabled="saving || !!testingType || !!pgAccountBusy"
            :loading="testingType === 'postgresql'"
            @click="saveAndTestConnection('postgresql')"
          >保存并测试</el-button>
        </div>
      </template>
      <el-form label-width="160px">
        <el-form-item label="主机">
          <el-input v-model="form.postgresql_host" placeholder="如 192.168.1.100" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input v-model="form.postgresql_port" placeholder="5432" />
        </el-form-item>
        <el-form-item label="数据库名">
          <el-input v-model="form.postgresql_database" placeholder="数据库名" />
        </el-form-item>
        <el-form-item label="管理员密码">
          <el-input v-model="form.postgresql_admin_password" type="password" show-password placeholder="PostgreSQL 管理员密码" />
          <div class="form-hint">管理员账号固定为 postgres，用于实例查询、业务账号发现和视图依赖变更。</div>
        </el-form-item>
        <el-form-item label="业务用户密码">
          <el-radio-group v-model="form.postgresql_password_mode" @change="handlePostgreSQLPasswordMode" :disabled="testingType === 'postgresql' || !!pgAccountBusy">
            <el-radio value="shared">所有业务用户同一个密码</el-radio>
            <el-radio value="individual">业务用户密码不同</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item v-if="form.postgresql_password_mode === 'shared'" label="业务密码">
          <div style="display: flex; gap: 12px; width: 100%;">
            <el-input v-model="form.postgresql_password" type="password" show-password :disabled="!!pgAccountBusy" placeholder="df_ 业务账号共用的密码" />
            <el-button type="primary" :loading="pgAccountBusy === '__shared__'" :disabled="!!pgAccountBusy || !form.postgresql_password" @click="savePostgreSQLSharedPassword">保存并应用全部账号</el-button>
          </div>
          <div class="form-hint">一次保存，平台连接全部业务账号时使用此密码。</div>
        </el-form-item>
        <el-form-item v-if="form.postgresql_password_mode === 'individual'" label="业务账号">
          <el-button :disabled="testingType === 'postgresql' || !!pgAccountBusy" @click="openPostgreSQLPasswords">配置业务用户密码</el-button>
          <span class="form-hint" style="margin-left: 12px;">已配置 {{ pgAccounts.filter(account => account.hasOverride).length }} 个账号</span>
        </el-form-item>
      </el-form>
    </el-card>

    <el-dialog v-model="pgPasswordDialog" title="业务用户密码配置" width="1000px" style="max-width: calc(100vw - 48px);" :close-on-click-modal="!pgAccountBusy" :close-on-press-escape="!pgAccountBusy" :show-close="!pgAccountBusy" @closed="closePostgreSQLPasswords">
      <div class="section-header" style="margin-bottom: 14px;">
        <span class="form-hint">相同密码批量填入，不同密码单独修改，最后一次保存。</span>
        <el-button size="small" :loading="pgDiscovering" :disabled="!!pgAccountBusy" @click="loadPostgreSQLAccounts">刷新账号</el-button>
      </div>
      <div class="pg-password-tools">
        <el-input v-model="pgAccountSearch" clearable placeholder="搜索业务账号" style="width: 200px;" :disabled="!!pgAccountBusy" />
        <el-button :disabled="pgDiscovering || !!pgAccountBusy" @click="selectPostgreSQLAccounts()">全选</el-button>
        <el-button :disabled="pgDiscovering || !!pgAccountBusy" @click="selectPostgreSQLAccounts(true)">仅选未配置</el-button>
        <span class="form-hint">已选 {{ pgSelectedAccounts.length }} 个 · 待保存 {{ pgPendingCount }} 个</span>
      </div>
      <div class="pg-password-tools">
        <el-input v-model="pgBulkPassword" type="password" show-password placeholder="选中账号使用的相同密码" style="width: 320px;" :disabled="pgDiscovering || !!pgAccountBusy" autocomplete="new-password" />
        <el-button type="primary" :disabled="!pgSelectedAccounts.length || !pgBulkPassword || pgDiscovering || !!pgAccountBusy" @click="applyPostgreSQLBulkPassword">应用到选中账号（{{ pgSelectedAccounts.length }}）</el-button>
      </div>
      <el-table ref="pgAccountTable" :data="pgVisibleAccounts" row-key="username" @selection-change="pgSelectedAccounts = $event" v-loading="pgDiscovering" size="small" border max-height="420" empty-text="没有匹配的业务账号，请刷新账号或调整搜索">
        <el-table-column type="selection" width="44" :selectable="(row: PostgreSQLBusinessAccount) => row.discovered && !pgAccountBusy" />
        <el-table-column prop="username" label="业务账号" min-width="170">
          <template #default="{ row }">{{ row.username }}<el-tag v-if="pgAccountsDiscovered && !row.discovered" type="warning" size="small" style="margin-left: 6px;">未发现</el-tag></template>
        </el-table-column>
        <el-table-column label="密码状态" width="90"><template #default="{ row }"><el-tag :type="pgPasswordDrafts[row.username] ? 'primary' : row.hasOverride ? 'success' : 'warning'" size="small">{{ pgPasswordDrafts[row.username] ? '待保存' : row.hasOverride ? '已配置' : '未配置' }}</el-tag></template></el-table-column>
        <el-table-column label="密码" min-width="200"><template #default="{ row }"><el-input v-model="pgPasswordDrafts[row.username]" type="password" show-password size="small" :disabled="!!pgAccountBusy" :placeholder="row.hasOverride ? '留空保留，输入新密码修改' : '请输入该账号的密码'" autocomplete="new-password" /></template></el-table-column>
        <el-table-column label="连接结果" min-width="140" show-overflow-tooltip><template #default="{ row }"><span v-if="pgAccountResults[row.username]" :style="{ color: pgAccountResults[row.username]?.success ? '#67c23a' : '#f56c6c' }">{{ pgAccountResults[row.username]?.message }}</span><span v-else>未测试</span></template></el-table-column>
        <el-table-column label="操作" width="190"><template #default="{ row }">
          <el-button link type="primary" :disabled="!!pgAccountBusy || !pgPasswordDrafts[row.username]" @click="savePostgreSQLPassword(row)">保存</el-button>
          <el-button v-if="row.hasOverride" link :disabled="!!pgAccountBusy" @click="clearPostgreSQLPassword(row)">清除</el-button>
          <el-button link type="primary" :loading="pgAccountBusy === row.username" :disabled="!!pgAccountBusy || !row.hasOverride || !!pgPasswordDrafts[row.username]" @click="testPostgreSQLBusinessAccount(row)">测试</el-button>
        </template></el-table-column>
      </el-table>
      <template #footer>
        <span class="form-hint" style="margin-right: 16px;">未填写的账号保留已保存密码。</span>
        <el-button :disabled="!!pgAccountBusy" @click="pgPasswordDialog = false">关闭</el-button>
        <el-button type="primary" :loading="pgAccountBusy === '__save_all__'" :disabled="!!pgAccountBusy || pgDiscovering" @click="savePostgreSQLPasswordDialog">保存并关闭{{ pgPendingCount ? `（${pgPendingCount} 个）` : '' }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="pgTestDialog" title="PostgreSQL 连接测试结果" width="900px" style="max-width: calc(100vw - 48px);">
      <template v-if="pgTestReport">
        <el-alert :title="pgTestReport.message" :type="pgTestReport.success && pgTestReport.accounts.length ? 'success' : 'warning'" :closable="false" />
        <el-table :data="pgTestReport.results" size="small" max-height="420" style="margin-top: 14px;">
          <el-table-column prop="username" label="测试账号" width="190" />
          <el-table-column label="密码来源" width="120"><template #default="{ row }">{{ row.passwordSource === 'admin' ? '管理员密码' : row.passwordSource === 'override' ? '独立密码' : '统一业务密码' }}</template></el-table-column>
          <el-table-column prop="message" label="测试结果" />
        </el-table>
      </template>
      <template #footer><el-button @click="pgTestDialog = false">关闭</el-button></template>
    </el-dialog>

    <!-- Save Button -->
    <div class="save-bar">
      <el-button type="primary" :loading="saving" :disabled="!!testingType || !!pgAccountBusy" @click="saveSettings">保存</el-button>
    </div>
  </div>
</template>

<style scoped>
.pg-password-tools {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

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

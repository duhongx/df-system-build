<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getK8sOverview, getK8sNodes, getK8sDeployments, getK8sPods,
  getK8sServices, getK8sConfigMaps, getK8sIngresses, getK8sConfigMap, updateK8sConfigMap,
  getK8sPodLogs, restartK8sDeployment,
  updateK8sImage, getK8sTopNodes, getK8sTopPods,
  updateK8sServicePorts, deleteK8sService, getK8sImageTags,
  syncK8sDeploymentRuntimeVersions,
  type DeploymentRuntimeVersion,
} from '../api/kubernetes'
import { useSettingsStore } from '../stores/settings'
import DeploymentConfigDialog from '../components/DeploymentConfigDialog.vue'

const route = useRoute()
const settingsStore = useSettingsStore()
const activeTab = computed(() => (route.meta?.k8sTab as string) || 'overview')
const namespace = computed(() => settingsStore.k8sNamespace)
const initialized = ref(false)
const loading = ref(false)

// Overview
const overview = ref<any>(null)

// Deployments
const deployments = ref<any[]>([])
const deploymentRuntimeVersions = ref<DeploymentRuntimeVersion[]>([])
const versionSyncing = ref('')
const runtimeVersionDialogVisible = ref(false)
const runtimeVersionDialogTitle = ref('')
const runtimeVersionDialogDetails = ref<RuntimeVersionDetail[]>([])
const runtimeVersionDialogJson = ref('')

// Pods
const pods = ref<any[]>([])

// Services
const services = ref<any[]>([])

// Nodes (wide)
const nodes = ref<any[]>([])

// ConfigMaps
const configmaps = ref<any[]>([])

// Ingresses
const ingresses = ref<any[]>([])

// Logs
const logDialogVisible = ref(false)
const logContent = ref('')
const logPodName = ref('')

// Top metrics
const topNodes = ref<any[]>([])
const topPods = ref<any[]>([])

// Image dialog (rollback)
const imageDialogVisible = ref(false)
const imageForm = ref({ name: '', image: '', container: '' })
const imageList = ref<string[]>([])
const imageLoading = ref(false)

// Deployment configuration
const configVisible = ref(false)
const configName = ref('')

// Service edit dialog
const svcDialogVisible = ref(false)
const svcForm = ref<{ name: string; type: string; ports: { port: number; targetPort: number; nodePort: number; protocol: string }[] }>({ name: '', type: 'ClusterIP', ports: [] })

// ConfigMap edit dialog
const cmDialogVisible = ref(false)
const cmName = ref('')
const cmData = ref<Record<string, string>>({})
const cmEditKey = ref('')
const cmLoading = ref(false)

onMounted(async () => {
  await settingsStore.fetchSettings()
  initialized.value = true
  await loadTab()
})

watch(namespace, () => {
  configVisible.value = false
  deployments.value = []; deploymentRuntimeVersions.value = []; pods.value = []
  services.value = []; configmaps.value = []; ingresses.value = []; overview.value = null
  if (initialized.value) void loadTab()
})
watch(activeTab, () => { configVisible.value = false; if (initialized.value) void loadTab() })

async function refreshPage() {
  await settingsStore.fetchSettings()
  await loadTab()
}

let loadSequence = 0
async function loadTab() {
  const ticket = ++loadSequence, ns = namespace.value, tab = activeTab.value
  const current = () => ticket === loadSequence && ns === namespace.value && tab === activeTab.value
  loading.value = true
  try {
    switch (tab) {
      case 'overview': {
        const data = await getK8sOverview(ns)
        if (!current()) return
        overview.value = data
        try { const top = await getK8sTopNodes(); if (current()) topNodes.value = top } catch { if (current()) topNodes.value = [] }
        break
      }
      case 'nodes': {
        const data = await getK8sNodes()
        if (!current()) return
        nodes.value = data
        try { const top = await getK8sTopNodes(); if (current()) topNodes.value = top } catch { if (current()) topNodes.value = [] }
        break
      }
      case 'deployments': {
        const data = await getK8sDeployments(ns)
        if (!current()) return
        deployments.value = data?.items || []
        deploymentRuntimeVersions.value = data?.runtimeVersions || []
        break
      }
      case 'pods': {
        const data = await getK8sPods(ns)
        if (!current()) return
        pods.value = data?.items || []
        try { const top = await getK8sTopPods(ns); if (current()) topPods.value = top } catch { if (current()) topPods.value = [] }
        break
      }
      case 'services': {
        const data = await getK8sServices(ns)
        if (current()) services.value = data?.items || []
        break
      }
      case 'configmaps': {
        const data = await getK8sConfigMaps(ns)
        if (current()) configmaps.value = data?.items || []
        break
      }
      case 'ingresses': {
        const data = await getK8sIngresses(ns)
        if (current()) ingresses.value = data?.items || []
        break
      }
    }
  } catch { /* handled by request interceptor */ }
  finally { if (current()) loading.value = false }
}

async function handleRestart(name: string) {
  await ElMessageBox.confirm(`确定重启 Deployment "${name}" 吗？`, '确认重启', { type: 'warning' })
  try {
    await restartK8sDeployment(name, namespace.value)
    ElMessage.success('重启命令已发送')
    setTimeout(loadTab, 2000)
  } catch (e) { /* handled */ }
}


async function handleViewLogs(podName: string) {
  logPodName.value = podName
  logContent.value = '加载中...'
  logDialogVisible.value = true
  try {
    const result = await getK8sPodLogs(podName, namespace.value, undefined, 200)
    logContent.value = result.logs || '(无日志)'
  } catch (e: any) {
    logContent.value = '获取日志失败: ' + (e?.message || '')
  }
}

function handleUpdateImage(dep: any) {
  const name = dep.metadata?.name
  const currentImage = dep.spec?.template?.spec?.containers?.[0]?.image || ''
  imageForm.value = { name, image: currentImage, container: name }
  imageList.value = []
  imageDialogVisible.value = true
  // Load available tags from Nexus
  imageLoading.value = true
  getK8sImageTags(name, namespace.value).then((data) => {
    imageList.value = data.images || []
    imageLoading.value = false
  }).catch(() => {
    imageList.value = []
    imageLoading.value = false
  })
}

async function handleSubmitImage() {
  if (!imageForm.value.image.trim()) { ElMessage.warning('请输入镜像地址'); return }
  try {
    await updateK8sImage(imageForm.value.name, namespace.value, imageForm.value.image, imageForm.value.container)
    ElMessage.success('镜像已更新')
    imageDialogVisible.value = false
    setTimeout(loadTab, 2000)
  } catch (e) { /* handled */ }
}

function handleOpenConfig(dep: any) {
  configName.value = dep.metadata?.name || ''
  configVisible.value = true
}

function getPodMetrics(podName: string) {
  return topPods.value.find((p: any) => p.name === podName)
}

function handleEditService(svc: any) {
  const ports = (svc.spec?.ports || []).map((p: any) => ({
    port: p.port || 0,
    targetPort: p.targetPort || p.port || 0,
    nodePort: p.nodePort || 0,
    protocol: p.protocol || 'TCP',
  }))
  svcForm.value = {
    name: svc.metadata?.name,
    type: svc.spec?.type || 'ClusterIP',
    ports,
  }
  svcDialogVisible.value = true
}

async function handleSubmitService() {
  try {
    await updateK8sServicePorts(svcForm.value.name, namespace.value, {
      type: svcForm.value.type,
      ports: svcForm.value.ports,
    })
    ElMessage.success('Service 已更新')
    svcDialogVisible.value = false
    setTimeout(loadTab, 1000)
  } catch (e) { /* handled */ }
}

async function handleDeleteService(name: string) {
  await ElMessageBox.confirm(`确定删除 Service "${name}" 吗？此操作不可恢复`, '确认删除', { type: 'warning' })
  try {
    await deleteK8sService(name, namespace.value)
    ElMessage.success('Service 已删除')
    setTimeout(loadTab, 1000)
  } catch (e) { /* handled */ }
}

async function handleEditConfigMap(cm: any) {
  const name = cm.metadata?.name
  cmName.value = name
  cmData.value = {}
  cmEditKey.value = ''
  cmDialogVisible.value = true
  cmLoading.value = true
  try {
    const result = await getK8sConfigMap(name, namespace.value)
    cmData.value = result?.data || {}
    // Auto-select first key
    const keys = Object.keys(cmData.value)
    if (keys.length > 0) cmEditKey.value = keys[0]
  } catch (e) { /* handled */ }
  finally { cmLoading.value = false }
}

async function handleSaveConfigMap() {
  try {
    await updateK8sConfigMap(cmName.value, namespace.value, cmData.value)
    ElMessage.success('ConfigMap 已保存')
    cmDialogVisible.value = false
    setTimeout(loadTab, 1000)
  } catch (e) { /* handled */ }
}

function podStatusType(phase: string) {
  if (phase === 'Running') return 'success'
  if (phase === 'Succeeded') return 'success'
  if (phase === 'Pending') return 'warning'
  if (phase === 'Failed') return 'danger'
  return 'info'
}

function depReady(dep: any) {
  const ready = dep.status?.readyReplicas || 0
  const desired = dep.spec?.replicas || 0
  return `${ready}/${desired}`
}

function runtimeVersionsForDeployment(deploymentName: string) {
  return deploymentRuntimeVersions.value.filter(item => item.deploymentName === deploymentName)
}

function runtimeVersionStatusType(status?: string) {
  if (status === 'synced') return 'success'
  if (status === 'failed') return 'danger'
  return 'info'
}

function runtimeVersionStatusText(status?: string) {
  if (status === 'synced') return '已采集'
  if (status === 'failed') return '失败'
  return status || '未采集'
}

type RuntimeVersionDetail = {
  label: string
  value: string
}

function parseRuntimeVersionJson(value?: string) {
  if (!value) return null
  try {
    return JSON.parse(value)
  } catch (e) {
    return null
  }
}

function versionText(value: unknown) {
  if (value === undefined || value === null || value === '') return '-'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

function runtimeCommitId(data: any) {
  const idValue = data?.git?.commit?.id
  if (typeof idValue === 'object') return idValue.abbrev || idValue.full || '-'
  return idValue || data?.git?.commit?.id?.abbrev || data?.commit || '-'
}

function formatRuntimeVersionDetails(value?: string): RuntimeVersionDetail[] {
  const data = parseRuntimeVersionJson(value)
  if (!data || typeof data !== 'object') {
    return [{ label: '原始值', value: value || '-' }]
  }
  if (data.git) {
    return [
      { label: 'branch', value: versionText(data.git.branch) },
      { label: 'commit.id', value: versionText(runtimeCommitId(data)) },
      { label: 'commit.time', value: versionText(data.git.commit?.time) },
    ]
  }
  const preferredKeys = ['xiTongId', 'xiTongMc', 'homePath', 'version', 'date', 'branch', 'commit']
  const details = preferredKeys
    .filter(key => data[key] !== undefined)
    .map(key => ({ label: key, value: versionText(data[key]) }))
  if (data.position !== undefined) {
    details.push({ label: 'position', value: versionText(data.position) })
  }
  return details.length ? details : Object.keys(data).map(key => ({ label: key, value: versionText(data[key]) }))
}

function formatRuntimeVersionJson(value?: string) {
  if (!value) return '-'
  const data = parseRuntimeVersionJson(value)
  if (!data) return value
  return JSON.stringify(data, null, 2)
}

function runtimeVersionParts(value?: string) {
  const data = parseRuntimeVersionJson(value)
  if (!data || typeof data !== 'object') {
    return { version: value || '-', branch: '-', commit: '-', time: '-' }
  }
  if (data.git) {
    return {
      version: versionText(data.version),
      branch: versionText(data.git.branch),
      commit: versionText(runtimeCommitId(data)),
      time: versionText(data.git.commit?.time),
    }
  }
  return {
    version: versionText(data.version),
    branch: versionText(data.branch),
    commit: versionText(data.commit),
    time: versionText(data.date),
  }
}

function runtimeAppIdentifier(versionRow: DeploymentRuntimeVersion | undefined, deploymentName: string) {
  if (!versionRow) return deploymentName
  if (deploymentName !== 'web-main') return deploymentName
  if (versionRow.appName === 'web-main' || versionRow.vueRole === 'main') return 'web-main'
  const pathMatch = versionRow.runtimeVersionPath?.match(/\/apps\/([^/]+)\/config\.json$/)
  return versionRow.appCode || pathMatch?.[1] || versionRow.appName || '-'
}

function sortedRuntimeVersions(deploymentName: string) {
  return runtimeVersionsForDeployment(deploymentName).slice().sort((a, b) => {
    const aId = runtimeAppIdentifier(a, deploymentName)
    const bId = runtimeAppIdentifier(b, deploymentName)
    if (aId === 'web-main') return -1
    if (bId === 'web-main') return 1
    return aId.localeCompare(bId, 'zh-Hans-CN', { numeric: true })
  })
}

type DeploymentTableRow = {
  rowKey: string
  deployment: any
  deploymentName: string
  runtimeVersion?: DeploymentRuntimeVersion
  appIdentifier: string
  businessAppName: string
  version: string
  branch: string
  commit: string
  versionTime: string
}

const deploymentTableRows = computed<DeploymentTableRow[]>(() => deployments.value.flatMap((deployment) => {
  const deploymentName = deployment.metadata?.name || '-'
  const versions = sortedRuntimeVersions(deploymentName)
  if (!versions.length) {
    return [{
      rowKey: `deployment-${deploymentName}`,
      deployment,
      deploymentName,
      appIdentifier: deploymentName,
      businessAppName: '-',
      version: '未采集',
      branch: '-',
      commit: '-',
      versionTime: '-',
    }]
  }
  const visibleVersions = deploymentName === 'web-main'
    ? versions
    : [versions.find(item => item.appName === deploymentName) || versions[0]]
  return visibleVersions.map((runtimeVersion) => {
    const parts = runtimeVersionParts(runtimeVersion.businessVersionJson)
    return {
      rowKey: `deployment-${deploymentName}-${runtimeVersion.id || runtimeVersion.appName || runtimeVersion.runtimeVersionPath}`,
      deployment,
      deploymentName,
      runtimeVersion,
      appIdentifier: runtimeAppIdentifier(runtimeVersion, deploymentName),
      businessAppName: runtimeVersion.appName || '-',
      version: parts.version,
      branch: parts.branch,
      commit: parts.commit,
      versionTime: parts.time,
    }
  })
}))

const deploymentMergedColumnIndexes = new Set([0, 1, 2, 10, 11])

function businessAppDisplay(row: DeploymentTableRow) {
  if (row.deploymentName !== 'web-main') {
    return row.businessAppName && row.businessAppName !== '-' ? row.businessAppName : row.deploymentName
  }
  if (row.appIdentifier === 'web-main') return 'web-main'
  if (!row.businessAppName || row.businessAppName === '-') return row.appIdentifier
  return `${row.appIdentifier} / ${row.businessAppName}`
}

function deploymentTableSpanMethod({ rowIndex, columnIndex }: { rowIndex: number; columnIndex: number }) {
  if (!deploymentMergedColumnIndexes.has(columnIndex)) return { rowspan: 1, colspan: 1 }
  const rows = deploymentTableRows.value
  const current = rows[rowIndex]
  if (!current) return { rowspan: 1, colspan: 1 }
  if (rowIndex > 0 && rows[rowIndex - 1]?.deploymentName === current.deploymentName) {
    return { rowspan: 0, colspan: 0 }
  }
  let rowspan = 1
  for (let i = rowIndex + 1; i < rows.length; i++) {
    if (rows[i].deploymentName !== current.deploymentName) break
    rowspan++
  }
  return { rowspan, colspan: 1 }
}

function deploymentRowClassName({ row }: { row: DeploymentTableRow }) {
  if (row.deploymentName !== 'web-main') return ''
  return row.appIdentifier === 'web-main' ? 'web-main-main-row' : 'web-main-child-row'
}

function openRuntimeVersionDialog(row: DeploymentTableRow) {
  if (!row.runtimeVersion) return
  runtimeVersionDialogTitle.value = `${row.deploymentName} / ${row.appIdentifier} / ${row.businessAppName}`
  runtimeVersionDialogDetails.value = formatRuntimeVersionDetails(row.runtimeVersion.businessVersionJson)
  runtimeVersionDialogJson.value = formatRuntimeVersionJson(row.runtimeVersion.businessVersionJson)
  runtimeVersionDialogVisible.value = true
}

async function handleSyncRuntimeVersions(deploymentName?: string) {
  const syncKey = deploymentName || '__all__'
  versionSyncing.value = syncKey
  try {
    const targets = deploymentName ? [deploymentName] : deployments.value.map(dep => dep.metadata?.name).filter(Boolean)
    const result = await syncK8sDeploymentRuntimeVersions(namespace.value, targets)
    deploymentRuntimeVersions.value = result.runtimeVersions || []
    ElMessage.success('运行版本已同步')
  } finally {
    versionSyncing.value = ''
  }
}
</script>

<template>
  <div class="k8s-page" :class="{ 'resource-list-page': ['deployments', 'pods', 'services', 'configmaps', 'ingresses'].includes(activeTab) }">
    <!-- Header -->
    <div class="k8s-header">
      <div class="header-left">
        <h4 class="page-title">Kubernetes 管理</h4>
        <el-tag effect="plain">{{ namespace }}</el-tag>
      </div>
      <div class="header-actions">
        <el-button
          v-if="activeTab === 'deployments'"
          size="small"
          type="primary"
          plain
          :loading="versionSyncing === '__all__'"
          @click="handleSyncRuntimeVersions()"
        >
          同步版本
        </el-button>
        <el-button size="small" @click="refreshPage" :loading="loading"><el-icon><Refresh /></el-icon>刷新</el-button>
      </div>
    </div>

    <!-- Content based on route -->
    <div class="content-area" v-loading="loading">
      <!-- Overview -->
      <div v-if="activeTab === 'overview'" class="overview-cards">
        <template v-if="overview">
          <div class="ov-card">
            <div class="ov-num">{{ overview.nodeCount }}</div>
            <div class="ov-label">节点</div>
          </div>
          <div class="ov-card">
            <div class="ov-num">{{ overview.deploymentCount }}</div>
            <div class="ov-label">Deployments</div>
          </div>
          <div class="ov-card">
            <div class="ov-num">{{ overview.podRunning }} / {{ overview.podCount }}</div>
            <div class="ov-label">Pods (Running/Total)</div>
          </div>
          <div class="ov-card">
            <div class="ov-num">{{ overview.serviceCount }}</div>
            <div class="ov-label">Services</div>
          </div>
        </template>
      </div>
      <div v-if="activeTab === 'overview' && topNodes.length" class="section-card" style="margin-top: 16px;">
        <h4 class="section-title">节点资源使用 (metrics-server)</h4>
        <el-table :data="topNodes" stripe size="small" border>
          <el-table-column prop="name" label="节点" min-width="180" />
          <el-table-column prop="cpuUsage" label="CPU 使用" width="120" />
          <el-table-column prop="memUsage" label="内存使用" width="120" />
        </el-table>
      </div>

      <!-- Deployments -->
      <div v-if="activeTab === 'deployments'" class="resource-table">
        <el-table
          :data="deploymentTableRows"
          height="100%"
          stripe
          size="small"
          border
          table-layout="fixed"
          row-key="rowKey"
          :span-method="deploymentTableSpanMethod"
          :row-class-name="deploymentRowClassName"
        >
          <el-table-column label="Deployment" min-width="180">
            <template #default="{ row }">
              <strong class="deployment-name">{{ row.deploymentName }}</strong>
            </template>
          </el-table-column>
          <el-table-column label="就绪" width="100">
            <template #default="{ row }">
              <el-tag :type="(row.deployment.status?.readyReplicas || 0) === (row.deployment.spec?.replicas || 0) ? 'success' : 'warning'" size="small">
                {{ depReady(row.deployment) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="镜像" min-width="300">
            <template #default="{ row }">
              <span class="image-text">{{ row.deployment.spec?.template?.spec?.containers?.[0]?.image || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="业务应用" min-width="220" show-overflow-tooltip>
            <template #default="{ row }">
              <span
                :class="[
                  'business-app-name',
                  row.deploymentName === 'web-main' && row.appIdentifier !== 'web-main' ? 'web-main-child-app' : '',
                ]"
              >
                {{ businessAppDisplay(row) }}
              </span>
            </template>
          </el-table-column>
          <el-table-column prop="version" label="版本" width="110" show-overflow-tooltip />
          <el-table-column prop="branch" label="分支" min-width="180" show-overflow-tooltip />
          <el-table-column prop="commit" label="Commit" min-width="130" show-overflow-tooltip />
          <el-table-column prop="versionTime" label="版本时间" min-width="160" show-overflow-tooltip />
          <el-table-column label="采集状态" width="100">
            <template #default="{ row }">
              <el-tag v-if="row.runtimeVersion" :type="runtimeVersionStatusType(row.runtimeVersion.status)" size="small">
                {{ runtimeVersionStatusText(row.runtimeVersion.status) }}
              </el-tag>
              <span v-else class="runtime-version-empty">未采集</span>
            </template>
          </el-table-column>
          <el-table-column label="版本数据" width="100">
            <template #default="{ row }">
              <el-button
                v-if="row.runtimeVersion"
                type="primary"
                link
                size="small"
                @click="openRuntimeVersionDialog(row)"
              >
                完整数据
              </el-button>
              <span v-else>-</span>
            </template>
          </el-table-column>
          <el-table-column label="创建时间" width="170">
            <template #default="{ row }">{{ row.deployment.metadata?.creationTimestamp?.replace('T', ' ').replace('Z', '') || '-' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="270" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleRestart(row.deploymentName)">重启</el-button>
              <el-button type="primary" link size="small" @click="handleUpdateImage(row.deployment)">回滚</el-button>
              <el-button type="primary" link size="small" @click="handleOpenConfig(row.deployment)">配置</el-button>
              <el-button
                type="primary"
                link
                size="small"
                :loading="versionSyncing === row.deploymentName"
                @click="handleSyncRuntimeVersions(row.deploymentName)"
              >
                同步版本
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Pods -->
      <div v-if="activeTab === 'pods'" class="resource-table">
        <el-table height="100%" :data="pods" stripe size="small" border>
          <el-table-column label="名称" min-width="280">
            <template #default="{ row }">{{ row.metadata?.name }}</template>
          </el-table-column>
          <el-table-column label="状态" width="100">
            <template #default="{ row }">
              <el-tag :type="podStatusType(row.status?.phase)" size="small">{{ row.status?.phase }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="重启次数" width="90">
            <template #default="{ row }">{{ row.status?.containerStatuses?.[0]?.restartCount || 0 }}</template>
          </el-table-column>
          <el-table-column label="节点" width="150">
            <template #default="{ row }">{{ row.spec?.nodeName || '-' }}</template>
          </el-table-column>
          <el-table-column label="IP" width="130">
            <template #default="{ row }">{{ row.status?.podIP || '-' }}</template>
          </el-table-column>
          <el-table-column label="CPU" width="80">
            <template #default="{ row }">{{ getPodMetrics(row.metadata?.name)?.cpuUsage || '-' }}</template>
          </el-table-column>
          <el-table-column label="内存" width="90">
            <template #default="{ row }">{{ getPodMetrics(row.metadata?.name)?.memUsage || '-' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="100" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleViewLogs(row.metadata?.name)">日志</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Services -->
      <div v-if="activeTab === 'services'" class="resource-table">
        <el-table height="100%" :data="services" stripe size="small" border>
          <el-table-column label="名称" min-width="200">
            <template #default="{ row }">{{ row.metadata?.name }}</template>
          </el-table-column>
          <el-table-column label="类型" width="100">
            <template #default="{ row }">{{ row.spec?.type }}</template>
          </el-table-column>
          <el-table-column label="ClusterIP" width="140">
            <template #default="{ row }">{{ row.spec?.clusterIP }}</template>
          </el-table-column>
          <el-table-column label="端口" min-width="200">
            <template #default="{ row }">
              <span v-for="(p, i) in (row.spec?.ports || [])" :key="i" style="margin-right: 8px;">
                {{ p.port }}{{ p.nodePort ? ':' + p.nodePort : '' }}/{{ p.protocol }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="140" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleEditService(row)">修改</el-button>
              <el-button type="danger" link size="small" @click="handleDeleteService(row.metadata?.name)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Nodes -->
      <div v-if="activeTab === 'nodes'">
        <el-table :data="nodes" stripe size="small" border>
          <el-table-column prop="name" label="名称" min-width="180" />
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="row.status === 'Ready' ? 'success' : 'danger'" size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="roles" label="角色" width="120" />
          <el-table-column prop="internalIP" label="内网 IP" width="140" />
          <el-table-column prop="version" label="版本" width="120" />
          <el-table-column prop="os" label="系统" width="140" />
          <el-table-column prop="runtime" label="容器运行时" width="160" />
          <el-table-column prop="age" label="运行时长" width="100" />
        </el-table>
        <div v-if="topNodes.length" style="margin-top: 16px;">
          <h4 style="font-size: 14px; font-weight: 600; margin: 0 0 8px 0;">资源使用 (metrics-server)</h4>
          <el-table :data="topNodes" stripe size="small" border>
            <el-table-column prop="name" label="节点" min-width="180" />
            <el-table-column prop="cpuUsage" label="CPU 使用" width="120" />
            <el-table-column prop="memUsage" label="内存使用" width="120" />
          </el-table>
        </div>
      </div>

      <!-- ConfigMaps -->
      <div v-if="activeTab === 'configmaps'" class="resource-table">
        <el-table height="100%" :data="configmaps" stripe size="small" border>
          <el-table-column label="名称" min-width="250">
            <template #default="{ row }">{{ row.metadata?.name }}</template>
          </el-table-column>
          <el-table-column label="数据条目" width="100">
            <template #default="{ row }">{{ Object.keys(row.data || {}).length }}</template>
          </el-table-column>
          <el-table-column label="创建时间" width="180">
            <template #default="{ row }">{{ row.metadata?.creationTimestamp?.replace('T', ' ').replace('Z', '') || '-' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="100" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleEditConfigMap(row)">编辑</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Ingresses -->
      <div v-if="activeTab === 'ingresses'" class="resource-table">
        <el-table height="100%" :data="ingresses" stripe size="small" border>
          <el-table-column label="名称" min-width="200">
            <template #default="{ row }">{{ row.metadata?.name }}</template>
          </el-table-column>
          <el-table-column label="Hosts" min-width="250">
            <template #default="{ row }">
              <span v-for="(rule, i) in (row.spec?.rules || [])" :key="i" style="margin-right: 8px;">
                {{ rule.host || '*' }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="路径" min-width="200">
            <template #default="{ row }">
              <template v-for="rule in (row.spec?.rules || [])" :key="rule.host">
                <span v-for="(path, i) in (rule.http?.paths || [])" :key="i" style="margin-right: 8px;">
                  {{ path.path || '/' }}→{{ path.backend?.service?.name }}:{{ path.backend?.service?.port?.number }}
                </span>
              </template>
            </template>
          </el-table-column>
          <el-table-column label="创建时间" width="180">
            <template #default="{ row }">{{ row.metadata?.creationTimestamp?.replace('T', ' ').replace('Z', '') || '-' }}</template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <!-- Log Dialog -->
    <el-dialog v-model="logDialogVisible" :title="`Pod 日志: ${logPodName}`" width="80%" top="5vh">
      <pre class="log-content">{{ logContent }}</pre>
    </el-dialog>

    <!-- Image Dialog (Rollback) -->
    <el-dialog v-model="imageDialogVisible" title="版本回退" width="650px">
      <el-form :model="imageForm" label-width="100px">
        <el-form-item label="Deployment">
          <el-input :model-value="imageForm.name" disabled />
        </el-form-item>
        <el-form-item label="选择版本" v-loading="imageLoading">
          <el-select v-model="imageForm.image" style="width: 100%;" filterable placeholder="选择要回退的镜像版本">
            <el-option v-for="img in imageList" :key="img" :label="img" :value="img" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="imageDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSubmitImage">确定</el-button>
      </template>
    </el-dialog>

    <DeploymentConfigDialog v-model="configVisible" :name="configName" :namespace="namespace" @updated="loadTab" />

    <!-- Service Edit Dialog -->
    <el-dialog v-model="svcDialogVisible" title="修改 Service" width="600px">
      <el-form :model="svcForm" label-width="100px">
        <el-form-item label="Service">
          <el-input :model-value="svcForm.name" disabled />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="svcForm.type">
            <el-radio value="ClusterIP">ClusterIP</el-radio>
            <el-radio value="NodePort">NodePort</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="端口映射">
          <div style="width: 100%;">
            <div v-for="(p, idx) in svcForm.ports" :key="idx" style="display: flex; gap: 8px; margin-bottom: 8px; align-items: center;">
              <el-input-number v-model="p.port" :min="1" :max="65535" placeholder="容器端口" controls-position="right" style="width: 120px;" />
              <span style="color: #909399;">→</span>
              <el-input-number v-model="p.targetPort" :min="1" :max="65535" placeholder="目标端口" controls-position="right" style="width: 120px;" />
              <template v-if="svcForm.type === 'NodePort'">
                <span style="color: #909399;">:</span>
                <el-input-number v-model="p.nodePort" :min="0" :max="65535" placeholder="NodePort" controls-position="right" style="width: 120px;" />
              </template>
              <el-button type="danger" link @click="svcForm.ports.splice(idx, 1)"><el-icon><Delete /></el-icon></el-button>
            </div>
            <el-button size="small" @click="svcForm.ports.push({ port: 8080, targetPort: 8080, nodePort: 0, protocol: 'TCP' })">
              <el-icon><Plus /></el-icon>添加端口
            </el-button>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="svcDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSubmitService">确定</el-button>
      </template>
    </el-dialog>

    <!-- ConfigMap Edit Dialog -->
    <el-dialog v-model="cmDialogVisible" :title="`编辑 ConfigMap: ${cmName}`" width="80%" top="5vh">
      <div v-loading="cmLoading">
        <div v-if="Object.keys(cmData).length > 1" style="margin-bottom: 12px;">
          <el-radio-group v-model="cmEditKey" size="small">
            <el-radio-button v-for="key in Object.keys(cmData)" :key="key" :value="key">{{ key }}</el-radio-button>
          </el-radio-group>
        </div>
        <el-input
          v-if="cmEditKey && cmData[cmEditKey] !== undefined"
          v-model="cmData[cmEditKey]"
          type="textarea"
          :rows="22"
          class="code-editor"
        />
      </div>
      <template #footer>
        <el-button @click="cmDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSaveConfigMap">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="runtimeVersionDialogVisible" :title="runtimeVersionDialogTitle || '完整版本数据'" width="760px">
      <div class="runtime-dialog-grid">
        <div v-for="detail in runtimeVersionDialogDetails" :key="detail.label">
          <span>{{ detail.label }}</span>
          <strong>{{ detail.value }}</strong>
        </div>
      </div>
      <div class="runtime-version-json-title">完整版本数据</div>
      <pre class="runtime-version-json">{{ runtimeVersionDialogJson }}</pre>
    </el-dialog>
  </div>
</template>

<style scoped>
.resource-list-page { flex: 1; min-height: 0; }
.resource-list-page .content-area { flex: 1; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
.resource-table { flex: 1; min-height: 0; }
.k8s-page { display: flex; flex-direction: column; gap: 12px; }
.k8s-header {
  display: flex; align-items: center; justify-content: space-between;
  background: #fff; padding: 12px 16px; border-radius: 6px; border: 1px solid #ebeef5;
}
.header-left { display: flex; align-items: center; gap: 16px; }
.header-actions { display: flex; align-items: center; gap: 8px; }
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0; }

.content-area {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  padding: 16px;
  min-height: 300px;
}

.overview-cards { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.ov-card { text-align: center; padding: 24px; background: #f9fafb; border-radius: 8px; }
.ov-num { font-size: 28px; font-weight: 700; color: #303133; }
.ov-label { font-size: 13px; color: #909399; margin-top: 4px; }

.section-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
.section-title { font-size: 14px; font-weight: 600; color: #303133; margin: 0 0 12px 0; }

.deployment-name {
  color: #1f2937;
  font-weight: 600;
}

.image-text {
  color: #606266;
  font-size: 12px;
  line-height: 1.4;
  word-break: break-all;
}

.business-app-name {
  display: inline-flex;
  align-items: center;
  color: #303133;
  font-weight: 500;
}

.business-app-name.web-main-child-app {
  padding-left: 12px;
  color: #606266;
}

.runtime-version-empty {
  color: #c0c4cc;
  font-size: 12px;
}

.runtime-dialog-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px 12px;
}

.runtime-dialog-grid > div {
  min-width: 0;
  border: 1px solid #f0f2f5;
  background: #fafafa;
  padding: 6px 8px;
}

.runtime-dialog-grid span {
  display: block;
  color: #8a9099;
  font-size: 11px;
}

.runtime-dialog-grid strong {
  display: block;
  margin-top: 3px;
  color: #303133;
  font-size: 12px;
  line-height: 1.4;
  word-break: break-all;
}

.runtime-version-json-title {
  margin-top: 10px;
  color: #606266;
  font-size: 12px;
  font-weight: 600;
}

.runtime-version-json {
  margin: 6px 0 0;
  max-height: 180px;
  overflow: auto;
  padding: 8px;
  background: #f7f8fa;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  color: #374151;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
}

:deep(.web-main-main-row) td {
  background: #f5f9ff !important;
}

:deep(.web-main-child-row) td {
  background: #fbfdff !important;
}

.log-content {
  background: #1e1e1e; color: #d4d4d4; padding: 16px; border-radius: 6px;
  font-family: 'JetBrains Mono', 'Consolas', monospace; font-size: 12px;
  max-height: 60vh; overflow: auto; white-space: pre-wrap; word-break: break-all;
}

:deep(.code-editor textarea) {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 13px;
  line-height: 1.5;
}
</style>

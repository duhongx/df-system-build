<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getK8sOverview, getK8sNamespaces, getK8sNodes, getK8sDeployments, getK8sPods,
  getK8sServices, getK8sConfigMaps, getK8sIngresses, getK8sConfigMap, updateK8sConfigMap,
  getK8sPodLogs, restartK8sDeployment, scaleK8sDeployment,
  updateK8sImage, updateK8sResources, getK8sTopNodes, getK8sTopPods,
  updateK8sServicePorts, deleteK8sService, getK8sImageTags
} from '../api/kubernetes'

const route = useRoute()
const activeTab = computed(() => (route.meta?.k8sTab as string) || 'overview')
const namespace = ref('default')
const namespaces = ref<string[]>([])
const loading = ref(false)

// Overview
const overview = ref<any>(null)

// Deployments
const deployments = ref<any[]>([])

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

// Resources dialog
const resourceDialogVisible = ref(false)
const resourceForm = ref({ name: '', container: '', cpuRequest: '', cpuLimit: '', memoryRequest: '', memoryLimit: '' })

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
  try {
    namespaces.value = await getK8sNamespaces()
  } catch (e) {
    namespaces.value = ['default']
  }
  await loadTab()
})

watch(namespace, () => loadTab())
watch(activeTab, () => loadTab())

async function loadTab() {
  loading.value = true
  try {
    switch (activeTab.value) {
      case 'overview':
        overview.value = await getK8sOverview(namespace.value)
        try { topNodes.value = await getK8sTopNodes() } catch (e) { topNodes.value = [] }
        break
      case 'nodes':
        nodes.value = await getK8sNodes()
        try { topNodes.value = await getK8sTopNodes() } catch (e) { topNodes.value = [] }
        break
      case 'deployments':
        const depData = await getK8sDeployments(namespace.value)
        deployments.value = depData?.items || []
        break
      case 'pods':
        const podData = await getK8sPods(namespace.value)
        pods.value = podData?.items || []
        try { topPods.value = await getK8sTopPods(namespace.value) } catch (e) { topPods.value = [] }
        break
      case 'services':
        const svcData = await getK8sServices(namespace.value)
        services.value = svcData?.items || []
        break
      case 'configmaps':
        const cmData = await getK8sConfigMaps(namespace.value)
        configmaps.value = cmData?.items || []
        break
      case 'ingresses':
        const ingData = await getK8sIngresses(namespace.value)
        ingresses.value = ingData?.items || []
        break
    }
  } catch (e) { /* handled */ }
  finally { loading.value = false }
}

async function handleRestart(name: string) {
  await ElMessageBox.confirm(`确定重启 Deployment "${name}" 吗？`, '确认重启', { type: 'warning' })
  try {
    await restartK8sDeployment(name, namespace.value)
    ElMessage.success('重启命令已发送')
    setTimeout(loadTab, 2000)
  } catch (e) { /* handled */ }
}

async function handleScale(name: string, current: number) {
  const { value } = await ElMessageBox.prompt('请输入副本数', '扩缩容', {
    confirmButtonText: '确定', inputValue: String(current), inputPattern: /^\d+$/, inputErrorMessage: '请输入数字'
  })
  if (value === undefined) return
  try {
    await scaleK8sDeployment(name, namespace.value, Number(value))
    ElMessage.success('扩缩容成功')
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

function handleUpdateResources(dep: any) {
  const name = dep.metadata?.name
  const container = dep.spec?.template?.spec?.containers?.[0]
  const res = container?.resources || {}
  resourceForm.value = {
    name,
    container: container?.name || name,
    cpuRequest: res.requests?.cpu || '',
    cpuLimit: res.limits?.cpu || '',
    memoryRequest: res.requests?.memory || '',
    memoryLimit: res.limits?.memory || '',
  }
  resourceDialogVisible.value = true
}

async function handleSubmitResources() {
  try {
    await updateK8sResources(resourceForm.value.name, namespace.value, {
      container: resourceForm.value.container,
      cpuRequest: resourceForm.value.cpuRequest,
      cpuLimit: resourceForm.value.cpuLimit,
      memoryRequest: resourceForm.value.memoryRequest,
      memoryLimit: resourceForm.value.memoryLimit,
    })
    ElMessage.success('资源配置已更新')
    resourceDialogVisible.value = false
    setTimeout(loadTab, 2000)
  } catch (e) { /* handled */ }
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
</script>

<template>
  <div class="k8s-page">
    <!-- Header -->
    <div class="k8s-header">
      <div class="header-left">
        <h4 class="page-title">Kubernetes 管理</h4>
        <el-select v-model="namespace" style="width: 180px;" placeholder="命名空间">
          <el-option v-for="ns in namespaces" :key="ns" :label="ns" :value="ns" />
        </el-select>
      </div>
      <el-button size="small" @click="loadTab" :loading="loading"><el-icon><Refresh /></el-icon>刷新</el-button>
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
          <el-table-column prop="cpuPercent" label="CPU %" width="100" />
          <el-table-column prop="memUsage" label="内存使用" width="120" />
          <el-table-column prop="memPercent" label="内存 %" width="100" />
        </el-table>
      </div>

      <!-- Deployments -->
      <div v-if="activeTab === 'deployments'">
        <el-table :data="deployments" stripe size="small" border>
          <el-table-column label="名称" min-width="200">
            <template #default="{ row }">{{ row.metadata?.name }}</template>
          </el-table-column>
          <el-table-column label="就绪" width="100">
            <template #default="{ row }">
              <el-tag :type="(row.status?.readyReplicas || 0) === (row.spec?.replicas || 0) ? 'success' : 'warning'" size="small">
                {{ depReady(row) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="镜像" min-width="300">
            <template #default="{ row }">
              <span style="font-size: 12px; color: #606266;">{{ row.spec?.template?.spec?.containers?.[0]?.image || '-' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="创建时间" width="170">
            <template #default="{ row }">{{ row.metadata?.creationTimestamp?.replace('T', ' ').replace('Z', '') || '-' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="260">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleRestart(row.metadata?.name)">重启</el-button>
              <el-button type="primary" link size="small" @click="handleScale(row.metadata?.name, row.spec?.replicas || 1)">扩缩容</el-button>
              <el-button type="primary" link size="small" @click="handleUpdateImage(row)">回滚</el-button>
              <el-button type="primary" link size="small" @click="handleUpdateResources(row)">资源</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Pods -->
      <div v-if="activeTab === 'pods'">
        <el-table :data="pods" stripe size="small" border>
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
          <el-table-column label="操作" width="100">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleViewLogs(row.metadata?.name)">日志</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Services -->
      <div v-if="activeTab === 'services'">
        <el-table :data="services" stripe size="small" border>
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
          <el-table-column label="操作" width="140">
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
            <el-table-column prop="cpuPercent" label="CPU %" width="100" />
            <el-table-column prop="memUsage" label="内存使用" width="120" />
            <el-table-column prop="memPercent" label="内存 %" width="100" />
          </el-table>
        </div>
      </div>

      <!-- ConfigMaps -->
      <div v-if="activeTab === 'configmaps'">
        <el-table :data="configmaps" stripe size="small" border>
          <el-table-column label="名称" min-width="250">
            <template #default="{ row }">{{ row.metadata?.name }}</template>
          </el-table-column>
          <el-table-column label="数据条目" width="100">
            <template #default="{ row }">{{ Object.keys(row.data || {}).length }}</template>
          </el-table-column>
          <el-table-column label="创建时间" width="180">
            <template #default="{ row }">{{ row.metadata?.creationTimestamp?.replace('T', ' ').replace('Z', '') || '-' }}</template>
          </el-table-column>
          <el-table-column label="操作" width="100">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleEditConfigMap(row)">编辑</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Ingresses -->
      <div v-if="activeTab === 'ingresses'">
        <el-table :data="ingresses" stripe size="small" border>
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

    <!-- Resources Dialog -->
    <el-dialog v-model="resourceDialogVisible" title="修改资源配置" width="500px">
      <el-form :model="resourceForm" label-width="110px">
        <el-form-item label="Deployment">
          <el-input :model-value="resourceForm.name" disabled />
        </el-form-item>
        <el-form-item label="CPU Request">
          <el-input v-model="resourceForm.cpuRequest" placeholder="如 100m, 500m, 1" />
        </el-form-item>
        <el-form-item label="CPU Limit">
          <el-input v-model="resourceForm.cpuLimit" placeholder="如 500m, 1, 2" />
        </el-form-item>
        <el-form-item label="Memory Request">
          <el-input v-model="resourceForm.memoryRequest" placeholder="如 128Mi, 256Mi, 1Gi" />
        </el-form-item>
        <el-form-item label="Memory Limit">
          <el-input v-model="resourceForm.memoryLimit" placeholder="如 256Mi, 512Mi, 2Gi" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="resourceDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSubmitResources">确定</el-button>
      </template>
    </el-dialog>

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
  </div>
</template>

<style scoped>
.k8s-page { display: flex; flex-direction: column; gap: 12px; }
.k8s-header {
  display: flex; align-items: center; justify-content: space-between;
  background: #fff; padding: 12px 16px; border-radius: 6px; border: 1px solid #ebeef5;
}
.header-left { display: flex; align-items: center; gap: 16px; }
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

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { listApps, createApp as apiCreateApp, updateApp, deleteApp, type Application } from '../api/application'
import { ElMessageBox, ElMessage } from 'element-plus'

const apps = ref<Application[]>([])
const loading = ref(true)
const searchText = ref('')
const filterType = ref('')
const dialogVisible = ref(false)
const editingApp = ref<Application | null>(null)
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

const form = ref({
  appName: '',
  appType: 'java' as 'java' | 'vue',
  gitRepo: '',
  vueRole: '' as string,
  isGateway: false,
  appCode: '',
  nodePort: 0,
  configMapContent: '',
  envTags: '',
})

// Multi-row Ingress editor state
const ingressList = ref<{ name: string; host: string }[]>([])

// Whether the Ingress section should appear in the form
const showIngressEditor = computed(() => {
  if (form.value.appType === 'java') return form.value.isGateway
  if (form.value.appType === 'vue') {
    return form.value.vueRole === 'main' || form.value.vueRole === 'standalone'
  }
  return false
})

function loadIngresses(jsonStr?: string) {
  try {
    ingressList.value = jsonStr ? JSON.parse(jsonStr) : []
  } catch {
    ingressList.value = []
  }
}

function serializeIngresses(): string {
  return ingressList.value.length > 0 ? JSON.stringify(ingressList.value) : ''
}

function addIngressRow() {
  ingressList.value.push({ name: form.value.appName || '', host: '' })
}

function removeIngressRow(idx: number) {
  ingressList.value.splice(idx, 1)
}

onMounted(async () => {
  await loadApps()
})

async function loadApps() {
  loading.value = true
  try {
    const result = await listApps({
      page: page.value,
      pageSize: pageSize.value,
      search: searchText.value || undefined,
      appType: filterType.value || undefined,
    })
    apps.value = result.list || []
    total.value = result.total || 0
  } catch (e) {
    apps.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function appTypeLabel(type: string) {
  return type === 'vue' ? 'Vue' : 'Java'
}

function appTypeTagType(type: string) {
  return type === 'vue' ? 'success' : ''
}

function vueRoleLabel(app: Application) {
  if (app.appType === 'java') {
    return app.isGateway ? '网关' : ''
  }
  if (app.appType !== 'vue') return ''
  if (app.vueRole === 'main') return '主应用'
  if (app.vueRole === 'sub') return app.appCode ? `子应用(${app.appCode})` : '子应用'
  if (app.vueRole === 'standalone') return '独立'
  return ''
}

function vueRoleTagType(app: Application) {
  if (app.appType === 'java' && app.isGateway) return 'warning'
  if (app.vueRole === 'main') return 'danger'
  if (app.vueRole === 'standalone') return 'warning'
  if (app.vueRole === 'sub') return ''
  return 'info'
}

function handleAdd() {
  editingApp.value = null
  form.value = {
    appName: '',
    appType: 'java',
    gitRepo: '',
    vueRole: '',
    isGateway: false,
    appCode: '',
    nodePort: 0,
    configMapContent: '',
    envTags: '',
  }
  ingressList.value = []
  dialogVisible.value = true
}

function handleEdit(app: Application) {
  editingApp.value = app
  form.value = {
    appName: app.appName,
    appType: app.appType,
    gitRepo: app.gitRepo,
    vueRole: app.vueRole || '',
    isGateway: !!app.isGateway,
    appCode: app.appCode || '',
    nodePort: app.nodePort || 0,
    configMapContent: app.configMapContent || '',
    envTags: app.envTags || '',
  }
  // Prefer the new ingresses JSON; fall back to legacy ingressHost.
  if (app.ingresses) {
    loadIngresses(app.ingresses)
  } else if (app.ingressHost) {
    ingressList.value = [{ name: app.appName, host: app.ingressHost }]
  } else {
    ingressList.value = []
  }
  dialogVisible.value = true
}

async function handleDelete(app: Application) {
  await ElMessageBox.confirm(`确定删除应用 "${app.appName}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await deleteApp(app.id)
    ElMessage.success('删除成功')
    await loadApps()
  } catch (e) {
    // handled by interceptor
  }
}

async function handleSave() {
  if (!form.value.appName.trim()) {
    ElMessage.warning('请输入应用名称')
    return
  }
  if (!form.value.gitRepo.trim()) {
    ElMessage.warning('请输入 Git 仓库地址')
    return
  }
  if (form.value.appType === 'vue' && !form.value.vueRole) {
    ElMessage.warning('Vue 项目必须选择应用角色')
    return
  }
  if (form.value.appType === 'vue' && form.value.vueRole === 'sub' && !form.value.appCode) {
    ElMessage.warning('子应用必须填写应用编号')
    return
  }

  // Validate ingress rows when present
  if (showIngressEditor.value) {
    for (const ing of ingressList.value) {
      if (!ing.name.trim() || !ing.host.trim()) {
        ElMessage.warning('Ingress 的名称和域名不能为空')
        return
      }
    }
  }

  const ingressesJson = showIngressEditor.value ? serializeIngresses() : ''
  // Keep ingressHost in sync with the first entry for backward compat.
  const firstHost = showIngressEditor.value && ingressList.value.length > 0 ? ingressList.value[0].host : ''

  try {
    if (editingApp.value) {
      await updateApp(editingApp.value.id, {
        appType: form.value.appType,
        gitRepo: form.value.gitRepo,
        vueRole: form.value.vueRole,
        isGateway: form.value.appType === 'java' ? form.value.isGateway : false,
        appCode: form.value.appCode,
        nodePort: form.value.nodePort || undefined,
        ingressHost: firstHost || undefined,
        ingresses: ingressesJson || undefined,
        configMapContent: form.value.configMapContent || undefined,
        envTags: form.value.envTags || undefined,
      })
      ElMessage.success('修改成功')
    } else {
      await apiCreateApp({
        appName: form.value.appName,
        appType: form.value.appType,
        gitRepo: form.value.gitRepo,
        vueRole: form.value.vueRole,
        isGateway: form.value.appType === 'java' ? form.value.isGateway : false,
        appCode: form.value.appCode,
        nodePort: form.value.nodePort || undefined,
        ingressHost: firstHost || undefined,
        ingresses: ingressesJson || undefined,
        configMapContent: form.value.configMapContent || undefined,
        envTags: form.value.envTags || undefined,
      })
      ElMessage.success('新增成功')
    }
    dialogVisible.value = false
    await loadApps()
  } catch (e) {
    // handled by interceptor
  }
}

function handleSearch() {
  page.value = 1
  loadApps()
}

function handlePageChange(newPage: number) {
  page.value = newPage
  loadApps()
}

function handleSizeChange(newSize: number) {
  pageSize.value = newSize
  page.value = 1
  loadApps()
}
</script>

<template>
  <div class="page">
    <!-- Toolbar -->
    <div class="toolbar-row">
      <div class="toolbar-left">
        <el-input
          v-model="searchText"
          placeholder="搜索应用名称 / 仓库地址"
          clearable
          style="width: 280px;"
          @keyup.enter="handleSearch"
          @clear="handleSearch"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-select v-model="filterType" placeholder="项目类型" clearable style="width: 120px;" @change="handleSearch">
          <el-option label="Java" value="java" />
          <el-option label="Vue" value="vue" />
        </el-select>
      </div>
      <div class="toolbar-right">
        <el-button type="primary" @click="handleAdd">
          <el-icon><Plus /></el-icon>新增应用
        </el-button>
      </div>
    </div>

    <!-- Table -->
    <div class="table-wrapper">
      <el-table :data="apps" v-loading="loading" stripe size="default">
        <el-table-column prop="appName" label="应用名称" width="200">
          <template #default="{ row }">
            <span class="link-text" @click="handleEdit(row)">{{ row.appName }}</span>
          </template>
        </el-table-column>
        <el-table-column label="项目类型" width="80" align="center">
          <template #default="{ row }">
            <el-tag :type="appTypeTagType(row.appType)" size="small" effect="plain">
              {{ appTypeLabel(row.appType) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="应用角色" width="120" align="center">
          <template #default="{ row }">
            <el-tag v-if="vueRoleLabel(row)" :type="vueRoleTagType(row)" size="small">
              {{ vueRoleLabel(row) }}
            </el-tag>
            <span v-else style="color: #c0c4cc;">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="gitRepo" label="Git 仓库地址" min-width="400" show-overflow-tooltip />
        <el-table-column label="操作" width="140" fixed="right" align="center">
          <template #default="{ row }">
            <el-button type="primary" link size="small" @click="handleEdit(row)">编辑</el-button>
            <el-button type="danger" link size="small" @click="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="pagination-row">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :page-sizes="[10, 20, 50, 100]"
          :total="total"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="handlePageChange"
          @size-change="handleSizeChange"
        />
      </div>
    </div>

    <!-- Add/Edit Dialog -->
    <el-dialog
      v-model="dialogVisible"
      :title="editingApp ? '编辑应用' : '新增应用'"
      width="560px"
      :close-on-click-modal="false"
    >
      <el-form :model="form" label-width="100px">
        <el-form-item label="应用名称" required>
          <el-input v-model="form.appName" placeholder="如 his-gateway、web-main" :disabled="!!editingApp" />
        </el-form-item>
        <el-form-item label="项目类型" required>
          <el-radio-group v-model="form.appType" @change="form.vueRole = ''; form.appCode = ''; form.isGateway = false; ingressList = []">
            <el-radio value="java">Java</el-radio>
            <el-radio value="vue">Vue</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="Git 仓库" required>
          <el-input v-model="form.gitRepo" placeholder="ssh://git@192.168.1.206/df-his-backend/..." />
        </el-form-item>
        <el-form-item label="网关服务" v-if="form.appType === 'java'">
          <el-checkbox v-model="form.isGateway">是否为 Java 网关 (会创建 Ingress)</el-checkbox>
          <div class="field-hint">勾选后下方可配置一个或多个对外 Ingress 域名</div>
        </el-form-item>
        <el-form-item label="应用角色" required v-if="form.appType === 'vue'">
          <el-radio-group v-model="form.vueRole">
            <el-radio value="main">主应用</el-radio>
            <el-radio value="sub">子应用</el-radio>
            <el-radio value="standalone">独立应用</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="应用编号" required v-if="form.appType === 'vue' && form.vueRole === 'sub'">
          <el-input v-model="form.appCode" placeholder="如 04、99、gymk" style="width: 160px;" />
          <div class="field-hint">构建产物将命名为 {编号}.zip</div>
        </el-form-item>
        <el-form-item label="NodePort" v-if="form.appType === 'java' || (form.appType === 'vue' && (form.vueRole === 'main' || form.vueRole === 'standalone'))">
          <el-input-number v-model="form.nodePort" :min="0" :max="32767" :step="1" placeholder="30000-32767" style="width: 160px;" />
          <div class="field-hint">K8s Service 对外端口 (30000-32767)，0 表示 ClusterIP，不暴露</div>
        </el-form-item>
        <el-form-item label="Ingress 列表" v-if="showIngressEditor">
          <div class="ingress-editor">
            <div v-for="(row, idx) in ingressList" :key="idx" class="ingress-row">
              <el-input v-model="row.name" placeholder="名称 (如 his-gateway-internal)" style="width: 220px;" />
              <el-input v-model="row.host" placeholder="域名 (如 api.his.com)" style="flex: 1;" />
              <el-button link type="danger" @click="removeIngressRow(idx)">删除</el-button>
            </div>
            <el-button size="small" @click="addIngressRow" plain>
              <el-icon><Plus /></el-icon>添加 Ingress
            </el-button>
            <div class="field-hint">Vue 主/独立应用通常一个 Ingress；Java 网关可配置多个对外域名</div>
          </div>
        </el-form-item>
        <el-form-item label="Nginx 配置" v-if="form.appType === 'vue' && (form.vueRole === 'main' || form.vueRole === 'standalone')">
          <el-input
            v-model="form.configMapContent"
            type="textarea"
            :autosize="{ minRows: 6, maxRows: 20 }"
            placeholder="nginx server 配置内容，将作为 ConfigMap 挂载到 /etc/nginx/conf.d/"
            style="font-family: monospace;"
          />
          <div class="field-hint">留空则不创建 ConfigMap</div>
        </el-form-item>
        <el-form-item label="环境标签">
          <el-input v-model="form.envTags" placeholder="如 四川 或 四川,重庆（逗号分隔）" />
          <div class="field-hint">留空表示所有环境都部署，填写后仅在对应客户环境下显示</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSave">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.toolbar-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
  background: #fff;
  padding: 12px 16px;
  border-radius: 6px;
  border: 1px solid #ebeef5;
}

.toolbar-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.toolbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
}

.table-wrapper {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  overflow: hidden;
}

.link-text {
  color: #409eff;
  cursor: pointer;
  font-weight: 500;
}

.link-text:hover {
  text-decoration: underline;
}

.field-hint {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
}

.pagination-row {
  display: flex;
  justify-content: flex-end;
  padding: 12px 16px;
  border-top: 1px solid #ebeef5;
}

.ingress-editor {
  width: 100%;
}

.ingress-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}
</style>

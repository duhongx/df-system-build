<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listTasks, createTask, updateTask as apiUpdateTask, deleteTask, executeTask, batchExecuteTasks, type Task } from '../api/task'
import { listAllApps, type Application } from '../api/application'
import { listBuildConfigs, type BuildConfig } from '../api/build-config'
import { useSettingsStore } from '../stores/settings'
import { formatTime as formatTimeStr } from '../utils/time'

const router = useRouter()
const settingsStore = useSettingsStore()
const loading = ref(true)
const searchText = ref('')
const filterType = ref('')
const selectedTasks = ref<Task[]>([])
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

const tasks = ref<Task[]>([])
const apps = ref<Application[]>([])
const buildConfigs = ref<BuildConfig[]>([])

// Execute dialog
const executeDialogVisible = ref(false)
const executeMode = ref<'single' | 'batch'>('single')
const executeForm = ref({ gitBranch: '', autoDeploy: true })
const currentTask = ref<Task | null>(null)

// Edit dialog
const editDialogVisible = ref(false)
const editTask = ref<Task | null>(null)
const editForm = ref({
  taskName: '',
  applicationId: null as number | null,
  gitBranch: '',
  buildConfigId: null as number | null,
  deployMode: 'deploy',
  k8sNamespace: '',
})

// Create dialog
const createDialogVisible = ref(false)
const createForm = ref({
  taskName: '',
  applicationId: null as number | null,
  gitBranch: '',
  buildConfigId: null as number | null,
  deployMode: 'deploy',
  k8sNamespace: '',
})

onMounted(async () => {
  if (!settingsStore.loaded) {
    await settingsStore.fetchSettings()
  }
  await loadAll()
})

async function loadAll() {
  loading.value = true
  try {
    const [taskResult, appList, bcList] = await Promise.all([
      listTasks({ page: page.value, pageSize: pageSize.value, search: searchText.value || undefined, appType: filterType.value || undefined }),
      listAllApps(),
      listBuildConfigs(),
    ])
    tasks.value = taskResult.list || []
    total.value = taskResult.total || 0
    apps.value = appList || []
    buildConfigs.value = bcList || []
  } catch (e) {
    tasks.value = []
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  page.value = 1
  loadAll()
}

function handlePageChange(newPage: number) {
  page.value = newPage
  loadAll()
}

function handleSizeChange(newSize: number) {
  pageSize.value = newSize
  page.value = 1
  loadAll()
}

function handleSelectionChange(rows: Task[]) {
  selectedTasks.value = rows
}

function lastStatusTag(status: string | undefined) {
  if (!status) return { type: 'info' as any, text: '-' }
  if (status === 'SUCCESS') return { type: 'success' as any, text: '成功' }
  if (status === 'FAILED') return { type: 'danger' as any, text: '失败' }
  if (status === 'RUNNING') return { type: 'primary' as any, text: '运行中' }
  return { type: 'info' as any, text: status }
}

function formatDuration(seconds: number | null | undefined) {
  if (!seconds) return '-'
  if (seconds < 60) return `${seconds}s`
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

// ---- Execute ----
function handleExecute(task: Task) {
  currentTask.value = task
  executeMode.value = 'single'
  executeForm.value = { gitBranch: task.gitBranch || '', autoDeploy: true }
  executeDialogVisible.value = true
}

function handleBatchExecute() {
  if (!selectedTasks.value.length) {
    ElMessage.warning('请至少选择一个任务')
    return
  }
  currentTask.value = null
  executeMode.value = 'batch'
  executeForm.value = { gitBranch: '', autoDeploy: true }
  executeDialogVisible.value = true
}

async function handleSubmitExecute() {
  if (!executeForm.value.gitBranch.trim()) {
    ElMessage.warning('请输入 Git 分支')
    return
  }

  try {
    if (executeMode.value === 'single' && currentTask.value) {
      await executeTask(currentTask.value.id, executeForm.value)
      ElMessage.success(`已提交: ${currentTask.value.taskName}`)
    } else {
      await batchExecuteTasks({
        taskIds: selectedTasks.value.map(t => t.id),
        gitBranch: executeForm.value.gitBranch,
        autoDeploy: executeForm.value.autoDeploy,
      })
      ElMessage.success(`已提交 ${selectedTasks.value.length} 个任务`)
    }
    executeDialogVisible.value = false
    router.push('/build-queue')
  } catch (e) {
    // handled by interceptor
  }
}

// ---- Create ----
function handleCreate() {
  createForm.value = {
    taskName: '',
    applicationId: null,
    gitBranch: '',
    buildConfigId: null,
    deployMode: 'deploy',
    k8sNamespace: '',
  }
  createDialogVisible.value = true
}

async function handleSaveCreate() {
  if (!validateForm(createForm.value)) return
  try {
    await createTask({
      taskName: createForm.value.taskName,
      applicationId: createForm.value.applicationId!,
      gitBranch: createForm.value.gitBranch,
      buildConfigId: createForm.value.buildConfigId!,
      deployMode: createForm.value.deployMode,
      k8sNamespace: createForm.value.k8sNamespace,
    })
    ElMessage.success('任务创建成功')
    createDialogVisible.value = false
    await loadAll()
  } catch (e) {
    // handled by interceptor
  }
}

// ---- Edit ----
function handleEdit(task: Task) {
  editTask.value = task
  editForm.value = {
    taskName: task.taskName,
    applicationId: task.applicationId,
    gitBranch: task.gitBranch,
    buildConfigId: task.buildConfigId,
    deployMode: (task as any).deployMode || 'deploy',
    k8sNamespace: (task as any).k8sNamespace || '',
  }
  editDialogVisible.value = true
}

async function handleSaveEdit() {
  if (!validateForm(editForm.value) || !editTask.value) return
  try {
    await apiUpdateTask(editTask.value.id, {
      taskName: editForm.value.taskName,
      applicationId: editForm.value.applicationId!,
      gitBranch: editForm.value.gitBranch,
      buildConfigId: editForm.value.buildConfigId!,
      deployMode: editForm.value.deployMode,
      k8sNamespace: editForm.value.k8sNamespace,
    })
    ElMessage.success('修改成功')
    editDialogVisible.value = false
    await loadAll()
  } catch (e) {
    // handled by interceptor
  }
}

function validateForm(f: any): boolean {
  if (!f.taskName?.trim()) { ElMessage.warning('请输入任务名称'); return false }
  if (!f.applicationId) { ElMessage.warning('请选择应用'); return false }
  if (!f.gitBranch?.trim()) { ElMessage.warning('请输入分支'); return false }
  if (!f.buildConfigId) { ElMessage.warning('请选择编译配置'); return false }
  return true
}

// ---- Delete ----
async function handleDelete(task: Task) {
  await ElMessageBox.confirm(`确定删除任务 "${task.taskName}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await deleteTask(task.id)
    ElMessage.success('删除成功')
    await loadAll()
  } catch (e) {
    // handled by interceptor
  }
}

// ---- History ----
function handleHistory(task: Task) {
  router.push({ path: '/release', query: { app: task.application?.appName || '' } })
}
</script>

<template>
  <div class="page">
    <div class="toolbar-row">
      <div class="toolbar-left">
        <el-input
          v-model="searchText"
          placeholder="搜索任务名称 / 应用名称"
          clearable
          style="width: 220px;"
          @keyup.enter="handleSearch"
          @clear="handleSearch"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-select v-model="filterType" placeholder="项目类型" clearable style="width: 110px;" @change="handleSearch">
          <el-option label="Java" value="java" />
          <el-option label="Vue" value="vue" />
        </el-select>
      </div>
      <div class="toolbar-right">
        <el-button @click="handleCreate">
          <el-icon><Plus /></el-icon>创建任务
        </el-button>
        <el-button type="primary" :disabled="!selectedTasks.length" @click="handleBatchExecute">
          <el-icon><VideoPlay /></el-icon>
          批量执行{{ selectedTasks.length ? ` (${selectedTasks.length})` : '' }}
        </el-button>
      </div>
    </div>

    <div class="table-wrapper">
      <el-table :data="tasks" v-loading="loading" @selection-change="handleSelectionChange" stripe size="default">
        <el-table-column type="selection" width="45" />
        <el-table-column prop="taskName" label="任务名称" min-width="180">
          <template #default="{ row }">
            <span class="task-name">{{ row.taskName }}</span>
          </template>
        </el-table-column>
        <el-table-column label="应用名称" min-width="155">
          <template #default="{ row }">{{ row.application?.appName || '-' }}</template>
        </el-table-column>
        <el-table-column prop="gitBranch" label="任务分支" min-width="130" />
        <el-table-column label="上次任务状态" min-width="120" align="center">
          <template #default="{ row }">
            <el-tag :type="lastStatusTag(row.lastStatus).type" size="small" effect="dark" v-if="row.lastStatus">
              {{ lastStatusTag(row.lastStatus).text }}
            </el-tag>
            <span v-else style="color: #909399;">-</span>
          </template>
        </el-table-column>
        <el-table-column label="上次执行时间" min-width="170">
          <template #default="{ row }">
            <span class="time-text">{{ formatTimeStr(row.lastRunTime) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="上次耗时" min-width="100">
          <template #default="{ row }">
            <span class="duration">{{ formatDuration(row.lastDurationSeconds) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="300" fixed="right" align="center">
          <template #default="{ row }">
            <div class="action-btns">
              <el-button size="small" @click="handleEdit(row)">修改</el-button>
              <el-button size="small" type="danger" plain @click="handleDelete(row)">删除</el-button>
              <el-button size="small" @click="handleHistory(row)">历史</el-button>
              <el-button size="small" type="primary" @click="handleExecute(row)">
                <el-icon style="margin-right: 3px;"><VideoPlay /></el-icon>执行
              </el-button>
            </div>
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

    <!-- Execute Dialog -->
    <el-dialog
      v-model="executeDialogVisible"
      :title="executeMode === 'single' ? `执行任务 - ${currentTask?.taskName}` : `批量执行 (${selectedTasks.length} 个任务)`"
      width="500px"
      :close-on-click-modal="false"
    >
      <div v-if="executeMode === 'batch'" class="batch-preview">
        <el-tag v-for="t in selectedTasks" :key="t.id" size="small" effect="plain" style="margin: 2px 4px;">{{ t.taskName }}</el-tag>
      </div>
      <el-form :model="executeForm" label-width="90px" style="margin-top: 16px;">
        <el-form-item label="Git 分支" required>
          <el-input v-model="executeForm.gitBranch" placeholder="如 release_2.15.3_250515" />
        </el-form-item>
        <el-form-item label="更新方式">
          <el-radio-group v-model="executeForm.autoDeploy">
            <el-radio :value="true">自动执行</el-radio>
            <el-radio :value="false">手动触发</el-radio>
          </el-radio-group>
          <div style="font-size: 12px; color: #909399; margin-top: 4px;">
            自动执行: 镜像推送后直接更新 K8s；手动触发: 镜像就绪后等待确认再更新
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="executeDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSubmitExecute"><el-icon><VideoPlay /></el-icon>开始执行</el-button>
      </template>
    </el-dialog>

    <!-- Create Dialog -->
    <el-dialog v-model="createDialogVisible" title="创建任务" width="540px" :close-on-click-modal="false">
      <el-form :model="createForm" label-width="100px">
        <el-form-item label="任务名称" required>
          <el-input v-model="createForm.taskName" placeholder="如 his-gateway-2sftp" />
        </el-form-item>
        <el-form-item label="选择应用" required>
          <el-select v-model="createForm.applicationId" placeholder="从应用管理中选择" filterable style="width: 100%;">
            <el-option v-for="app in apps" :key="app.id" :label="app.appName" :value="app.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="分支" required>
          <el-input v-model="createForm.gitBranch" placeholder="如 release_2.15.3_250515" />
        </el-form-item>
        <el-form-item label="编译配置" required>
          <el-select v-model="createForm.buildConfigId" placeholder="选择编译配置" style="width: 100%;">
            <el-option v-for="bc in buildConfigs" :key="bc.id" :label="`${bc.name} (${bc.buildMode})`" :value="bc.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="部署模式">
          <el-radio-group v-model="createForm.deployMode">
            <el-radio value="deploy">构建镜像 + K8s 部署</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="K8s 命名空间">
          <el-input v-model="createForm.k8sNamespace" :placeholder="`留空使用全局默认：${settingsStore.k8sNamespace}`" />
          <div class="form-hint">留空则使用系统设置中的全局默认命名空间</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSaveCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- Edit Dialog -->
    <el-dialog v-model="editDialogVisible" :title="`修改任务 - ${editTask?.taskName}`" width="540px" :close-on-click-modal="false">
      <el-form :model="editForm" label-width="100px">
        <el-form-item label="任务名称" required>
          <el-input v-model="editForm.taskName" />
        </el-form-item>
        <el-form-item label="选择应用" required>
          <el-select v-model="editForm.applicationId" placeholder="从应用管理中选择" filterable style="width: 100%;">
            <el-option v-for="app in apps" :key="app.id" :label="app.appName" :value="app.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="分支" required>
          <el-input v-model="editForm.gitBranch" placeholder="如 release_2.15.3_250515" />
        </el-form-item>
        <el-form-item label="编译配置" required>
          <el-select v-model="editForm.buildConfigId" placeholder="选择编译配置" style="width: 100%;">
            <el-option v-for="bc in buildConfigs" :key="bc.id" :label="`${bc.name} (${bc.buildMode})`" :value="bc.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="部署模式">
          <el-radio-group v-model="editForm.deployMode">
            <el-radio value="deploy">构建镜像 + K8s 部署</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="K8s 命名空间">
          <el-input v-model="editForm.k8sNamespace" :placeholder="`留空使用全局默认：${settingsStore.k8sNamespace}`" />
          <div class="form-hint">留空则使用系统设置中的全局默认命名空间</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSaveEdit">保存</el-button>
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

.task-name {
  font-weight: 500;
  color: #303133;
}

.time-text {
  font-size: 12px;
  color: #606266;
}

.duration {
  font-size: 12px;
  color: #606266;
  font-weight: 500;
}

.batch-preview {
  background: #f5f7fa;
  border-radius: 4px;
  padding: 10px 12px;
  max-height: 100px;
  overflow-y: auto;
}

.action-btns {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
}

.form-hint {
  font-size: 12px;
  color: #909399;
  margin-top: 2px;
}

.pagination-row {
  display: flex;
  justify-content: flex-end;
  padding: 12px 16px;
  border-top: 1px solid #ebeef5;
}
</style>

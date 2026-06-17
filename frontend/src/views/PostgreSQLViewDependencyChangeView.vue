<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  analyzeSQLViewDependencyTask,
  createSQLViewDependencyTask,
  executeSQLViewDependencyTask,
  exportSQLViewDependencyPlan,
  exportSQLViewDependencyRestorePlan,
  getSQLViewDependencyTask,
  listSQLViewDependencyTasks,
  precheckSQLViewDependencyTask,
  restoreSQLViewDependencyTask,
  type SQLViewDependencyItem,
  type SQLViewDependencyTask,
  type SQLViewDependencyTaskRequest,
} from '../api/postgresql'
import { formatTime as formatTimeStr } from '../utils/time'

const loading = ref(false)
const analyzing = ref(false)
const prechecking = ref(false)
const executing = ref(false)
const restoring = ref(false)
const exporting = ref(false)
const tasks = ref<SQLViewDependencyTask[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const currentTask = ref<SQLViewDependencyTask | null>(null)
const items = ref<SQLViewDependencyItem[]>([])
const planPreview = ref('')
const form = ref<SQLViewDependencyTaskRequest>({
  schemaName: '',
  tableName: '',
  columnName: '',
  alterSql: '',
  lockTimeout: '3s',
  statementTimeout: '10min',
})

onMounted(loadTasks)

async function loadTasks() {
  loading.value = true
  try {
    const data = await listSQLViewDependencyTasks(page.value, pageSize)
    tasks.value = data.list || []
    total.value = data.total || 0
  } finally {
    loading.value = false
  }
}

async function handleCreateTask() {
  if (!form.value.schemaName.trim() || !form.value.tableName.trim() || !form.value.columnName.trim() || !form.value.alterSql.trim()) {
    ElMessage.warning('请填写 schema、表、字段和 ALTER SQL')
    return
  }
  const task = await createSQLViewDependencyTask({ ...form.value })
  currentTask.value = task
  items.value = []
  planPreview.value = ''
  ElMessage.success('任务已创建')
  await loadTasks()
}

async function handleOpenTask(task: SQLViewDependencyTask) {
  const data = await getSQLViewDependencyTask(task.id)
  currentTask.value = data.task
  items.value = data.items || []
  planPreview.value = ''
  form.value = {
    schemaName: data.task.schemaName,
    tableName: data.task.tableName,
    columnName: data.task.columnName,
    alterSql: data.task.alterSql,
    lockTimeout: data.task.lockTimeout || '3s',
    statementTimeout: data.task.statementTimeout || '10min',
  }
}

async function refreshCurrentTask() {
  if (!currentTask.value) return
  const data = await getSQLViewDependencyTask(currentTask.value.id)
  currentTask.value = data.task
  items.value = data.items || []
  await loadTasks()
}

function showOperationError(err: unknown) {
  const message = err instanceof Error ? err.message : '操作失败'
  ElMessage.error(message)
}

async function handleAnalyze() {
  if (!currentTask.value) {
    ElMessage.warning('请先创建或选择任务')
    return
  }
  analyzing.value = true
  try {
    const data = await analyzeSQLViewDependencyTask(currentTask.value.id)
    currentTask.value = data.task
    items.value = data.items || []
    planPreview.value = ''
    ElMessage.success(`分析完成，依赖视图 ${items.value.length} 个`)
    await loadTasks()
  } catch (err) {
    await refreshCurrentTask()
    showOperationError(err)
  } finally {
    analyzing.value = false
  }
}

async function handlePrecheck() {
  if (!currentTask.value) {
    ElMessage.warning('请先创建或选择任务')
    return
  }
  prechecking.value = true
  try {
    const data = await precheckSQLViewDependencyTask(currentTask.value.id)
    currentTask.value = data.task
    items.value = data.items || []
    ElMessage.success(data.task.executeMessage || '预检通过')
    await loadTasks()
  } catch (err) {
    await refreshCurrentTask()
    showOperationError(err)
  } finally {
    prechecking.value = false
  }
}

async function handleExecute() {
  if (!currentTask.value) {
    ElMessage.warning('请先创建或选择任务')
    return
  }
  await ElMessageBox.confirm(
    `短锁执行会在一个事务内 DROP 依赖视图、执行 ALTER TABLE、再恢复视图；lock_timeout=${currentTask.value.lockTimeout || form.value.lockTimeout}，拿不到锁会失败回滚。确认继续？`,
    '高风险确认',
    { type: 'error', confirmButtonText: '短锁执行' },
  )
  executing.value = true
  try {
    const data = await executeSQLViewDependencyTask(currentTask.value.id)
    currentTask.value = data.task
    items.value = data.items || []
    ElMessage.success(data.task.executeMessage || '执行完成')
    await loadTasks()
  } catch (err) {
    await refreshCurrentTask()
    showOperationError(err)
  } finally {
    executing.value = false
  }
}

async function handleRestore() {
  if (!currentTask.value) {
    ElMessage.warning('请先选择任务')
    return
  }
  await ElMessageBox.confirm('将按备份顺序恢复视图、owner、权限、注释和索引。确认继续？', '恢复视图', { type: 'warning', confirmButtonText: '恢复视图' })
  restoring.value = true
  try {
    const data = await restoreSQLViewDependencyTask(currentTask.value.id)
    currentTask.value = data.task
    items.value = data.items || []
    ElMessage.success(data.task.executeMessage || '恢复完成')
    await loadTasks()
  } catch (err) {
    await refreshCurrentTask()
    showOperationError(err)
  } finally {
    restoring.value = false
  }
}

async function handleExportPlan() {
  if (!currentTask.value) {
    ElMessage.warning('请先选择任务')
    return
  }
  exporting.value = true
  try {
    const content = await exportSQLViewDependencyPlan(currentTask.value.id)
    planPreview.value = content
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${currentTask.value.schemaName}.${currentTask.value.tableName}.${currentTask.value.columnName}-view-dependency-plan.sql`
    a.click()
  } finally {
    exporting.value = false
  }
}

async function handleExportRestorePlan() {
  if (!currentTask.value) {
    ElMessage.warning('请先选择任务')
    return
  }
  exporting.value = true
  try {
    const content = await exportSQLViewDependencyRestorePlan(currentTask.value.id)
    planPreview.value = content
    const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${currentTask.value.schemaName}.${currentTask.value.tableName}.${currentTask.value.columnName}-view-dependency-restore.sql`
    a.click()
  } finally {
    exporting.value = false
  }
}

function statusTag(status: string) {
  if (status === 'SUCCESS') return 'success'
  if (status.includes('FAILED')) return 'danger'
  if (status === 'ANALYZED') return 'warning'
  return 'info'
}
</script>

<template>
  <div class="view-dep-page">
    <aside class="task-panel">
      <div class="panel-title">视图依赖变更</div>
      <div class="task-list" v-loading="loading">
        <div v-for="task in tasks" :key="task.id" class="task-item" :class="{ active: currentTask?.id === task.id }" @click="handleOpenTask(task)">
          <div class="task-name">{{ task.schemaName }}.{{ task.tableName }}.{{ task.columnName }}</div>
          <div class="task-meta">
            <el-tag :type="statusTag(task.status)" size="small">{{ task.status }}</el-tag>
            <span>{{ formatTimeStr(task.createdAt)?.slice(5) }}</span>
          </div>
        </div>
        <el-empty v-if="!tasks.length" :image-size="48" description="暂无任务" />
      </div>
      <el-pagination v-if="total > pageSize" v-model:current-page="page" small layout="prev, pager, next" :page-size="pageSize" :total="total" @current-change="loadTasks" />
    </aside>

    <main class="workspace">
      <section class="section">
        <div class="section-title">创建任务</div>
        <div class="form-grid">
          <el-input v-model="form.schemaName" placeholder="schemaName" />
          <el-input v-model="form.tableName" placeholder="tableName" />
          <el-input v-model="form.columnName" placeholder="columnName" />
          <el-input v-model="form.lockTimeout" placeholder="lock_timeout" />
          <el-input v-model="form.statementTimeout" placeholder="statement_timeout" />
        </div>
        <el-input v-model="form.alterSql" class="sql-input" type="textarea" :rows="5" placeholder="ALTER TABLE schema.table ALTER COLUMN column TYPE ..." />
        <div class="actions">
          <el-button type="primary" @click="handleCreateTask">创建任务</el-button>
          <el-button type="warning" :disabled="!currentTask" :loading="analyzing" @click="handleAnalyze">分析依赖</el-button>
          <el-button :disabled="!currentTask" :loading="prechecking" @click="handlePrecheck">执行预检</el-button>
          <el-button type="danger" :disabled="!currentTask" :loading="executing" @click="handleExecute">短锁执行</el-button>
          <el-button type="warning" plain :disabled="!currentTask" :loading="restoring" @click="handleRestore">恢复视图</el-button>
          <el-button :disabled="!currentTask" :loading="exporting" @click="handleExportPlan">导出执行计划</el-button>
          <el-button :disabled="!currentTask" :loading="exporting" @click="handleExportRestorePlan">导出恢复 SQL</el-button>
        </div>
      </section>

      <section class="section result-section">
        <div class="section-title">依赖视图</div>
        <el-table :data="items" size="small" height="280">
          <el-table-column prop="dropOrder" label="Drop顺序" width="90" />
          <el-table-column prop="restoreOrder" label="恢复顺序" width="90" />
          <el-table-column prop="objectSchema" label="Schema" width="140" />
          <el-table-column prop="objectName" label="视图" min-width="180" />
          <el-table-column prop="objectKind" label="类型" width="80" />
          <el-table-column prop="ownerName" label="Owner" width="140" />
          <el-table-column prop="status" label="状态" width="110" />
        </el-table>
      </section>

      <section class="section plan-section">
        <div class="section-title">执行计划预览</div>
        <el-input v-model="planPreview" type="textarea" :rows="10" readonly placeholder="导出执行计划后显示" />
      </section>
    </main>
  </div>
</template>

<style scoped>
.view-dep-page { display: flex; height: calc(100vh - 130px); gap: 12px; overflow: hidden; }
.task-panel { width: 300px; flex-shrink: 0; border: 1px solid #ebeef5; border-radius: 6px; background: #fff; display: flex; flex-direction: column; }
.panel-title { padding: 12px; font-weight: 600; border-bottom: 1px solid #ebeef5; }
.task-list { flex: 1; overflow-y: auto; padding: 8px; }
.task-item { padding: 8px 10px; border: 1px solid transparent; border-radius: 4px; cursor: pointer; margin-bottom: 6px; }
.task-item:hover { background: #f5f7fa; }
.task-item.active { background: #ecf5ff; border-color: #d9ecff; }
.task-name { font-size: 13px; font-weight: 500; color: #303133; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.task-meta { display: flex; gap: 8px; align-items: center; margin-top: 5px; color: #909399; font-size: 12px; }
.workspace { flex: 1; min-width: 0; overflow-y: auto; display: flex; flex-direction: column; gap: 12px; }
.section { background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 12px; }
.section-title { font-weight: 600; margin-bottom: 10px; }
.form-grid { display: grid; grid-template-columns: repeat(5, minmax(120px, 1fr)); gap: 8px; margin-bottom: 8px; }
.sql-input :deep(textarea), .plan-section :deep(textarea) { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; line-height: 1.5; }
.actions { margin-top: 10px; display: flex; gap: 8px; flex-wrap: wrap; }
.result-section { flex-shrink: 0; }
.plan-section { flex-shrink: 0; }
@media (max-width: 900px) {
  .view-dep-page { flex-direction: column; height: auto; overflow: visible; }
  .task-panel { width: auto; min-height: 220px; }
  .form-grid { grid-template-columns: 1fr; }
}
</style>

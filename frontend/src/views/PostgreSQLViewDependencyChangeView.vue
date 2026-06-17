<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
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
  executionMode: 'STEP',
  lockTimeout: '3s',
  statementTimeout: '10min',
})

const currentStatus = computed(() => currentTask.value?.status || '')
const currentExecutionMode = computed(() => currentTask.value?.executionMode || form.value.executionMode || 'STEP')
const currentStep = computed(() => {
  if (!currentTask.value) return 0
  if (['CREATED'].includes(currentStatus.value)) return 1
  if (['ANALYZED', 'PRECHECK_FAILED'].includes(currentStatus.value)) return 2
  if (['PRECHECK_PASSED', 'EXECUTING', 'FAILED', 'RESTORE_FAILED', 'RESTORING'].includes(currentStatus.value)) return 3
  return 4
})
const dependencySummary = computed(() => {
  const viewCount = items.value.filter(item => item.objectKind === 'v').length
  const materializedCount = items.value.filter(item => item.objectKind === 'm').length
  const maxDepth = items.value.reduce((max, item) => Math.max(max, item.depth || 0), 0)
  return { viewCount, materializedCount, maxDepth }
})
const canAnalyze = computed(() => !!currentTask.value && !['EXECUTING', 'RESTORING'].includes(currentStatus.value))
const canPrecheck = computed(() => !!currentTask.value && items.value.length > 0 && !['EXECUTING', 'RESTORING'].includes(currentStatus.value))
const canExecute = computed(() => currentStatus.value === 'PRECHECK_PASSED')
const canRestore = computed(() => !!currentTask.value && items.value.length > 0 && ['FAILED', 'RESTORE_FAILED', 'SUCCESS', 'RESTORED'].includes(currentStatus.value))

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
    ElMessage.warning('请填写目标 Schema、目标表、目标字段和 ALTER SQL')
    return
  }
  const task = await createSQLViewDependencyTask({ ...form.value })
  currentTask.value = task
  items.value = []
  planPreview.value = ''
  ElMessage.success('任务已保存，请继续分析依赖')
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
    executionMode: data.task.executionMode || 'STEP',
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
    ElMessage.warning('请先保存任务')
    return
  }
  analyzing.value = true
  try {
    const data = await analyzeSQLViewDependencyTask(currentTask.value.id)
    currentTask.value = data.task
    items.value = data.items || []
    planPreview.value = ''
    ElMessage.success(`分析完成，依赖对象 ${items.value.length} 个`)
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
    ElMessage.warning('请先保存任务')
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
    ElMessage.warning('请先选择任务')
    return
  }
  const isStep = currentExecutionMode.value === 'STEP'
  const message = isStep
    ? '将按分步执行方式处理字段变更：删除依赖视图并提交，执行 ALTER TABLE，再按备份恢复视图并校验。执行过程中依赖视图会短暂不可用。确认继续？'
    : '将在一个数据库事务内处理字段变更：删除依赖视图、执行 ALTER TABLE、恢复视图并校验。任一步失败会整体回滚。确认继续？'
  await ElMessageBox.confirm(message, '执行确认', { type: isStep ? 'warning' : 'error', confirmButtonText: '执行变更' })
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
  await ElMessageBox.confirm('将按备份恢复视图、owner、权限、注释、索引并执行校验。确认继续？', '恢复视图', { type: 'warning', confirmButtonText: '恢复视图' })
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
  if (['SUCCESS', 'RESTORED', 'PRECHECK_PASSED'].includes(status)) return 'success'
  if (status.includes('FAILED')) return 'danger'
  if (['ANALYZED', 'CREATED'].includes(status)) return 'warning'
  return 'info'
}

function statusText(status: string) {
  const map: Record<string, string> = {
    CREATED: '待分析',
    ANALYZED: '已分析',
    PRECHECK_PASSED: '预检通过',
    PRECHECK_FAILED: '预检失败',
    EXECUTING: '执行中',
    SUCCESS: '执行成功',
    FAILED: '执行失败',
    RESTORING: '恢复中',
    RESTORED: '已恢复',
    RESTORE_FAILED: '恢复失败',
  }
  return map[status] || status || '未保存'
}

function executionModeText(mode?: string) {
  return mode === 'TRANSACTION' ? '事务执行' : '分步执行'
}

function objectKindText(kind: string) {
  return kind === 'm' ? '物化视图' : '视图'
}
</script>

<template>
  <div class="view-dep-page">
    <section class="page-header">
      <div>
        <h2>字段变更助手</h2>
        <p>处理 ALTER COLUMN 时被视图依赖阻塞的变更，支持依赖分析、预检、事务执行、分步执行和恢复。</p>
      </div>
      <el-button :loading="loading" @click="loadTasks">刷新历史任务</el-button>
    </section>

    <section class="workflow-steps">
      <el-steps :active="currentStep" finish-status="success" align-center>
        <el-step title="填写变更" />
        <el-step title="分析依赖" />
        <el-step title="执行预检" />
        <el-step title="执行变更" />
        <el-step title="结果恢复" />
      </el-steps>
    </section>

    <section v-if="currentTask" class="task-summary">
      <div>
        <span class="summary-label">目标字段</span>
        <strong>{{ currentTask.schemaName }}.{{ currentTask.tableName }}.{{ currentTask.columnName }}</strong>
      </div>
      <div>
        <span class="summary-label">状态</span>
        <el-tag :type="statusTag(currentTask.status)" size="small">{{ statusText(currentTask.status) }}</el-tag>
      </div>
      <div>
        <span class="summary-label">执行方式</span>
        <strong>{{ executionModeText(currentTask.executionMode) }}</strong>
      </div>
      <div>
        <span class="summary-label">依赖对象</span>
        <strong>{{ items.length }} 个</strong>
      </div>
      <div class="summary-message">
        <span class="summary-label">最近消息</span>
        <span>{{ currentTask.executeMessage || currentTask.riskReason || '-' }}</span>
      </div>
    </section>

    <div class="content-grid">
      <main class="workspace">
        <section class="section">
          <div class="section-head">
            <div>
              <h3>1. 字段变更</h3>
              <p>填写目标字段和 ALTER SQL。系统会校验 SQL 是否只修改同一个字段。</p>
            </div>
            <el-button type="primary" @click="handleCreateTask">保存任务</el-button>
          </div>
          <div class="form-grid">
            <el-form-item label="目标 Schema">
              <el-input v-model="form.schemaName" placeholder="例如 his" />
            </el-form-item>
            <el-form-item label="目标表">
              <el-input v-model="form.tableName" placeholder="例如 patient" />
            </el-form-item>
            <el-form-item label="目标字段">
              <el-input v-model="form.columnName" placeholder="例如 code" />
            </el-form-item>
            <el-form-item label="锁等待超时">
              <el-input v-model="form.lockTimeout" placeholder="默认 3s" />
            </el-form-item>
            <el-form-item label="执行超时">
              <el-input v-model="form.statementTimeout" placeholder="默认 10min" />
            </el-form-item>
          </div>
          <el-input v-model="form.alterSql" class="sql-input" type="textarea" :rows="5" placeholder="ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);" />
          <div class="mode-panel">
            <div class="mode-title">执行方式</div>
            <el-radio-group v-model="form.executionMode">
              <el-radio-button label="STEP">分步执行</el-radio-button>
              <el-radio-button label="TRANSACTION">事务执行</el-radio-button>
            </el-radio-group>
            <div class="mode-desc">
              <p><strong>分步执行：</strong>删除依赖视图并提交，再修改字段，最后按备份恢复视图。适合大表和维护窗口。</p>
              <p><strong>事务执行：</strong>删除视图、修改字段、恢复视图都在一个事务内完成。适合依赖少、执行快的变更。</p>
            </div>
          </div>
        </section>

        <section class="section">
          <div class="section-head">
            <div>
              <h3>2. 依赖分析</h3>
              <p>分析依赖目标字段的视图，生成删除、恢复、权限、注释、索引和校验 SQL。</p>
            </div>
            <div class="actions">
              <el-button type="warning" :disabled="!canAnalyze" :loading="analyzing" @click="handleAnalyze">分析依赖</el-button>
              <el-button :disabled="!currentTask" :loading="exporting" @click="handleExportPlan">导出执行 SQL</el-button>
              <el-button :disabled="!currentTask" :loading="exporting" @click="handleExportRestorePlan">导出恢复 SQL</el-button>
            </div>
          </div>
          <div class="dependency-stats">
            <div><span>视图</span><strong>{{ dependencySummary.viewCount }}</strong></div>
            <div><span>物化视图</span><strong>{{ dependencySummary.materializedCount }}</strong></div>
            <div><span>最大层级</span><strong>{{ dependencySummary.maxDepth }}</strong></div>
            <div><span>备份状态</span><strong>{{ items.length ? '已生成' : '-' }}</strong></div>
          </div>
          <el-table :data="items" size="small" height="300" border>
            <el-table-column prop="dropOrder" label="删除顺序" width="90" />
            <el-table-column prop="restoreOrder" label="恢复顺序" width="90" />
            <el-table-column label="类型" width="100">
              <template #default="{ row }">{{ objectKindText(row.objectKind) }}</template>
            </el-table-column>
            <el-table-column prop="objectSchema" label="Schema" width="130" />
            <el-table-column prop="objectName" label="对象名" min-width="220" show-overflow-tooltip />
            <el-table-column prop="depth" label="层级" width="80" />
            <el-table-column prop="ownerName" label="Owner" width="140" show-overflow-tooltip />
            <el-table-column prop="status" label="状态" width="110" />
          </el-table>
        </section>

        <section class="section">
          <div class="section-head">
            <div>
              <h3>3. 预检与执行</h3>
              <p>执行前检查字段、锁等待和备份是否仍然有效；预检通过后才能执行变更。</p>
            </div>
            <div class="actions">
              <el-button :disabled="!canPrecheck" :loading="prechecking" @click="handlePrecheck">执行预检</el-button>
              <el-button type="danger" :disabled="!canExecute" :loading="executing" @click="handleExecute">执行变更</el-button>
            </div>
          </div>
          <div class="execution-cards">
            <div>
              <span>锁等待超时</span>
              <strong>{{ currentTask?.lockTimeout || form.lockTimeout || '3s' }}</strong>
              <p>拿不到锁时失败，避免长时间阻塞业务。</p>
            </div>
            <div>
              <span>执行超时</span>
              <strong>{{ currentTask?.statementTimeout || form.statementTimeout || '10min' }}</strong>
              <p>超过时间时中断，避免长期占用资源。</p>
            </div>
            <div>
              <span>执行方式</span>
              <strong>{{ executionModeText(currentExecutionMode) }}</strong>
              <p>{{ currentExecutionMode === 'TRANSACTION' ? '失败由事务回滚。' : '失败后按备份自动恢复视图。' }}</p>
            </div>
          </div>
        </section>

        <section class="section danger-section">
          <div class="section-head">
            <div>
              <h3>4. 结果与恢复</h3>
              <p>执行失败或需要人工兜底时，使用已备份的视图定义恢复依赖对象。</p>
            </div>
            <el-button type="warning" plain :disabled="!canRestore" :loading="restoring" @click="handleRestore">恢复视图</el-button>
          </div>
          <div class="result-line">
            <span>当前结果：</span>
            <el-tag :type="statusTag(currentStatus)" size="small">{{ statusText(currentStatus) }}</el-tag>
            <span class="result-message">{{ currentTask?.executeMessage || '-' }}</span>
          </div>
        </section>

        <section class="section plan-section">
          <div class="section-head">
            <div>
              <h3>SQL 预览</h3>
              <p>导出执行 SQL 或恢复 SQL 后，会在这里保留最近一次导出内容。</p>
            </div>
          </div>
          <el-input v-model="planPreview" type="textarea" :rows="10" readonly placeholder="导出 SQL 后显示" />
        </section>
      </main>

      <aside class="task-panel">
        <div class="panel-title">历史任务</div>
        <div class="task-list" v-loading="loading">
          <div v-for="task in tasks" :key="task.id" class="task-item" :class="{ active: currentTask?.id === task.id }" @click="handleOpenTask(task)">
            <div class="task-name">{{ task.schemaName }}.{{ task.tableName }}.{{ task.columnName }}</div>
            <div class="task-meta">
              <el-tag :type="statusTag(task.status)" size="small">{{ statusText(task.status) }}</el-tag>
              <span>{{ executionModeText(task.executionMode) }}</span>
              <span>{{ formatTimeStr(task.createdAt)?.slice(5) }}</span>
            </div>
          </div>
          <el-empty v-if="!tasks.length" :image-size="48" description="暂无任务" />
        </div>
        <el-pagination v-if="total > pageSize" v-model:current-page="page" small layout="prev, pager, next" :page-size="pageSize" :total="total" @current-change="loadTasks" />
      </aside>
    </div>
  </div>
</template>

<style scoped>
.view-dep-page { display: flex; flex-direction: column; gap: 12px; min-height: calc(100vh - 130px); }
.page-header, .workflow-steps, .task-summary, .section, .task-panel { background: #fff; border: 1px solid #ebeef5; border-radius: 6px; }
.page-header { display: flex; justify-content: space-between; align-items: center; padding: 16px 18px; }
.page-header h2 { margin: 0 0 6px; font-size: 20px; color: #1f2937; }
.page-header p, .section-head p, .mode-desc p, .execution-cards p { margin: 0; color: #909399; font-size: 13px; line-height: 1.6; }
.workflow-steps { padding: 16px 18px; }
.task-summary { display: grid; grid-template-columns: repeat(4, minmax(140px, 1fr)); gap: 12px; padding: 12px 16px; align-items: center; }
.summary-label { display: block; color: #909399; font-size: 12px; margin-bottom: 4px; }
.summary-message { grid-column: span 4; color: #606266; }
.content-grid { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 12px; align-items: start; }
.workspace { min-width: 0; display: flex; flex-direction: column; gap: 12px; }
.section { padding: 14px 16px; }
.section-head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; margin-bottom: 12px; }
.section-head h3 { margin: 0 0 6px; font-size: 16px; color: #303133; }
.form-grid { display: grid; grid-template-columns: repeat(5, minmax(120px, 1fr)); gap: 10px; }
.form-grid :deep(.el-form-item) { margin-bottom: 8px; }
.sql-input { margin-top: 4px; }
.sql-input :deep(textarea), .plan-section :deep(textarea) { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; line-height: 1.5; }
.mode-panel { margin-top: 12px; padding: 12px; border: 1px solid #ebeef5; border-radius: 6px; background: #fafafa; }
.mode-title { font-weight: 600; margin-bottom: 8px; color: #303133; }
.mode-desc { margin-top: 10px; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.actions { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
.dependency-stats { display: grid; grid-template-columns: repeat(4, minmax(120px, 1fr)); gap: 10px; margin-bottom: 12px; }
.dependency-stats > div, .execution-cards > div { border: 1px solid #ebeef5; border-radius: 6px; padding: 10px 12px; background: #fbfcff; }
.dependency-stats span, .execution-cards span { display: block; color: #909399; font-size: 12px; margin-bottom: 6px; }
.dependency-stats strong, .execution-cards strong { color: #1f2937; font-size: 18px; }
.execution-cards { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.danger-section { border-color: #f5dab1; }
.result-line { display: flex; align-items: center; gap: 8px; color: #606266; }
.result-message { color: #909399; }
.task-panel { max-height: calc(100vh - 190px); display: flex; flex-direction: column; overflow: hidden; }
.panel-title { padding: 12px; font-weight: 600; border-bottom: 1px solid #ebeef5; }
.task-list { flex: 1; overflow-y: auto; padding: 8px; }
.task-item { padding: 9px 10px; border: 1px solid transparent; border-radius: 4px; cursor: pointer; margin-bottom: 6px; }
.task-item:hover { background: #f5f7fa; }
.task-item.active { background: #ecf5ff; border-color: #d9ecff; }
.task-name { font-size: 13px; font-weight: 500; color: #303133; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.task-meta { display: flex; gap: 8px; align-items: center; margin-top: 5px; color: #909399; font-size: 12px; flex-wrap: wrap; }
@media (max-width: 1100px) {
  .content-grid { grid-template-columns: 1fr; }
  .task-panel { max-height: 320px; }
  .form-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 760px) {
  .page-header, .section-head { flex-direction: column; align-items: stretch; }
  .task-summary, .dependency-stats, .execution-cards, .mode-desc { grid-template-columns: 1fr; }
  .summary-message { grid-column: span 1; }
  .form-grid { grid-template-columns: 1fr; }
}
</style>

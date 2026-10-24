<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { createViewTaskPoller, viewStageProgress, viewTaskIsBusy } from '../utils/sql-view-progress'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  analyzeSQLViewDependencyTask,
  createSQLViewDependencyTask,
  executeSQLViewDependencyTask,
  getSQLViewDependencyTask,
  restoreSQLViewDependencyItem,
  restoreSQLViewDependencyTask,
  verifySQLViewDependencyItem,
  type SQLViewDependencyChange,
  type SQLViewDependencyItem,
  type SQLViewDependencyStep,
  type SQLViewDependencyTask,
  type SQLViewDependencyTaskResult,
} from '../api/postgresql'
import { getSettings } from '../api/settings'
import {cancelSQLViewTaskTransfer} from '../api/postgresql'
async function cancelTransfer(){if(!currentTask.value)return;try{await ElMessageBox.confirm('撤回后原 SQL 仍保留失败，可返回文件编辑或单独重试。执行中、生效或待核实的任务不能撤回。','撤回转入',{type:'warning'})}catch{return};await cancelSQLViewTaskTransfer(currentTask.value.id);await refreshCurrentTask();ElMessage.success('已撤回关联，原 SQL 保留失败状态')}

const route = useRoute()
const router = useRouter()
const pollError = ref('')
const taskLoading = ref(false)
let disposed = false
let routeLoadGeneration = 0
const analyzing = ref(false)
const executing = ref(false)
const restoring = ref(false)
const backupActionLoading = ref(false)
const backupDrawerVisible = ref(false)
const selectedBackupItem = ref<SQLViewDependencyItem | null>(null)
const backupActiveTab = ref('definition')
const currentTask = ref<SQLViewDependencyTask | null>(null)
const dependencyChanges = ref<SQLViewDependencyChange[]>([])
const dependencyItems = ref<SQLViewDependencyItem[]>([])
const executionSteps = ref<SQLViewDependencyStep[]>([])
const lastTaskSQL = ref('')
const plannedAlterBatches = ref(0)

const form = ref({
  alterSql: '',
  executionMode: 'STEP',
  lockTimeout: '5s',
  statementTimeout: '10min',
})

const dependencyCount = computed(() => dependencyItems.value.length)
const changeCount = computed(() => dependencyChanges.value.length)
const materializedCount = computed(() => dependencyItems.value.filter(item => item.objectKind === 'm').length)
const taskBusy = computed(() => analyzing.value || executing.value || restoring.value || backupActionLoading.value || taskLoading.value || viewTaskIsBusy(currentTask.value?.status))
const sameTaskBlocked = computed(() => currentTask.value?.sourceSyncStatus==='CANCELED'||lastTaskSQL.value === form.value.alterSql.trim() && (taskUncertain.value || currentTask.value?.status === 'SUCCESS' || executionSteps.value.some(s => s.stepName === 'ALTER_TABLE' && ['SUCCESS', 'UNKNOWN', 'RUNNING'].includes(s.status))))
const taskUncertain = computed(() => currentTask.value?.outcomeUncertain || currentTask.value?.status === 'UNKNOWN')
const canRestore = computed(() => Boolean(
  currentTask.value?.id &&
  currentTask.value.sourceSyncStatus !== 'CANCELED' &&
  dependencyItems.value.length > 0 &&
  ['FAILED', 'RESTORE_FAILED', 'VERIFY_FAILED', 'UNKNOWN'].includes(currentTask.value.status),
))
const hasAnalysisResult = computed(() => {
  const status = currentTask.value?.status || ''
  return Boolean(
    dependencyChanges.value.length > 0 ||
      dependencyItems.value.length > 0 ||
      executionSteps.value.length > 0 ||
      currentTask.value?.analyzedAt ||
      ['ANALYZED', 'PRECHECKED', 'PRECHECK_PASSED', 'PRECHECK_FAILED', 'EXECUTING', 'SUCCESS', 'FAILED', 'RESTORING', 'RESTORE_FAILED', 'RESTORED'].includes(status),
  )
})

type ProcessStageStatus = 'pending' | 'running' | 'success' | 'failed' | 'skipped' | 'unneeded'

function stageStatusText(status: ProcessStageStatus) {
  return ({
    pending: '待处理',
    running: '执行中',
    success: '完成',
    failed: '失败',
    skipped: '未执行',
    unneeded: '无需处理',
  } as Record<ProcessStageStatus, string>)[status]
}

function stageStatusType(status: ProcessStageStatus) {
  if (status === 'success') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'running') return 'warning'
  return 'info'
}

function stageFromStepNames(names: string[]) {
  return viewStageProgress(executionSteps.value, names)
}
function completedStageSummary(stage: ReturnType<typeof viewStageProgress>) {
  return `已完成 ${stage.completed}/${stage.count}${stage.skipped ? `，未执行 ${stage.skipped}` : ''}`
}

const completedChanges = computed(() => dependencyChanges.value.filter(change => change.status === 'SUCCESS').length)
const alterBatchCount = computed(() => executionSteps.value.filter(step => step.stepName === 'ALTER_TABLE').length || plannedAlterBatches.value)
const currentStep = computed(() => executionSteps.value.find(step => step.status === 'RUNNING'))
function stepDuration(step: SQLViewDependencyStep) {
  if (!step.startedAt) return '-'
  const seconds = Math.max(0, Math.floor(((step.finishedAt ? Date.parse(step.finishedAt) : Date.now()) - Date.parse(step.startedAt)) / 1000))
  return seconds < 60 ? `${seconds} 秒` : `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`
}
const poller = createViewTaskPoller(
  id => getSQLViewDependencyTask(id, true),
  data => {
    if (data.task.id !== currentTask.value?.id) return
    const previous = currentTask.value.status
    applyTaskResult(data)
    pollError.value = ''
    if (viewTaskIsBusy(previous) && !viewTaskIsBusy(data.task.status)) notifyTaskResult(data)
    if (!taskBusy.value) poller.stop()
  },
  error => { pollError.value = error instanceof Error ? error.message : '进度刷新失败' },
)
watch([() => currentTask.value?.id, () => taskBusy.value], ([id, busy]) => {
  if (!disposed && id && busy) poller.start(id)
  else poller.stop()
}, { flush: 'sync' })
onBeforeUnmount(() => { disposed = true; poller.stop() })
function notifyTaskResult(data: SQLViewDependencyTaskResult) {
  if (data.task.status === 'SUCCESS') ElMessage.success(data.task.executeMessage || '执行完成')
  else if (data.task.status === 'RESTORED') ElMessage.warning(data.task.executeMessage || '视图已恢复')
  else if (['FAILED', 'PRECHECK_FAILED', 'UNKNOWN', 'RESTORE_FAILED'].includes(data.task.status)) ElMessage.error(data.task.executeMessage || '执行失败')
}

const executionStages = computed(() => {
  const backupStatus: ProcessStageStatus = hasAnalysisResult.value ? 'success' : 'pending'
  const noViews = Boolean(currentTask.value?.analyzedAt) && dependencyCount.value === 0
  const drop = stageFromStepNames(['DROP_DEPENDENCY'])
  const alter = stageFromStepNames(['ALTER_TABLE'])
  const restore = stageFromStepNames(['RESTORE_OBJECT', 'REFRESH_MATERIALIZED_VIEW', 'RESTORE_COMMENT', 'RESTORE_GRANT', 'SQL'])
  const verify = stageFromStepNames(['VERIFY'])
  return [
    {
      key: 'backup',
      order: 1,
      title: '备份视图',
      status: backupStatus,
      summary: noViews ? '分析完成，没有依赖视图需要备份' : backupStatus === 'success' ? `已保存 ${dependencyItems.value.length} 个依赖对象定义、权限、注释和索引信息` : '分析后生成依赖对象备份',
      error: '',
    },
    {
      key: 'drop',
      order: 2,
      title: '删除视图',
      status: noViews ? 'unneeded' as const : drop.status,
      summary: noViews ? '没有依赖视图需要删除' : drop.count > 0 ? completedStageSummary(drop) : '执行时按依赖层级删除阻塞对象',
      error: drop.error,
    },
    {
      key: 'alter',
      order: 3,
      title: '执行SQL',
      status: alter.status,
      summary: alter.count > 0 ? `已完成 ${completedChanges.value}/${changeCount.value} 列，${alter.completed}/${alter.count} 批` : '等待字段变更',
      error: alter.error,
    },
    {
      key: 'restore',
      order: 4,
      title: '恢复视图',
      status: noViews ? 'unneeded' as const : restore.status,
      summary: noViews ? '没有依赖视图需要恢复' : restore.count > 0 ? completedStageSummary(restore) : '字段变更后按备份恢复视图和附属对象',
      error: restore.error,
    },
    {
      key: 'verify',
      order: 5,
      title: '视图检测',
      status: noViews ? 'unneeded' as const : verify.status,
      summary: noViews ? '没有依赖视图需要检测' : verify.count > 0 ? completedStageSummary(verify) : '恢复后校验视图存在并可访问',
      error: verify.error,
    },
  ]
})

function taskStatusText(status?: string) {
  return ({
    CREATED: '已创建',
    ANALYZED: '已分析',
    PRECHECKED: '已校验',
    PRECHECK_PASSED: '预检通过',
    PRECHECK_FAILED: '预检失败',
    EXECUTING: '执行中',
    SUCCESS: '成功',
    APPLIED: '待提交',
    RUNNING: '执行中',
    PENDING: '待执行',
    FAILED: '失败',
    UNKNOWN: '结果待核实',
    ANALYZING: '分析中',
    PRECHECKING: '预检中',
    VERIFYING: '校验中',
    SKIPPED: '未执行',
    ROLLED_BACK: '已回滚',
    RESTORING: '恢复中',
    RESTORE_FAILED: '恢复失败',
    RESTORED: '已恢复',
  } as Record<string, string>)[status || ''] || status || '-'
}

function taskStatusType(status?: string) {
  if (status === 'SUCCESS' || status === 'RESTORED') return 'success'
  if (status === 'UNKNOWN' || status === 'ROLLED_BACK' || status === 'FAILED' || status === 'PRECHECK_FAILED' || status === 'RESTORE_FAILED') return 'danger'
  if (status === 'RUNNING' || status === 'APPLIED' || status === 'EXECUTING' || status === 'RESTORING' || status === 'PRECHECK_PASSED') return 'warning'
  return 'info'
}

function objectKindText(kind: string) {
  return kind === 'm' ? '物化视图' : '视图'
}

function backupObjectName(item: SQLViewDependencyItem) {
  return `${item.objectSchema}.${item.objectName}`
}

function joinSQLParts(parts: Array<string | undefined>) {
  return parts.map(part => (part || '').trim()).filter(Boolean).join('\n\n')
}

function backupRestoreSQL(item: SQLViewDependencyItem) {
  return joinSQLParts([
    item.createSql,
    item.restoreRefreshSql,
    item.restoreRulesSql,
    item.restoreTriggersSql,
    item.restoreOwnerSql,
    item.restoreGrantsSql,
    item.restoreCommentsSql,
    item.restoreIndexesSql,
    item.verifySql,
  ])
}

function backupSupplementSQL(item: SQLViewDependencyItem) {
  return joinSQLParts([
    item.restoreRulesSql,
    item.restoreTriggersSql,
    item.restoreOwnerSql,
    item.restoreGrantsSql,
    item.restoreCommentsSql,
    item.restoreIndexesSql,
  ])
}

function prettyBackupJSON(text?: string) {
  const trimmed = (text || '').trim()
  if (!trimmed) return '-'
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return trimmed
  }
}

function syncSelectedBackupItem() {
  if (!selectedBackupItem.value) return
  selectedBackupItem.value = dependencyItems.value.find(item => item.id === selectedBackupItem.value?.id) || selectedBackupItem.value
}

function openBackupDrawer(item?: SQLViewDependencyItem) {
  const target = item || dependencyItems.value[0]
  if (!target) {
    ElMessage.warning('当前任务没有可查看的视图备份')
    return
  }
  selectedBackupItem.value = target
  backupActiveTab.value = 'definition'
  backupDrawerVisible.value = true
}

async function copyBackupSQL() {
  if (!selectedBackupItem.value) return
  const content = backupRestoreSQL(selectedBackupItem.value)
  if (!content) {
    ElMessage.warning('当前备份没有可复制的恢复 SQL')
    return
  }
  await navigator.clipboard.writeText(content)
  ElMessage.success('已复制恢复 SQL')
}

async function handleRestoreBackupItem(item?: SQLViewDependencyItem) {
  const target = item || selectedBackupItem.value
  if (!currentTask.value?.id || !target?.id) {
    ElMessage.warning('请先选择一个视图备份')
    return
  }
  if (!canRestore.value || taskBusy.value) return
  backupActionLoading.value = true
  try {
    await ElMessageBox.confirm(`将按备份恢复 ${backupObjectName(target)}，确认继续？`, '恢复当前视图', {
      type: 'warning',
      confirmButtonText: '恢复当前视图',
    })
    const result = await restoreSQLViewDependencyItem(currentTask.value.id, target.id)
    applyTaskResult(result)
    syncSelectedBackupItem()
    ElMessage.success(result.task.executeMessage || '当前视图恢复完成')
  } catch (err) {
    if (err !== 'cancel') {
      await refreshCurrentTask()
      syncSelectedBackupItem()
      ElMessage.error(err instanceof Error ? err.message : '当前视图恢复失败')
    }
  } finally {
    backupActionLoading.value = false
  }
}

async function handleVerifyBackupItem(item?: SQLViewDependencyItem) {
  const target = item || selectedBackupItem.value
  if (!currentTask.value?.id || !target?.id) {
    ElMessage.warning('请先选择一个视图备份')
    return
  }
  backupActionLoading.value = true
  try {
    const result = await verifySQLViewDependencyItem(currentTask.value.id, target.id)
    applyTaskResult(result)
    syncSelectedBackupItem()
    ElMessage.success(result.task.executeMessage || '当前视图校验完成')
  } catch (err) {
    await refreshCurrentTask()
    syncSelectedBackupItem()
    ElMessage.error(err instanceof Error ? err.message : '当前视图校验失败')
  } finally {
    backupActionLoading.value = false
  }
}

function applyTaskResult(data: SQLViewDependencyTaskResult) {
  if (disposed || (route.query.taskId && String(data.task.id) !== String(route.query.taskId))) return
  // A fast background completion can arrive before the submission response.
  if (data.task.id === currentTask.value?.id && Date.parse(data.task.updatedAt) < Date.parse(currentTask.value.updatedAt)) return
  currentTask.value = data.task
  dependencyItems.value = data.items || []
  dependencyChanges.value = data.changes || []
  executionSteps.value = data.steps || []
  plannedAlterBatches.value = data.alterBatchCount || 0
  syncSelectedBackupItem()
}

async function loadSQLExecutionSettings() {
  try {
    const settings = await getSettings()
    form.value.lockTimeout = settings.sql_execute_lock_timeout || '5s'
    form.value.statementTimeout = settings.sql_execute_statement_timeout || '10min'
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : '加载 SQL 执行配置失败')
  }
}

async function ensureTaskForCurrentSQL() {
  if(currentTask.value?.sourceFileId&&form.value.alterSql.trim()!==lastTaskSQL.value){ElMessage.warning('关联源 SQL 不可在此修改，请处理原任务');return null}
  const sql = form.value.alterSql.trim()
  if (!sql) {
    ElMessage.warning('请输入需要处理视图依赖的 SQL')
    return null
  }
  if (currentTask.value?.id && lastTaskSQL.value === sql) {
    return currentTask.value
  }
  const task = await createSQLViewDependencyTask({
    alterSql: sql,
    executionMode: form.value.executionMode,
    lockTimeout: form.value.lockTimeout,
    statementTimeout: form.value.statementTimeout,
  })
  currentTask.value = task
  await router.replace({ query: { ...route.query, taskId: String(task.id) } })
  dependencyChanges.value = []
  dependencyItems.value = []
  executionSteps.value = []
  plannedAlterBatches.value = 0
  lastTaskSQL.value = sql
  return task
}

async function handleAnalyze() {
  analyzing.value = true
  try {
    const task = await ensureTaskForCurrentSQL()
    if (!task) return
    const data = await analyzeSQLViewDependencyTask(task.id)
    applyTaskResult(data)
    ElMessage.success(`分析完成，字段变更 ${dependencyChanges.value.length} 条，依赖对象 ${dependencyItems.value.length} 个`)
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : '分析失败')
  } finally {
    analyzing.value = false
  }
}

async function handleExecute() {
  executing.value = true
  try {
    let task = await ensureTaskForCurrentSQL()
    if (!task) return
    if (!task.analyzedAt) {
      const data = await analyzeSQLViewDependencyTask(task.id)
      applyTaskResult(data)
      task = data.task
    }
    await ElMessageBox.confirm(
      dependencyItems.value.length === 0
        ? `分析未发现依赖视图，将执行 ${dependencyChanges.value.length || 1} 条字段变更，无需删除或重建视图。确认继续？`
        : `系统会处理 ${dependencyChanges.value.length || 1} 条字段变更，按 ${alterBatchCount.value || dependencyChanges.value.length || 1} 批执行：先备份并删除依赖视图，再执行字段变更 SQL，最后按备份重建视图。执行期间相关视图会短暂不可用，确认继续？`,
      '执行确认',
      { type: 'warning', confirmButtonText: '执行' },
    )
    const result = await executeSQLViewDependencyTask(task.id)
    applyTaskResult(result)
    if (viewTaskIsBusy(result.task.status)) ElMessage.success('任务已提交')
    else notifyTaskResult(result)
  } catch (err) {
    if (err !== 'cancel') {
      await refreshCurrentTask()
      ElMessage.error(err instanceof Error ? err.message : '执行失败')
    }
  } finally {
    executing.value = false
  }
}

async function handleRestore() {
  if (!currentTask.value?.id) {
    ElMessage.warning('请先分析或执行一个 SQL')
    return
  }
  if (!canRestore.value || taskBusy.value) return
  restoring.value = true
  try {
    await ElMessageBox.confirm('将按当前任务的备份信息恢复依赖视图，确认恢复？', '恢复确认', { type: 'warning', confirmButtonText: '恢复' })
    const result = await restoreSQLViewDependencyTask(currentTask.value.id)
    applyTaskResult(result)
    ElMessage.success(result.task.executeMessage || '恢复完成')
  } catch (err) {
    if (err !== 'cancel') {
      await refreshCurrentTask()
      ElMessage.error(err instanceof Error ? err.message : '恢复失败')
    }
  } finally {
    restoring.value = false
  }
}

async function refreshCurrentTask() {
  if (!currentTask.value?.id || disposed) return
  await poller.start(currentTask.value.id)
}

async function loadRouteTask() {
  const id = Number(route.query.taskId)
  if (id === currentTask.value?.id) return
  const generation = ++routeLoadGeneration
  currentTask.value = null
  dependencyChanges.value = []
  dependencyItems.value = []
  executionSteps.value = []
  plannedAlterBatches.value = 0
  lastTaskSQL.value = ''
  poller.stop()
  if (!Number.isSafeInteger(id) || id <= 0) {
    form.value.alterSql = ''
    taskLoading.value = false
    return
  }
  taskLoading.value = true
  try {
    const data = await getSQLViewDependencyTask(id)
    if (disposed || generation !== routeLoadGeneration) return
    form.value.alterSql = data.task.alterSql
    form.value.executionMode = data.task.executionMode || 'STEP'
    form.value.lockTimeout = data.task.lockTimeout || form.value.lockTimeout
    form.value.statementTimeout = data.task.statementTimeout || form.value.statementTimeout
    lastTaskSQL.value = data.task.alterSql.trim()
    applyTaskResult(data)
  } catch (error) {
    if (!disposed && generation === routeLoadGeneration) ElMessage.error(error instanceof Error ? error.message : '加载任务失败')
  } finally {
    if (generation === routeLoadGeneration) taskLoading.value = false
  }
}
watch(() => route.query.taskId, loadRouteTask)
onMounted(async () => {
  await loadSQLExecutionSettings()
  if (!disposed) await loadRouteTask()
})

</script>

<template>
  <div class="view-dependency-page">
    <section class="work-card">
      <div class="sql-header">
        <div>
          <h2>视图依赖变更</h2>
          <h3>变更 SQL</h3>
        </div>
        <div class="actions">
          <el-button :disabled="taskBusy || sameTaskBlocked" :loading="analyzing" @click="handleAnalyze">分析</el-button>
          <el-button type="primary" :disabled="taskBusy || sameTaskBlocked" :loading="executing" @click="handleExecute">执行</el-button>
          <el-button :type="canRestore ? 'warning' : 'info'" :disabled="!canRestore || taskBusy" :loading="restoring" @click="handleRestore">恢复</el-button>
        </div>
      </div>
      <el-input
        type="textarea"
        v-model="form.alterSql"
        :disabled="taskBusy||!!currentTask?.sourceFileId"
        :rows="8"
        resize="vertical"
        class="sql-input"
        placeholder="ALTER TABLE df_zhushuju.patient ALTER COLUMN code TYPE varchar(64);"
      />
    </section>

    <div v-if="currentTask?.sourceFileId" class="source-task-note">来源：{{currentTask.sourceFileName}} · 任务 #{{currentTask.id}} · {{currentTask.sourceSyncStatus==='SUCCESS'?'已回写源 SQL 成功状态':currentTask.sourceSyncStatus==='CANCELED'?'已撤回，原 SQL 保留失败':'等待字段变更、视图恢复与校验完成后回写源 SQL'}}<el-button link type="primary" @click="router.push({name:'PostgreSQLSQLExecution',query:{fileId:String(currentTask.sourceFileId)}})">返回原 SQL 文件</el-button><el-button v-if="currentTask.sourceSyncStatus==='WAITING'&&!taskBusy&&!taskUncertain" link type="warning" @click="cancelTransfer">撤回转入</el-button></div>
    <el-alert v-if="taskUncertain" title="字段变更结果待核实，禁止重放变更。可按备份恢复并校验视图；恢复视图不会确认字段变更结果。" type="warning" :closable="false" />
    <el-alert v-if="pollError" :title="`进度刷新失败：${pollError}，正在重试`" type="warning" :closable="false" />
    <section v-if="hasAnalysisResult" class="process-card">
      <div class="table-header">
        <div>
          <h3>执行过程<span v-if="alterBatchCount" class="progress-count">{{ completedChanges }}/{{ changeCount }} 列 · {{ alterBatchCount }} 批</span></h3>
        </div>
        <el-button size="small" :disabled="!currentTask" @click="refreshCurrentTask">刷新</el-button>
      </div>
      <div v-if="currentStep" class="current-step">
        <el-tag type="warning">{{ taskStatusText(currentTask?.status) }}</el-tag>
        <span class="step-sql" :title="currentStep.sqlContent">{{ currentStep.sqlContent }}</span>
        <span class="step-duration">{{ stepDuration(currentStep) }}</span>
      </div>
      <div class="stage-list">
        <div v-for="stage in executionStages" :key="stage.key" :class="['stage-item', `is-${stage.status}`]">
          <div class="stage-title">
            <span class="stage-index">{{ stage.order }}</span>
            <strong>{{ stage.title }}</strong>
            <el-tag size="small" :type="stageStatusType(stage.status)">{{ stageStatusText(stage.status) }}</el-tag>
          </div>
          <p>{{ stage.summary }}</p>
          <span v-if="stage.error" class="stage-error">{{ stage.error }}</span>
          <el-button
            v-if="stage.key === 'backup' && dependencyItems.length"
            class="stage-action"
            size="small"
            text
            type="primary"
            @click="openBackupDrawer()"
          >
            查看备份
          </el-button>
        </div>
      </div>
    </section>

    <section v-if="hasAnalysisResult" class="summary-card">
      <div class="summary-item">
        <span>字段变更</span>
        <strong>{{ changeCount }}</strong>
      </div>
      <div class="summary-item">
        <span>首个目标</span>
        <strong v-if="currentTask">{{ currentTask.schemaName }}.{{ currentTask.tableName }}.{{ currentTask.columnName }}</strong>
        <strong v-else>-</strong>
      </div>
      <div class="summary-item">
        <span>任务状态</span>
        <el-tag :type="taskStatusType(currentTask?.status)">{{ taskStatusText(currentTask?.status) }}</el-tag>
      </div>
      <div class="summary-item">
        <span>依赖视图</span>
        <strong>{{ dependencyCount }}</strong>
      </div>
      <div class="summary-item">
        <span>物化视图</span>
        <strong>{{ materializedCount }}</strong>
      </div>
    </section>

    <section v-if="hasAnalysisResult" class="change-card">
      <div class="table-header">
        <div>
          <h3>字段变更</h3>
        </div>
      </div>
      <el-table :data="dependencyChanges" size="small" border stripe max-height="260">
        <el-table-column prop="changeOrder" label="顺序" width="70" align="center" />
        <el-table-column prop="lineNumber" label="行号" width="80" align="center" />
        <el-table-column label="目标字段" min-width="260">
          <template #default="{ row }">
            <span class="object-name">{{ row.schemaName }}.{{ row.tableName }}.{{ row.columnName }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag size="small" :type="taskStatusType(row.status)">{{ taskStatusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="执行批次" width="100" align="center">
          <template #default="{ row }">{{ executionSteps.find(step => step.id === row.executionStepId)?.stepOrder || '-' }}</template>
        </el-table-column>
        <el-table-column prop="alterSql" label="SQL" min-width="420" show-overflow-tooltip />
        <el-table-column prop="errorMessage" label="错误" min-width="220" show-overflow-tooltip />
      </el-table>
    </section>

    <section v-if="hasAnalysisResult" class="dependency-card">
      <div class="table-header">
        <div>
          <h3>依赖对象</h3>
        </div>
      </div>
      <el-table :data="dependencyItems" size="small" border stripe max-height="50vh">
        <el-table-column type="index" width="58" align="center" label="序号" />
        <el-table-column label="对象" min-width="260">
          <template #default="{ row }">
            <span class="object-name">{{ row.objectSchema }}.{{ row.objectName }}</span>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="110">
          <template #default="{ row }">{{ objectKindText(row.objectKind) }}</template>
        </el-table-column>
        <el-table-column prop="depth" label="层级" width="80" align="center" />
        <el-table-column prop="dropOrder" label="删除顺序" width="100" align="center" />
        <el-table-column prop="restoreOrder" label="恢复顺序" width="100" align="center" />
        <el-table-column prop="ownerName" label="Owner" width="140" show-overflow-tooltip />
        <el-table-column prop="status" label="状态" width="110" show-overflow-tooltip />
        <el-table-column prop="errorMessage" label="错误" min-width="240" show-overflow-tooltip />
        <el-table-column label="操作" width="210" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" size="small" @click="openBackupDrawer(row)">查看备份</el-button>
            <el-button link :type="canRestore ? 'warning' : 'info'" size="small" :disabled="!canRestore || taskBusy" :loading="backupActionLoading" @click="handleRestoreBackupItem(row)">恢复</el-button>
            <el-button link type="success" size="small" :disabled="taskBusy" :loading="backupActionLoading" @click="handleVerifyBackupItem(row)">校验</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <el-drawer v-model="backupDrawerVisible" title="视图备份详情" size="58%" append-to-body>
      <div v-if="selectedBackupItem" class="backup-drawer">
        <div class="backup-summary">
          <div>
            <span>对象</span>
            <strong>{{ backupObjectName(selectedBackupItem) }}</strong>
          </div>
          <div>
            <span>类型</span>
            <strong>{{ objectKindText(selectedBackupItem.objectKind) }}</strong>
          </div>
          <div>
            <span>Owner</span>
            <strong>{{ selectedBackupItem.ownerName || '-' }}</strong>
          </div>
          <div>
            <span>备份哈希</span>
            <strong>{{ selectedBackupItem.backupHash || '-' }}</strong>
          </div>
          <div>
            <span>状态</span>
            <strong>{{ selectedBackupItem.status || '-' }}</strong>
          </div>
        </div>

        <el-tabs v-model="backupActiveTab">
          <el-tab-pane label="视图定义" name="definition">
            <h4>原始定义</h4>
            <pre class="backup-code">{{ selectedBackupItem.definition || '-' }}</pre>
            <h4>重建 SQL</h4>
            <pre class="backup-code">{{ selectedBackupItem.createSql || '-' }}</pre>
          </el-tab-pane>
          <el-tab-pane label="恢复脚本" name="restore">
            <div class="backup-toolbar">
              <el-button size="small" @click="copyBackupSQL">复制恢复 SQL</el-button>
            </div>
            <pre class="backup-code">{{ backupRestoreSQL(selectedBackupItem) || '-' }}</pre>
          </el-tab-pane>
          <el-tab-pane label="权限注释索引" name="metadata">
            <h4>附属对象恢复 SQL</h4>
            <pre class="backup-code">{{ backupSupplementSQL(selectedBackupItem) || '-' }}</pre>
            <h4>备份元数据</h4>
            <div class="backup-json-grid">
              <div>
                <span>权限</span>
                <pre class="backup-code">{{ prettyBackupJSON(selectedBackupItem.grantsJson) }}</pre>
              </div>
              <div>
                <span>注释</span>
                <pre class="backup-code">{{ prettyBackupJSON(selectedBackupItem.commentsJson) }}</pre>
              </div>
              <div>
                <span>索引</span>
                <pre class="backup-code">{{ prettyBackupJSON(selectedBackupItem.indexesJson) }}</pre>
              </div>
              <div>
                <span>规则</span>
                <pre class="backup-code">{{ prettyBackupJSON(selectedBackupItem.rulesJson) }}</pre>
              </div>
              <div>
                <span>触发器</span>
                <pre class="backup-code">{{ prettyBackupJSON(selectedBackupItem.triggersJson) }}</pre>
              </div>
              <div>
                <span>选项</span>
                <pre class="backup-code">{{ prettyBackupJSON(selectedBackupItem.optionsJson) }}</pre>
              </div>
            </div>
          </el-tab-pane>
        </el-tabs>

        <div class="backup-footer">
          <el-button :disabled="taskBusy" :loading="backupActionLoading" @click="handleVerifyBackupItem()">校验当前视图</el-button>
          <el-button :type="canRestore ? 'warning' : 'info'" :disabled="!canRestore || taskBusy" :loading="backupActionLoading" @click="handleRestoreBackupItem()">恢复当前视图</el-button>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.source-task-note{display:flex;align-items:center;flex-wrap:wrap;gap:8px 12px;padding:12px;border:1px solid var(--el-border-color);border-radius:6px;font-size:13px;line-height:1.6;overflow-wrap:anywhere}

.view-dependency-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
}

.work-card,
.process-card,
.summary-card,
.change-card,
.dependency-card {
  background: #fff;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
}

.work-card h2,
.work-card h3,
.process-card h3,
.change-card h3,
.dependency-card h3 {
  margin: 0;
  color: #303133;
}

.work-card p,
.process-card p,
.change-card p,
.dependency-card p {
  margin: 8px 0 0;
  color: #909399;
}

.work-card h2 {
  margin-bottom: 12px;
}

.work-card {
  padding: 16px 18px;
}

.sql-header,
.table-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.sql-input :deep(.el-textarea__inner) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  line-height: 1.55;
}

.process-card {
  padding: 16px 18px;
}

.progress-count {
  margin-left: 12px;
  color: #606266;
  font-size: 13px;
  font-weight: 400;
}
.current-step {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
  padding: 10px 12px;
  border-radius: 4px;
  background: #f5f7fa;
}
.step-sql {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font-family: ui-monospace, monospace;
}
.step-duration { flex-shrink: 0; color: #606266; }

.stage-list {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 10px;
}

.stage-item {
  min-height: 112px;
  padding: 12px;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
  background: #fafafa;
}

.stage-item.is-success {
  border-color: #b3e19d;
  background: #f0f9eb;
}

.stage-item.is-running {
  border-color: #f3d19e;
  background: #fdf6ec;
}

.stage-item.is-failed {
  border-color: #fab6b6;
  background: #fef0f0;
}

.stage-title {
  display: flex;
  align-items: center;
  gap: 8px;
}

.stage-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 50%;
  color: #fff;
  background: #409eff;
  font-size: 12px;
  font-weight: 600;
  flex-shrink: 0;
}

.stage-error {
  display: block;
  margin-top: 8px;
  color: #f56c6c;
  font-size: 12px;
  word-break: break-all;
}

.stage-action {
  margin-top: 8px;
  padding-left: 0;
}

.summary-card {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
}

.summary-item {
  padding: 14px 16px;
  border-right: 1px solid #ebeef5;
}

.summary-item:last-child {
  border-right: 0;
}

.summary-item span {
  display: block;
  margin-bottom: 8px;
  color: #909399;
}

.summary-item strong {
  color: #303133;
}

.change-card,
.dependency-card {
  padding: 16px 18px;
}

.object-name {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}

.backup-drawer {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.backup-summary {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  border: 1px solid #ebeef5;
  border-radius: 6px;
  overflow: hidden;
}

.backup-summary > div {
  padding: 10px 12px;
  border-right: 1px solid #ebeef5;
  background: #fafafa;
  min-width: 0;
}

.backup-summary > div:last-child {
  border-right: 0;
}

.backup-summary span,
.backup-json-grid span {
  display: block;
  margin-bottom: 6px;
  color: #909399;
  font-size: 12px;
}

.backup-summary strong {
  display: block;
  color: #303133;
  word-break: break-all;
}

.backup-toolbar,
.backup-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-bottom: 10px;
}

.backup-footer {
  margin-top: 4px;
}

.backup-code {
  max-height: 280px;
  margin: 8px 0 14px;
  padding: 10px 12px;
  overflow: auto;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  background: #f8fafc;
  color: #303133;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  white-space: pre-wrap;
}

.backup-json-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

@media (max-width: 1100px) {
  .summary-card {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .stage-list {
    grid-template-columns: 1fr;
  }

  .sql-header {
    flex-direction: column;
  }

  .backup-summary,
  .backup-json-grid {
    grid-template-columns: 1fr;
  }
}
</style>

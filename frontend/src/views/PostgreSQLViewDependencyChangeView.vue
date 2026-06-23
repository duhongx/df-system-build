<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
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

const loading = ref(false)
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

const form = ref({
  alterSql: '',
  executionMode: 'STEP',
  lockTimeout: '5s',
  statementTimeout: '10min',
})

const dependencyCount = computed(() => dependencyItems.value.length)
const changeCount = computed(() => dependencyChanges.value.length)
const materializedCount = computed(() => dependencyItems.value.filter(item => item.objectKind === 'm').length)
const canRestore = computed(() => Boolean(currentTask.value?.id && dependencyItems.value.length > 0))
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

type ProcessStageStatus = 'pending' | 'running' | 'success' | 'failed'

function stageStatusText(status: ProcessStageStatus) {
  return ({
    pending: '待处理',
    running: '执行中',
    success: '完成',
    failed: '失败',
  } as Record<ProcessStageStatus, string>)[status]
}

function stageStatusType(status: ProcessStageStatus) {
  if (status === 'success') return 'success'
  if (status === 'failed') return 'danger'
  if (status === 'running') return 'warning'
  return 'info'
}

function stageFromStepNames(names: string[]): { status: ProcessStageStatus; count: number; error: string } {
  const matched = executionSteps.value.filter(step => names.includes(step.stepName))
  if (matched.some(step => step.status === 'FAILED')) {
    return { status: 'failed', count: matched.length, error: matched.find(step => step.status === 'FAILED')?.errorMessage || '' }
  }
  if (matched.some(step => step.status === 'RUNNING')) {
    return { status: 'running', count: matched.length, error: '' }
  }
  if (matched.length > 0 && matched.every(step => step.status === 'SUCCESS')) {
    return { status: 'success', count: matched.length, error: '' }
  }
  return { status: 'pending', count: matched.length, error: '' }
}

const executionStages = computed(() => {
  const backupStatus: ProcessStageStatus = hasAnalysisResult.value ? 'success' : 'pending'
  const drop = stageFromStepNames(['DROP_DEPENDENCY'])
  const alter = stageFromStepNames(['ALTER_TABLE'])
  const restore = stageFromStepNames(['RESTORE_OBJECT', 'REFRESH_MATERIALIZED_VIEW', 'RESTORE_COMMENT', 'RESTORE_GRANT'])
  const verify = stageFromStepNames(['VERIFY'])
  return [
    {
      key: 'backup',
      order: 1,
      title: '备份视图',
      status: backupStatus,
      summary: backupStatus === 'success' ? `已保存 ${dependencyItems.value.length} 个依赖对象定义、权限、注释和索引信息` : '分析后生成依赖对象备份',
      error: '',
    },
    {
      key: 'drop',
      order: 2,
      title: '删除视图',
      status: drop.status,
      summary: drop.count > 0 ? `已处理 ${drop.count} 条删除依赖对象 SQL` : '执行时按依赖层级删除阻塞对象',
      error: drop.error,
    },
    {
      key: 'alter',
      order: 3,
      title: '执行SQL',
      status: alter.status,
      summary: alter.count > 0 ? `已处理 ${alter.count} 条字段变更 SQL` : '删除依赖后执行原始字段变更 SQL',
      error: alter.error,
    },
    {
      key: 'restore',
      order: 4,
      title: '恢复视图',
      status: restore.status,
      summary: restore.count > 0 ? `已处理 ${restore.count} 条重建、权限和注释恢复 SQL` : '字段变更后按备份恢复视图和附属对象',
      error: restore.error,
    },
    {
      key: 'verify',
      order: 5,
      title: '视图检测',
      status: verify.status,
      summary: verify.count > 0 ? `已处理 ${verify.count} 条存在性检测 SQL` : '恢复后校验视图存在并可访问',
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
    FAILED: '失败',
    RESTORING: '恢复中',
    RESTORE_FAILED: '恢复失败',
    RESTORED: '已恢复',
  } as Record<string, string>)[status || ''] || status || '-'
}

function taskStatusType(status?: string) {
  if (status === 'SUCCESS' || status === 'RESTORED') return 'success'
  if (status === 'FAILED' || status === 'PRECHECK_FAILED' || status === 'RESTORE_FAILED') return 'danger'
  if (status === 'EXECUTING' || status === 'RESTORING' || status === 'PRECHECK_PASSED') return 'warning'
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
  currentTask.value = data.task
  dependencyItems.value = data.items || []
  dependencyChanges.value = data.changes || []
  executionSteps.value = data.steps || []
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
  dependencyChanges.value = []
  dependencyItems.value = []
  executionSteps.value = []
  lastTaskSQL.value = sql
  return task
}

async function handleAnalyze() {
  analyzing.value = true
  loading.value = true
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
    loading.value = false
  }
}

async function handleExecute() {
  executing.value = true
  loading.value = true
  try {
    let task = await ensureTaskForCurrentSQL()
    if (!task) return
    if (dependencyItems.value.length === 0) {
      const data = await analyzeSQLViewDependencyTask(task.id)
      applyTaskResult(data)
      task = data.task
    }
    await ElMessageBox.confirm(
      `系统会处理 ${dependencyChanges.value.length || 1} 条字段变更：先备份并删除依赖视图，再执行字段变更 SQL，最后按备份重建视图。执行期间相关视图会短暂不可用，确认继续？`,
      '执行确认',
      { type: 'warning', confirmButtonText: '执行' },
    )
    const result = await executeSQLViewDependencyTask(task.id)
    applyTaskResult(result)
    ElMessage.success(result.task.executeMessage || '执行完成')
  } catch (err) {
    if (err !== 'cancel') {
      ElMessage.error(err instanceof Error ? err.message : '执行失败')
    }
  } finally {
    executing.value = false
    loading.value = false
  }
}

async function handleRestore() {
  if (!currentTask.value?.id) {
    ElMessage.warning('请先分析或执行一个 SQL')
    return
  }
  restoring.value = true
  loading.value = true
  try {
    await ElMessageBox.confirm('将按当前任务的备份信息恢复依赖视图，确认恢复？', '恢复确认', { type: 'warning', confirmButtonText: '恢复' })
    const result = await restoreSQLViewDependencyTask(currentTask.value.id)
    applyTaskResult(result)
    ElMessage.success(result.task.executeMessage || '恢复完成')
  } catch (err) {
    if (err !== 'cancel') {
      ElMessage.error(err instanceof Error ? err.message : '恢复失败')
    }
  } finally {
    restoring.value = false
    loading.value = false
  }
}

async function refreshCurrentTask() {
  if (!currentTask.value?.id) return
  try {
    const data = await getSQLViewDependencyTask(currentTask.value.id)
    applyTaskResult(data)
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : '刷新任务失败')
  }
}

onMounted(loadSQLExecutionSettings)
</script>

<template>
  <div class="view-dependency-page" v-loading="loading">
    <section class="work-card">
      <div class="sql-header">
        <div>
          <h2>视图依赖变更</h2>
          <h3>变更 SQL</h3>
          <p>可粘贴多条 ALTER TABLE ... ALTER COLUMN ... TYPE ... SQL；系统会统一分析依赖、备份、执行和恢复。</p>
        </div>
        <div class="actions">
          <el-button :loading="analyzing" @click="handleAnalyze">分析</el-button>
          <el-button type="primary" :loading="executing" @click="handleExecute">执行</el-button>
          <el-button type="warning" :disabled="!canRestore" :loading="restoring" @click="handleRestore">恢复</el-button>
        </div>
      </div>
      <el-input
        type="textarea"
        v-model="form.alterSql"
        :rows="8"
        resize="vertical"
        class="sql-input"
        placeholder="ALTER TABLE his.patient ALTER COLUMN code TYPE varchar(64);"
      />
    </section>

    <section v-if="hasAnalysisResult" class="process-card">
      <div class="table-header">
        <div>
          <h3>执行过程</h3>
          <p>展示本次任务从备份、删除依赖、执行变更到恢复检测的处理过程。</p>
        </div>
        <el-button size="small" :disabled="!currentTask" @click="refreshCurrentTask">刷新</el-button>
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
          <p>分析后会列出本次任务中的每一条字段类型变更，执行时按 SQL 原始顺序处理。</p>
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
        <el-table-column prop="alterSql" label="SQL" min-width="420" show-overflow-tooltip />
        <el-table-column prop="errorMessage" label="错误" min-width="220" show-overflow-tooltip />
      </el-table>
    </section>

    <section v-if="hasAnalysisResult" class="dependency-card">
      <div class="table-header">
        <div>
          <h3>依赖对象</h3>
          <p>执行时会按删除顺序处理依赖对象，变更后再按恢复顺序重建。</p>
        </div>
      </div>
      <el-table :data="dependencyItems" size="small" border stripe height="420">
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
            <el-button link type="warning" size="small" :loading="backupActionLoading" @click="handleRestoreBackupItem(row)">恢复</el-button>
            <el-button link type="success" size="small" :loading="backupActionLoading" @click="handleVerifyBackupItem(row)">校验</el-button>
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
          <el-button :loading="backupActionLoading" @click="handleVerifyBackupItem()">校验当前视图</el-button>
          <el-button type="warning" :loading="backupActionLoading" @click="handleRestoreBackupItem()">恢复当前视图</el-button>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
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

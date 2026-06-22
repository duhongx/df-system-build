<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  analyzeSQLViewDependencyTask,
  createSQLViewDependencyTask,
  executeSQLViewDependencyTask,
  getSQLViewDependencyTask,
  restoreSQLViewDependencyTask,
  type SQLViewDependencyChange,
  type SQLViewDependencyItem,
  type SQLViewDependencyTask,
} from '../api/postgresql'
import { getSettings } from '../api/settings'

const loading = ref(false)
const analyzing = ref(false)
const executing = ref(false)
const restoring = ref(false)
const currentTask = ref<SQLViewDependencyTask | null>(null)
const dependencyChanges = ref<SQLViewDependencyChange[]>([])
const dependencyItems = ref<SQLViewDependencyItem[]>([])
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
    RESTORE_FAILED: '恢复失败',
    RESTORED: '已恢复',
  } as Record<string, string>)[status || ''] || status || '-'
}

function taskStatusType(status?: string) {
  if (status === 'SUCCESS' || status === 'RESTORED') return 'success'
  if (status === 'FAILED' || status === 'PRECHECK_FAILED' || status === 'RESTORE_FAILED') return 'danger'
  if (status === 'EXECUTING' || status === 'PRECHECK_PASSED') return 'warning'
  return 'info'
}

function objectKindText(kind: string) {
  return kind === 'm' ? '物化视图' : '视图'
}

function applyTaskResult(data: { task: SQLViewDependencyTask; items?: SQLViewDependencyItem[]; changes?: SQLViewDependencyChange[] }) {
  currentTask.value = data.task
  dependencyItems.value = data.items || []
  dependencyChanges.value = data.changes || []
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
    <section class="page-header">
      <div>
        <h2>视图依赖变更</h2>
        <p>输入被视图依赖阻塞的字段变更 SQL，先分析依赖，再执行备份、变更和视图重建；失败后可按备份恢复。</p>
      </div>
      <el-button size="small" :disabled="!currentTask" @click="refreshCurrentTask">刷新当前结果</el-button>
    </section>

    <section class="work-card">
      <div class="sql-header">
        <div>
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

    <section class="summary-card">
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

    <section class="change-card">
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

    <section class="dependency-card">
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
      </el-table>
    </section>
  </div>
</template>

<style scoped>
.view-dependency-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 16px;
}

.page-header,
.work-card,
.summary-card,
.change-card,
.dependency-card {
  background: #fff;
  border: 1px solid #e4e7ed;
  border-radius: 6px;
}

.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding: 16px 18px;
}

.page-header h2,
.work-card h3,
.change-card h3,
.dependency-card h3 {
  margin: 0;
  color: #303133;
}

.page-header p,
.work-card p,
.change-card p,
.dependency-card p {
  margin: 8px 0 0;
  color: #909399;
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

@media (max-width: 1100px) {
  .summary-card {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .sql-header,
  .page-header {
    flex-direction: column;
  }
}
</style>

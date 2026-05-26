<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  cancelSQLFile,
  deleteSQLFile,
  executeSQLBatch,
  executeSQLFileWithOptions,
  exportNotExecutableSQL,
  getSQLBatch,
  getSQLFile,
  importServerSQL,
  listSQLBatches,
  listDoneSQLFiles,
  listTodoSQLFiles,
  parseSQLBatch,
  parseSQLFile,
  skipSQLFile,
  skipSQLStatement,
  type ParseSQLBatchFile,
  type SQLChangeBatch,
  type SQLChangeFile,
  type SQLChangeStatement,
  type SQLExecuteOptions,
} from '../api/postgresql'
import { formatTime as formatTimeStr } from '../utils/time'

const parsing = ref(false)
const executing = ref(false)
const importing = ref(false)
const loadingTodo = ref(false)
const loadingDone = ref(false)

const form = ref({ fileName: '', content: '', overwrite: true })
const serverFilePath = ref('')
const batchFiles = ref<ParseSQLBatchFile[]>([])
const options = ref<SQLExecuteOptions>({
  skipExistsColumn: true,
  skipExistsTable: true,
  skipUniqueConstraint: true,
  requireRiskConfirmation: true,
  confirmWarnRisk: false,
})

const currentBatch = ref<SQLChangeBatch | null>(null)
const batchFileList = ref<SQLChangeFile[]>([])
const currentFile = ref<SQLChangeFile | null>(null)
const statements = ref<SQLChangeStatement[]>([])
const batches = ref<SQLChangeBatch[]>([])
const todoFiles = ref<SQLChangeFile[]>([])
const doneFiles = ref<SQLChangeFile[]>([])
const batchTotal = ref(0)
const todoTotal = ref(0)
const doneTotal = ref(0)
const batchPage = ref(1)
const todoPage = ref(1)
const donePage = ref(1)
const pageSize = 10

const totalCount = computed(() => statements.value.length)
const successCount = computed(() => statements.value.filter(s => s.executeStatus === 'SUCCESS').length)
const failedCount = computed(() => statements.value.filter(s => s.executeStatus === 'FAILED').length)
const notExecutableCount = computed(() => statements.value.filter(s => s.executeStatus === 'NOT_EXECUTABLE' || s.riskLevel === 'BLOCKED').length)
const skippedCount = computed(() => statements.value.filter(s => s.executeStatus === 'SKIPPED').length)

onMounted(() => {
  loadBatches()
  loadTodo()
  loadDone()
})

async function loadBatches() {
  const data = await listSQLBatches(batchPage.value, pageSize)
  batches.value = data.list || []
  batchTotal.value = data.total || 0
}

async function loadTodo() {
  loadingTodo.value = true
  try {
    const data = await listTodoSQLFiles(todoPage.value, pageSize)
    todoFiles.value = data.list || []
    todoTotal.value = data.total || 0
  } finally {
    loadingTodo.value = false
  }
}

async function loadDone() {
  loadingDone.value = true
  try {
    const data = await listDoneSQLFiles(donePage.value, pageSize)
    doneFiles.value = data.list || []
    doneTotal.value = data.total || 0
  } finally {
    loadingDone.value = false
  }
}

async function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || [])
  if (files.length === 0) return
  const parsedFiles = await Promise.all(files.map(async file => ({
    fileName: file.name,
    content: await file.text(),
  })))
  batchFiles.value = parsedFiles
  form.value.fileName = parsedFiles[0].fileName
  form.value.content = parsedFiles[0].content
  if (parsedFiles.length > 1) {
    ElMessage.success(`已选择 ${parsedFiles.length} 个 SQL 文件，可解析为批次`)
  }
  input.value = ''
}

async function handleParse() {
  if (batchFiles.value.length > 1) {
    await handleParseBatch()
    return
  }
  if (!form.value.content.trim()) {
    ElMessage.warning('请上传 SQL 文件或粘贴 SQL 内容')
    return
  }
  parsing.value = true
  try {
    const data = await parseSQLFile(form.value)
    currentFile.value = data.file
    statements.value = data.statements || []
    ElMessage.success(`已解析 ${statements.value.length} 条 SQL`)
    await loadTodo()
  } finally {
    parsing.value = false
  }
}

async function handleParseBatch() {
  if (batchFiles.value.length === 0) {
    ElMessage.warning('请选择 SQL 文件')
    return
  }
  parsing.value = true
  try {
    const data = await parseSQLBatch({
      batchName: form.value.fileName.replace(/\.[^.]+$/, '') || undefined,
      overwrite: form.value.overwrite,
      files: batchFiles.value,
    })
    currentBatch.value = data.batch
    batchFileList.value = data.files || []
    ElMessage.success(`已解析批次，包含 ${batchFileList.value.length} 个 SQL 文件`)
    await Promise.all([loadBatches(), loadTodo()])
  } finally {
    parsing.value = false
  }
}

async function handleExecuteContent() {
  if (!form.value.content.trim()) {
    ElMessage.warning('请上传 SQL 文件或粘贴 SQL 内容')
    return
  }
  executing.value = true
  try {
    const parsed = await parseSQLFile(form.value)
    const confirmedOptions = await buildConfirmedOptions(parsed.statements || [])
    const data = await executeSQLFileWithOptions(parsed.file.id, confirmedOptions)
    currentFile.value = data.file
    statements.value = data.statements || []
    ElMessage.success(data.file.executeMessage || '执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally {
    executing.value = false
  }
}

async function handleExecuteFile(row = currentFile.value) {
  if (!row) {
    ElMessage.warning('请选择 SQL 文件')
    return
  }
  executing.value = true
  try {
    const loaded = await getSQLFile(row.id)
    const confirmedOptions = await buildConfirmedOptions(loaded.statements || [])
    const data = await executeSQLFileWithOptions(row.id, confirmedOptions)
    currentFile.value = data.file
    statements.value = data.statements || []
    ElMessage.success(data.file.executeMessage || '执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally {
    executing.value = false
  }
}

async function handleExecuteBatch(row = currentBatch.value) {
  if (!row) {
    ElMessage.warning('请选择 SQL 批次')
    return
  }
  await ElMessageBox.confirm(`确定按顺序执行批次 "${row.batchName}" 吗？`, '确认执行批次', { type: 'warning' })
  executing.value = true
  try {
    const data = await executeSQLBatch(row.id, { ...options.value, confirmWarnRisk: true })
    currentBatch.value = data.batch
    batchFileList.value = data.files || []
    ElMessage.success(data.batch.executeMessage || '批次执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally {
    executing.value = false
  }
}

async function handleCancelExecution() {
  if (!currentFile.value) {
    ElMessage.warning('请选择正在执行的 SQL 文件')
    return
  }
  await cancelSQLFile(currentFile.value.id)
  ElMessage.warning('已提交取消请求')
}

async function handleOpen(row: SQLChangeFile) {
  const data = await getSQLFile(row.id)
  currentFile.value = data.file
  statements.value = data.statements || []
  form.value = {
    fileName: data.file.fileName || '',
    content: data.file.fileContent || '',
    overwrite: true,
  }
}

async function handleOpenBatch(row: SQLChangeBatch) {
  const data = await getSQLBatch(row.id)
  currentBatch.value = data.batch
  batchFileList.value = data.files || []
}

async function handleSkipFile(row: SQLChangeFile) {
  await ElMessageBox.confirm(`确定跳过 "${row.fileName}" 吗？`, '确认跳过', { type: 'warning' })
  await skipSQLFile(row.id)
  ElMessage.success('已跳过')
  await Promise.all([loadTodo(), loadDone()])
}

async function handleDeleteFile(row: SQLChangeFile) {
  await ElMessageBox.confirm(`确定删除 "${row.fileName}" 吗？`, '确认删除', { type: 'warning' })
  await deleteSQLFile(row.id)
  ElMessage.success('已删除')
  await loadTodo()
}

async function handleSkipStatement(row: SQLChangeStatement) {
  await skipSQLStatement(row.id)
  row.executeStatus = 'SKIPPED'
  row.executeMessage = '手工跳过'
  ElMessage.success('已跳过')
}

async function handleImportServerSQL() {
  if (!serverFilePath.value.trim()) {
    ElMessage.warning('请输入服务器 SQL 或 ZIP 文件路径')
    return
  }
  importing.value = true
  try {
    const data = await importServerSQL(serverFilePath.value, true)
    ElMessage.success(`已导入 ${data.count} 个 SQL 文件`)
    await loadTodo()
  } finally {
    importing.value = false
  }
}

async function handleExport() {
  if (!currentFile.value) {
    ElMessage.warning('请选择 SQL 文件')
    return
  }
  const content = await exportNotExecutableSQL(currentFile.value.id)
  if (!content.trim()) {
    ElMessage.warning('没有需要导出的 SQL')
    return
  }
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${currentFile.value.fileName || 'sql'}-not-executable.sql`
  a.click()
  URL.revokeObjectURL(url)
}

function statusTag(status: string) {
  if (status === 'SUCCESS') return 'success'
  if (status === 'FAILED' || status === 'PARTIAL_FAILED' || status === 'NOT_EXECUTABLE') return 'danger'
  if (status === 'RUNNING') return 'primary'
  if (status === 'SKIPPED') return 'info'
  if (status === 'CANCELED') return 'warning'
  return 'info'
}

async function buildConfirmedOptions(targetStatements: SQLChangeStatement[]) {
  const warnStatements = targetStatements.filter(s => s.riskLevel === 'WARN' && s.executeStatus !== 'SUCCESS' && s.executeStatus !== 'SKIPPED')
  if (warnStatements.length === 0) {
    return { ...options.value, confirmWarnRisk: false }
  }
  const riskTypes = Array.from(new Set(warnStatements.map(s => s.sqlType))).slice(0, 8).join('、')
  await ElMessageBox.confirm(
    `存在 ${warnStatements.length} 条 WARN 风险 SQL：${riskTypes}。确认后将继续执行可执行语句。`,
    '确认 SQL 风险',
    { type: 'warning', confirmButtonText: '确认执行', cancelButtonText: '取消' },
  )
  return { ...options.value, confirmWarnRisk: true }
}

function strategyLabel(row: SQLChangeStatement) {
  const strategy = row.executionStrategy || 'DIRECT'
  if (strategy === 'DIRECT_NO_TRANSACTION') return '直接执行/非事务'
  if (strategy === 'MANUAL_EXPORT') return '导出处理'
  return '直接执行'
}

function strategyTag(row: SQLChangeStatement) {
  const strategy = row.executionStrategy || 'DIRECT'
  if (strategy === 'MANUAL_EXPORT') return 'danger'
  if (strategy === 'DIRECT_NO_TRANSACTION') return 'warning'
  return 'info'
}

function reasonText(row: SQLChangeStatement) {
  if (row.sqlType === 'MISSING_SCHEMA') {
    return row.executeMessage || `业务对象必须显式指定 schema，例如 his.patient。${row.riskReason || '当前 SQL 禁止执行。'}`
  }
  return row.executeMessage || row.riskReason
}
</script>

<template>
  <div class="sql-execution-page">
    <div class="page-title-row">
      <div>
        <h4 class="page-title">SQL 执行</h4>
        <p class="page-desc">上传、粘贴或导入服务器 SQL 文件，系统自动解析后执行可执行语句。</p>
      </div>
      <div class="title-actions">
        <el-button size="small" :disabled="!currentFile" @click="handleExport">导出不可执行 SQL</el-button>
      </div>
    </div>

    <div class="content-card editor-card">
      <div class="toolbar-row">
        <el-input v-model="form.fileName" placeholder="SQL 文件名" style="width: 280px;" />
        <label class="upload-btn">
          <input type="file" accept=".sql,.txt" multiple @change="handleFileChange" />
          <span>上传 SQL</span>
        </label>
        <el-tag v-if="batchFiles.length > 1" type="warning" size="small">已选择 {{ batchFiles.length }} 个文件</el-tag>
        <el-checkbox v-model="form.overwrite">同名覆盖</el-checkbox>
        <el-checkbox v-model="options.skipExistsColumn">字段已存在则跳过</el-checkbox>
        <el-checkbox v-model="options.skipExistsTable">对象已存在则跳过</el-checkbox>
        <el-checkbox v-model="options.skipUniqueConstraint">唯一冲突则跳过</el-checkbox>
      </div>
      <el-input
        v-model="form.content"
        type="textarea"
        :rows="14"
        placeholder="粘贴 SQL 内容，或上传 .sql 文件"
        class="sql-textarea"
      />
      <div class="action-row">
        <el-button :loading="parsing" @click="handleParse">仅解析</el-button>
        <el-button type="primary" :loading="executing" @click="handleExecuteContent">解析并执行</el-button>
        <el-button :disabled="!currentFile" :loading="executing" @click="handleExecuteFile()">执行当前文件</el-button>
        <el-button v-if="currentFile?.executeStatus === 'RUNNING'" type="warning" @click="handleCancelExecution">取消执行</el-button>
        <el-button :disabled="!currentBatch" :loading="executing" @click="handleExecuteBatch()">执行当前批次</el-button>
        <el-input v-model="serverFilePath" placeholder="服务器 .sql 或 .zip 文件路径" style="width: 360px;" />
        <el-button :loading="importing" @click="handleImportServerSQL">导入服务器文件</el-button>
      </div>
    </div>

    <div class="summary-row">
      <div class="summary-item"><span>总 SQL</span><strong>{{ totalCount }}</strong></div>
      <div class="summary-item success"><span>已执行</span><strong>{{ successCount }}</strong></div>
      <div class="summary-item danger"><span>失败</span><strong>{{ failedCount }}</strong></div>
      <div class="summary-item warning"><span>不可执行</span><strong>{{ notExecutableCount }}</strong></div>
      <div class="summary-item"><span>跳过</span><strong>{{ skippedCount }}</strong></div>
    </div>

    <div class="content-card">
      <div class="section-title">SQL 明细</div>
      <el-table :data="statements" size="small" border>
        <el-table-column prop="lineNumber" label="行" width="64" />
        <el-table-column prop="sqlType" label="类型" width="150" />
        <el-table-column prop="executeStatus" label="状态" width="130">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="策略" width="150">
          <template #default="{ row }">
            <el-tag :type="strategyTag(row)" size="small">{{ strategyLabel(row) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="affectedRows" label="影响行数" width="100" />
        <el-table-column prop="durationMs" label="耗时(ms)" width="100" />
        <el-table-column prop="sqlState" label="SQLState" width="100" />
        <el-table-column prop="sqlContent" label="SQL" min-width="320" show-overflow-tooltip />
        <el-table-column label="原因" min-width="300" show-overflow-tooltip>
          <template #default="{ row }">
            <el-tag v-if="row.sqlType === 'MISSING_SCHEMA'" type="danger" size="small" class="risk-tag">缺少 schema</el-tag>
            <span>{{ reasonText(row) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button
              size="small"
              link
              type="warning"
              :disabled="row.executeStatus === 'SUCCESS' || row.executeStatus === 'SKIPPED'"
              @click="handleSkipStatement(row)"
            >跳过</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="file-grid">
      <div class="content-card">
        <div class="section-title">SQL 批次</div>
        <el-table :data="batches" size="small" border @row-click="handleOpenBatch">
          <el-table-column prop="batchName" label="批次" min-width="180" show-overflow-tooltip />
          <el-table-column prop="totalFiles" label="文件数" width="80" />
          <el-table-column prop="executeStatus" label="状态" width="130">
            <template #default="{ row }"><el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="createdAt" label="创建时间" width="160">
            <template #default="{ row }">{{ formatTimeStr(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="90" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click.stop="handleExecuteBatch(row)">执行</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div class="pager-row">
          <el-pagination v-model:current-page="batchPage" small layout="prev, pager, next" :page-size="pageSize" :total="batchTotal" @current-change="loadBatches" />
        </div>
      </div>

      <div class="content-card">
        <div class="section-title">当前批次文件</div>
        <el-table :data="batchFileList" size="small" border @row-click="handleOpen">
          <el-table-column prop="batchSortNo" label="序号" width="70" />
          <el-table-column prop="fileName" label="文件" min-width="180" show-overflow-tooltip />
          <el-table-column prop="executeStatus" label="状态" width="130">
            <template #default="{ row }"><el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag></template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <div class="file-grid">
      <div class="content-card">
        <div class="section-title">待执行文件</div>
        <el-table :data="todoFiles" v-loading="loadingTodo" size="small" border @row-click="handleOpen">
          <el-table-column prop="fileName" label="文件" min-width="180" show-overflow-tooltip />
          <el-table-column prop="executeStatus" label="状态" width="130">
            <template #default="{ row }"><el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="createdAt" label="创建时间" width="160">
            <template #default="{ row }">{{ formatTimeStr(row.createdAt) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="170" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click.stop="handleExecuteFile(row)">执行</el-button>
              <el-button type="warning" link size="small" @click.stop="handleSkipFile(row)">跳过</el-button>
              <el-button type="danger" link size="small" @click.stop="handleDeleteFile(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
        <div class="pager-row">
          <el-pagination v-model:current-page="todoPage" small layout="prev, pager, next" :page-size="pageSize" :total="todoTotal" @current-change="loadTodo" />
        </div>
      </div>

      <div class="content-card">
        <div class="section-title">已执行文件</div>
        <el-table :data="doneFiles" v-loading="loadingDone" size="small" border @row-click="handleOpen">
          <el-table-column prop="fileName" label="文件" min-width="180" show-overflow-tooltip />
          <el-table-column prop="executeStatus" label="状态" width="120">
            <template #default="{ row }"><el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag></template>
          </el-table-column>
          <el-table-column prop="executeTime" label="执行时间" width="160">
            <template #default="{ row }">{{ formatTimeStr(row.executeTime) }}</template>
          </el-table-column>
        </el-table>
        <div class="pager-row">
          <el-pagination v-model:current-page="donePage" small layout="prev, pager, next" :page-size="pageSize" :total="doneTotal" @current-change="loadDone" />
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sql-execution-page { display: flex; flex-direction: column; gap: 16px; }
.page-title-row { display: flex; align-items: flex-start; justify-content: space-between; }
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0; }
.page-desc { font-size: 13px; color: #909399; margin: 4px 0 0; }
.content-card { background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 16px; }
.toolbar-row,
.action-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.action-row { margin-top: 10px; }
.upload-btn { height: 32px; border: 1px solid #dcdfe6; border-radius: 4px; padding: 0 12px; display: flex; align-items: center; cursor: pointer; color: #606266; font-size: 13px; }
.upload-btn input { display: none; }
.sql-textarea { margin-top: 10px; }
.sql-textarea :deep(textarea) { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; line-height: 1.5; }
.summary-row { display: grid; grid-template-columns: repeat(5, minmax(120px, 1fr)); gap: 12px; }
.summary-item { background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 12px 14px; display: flex; align-items: center; justify-content: space-between; color: #606266; }
.summary-item strong { font-size: 20px; color: #303133; }
.summary-item.success strong { color: #67c23a; }
.summary-item.danger strong { color: #f56c6c; }
.summary-item.warning strong { color: #e6a23c; }
.section-title { font-size: 14px; font-weight: 600; color: #303133; margin-bottom: 12px; }
.risk-tag { margin-right: 6px; }
.file-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.pager-row { display: flex; justify-content: flex-end; margin-top: 10px; }
@media (max-width: 1200px) {
  .file-grid { grid-template-columns: 1fr; }
  .summary-row { grid-template-columns: repeat(2, minmax(120px, 1fr)); }
}
</style>

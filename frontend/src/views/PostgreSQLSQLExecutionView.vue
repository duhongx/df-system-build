<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  cancelSQLBatch,
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

const selectedTab = ref('todo')
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
const pageSize = 15

const totalCount = computed(() => statements.value.length)
const successCount = computed(() => statements.value.filter(s => s.executeStatus === 'SUCCESS').length)
const failedCount = computed(() => statements.value.filter(s => s.executeStatus === 'FAILED').length)
const notExecutableCount = computed(() => statements.value.filter(s => s.executeStatus === 'NOT_EXECUTABLE' || s.riskLevel === 'BLOCKED').length)
const skippedCount = computed(() => statements.value.filter(s => s.executeStatus === 'SKIPPED').length)

onMounted(() => { loadBatches(); loadTodo(); loadDone() })

async function loadBatches() {
  const data = await listSQLBatches(batchPage.value, pageSize)
  batches.value = data.list || []; batchTotal.value = data.total || 0
}
async function loadTodo() {
  loadingTodo.value = true
  try { const data = await listTodoSQLFiles(todoPage.value, pageSize); todoFiles.value = data.list || []; todoTotal.value = data.total || 0 }
  finally { loadingTodo.value = false }
}
async function loadDone() {
  loadingDone.value = true
  try { const data = await listDoneSQLFiles(donePage.value, pageSize); doneFiles.value = data.list || []; doneTotal.value = data.total || 0 }
  finally { loadingDone.value = false }
}

async function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || [])
  if (files.length === 0) return
  const parsedFiles = await Promise.all(files.map(async file => ({ fileName: file.name, content: await file.text() })))
  batchFiles.value = parsedFiles
  form.value.fileName = parsedFiles[0].fileName
  form.value.content = parsedFiles[0].content
  if (parsedFiles.length > 1) ElMessage.success(`已选择 ${parsedFiles.length} 个 SQL 文件`)
  input.value = ''
}

async function handleParse() {
  if (batchFiles.value.length > 1) { await handleParseBatch(); return }
  if (!form.value.content.trim()) { ElMessage.warning('请粘贴 SQL 内容或上传文件'); return }
  parsing.value = true
  try {
    const data = await parseSQLFile(form.value)
    currentFile.value = data.file; statements.value = data.statements || []
    ElMessage.success(`已解析 ${statements.value.length} 条 SQL`); await loadTodo()
  } finally { parsing.value = false }
}

async function handleParseBatch() {
  if (batchFiles.value.length === 0) { ElMessage.warning('请选择 SQL 文件'); return }
  parsing.value = true
  try {
    const data = await parseSQLBatch({ batchName: form.value.fileName.replace(/\.[^.]+$/, '') || undefined, overwrite: form.value.overwrite, files: batchFiles.value })
    currentBatch.value = data.batch; batchFileList.value = data.files || []
    ElMessage.success(`已解析批次，包含 ${batchFileList.value.length} 个文件`)
    await Promise.all([loadBatches(), loadTodo()])
  } finally { parsing.value = false }
}

async function handleExecuteContent() {
  if (!form.value.content.trim()) { ElMessage.warning('请粘贴 SQL 内容或上传文件'); return }
  executing.value = true
  try {
    const parsed = await parseSQLFile(form.value)
    const confirmedOptions = await buildConfirmedOptions(parsed.statements || [])
    const data = await executeSQLFileWithOptions(parsed.file.id, confirmedOptions)
    currentFile.value = data.file; statements.value = data.statements || []
    ElMessage.success(data.file.executeMessage || '执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally { executing.value = false }
}

async function handleExecuteFile(row = currentFile.value) {
  if (!row) { ElMessage.warning('请选择 SQL 文件'); return }
  executing.value = true
  try {
    const loaded = await getSQLFile(row.id)
    const confirmedOptions = await buildConfirmedOptions(loaded.statements || [])
    const data = await executeSQLFileWithOptions(row.id, confirmedOptions)
    currentFile.value = data.file; statements.value = data.statements || []
    ElMessage.success(data.file.executeMessage || '执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally { executing.value = false }
}

async function handleExecuteBatch(row = currentBatch.value) {
  if (!row) { ElMessage.warning('请选择 SQL 批次'); return }
  await ElMessageBox.confirm(`确定执行批次 "${row.batchName}" 吗？`, '确认', { type: 'warning' })
  executing.value = true
  try {
    const data = await executeSQLBatch(row.id, { ...options.value, confirmWarnRisk: true })
    currentBatch.value = data.batch; batchFileList.value = data.files || []
    ElMessage.success(data.batch.executeMessage || '批次执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally { executing.value = false }
}

async function handleCancelExecution() { if (currentFile.value) { await cancelSQLFile(currentFile.value.id); ElMessage.warning('已提交取消请求') } }
async function handleCancelBatch() { if (currentBatch.value) { const b = await cancelSQLBatch(currentBatch.value.id); currentBatch.value = b; ElMessage.warning('已提交取消请求') } }

async function handleOpen(row: SQLChangeFile) {
  const data = await getSQLFile(row.id)
  currentFile.value = data.file; statements.value = data.statements || []
  form.value = { fileName: data.file.fileName || '', content: data.file.fileContent || '', overwrite: true }
}
async function handleOpenBatch(row: SQLChangeBatch) {
  const data = await getSQLBatch(row.id); currentBatch.value = data.batch; batchFileList.value = data.files || []
}
async function handleSkipFile(row: SQLChangeFile) {
  await ElMessageBox.confirm(`确定跳过 "${row.fileName}" 吗？`, '确认', { type: 'warning' })
  await skipSQLFile(row.id); ElMessage.success('已跳过'); await Promise.all([loadTodo(), loadDone()])
}
async function handleDeleteFile(row: SQLChangeFile) {
  await ElMessageBox.confirm(`确定删除 "${row.fileName}" 吗？`, '确认', { type: 'warning' })
  await deleteSQLFile(row.id); ElMessage.success('已删除'); await loadTodo()
}
async function handleSkipStatement(row: SQLChangeStatement) {
  await skipSQLStatement(row.id); row.executeStatus = 'SKIPPED'; row.executeMessage = '手工跳过'; ElMessage.success('已跳过')
}
async function handleImportServerSQL() {
  if (!serverFilePath.value.trim()) { ElMessage.warning('请输入文件路径'); return }
  importing.value = true
  try { const data = await importServerSQL(serverFilePath.value, true); ElMessage.success(`已导入 ${data.count} 个文件`); await loadTodo() }
  finally { importing.value = false }
}
async function handleExport() {
  if (!currentFile.value) { ElMessage.warning('请选择文件'); return }
  const content = await exportNotExecutableSQL(currentFile.value.id)
  if (!content.trim()) { ElMessage.warning('没有需要导出的 SQL'); return }
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const a = document.createElement('a'); a.href = URL.createObjectURL(blob)
  a.download = `${currentFile.value.fileName || 'sql'}-not-executable.sql`; a.click()
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
  if (warnStatements.length === 0) return { ...options.value, confirmWarnRisk: false }
  const riskTypes = Array.from(new Set(warnStatements.map(s => s.sqlType))).slice(0, 8).join('、')
  await ElMessageBox.confirm(`存在 ${warnStatements.length} 条 WARN 风险 SQL：${riskTypes}`, '确认风险', { type: 'warning', confirmButtonText: '确认执行' })
  return { ...options.value, confirmWarnRisk: true }
}

function reasonText(row: SQLChangeStatement) {
  if (row.sqlType === 'MISSING_SCHEMA') return row.executeMessage || `必须显式指定 schema。${row.riskReason || ''}`
  return row.executeMessage || row.riskReason
}
</script>

<template>
  <div class="sql-page">
    <!-- Left Panel: File List -->
    <aside class="left-panel">
      <el-tabs v-model="selectedTab" class="panel-tabs">
        <el-tab-pane label="待执行" name="todo">
          <div class="file-list" v-loading="loadingTodo">
            <div v-for="f in todoFiles" :key="f.id" class="file-item" :class="{ active: currentFile?.id === f.id }" @click="handleOpen(f)">
              <div class="file-name">{{ f.fileName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(f.executeStatus)" size="small">{{ f.executeStatus }}</el-tag>
                <span class="file-time">{{ formatTimeStr(f.createdAt)?.slice(5) }}</span>
              </div>
              <div class="file-actions">
                <el-button size="small" link type="primary" @click.stop="handleExecuteFile(f)">执行</el-button>
                <el-button size="small" link type="warning" @click.stop="handleSkipFile(f)">跳过</el-button>
                <el-button size="small" link type="danger" @click.stop="handleDeleteFile(f)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!todoFiles.length" :image-size="40" description="暂无" />
          </div>
          <el-pagination v-if="todoTotal > pageSize" v-model:current-page="todoPage" small layout="prev, pager, next" :page-size="pageSize" :total="todoTotal" @current-change="loadTodo" />
        </el-tab-pane>

        <el-tab-pane label="已执行" name="done">
          <div class="file-list" v-loading="loadingDone">
            <div v-for="f in doneFiles" :key="f.id" class="file-item" :class="{ active: currentFile?.id === f.id }" @click="handleOpen(f)">
              <div class="file-name">{{ f.fileName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(f.executeStatus)" size="small">{{ f.executeStatus }}</el-tag>
                <span class="file-time">{{ formatTimeStr(f.executeTime)?.slice(5) }}</span>
              </div>
            </div>
            <el-empty v-if="!doneFiles.length" :image-size="40" description="暂无" />
          </div>
          <el-pagination v-if="doneTotal > pageSize" v-model:current-page="donePage" small layout="prev, pager, next" :page-size="pageSize" :total="doneTotal" @current-change="loadDone" />
        </el-tab-pane>

        <el-tab-pane label="批次" name="batch">
          <div class="file-list">
            <div v-for="b in batches" :key="b.id" class="file-item" :class="{ active: currentBatch?.id === b.id }" @click="handleOpenBatch(b)">
              <div class="file-name">{{ b.batchName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(b.executeStatus)" size="small">{{ b.executeStatus }}</el-tag>
                <span class="file-time">{{ b.totalFiles }} 文件</span>
              </div>
              <div class="file-actions">
                <el-button size="small" link type="primary" @click.stop="handleExecuteBatch(b)">执行</el-button>
              </div>
            </div>
            <el-empty v-if="!batches.length" :image-size="40" description="暂无" />
          </div>
          <el-pagination v-if="batchTotal > pageSize" v-model:current-page="batchPage" small layout="prev, pager, next" :page-size="pageSize" :total="batchTotal" @current-change="loadBatches" />
        </el-tab-pane>
      </el-tabs>

      <div class="panel-footer">
        <label class="upload-btn">
          <input type="file" accept=".sql,.txt" multiple @change="handleFileChange" />
          <el-button size="small" style="pointer-events: none;">上传 SQL</el-button>
        </label>
        <div class="import-row">
          <el-input v-model="serverFilePath" size="small" placeholder="服务器 .sql/.zip 路径" />
          <el-button size="small" :loading="importing" @click="handleImportServerSQL">导入</el-button>
        </div>
      </div>
    </aside>

    <!-- Right Panel: Editor + Results -->
    <main class="right-panel">
      <!-- Editor -->
      <div class="editor-section">
        <div class="editor-toolbar">
          <el-input v-model="form.fileName" size="small" placeholder="文件名" style="width: 200px;" />
          <el-tag v-if="batchFiles.length > 1" type="warning" size="small">{{ batchFiles.length }} 个文件</el-tag>
          <div class="toolbar-spacer" />
          <el-popover placement="bottom-end" :width="200" trigger="click">
            <template #reference>
              <el-button size="small" link type="primary">执行选项</el-button>
            </template>
            <div class="options-pop">
              <el-checkbox v-model="form.overwrite" size="small">同名覆盖</el-checkbox>
              <el-checkbox v-model="options.skipExistsColumn" size="small">字段已存在跳过</el-checkbox>
              <el-checkbox v-model="options.skipExistsTable" size="small">对象已存在跳过</el-checkbox>
              <el-checkbox v-model="options.skipUniqueConstraint" size="small">唯一冲突跳过</el-checkbox>
            </div>
          </el-popover>
        </div>
        <el-input v-model="form.content" type="textarea" :rows="6" placeholder="粘贴 SQL 内容，或从左侧选择文件查看" class="sql-textarea" />
        <div class="editor-actions">
          <el-button size="small" :loading="parsing" @click="handleParse">仅解析</el-button>
          <el-button size="small" type="warning" :loading="executing" @click="handleExecuteContent">解析并执行</el-button>
          <el-button size="small" type="primary" :disabled="!currentFile" :loading="executing" @click="handleExecuteFile()">执行选中文件</el-button>
          <el-button size="small" v-if="currentFile?.executeStatus === 'RUNNING'" type="danger" @click="handleCancelExecution">取消</el-button>
          <el-button size="small" v-if="currentBatch?.executeStatus === 'RUNNING'" type="danger" @click="handleCancelBatch">取消批次</el-button>
          <div class="toolbar-spacer" />
          <el-button size="small" link type="primary" :disabled="!currentFile" @click="handleExport">导出不可执行 SQL</el-button>
        </div>
      </div>

      <!-- Result section: summary + batch bar + table all in one card -->
      <div class="result-section">
        <!-- Summary -->
        <div class="summary-bar" v-if="totalCount > 0">
          <span>总 <strong>{{ totalCount }}</strong></span>
          <span class="s-success">成功 <strong>{{ successCount }}</strong></span>
          <span class="s-danger">失败 <strong>{{ failedCount }}</strong></span>
          <span class="s-warning">不可执行 <strong>{{ notExecutableCount }}</strong></span>
          <span>跳过 <strong>{{ skippedCount }}</strong></span>
        </div>

        <!-- Batch files (when batch selected) -->
        <div v-if="currentBatch && batchFileList.length" class="batch-files-bar">
          <span class="batch-label">批次: {{ currentBatch.batchName }}</span>
          <el-tag v-for="f in batchFileList" :key="f.id" :type="statusTag(f.executeStatus)" size="small" class="batch-file-tag" @click="handleOpen(f)">
            {{ f.fileName }}
          </el-tag>
        </div>

        <!-- SQL Detail Table -->
        <div class="table-wrap">
          <el-table v-if="statements.length" :data="statements" size="small" border height="100%" stripe>
            <el-table-column prop="lineNumber" label="行" width="55" align="center" />
            <el-table-column prop="sqlType" label="类型" width="140" />
            <el-table-column prop="executeStatus" label="状态" width="110" align="center">
              <template #default="{ row }">
                <el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="sqlContent" label="SQL" min-width="380" show-overflow-tooltip />
            <el-table-column label="原因" min-width="280" show-overflow-tooltip>
              <template #default="{ row }">{{ reasonText(row) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="70" fixed="right" align="center">
              <template #default="{ row }">
                <el-button size="small" link type="warning" :disabled="row.executeStatus === 'SUCCESS' || row.executeStatus === 'SKIPPED'" @click="handleSkipStatement(row)">跳过</el-button>
              </template>
            </el-table-column>
          </el-table>
          <el-empty v-else :image-size="60" description="选择左侧文件或粘贴 SQL 后解析" />
        </div>
      </div>
    </main>
  </div>
</template>

<style scoped>
.sql-page { display: flex; height: calc(100vh - 130px); gap: 0; overflow: hidden; }

/* Left Panel */
.left-panel { width: 280px; flex-shrink: 0; background: #fff; border: 1px solid #ebeef5; border-radius: 6px; display: flex; flex-direction: column; overflow: hidden; }
.panel-tabs { flex: 1; display: flex; flex-direction: column; overflow: hidden; }
:deep(.panel-tabs .el-tabs__content) { flex: 1; overflow: hidden; display: flex; flex-direction: column; }
:deep(.panel-tabs .el-tab-pane) { flex: 1; overflow: hidden; display: flex; flex-direction: column; }
:deep(.panel-tabs .el-tabs__header) { margin-bottom: 0; padding: 0 12px; }
.file-list { flex: 1; overflow-y: auto; padding: 4px 8px; min-height: 200px; }
.file-item { padding: 8px 10px; border-radius: 4px; cursor: pointer; margin-bottom: 4px; border: 1px solid transparent; }
.file-item:hover { background: #f5f7fa; }
.file-item.active { background: #ecf5ff; border-color: #d9ecff; }
.file-name { font-size: 13px; font-weight: 500; color: #303133; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.file-meta { display: flex; align-items: center; gap: 6px; margin-top: 4px; }
.file-time { font-size: 11px; color: #c0c4cc; }
.file-actions { margin-top: 4px; display: flex; gap: 4px; }
.panel-footer { padding: 10px; border-top: 1px solid #ebeef5; }
.upload-btn { display: block; margin-bottom: 8px; }
.upload-btn input { display: none; }
.import-row { display: flex; gap: 6px; }

/* Right Panel */
.right-panel { flex: 1; display: flex; flex-direction: column; gap: 10px; overflow: hidden; margin-left: 12px; min-width: 0; }
.editor-section { background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 12px; flex-shrink: 0; }
.editor-toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
.toolbar-spacer { flex: 1; }
.options-pop { display: flex; flex-direction: column; gap: 4px; }
.sql-textarea :deep(textarea) { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; line-height: 1.5; }
.editor-actions { display: flex; align-items: center; gap: 8px; margin-top: 10px; flex-wrap: wrap; }

/* Result section wraps summary + batch + table */
.result-section { flex: 1; min-height: 0; overflow: hidden; background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 12px; display: flex; flex-direction: column; gap: 10px; }
.table-wrap { flex: 1; min-height: 0; overflow: hidden; }

/* Summary */
.summary-bar { display: flex; align-items: center; gap: 16px; padding: 8px 14px; background: #f5f7fa; border-radius: 6px; font-size: 13px; color: #606266; flex-shrink: 0; }
.summary-bar strong { font-size: 15px; margin-left: 4px; }
.s-success strong { color: #67c23a; }
.s-danger strong { color: #f56c6c; }
.s-warning strong { color: #e6a23c; }

/* Batch files bar */
.batch-files-bar { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; padding: 8px 14px; background: #fdf6ec; border: 1px solid #faecd8; border-radius: 6px; flex-shrink: 0; }
.batch-label { font-size: 13px; font-weight: 500; color: #e6a23c; margin-right: 8px; }
.batch-file-tag { cursor: pointer; }

@media (max-width: 1200px) {
  .sql-page { flex-direction: column; height: auto; overflow: visible; }
  .left-panel { width: 100%; height: 300px; }
  .right-panel { margin-left: 0; margin-top: 12px; overflow: visible; }
  .result-section { min-height: 400px; }
  .table-wrap { min-height: 360px; }
}
</style>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import JSZip from 'jszip'
import {
  cancelSQLBatch,
  cancelSQLFile,
  deleteSQLFile,
  executeSQLFileWithOptions,
  getSQLBatch,
  getSQLFile,
  getSQLForceWhitelist,
  listSQLBatches,
  listDoneSQLFiles,
  listTodoSQLFiles,
  parseSQLBatch,
  parseSQLFile,
  saveSQLForceWhitelist,
  skipSQLFile,
  skipSQLStatement,
  type ParseSQLBatchFile,
  type SQLChangeBatch,
  type SQLChangeFile,
  type SQLForceWhitelistOption,
  type SQLChangeStatement,
  type SQLExecuteOptions,
} from '../api/postgresql'
import { formatTime as formatTimeStr } from '../utils/time'

type FileListMode = 'local' | 'todo' | 'done' | 'batch' | 'batchFiles'
type SQLViewMode = 'list' | 'full' | 'anomaly'

const parsing = ref(false)
const executing = ref(false)
const loadingTodo = ref(false)
const loadingDone = ref(false)
const whitelistSaving = ref(false)
const optionsDialogVisible = ref(false)

const selectedTab = ref<FileListMode>('todo')
const sqlViewMode = ref<SQLViewMode>('list')
const localFileIndex = ref(0)
const form = ref({ fileName: '', content: '', overwrite: true })
const batchFiles = ref<ParseSQLBatchFile[]>([])
const whitelistDialogVisible = ref(false)
const forceWhitelistOptions = ref<SQLForceWhitelistOption[]>([])
const forceWhitelistEnabled = ref<string[]>([])
const selectedStatement = ref<SQLChangeStatement | null>(null)
const options = ref<SQLExecuteOptions>({
  skipExistsColumn: true,
  skipExistsTable: true,
  skipUniqueConstraint: true,
  requireRiskConfirmation: true,
  confirmWarnRisk: false,
  forceBlockedSql: true,
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
const ZIP_MAX_SQL_FILES = 500
const ZIP_MAX_SQL_FILE_SIZE = 50 * 1024 * 1024
const ZIP_MAX_TOTAL_SIZE = 50 * 1024 * 1024

const totalCount = computed(() => statements.value.length)
const successCount = computed(() => statements.value.filter(s => s.executeStatus === 'SUCCESS').length)
const failedCount = computed(() => statements.value.filter(s => s.executeStatus === 'FAILED').length)
const notExecutableCount = computed(() => statements.value.filter(s => s.executeStatus === 'NOT_EXECUTABLE').length)
const skippedCount = computed(() => statements.value.filter(s => s.executeStatus === 'SKIPPED').length)
const timeoutCount = computed(() => statements.value.filter(s => s.executeStatus === 'TIMEOUT').length)
const pendingCount = computed(() => statements.value.filter(s => !s.executeStatus || s.executeStatus === 'PENDING').length)
const anomalyStatements = computed(() => statements.value.filter(isAnomalyStatement))
const visibleStatements = computed(() => sqlViewMode.value === 'anomaly' ? anomalyStatements.value : statements.value)
const activeBatchName = computed(() => currentBatch.value?.batchName || form.value.fileName?.replace(/\.[^.]+$/, '') || '未选择批次')
const summaryFileCount = computed(() => {
  if (batchFiles.value.length > 0) return batchFiles.value.length
  if (batchFileList.value.length > 0) return batchFileList.value.length
  if (currentBatch.value?.totalFiles) return currentBatch.value.totalFiles
  return todoTotal.value || doneTotal.value || 0
})
const forceWhitelistSummary = computed(() => `白名单 ${forceWhitelistEnabled.value.length} 项`)
const executeOptionSummary = computed(() => {
  const items: string[] = []
  if (options.value.skipExistsTable) items.push('已存在表')
  if (options.value.skipExistsColumn) items.push('已存在字段')
  if (options.value.skipUniqueConstraint) items.push('唯一约束冲突')
  return items.length ? items.join(' / ') : '未启用跳过规则'
})
const fileModeOptions = computed(() => {
  const modes = []
  if (batchFiles.value.length > 0) modes.push({ label: `本次导入(${batchFiles.value.length})`, value: 'local' })
  if (batchFileList.value.length > 0) modes.push({ label: `批次文件(${batchFileList.value.length})`, value: 'batchFiles' })
  modes.push(
    { label: '待执行', value: 'todo' },
    { label: '已执行', value: 'done' },
    { label: '批次', value: 'batch' },
  )
  return modes
})

onMounted(() => { loadBatches(); loadTodo(); loadDone(); loadSQLForceWhitelistConfig() })

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

async function loadSQLForceWhitelistConfig() {
  const data = await getSQLForceWhitelist()
  forceWhitelistOptions.value = data.available || []
  forceWhitelistEnabled.value = data.enabled || []
}

async function handleSaveSQLForceWhitelist() {
  whitelistSaving.value = true
  try {
    const data = await saveSQLForceWhitelist(forceWhitelistEnabled.value)
    forceWhitelistOptions.value = data.available || []
    forceWhitelistEnabled.value = data.enabled || []
    whitelistDialogVisible.value = false
    ElMessage.success('白名单配置已保存')
  } finally {
    whitelistSaving.value = false
  }
}

async function handleRefreshAll() {
  await Promise.all([loadBatches(), loadTodo(), loadDone()])
}

async function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files || [])
  if (files.length === 0) return
  try {
    const parsedFiles = (await Promise.all(files.map(readSQLUploadFile))).flat()
    if (parsedFiles.length === 0) {
      ElMessage.warning('未找到 SQL 文件')
      return
    }
    batchFiles.value = parsedFiles
    selectedTab.value = 'local'
    handleOpenLocalFile(parsedFiles[0], 0)
    if (parsedFiles.length > 1) ElMessage.success(`已选择 ${parsedFiles.length} 个 SQL 文件`)
  } catch (err: any) {
    ElMessage.error(err?.message || '读取 SQL 文件失败')
  } finally {
    input.value = ''
  }
}

async function readSQLUploadFile(file: File): Promise<ParseSQLBatchFile[]> {
  const lowerName = file.name.toLowerCase()
  if (lowerName.endsWith('.zip')) {
    if (file.size > ZIP_MAX_TOTAL_SIZE) throw new Error('ZIP 文件超过大小限制 (50MB)')
    let zip: JSZip
    try {
      zip = await JSZip.loadAsync(file)
    } catch {
      throw new Error('ZIP 文件解析失败，请确认文件未损坏')
    }
    const entries = Object.values(zip.files).filter(entry => !entry.dir && entry.name.toLowerCase().endsWith('.sql'))
    if (entries.length > ZIP_MAX_SQL_FILES) throw new Error(`ZIP 内 SQL 文件数量超过限制 (${ZIP_MAX_SQL_FILES})`)
    const seenNames = new Set<string>()
    const duplicateNames = new Set<string>()
    const parsed: ParseSQLBatchFile[] = []
    let totalSize = 0
    for (const entry of entries) {
      const fileName = entry.name.split('/').pop() || entry.name
      const normalizedName = fileName.toLowerCase()
      if (seenNames.has(normalizedName)) duplicateNames.add(fileName)
      seenNames.add(normalizedName)
      const content = await entry.async('string')
      const size = new Blob([content]).size
      if (size > ZIP_MAX_SQL_FILE_SIZE) throw new Error(`SQL 文件 ${fileName} 超过大小限制 (50MB)`)
      totalSize += size
      if (totalSize > ZIP_MAX_TOTAL_SIZE) throw new Error('ZIP 内 SQL 文件总大小超过限制 (50MB)')
      parsed.push({ fileName, content })
    }
    if (duplicateNames.size > 0) throw new Error(`ZIP 内存在同名 SQL 文件: ${Array.from(duplicateNames).slice(0, 5).join('、')}`)
    return parsed
  }
  if (lowerName.endsWith('.sql')) {
    if (file.size > ZIP_MAX_SQL_FILE_SIZE) throw new Error('SQL 文件超过大小限制 (50MB)')
    return [{ fileName: file.name, content: await file.text() }]
  }
  return []
}

async function handleParse() {
  if (batchFiles.value.length > 1) {
    await handleParseBatch()
    return
  }
  if (!form.value.content.trim()) {
    ElMessage.warning('请粘贴 SQL 内容或上传文件')
    return
  }
  parsing.value = true
  try {
    const data = await parseSQLFile(form.value)
    applyLoadedFile(data.file, data.statements || [])
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
    const data = await parseSQLBatch({ batchName: form.value.fileName.replace(/\.[^.]+$/, '') || undefined, overwrite: form.value.overwrite, files: batchFiles.value })
    currentBatch.value = data.batch
    batchFileList.value = data.files || []
    batchFiles.value = []
    selectedTab.value = 'batchFiles'
    ElMessage.success(`已解析批次，包含 ${batchFileList.value.length} 个文件`)
    await Promise.all([loadBatches(), loadTodo()])
    if (batchFileList.value.length > 0) await handleOpen(batchFileList.value[0])
  } finally {
    parsing.value = false
  }
}

async function handleExecuteContent() {
  if (!form.value.content.trim()) {
    ElMessage.warning('请粘贴 SQL 内容或上传文件')
    return
  }
  executing.value = true
  try {
    const parsed = await parseSQLFile(form.value)
    const confirmedOptions = await buildConfirmedOptions(parsed.statements || [])
    const data = await executeSQLFileWithOptions(parsed.file.id, confirmedOptions)
    applyLoadedFile(data.file, data.statements || [])
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
    applyLoadedFile(data.file, data.statements || [])
    ElMessage.success(data.file.executeMessage || '执行完成')
    await Promise.all([loadBatches(), loadTodo(), loadDone()])
  } finally {
    executing.value = false
  }
}

async function handleCancelExecution() {
  if (currentFile.value) {
    await cancelSQLFile(currentFile.value.id)
    ElMessage.warning('已提交取消请求')
  }
}

async function handleCancelBatch() {
  if (currentBatch.value) {
    const b = await cancelSQLBatch(currentBatch.value.id)
    currentBatch.value = b
    ElMessage.warning('已提交取消请求')
  }
}

function selectStatement(row: SQLChangeStatement) {
  selectedStatement.value = row
}

async function handleCopySelectedSQL() {
  if (!selectedStatement.value) return
  await navigator.clipboard?.writeText(selectedStatement.value.sqlContent)
  ElMessage.success('SQL 已复制')
}

async function handleOpen(row: SQLChangeFile) {
  const data = await getSQLFile(row.id)
  applyLoadedFile(data.file, data.statements || [])
}

async function handleOpenBatch(row: SQLChangeBatch) {
  const data = await getSQLBatch(row.id)
  currentBatch.value = data.batch
  batchFileList.value = data.files || []
  selectedTab.value = 'batchFiles'
  if (batchFileList.value.length > 0) await handleOpen(batchFileList.value[0])
}

function handleOpenLocalFile(file: ParseSQLBatchFile, index: number) {
  localFileIndex.value = index
  form.value = { fileName: file.fileName || '', content: file.content || '', overwrite: true }
  currentFile.value = null
  statements.value = []
  selectedStatement.value = null
  sqlViewMode.value = 'full'
}

function applyLoadedFile(file: SQLChangeFile, loadedStatements: SQLChangeStatement[]) {
  currentFile.value = file
  form.value = { fileName: file.fileName || '', content: file.fileContent || '', overwrite: true }
  statements.value = loadedStatements
  selectedStatement.value = anomalyStatements.value[0] || loadedStatements[0] || null
  sqlViewMode.value = loadedStatements.length > 0 ? 'list' : 'full'
}

async function handleSkipFile(row: SQLChangeFile) {
  await ElMessageBox.confirm(`确定跳过 "${row.fileName}" 吗？`, '确认', { type: 'warning' })
  await skipSQLFile(row.id)
  ElMessage.success('已跳过')
  await Promise.all([loadTodo(), loadDone()])
}

async function handleDeleteFile(row: SQLChangeFile) {
  await ElMessageBox.confirm(`确定删除 "${row.fileName}" 吗？`, '确认', { type: 'warning' })
  await deleteSQLFile(row.id)
  ElMessage.success('已删除')
  await loadTodo()
}

async function handleSkipStatement(row: SQLChangeStatement) {
  await skipSQLStatement(row.id)
  row.executeStatus = 'SKIPPED'
  row.executeMessage = '手工跳过'
  ElMessage.success('已跳过')
  if (currentFile.value) {
    await handleOpen(currentFile.value)
    await Promise.all([loadTodo(), loadDone()])
  }
}

async function handleRetryCurrentFile() {
  if (!currentFile.value) {
    ElMessage.warning('请选择 SQL 文件')
    return
  }
  await handleExecuteFile(currentFile.value)
}

function handleExportAnomalySQL() {
  if (!currentFile.value && !form.value.fileName) {
    ElMessage.warning('请选择文件')
    return
  }
  const rows = anomalyStatements.value
  if (!rows.length) {
    ElMessage.warning('当前文件没有异常 SQL')
    return
  }
  const content = rows.map(row => [
    `-- 文件: ${form.value.fileName || currentFile.value?.fileName || 'sql'}`,
    `-- 行号: ${row.lineNumber}`,
    `-- 状态: ${row.executeStatus}`,
    `-- 类型: ${row.sqlType}`,
    `-- 原因: ${reasonText(row) || '-'}`,
    row.sqlContent.trim(),
    row.sqlContent.trim().endsWith(';') ? '' : ';',
  ].filter(Boolean).join('\n')).join('\n\n')
  downloadText(`${form.value.fileName || 'sql'}-异常SQL.sql`, content)
}

function handleExportSelectedSQL(row = selectedStatement.value) {
  if (!row) {
    ElMessage.warning('请选择 SQL')
    return
  }
  const content = [
    `-- 文件: ${form.value.fileName || currentFile.value?.fileName || 'sql'}`,
    `-- 行号: ${row.lineNumber}`,
    `-- 状态: ${row.executeStatus}`,
    `-- 类型: ${row.sqlType}`,
    `-- 原因: ${reasonText(row) || '-'}`,
    row.sqlContent.trim(),
    row.sqlContent.trim().endsWith(';') ? '' : ';',
  ].filter(Boolean).join('\n')
  downloadText(`${form.value.fileName || 'sql'}-line-${row.lineNumber}.sql`, content)
}

function downloadText(fileName: string, content: string) {
  const blob = new Blob([content], { type: 'text/plain;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = fileName
  a.click()
  URL.revokeObjectURL(a.href)
}

function statusTag(status: string) {
  if (status === 'SUCCESS') return 'success'
  if (status === 'FAILED' || status === 'PARTIAL_FAILED' || status === 'NOT_EXECUTABLE' || status === 'TIMEOUT') return 'danger'
  if (status === 'RUNNING') return 'primary'
  if (status === 'SKIPPED') return 'info'
  if (status === 'CANCELED') return 'warning'
  return 'info'
}

function statementStatusText(status: string) {
  return ({
    SUCCESS: '成功',
    FAILED: '失败',
    PARTIAL_FAILED: '部分失败',
    NOT_EXECUTABLE: '不可执行',
    TIMEOUT: '超时',
    RUNNING: '执行中',
    SKIPPED: '跳过',
    CANCELED: '已取消',
    PENDING: '待执行',
  } as Record<string, string>)[status] || status || '待执行'
}

async function buildConfirmedOptions(targetStatements: SQLChangeStatement[]) {
  const blockedStatements = targetStatements.filter(s => (s.riskLevel === 'BLOCKED' || s.executeStatus === 'NOT_EXECUTABLE') && s.executeStatus !== 'SUCCESS' && s.executeStatus !== 'SKIPPED')
  if (options.value.forceBlockedSql && blockedStatements.length > 0) {
    const forceableBlockedStatements = blockedStatements.filter(s => forceWhitelistEnabled.value.includes(s.sqlType))
    const hardBlockedStatements = blockedStatements.filter(s => !forceWhitelistEnabled.value.includes(s.sqlType))
    const forceableTypes = Array.from(new Set(forceableBlockedStatements.map(s => s.sqlType))).slice(0, 8).join('、') || '无'
    const hardTypes = Array.from(new Set(hardBlockedStatements.map(s => s.sqlType))).slice(0, 8).join('、') || '无'
    await ElMessageBox.confirm(`将强制提交白名单内阻断 SQL ${forceableBlockedStatements.length} 条：${forceableTypes}。仍会被后端阻断 ${hardBlockedStatements.length} 条：${hardTypes}。`, '高风险确认', { type: 'error', confirmButtonText: '强制执行' })
  }
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

function formatSQLSingleLine(sql: string) {
  return (sql || '').replace(/\s+/g, ' ').trim()
}

function formatDuration(ms?: number) {
  if (!ms) return '-'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

function isAnomalyStatement(row: SQLChangeStatement) {
  return ['FAILED', 'PARTIAL_FAILED', 'NOT_EXECUTABLE', 'TIMEOUT', 'CANCELED'].includes(row.executeStatus)
}
</script>

<template>
  <div class="sql-execution-page">
    <section class="sql-command-bar">
      <div class="command-status">
        <div class="command-title">
          <strong>SQL 执行</strong>
          <span>批次：{{ activeBatchName }}</span>
        </div>
        <div class="summary-tags">
          <span>文件 {{ summaryFileCount }}</span>
          <span>SQL {{ totalCount }}</span>
          <span class="ok">成功 {{ successCount }}</span>
          <span class="danger">失败 {{ failedCount }}</span>
          <span class="danger">不可执行 {{ notExecutableCount }}</span>
          <span class="warn">超时 {{ timeoutCount }}</span>
          <span>跳过 {{ skippedCount }}</span>
          <span class="warn">待执行 {{ pendingCount }}</span>
        </div>
      </div>
      <div class="command-actions">
        <label class="upload-btn">
          <input type="file" accept=".sql,.zip" multiple @change="handleFileChange" />
          <el-button size="small" style="pointer-events: none;">上传SQL/ZIP</el-button>
        </label>
        <el-button size="small" :loading="parsing" @click="handleParse">解析</el-button>
        <el-button size="small" type="warning" :loading="executing" @click="handleExecuteContent">解析并执行</el-button>
        <el-dropdown trigger="click">
          <el-button size="small">更多</el-button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item :disabled="!currentFile" @click="handleRetryCurrentFile">重试异常</el-dropdown-item>
              <el-dropdown-item :disabled="!currentFile" @click="handleCancelExecution">取消当前</el-dropdown-item>
              <el-dropdown-item :disabled="!currentBatch" @click="handleCancelBatch">取消批次</el-dropdown-item>
              <el-dropdown-item :disabled="!anomalyStatements.length" @click="handleExportAnomalySQL">导出异常SQL</el-dropdown-item>
              <el-dropdown-item @click="optionsDialogVisible = true">执行选项</el-dropdown-item>
              <el-dropdown-item @click="whitelistDialogVisible = true">白名单配置</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </section>

    <section class="sql-workbench">
      <aside class="sql-file-panel">
        <div class="panel-title-row">
          <span class="panel-title">SQL文件</span>
          <el-button size="small" link type="primary" @click="handleRefreshAll">刷新</el-button>
        </div>
        <el-radio-group v-model="selectedTab" size="small" class="file-mode-tabs">
          <el-radio-button v-for="mode in fileModeOptions" :key="mode.value" :label="mode.value">{{ mode.label }}</el-radio-button>
        </el-radio-group>

        <div class="file-list" v-loading="loadingTodo && selectedTab === 'todo' || loadingDone && selectedTab === 'done'">
          <template v-if="selectedTab === 'local'">
            <div v-for="(f, index) in batchFiles" :key="`${f.fileName}-${index}`" class="file-item" :class="{ active: localFileIndex === index }" @click="handleOpenLocalFile(f, index)">
              <div class="file-name">{{ f.fileName }}</div>
              <div class="file-meta">
                <el-tag type="warning" size="small">未解析</el-tag>
                <span>本次导入</span>
              </div>
            </div>
            <el-empty v-if="!batchFiles.length" :image-size="48" description="暂无导入文件" />
          </template>

          <template v-else-if="selectedTab === 'batchFiles'">
            <div v-for="f in batchFileList" :key="f.id" class="file-item" :class="{ active: currentFile?.id === f.id }" @click="handleOpen(f)">
              <div class="file-name">{{ f.fileName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(f.executeStatus)" size="small">{{ statementStatusText(f.executeStatus) }}</el-tag>
                <span>{{ formatTimeStr(f.createdAt)?.slice(5) }}</span>
              </div>
            </div>
            <el-empty v-if="!batchFileList.length" :image-size="48" description="请先从批次中选择" />
          </template>

          <template v-else-if="selectedTab === 'todo'">
            <div v-for="f in todoFiles" :key="f.id" class="file-item" :class="{ active: currentFile?.id === f.id }" @click="handleOpen(f)">
              <div class="file-name">{{ f.fileName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(f.executeStatus)" size="small">{{ statementStatusText(f.executeStatus) }}</el-tag>
                <span>{{ formatTimeStr(f.createdAt)?.slice(5) }}</span>
              </div>
              <div class="file-actions">
                <el-button size="small" link type="primary" @click.stop="handleExecuteFile(f)">执行</el-button>
                <el-button size="small" link type="warning" @click.stop="handleSkipFile(f)">跳过</el-button>
                <el-button size="small" link type="danger" @click.stop="handleDeleteFile(f)">删除</el-button>
              </div>
            </div>
            <el-empty v-if="!todoFiles.length" :image-size="48" description="暂无待执行文件" />
            <el-pagination v-if="todoTotal > pageSize" v-model:current-page="todoPage" small layout="prev, pager, next" :page-size="pageSize" :total="todoTotal" @current-change="loadTodo" />
          </template>

          <template v-else-if="selectedTab === 'done'">
            <div v-for="f in doneFiles" :key="f.id" class="file-item" :class="{ active: currentFile?.id === f.id }" @click="handleOpen(f)">
              <div class="file-name">{{ f.fileName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(f.executeStatus)" size="small">{{ statementStatusText(f.executeStatus) }}</el-tag>
                <span>{{ formatTimeStr(f.executeTime)?.slice(5) }}</span>
              </div>
            </div>
            <el-empty v-if="!doneFiles.length" :image-size="48" description="暂无已执行文件" />
            <el-pagination v-if="doneTotal > pageSize" v-model:current-page="donePage" small layout="prev, pager, next" :page-size="pageSize" :total="doneTotal" @current-change="loadDone" />
          </template>

          <template v-else>
            <div v-for="b in batches" :key="b.id" class="file-item" :class="{ active: currentBatch?.id === b.id }" @click="handleOpenBatch(b)">
              <div class="file-name">{{ b.batchName }}</div>
              <div class="file-meta">
                <el-tag :type="statusTag(b.executeStatus)" size="small">{{ statementStatusText(b.executeStatus) }}</el-tag>
                <span>{{ b.totalFiles }} 文件</span>
              </div>
            </div>
            <el-empty v-if="!batches.length" :image-size="48" description="暂无批次" />
            <el-pagination v-if="batchTotal > pageSize" v-model:current-page="batchPage" small layout="prev, pager, next" :page-size="pageSize" :total="batchTotal" @current-change="loadBatches" />
          </template>
        </div>
      </aside>

      <main class="sql-main-panel">
        <div class="sql-context-row">
          <div>
            <div class="current-file-title">当前文件：{{ form.fileName || '-' }}</div>
          </div>
          <div class="option-chips">
            <span class="option-label">当前执行选项</span>
            <span>{{ executeOptionSummary }}</span>
            <span>强制执行 {{ options.forceBlockedSql ? '开启' : '关闭' }}</span>
            <span>{{ forceWhitelistSummary }}</span>
            <el-button size="small" link type="primary" @click="optionsDialogVisible = true">调整</el-button>
            <el-button size="small" link type="primary" @click="whitelistDialogVisible = true">白名单</el-button>
          </div>
        </div>

        <el-tabs v-model="sqlViewMode" class="sql-content-tabs">
          <el-tab-pane label="SQL列表" name="list">
            <div class="table-wrap">
              <el-table v-if="visibleStatements.length" :data="visibleStatements" size="small" border height="100%" stripe highlight-current-row @row-click="selectStatement">
                <el-table-column type="index" label="序号" width="58" align="center" />
                <el-table-column prop="lineNumber" label="行号" width="70" align="center" />
                <el-table-column prop="executeStatus" label="状态" width="100" align="center">
                  <template #default="{ row }">
                    <el-tag :type="statusTag(row.executeStatus)" size="small">{{ statementStatusText(row.executeStatus) }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column prop="sqlType" label="类型" width="150" show-overflow-tooltip />
                <el-table-column label="SQL摘要" min-width="360" show-overflow-tooltip>
                  <template #default="{ row }">{{ formatSQLSingleLine(row.sqlContent) }}</template>
                </el-table-column>
                <el-table-column label="执行信息" min-width="240" show-overflow-tooltip>
                  <template #default="{ row }">{{ reasonText(row) || '执行成功' }}</template>
                </el-table-column>
                <el-table-column prop="estimatedRows" label="预估行数" width="95" align="right" />
                <el-table-column prop="affectedRows" label="影响行数" width="95" align="right" />
                <el-table-column label="耗时" width="85" align="right">
                  <template #default="{ row }">{{ formatDuration(row.durationMs) }}</template>
                </el-table-column>
                <el-table-column label="操作" width="150" fixed="right" align="center">
                  <template #default="{ row }">
                    <el-button size="small" link type="primary" :disabled="row.executeStatus === 'SUCCESS'" @click.stop="handleRetryCurrentFile">重试</el-button>
                    <el-button size="small" link type="warning" :disabled="row.executeStatus === 'SUCCESS' || row.executeStatus === 'SKIPPED'" @click.stop="handleSkipStatement(row)">跳过</el-button>
                    <el-button size="small" link type="primary" @click.stop="selectedStatement = row">查看</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <el-empty v-else :image-size="60" description="当前文件尚未解析 SQL" />
            </div>
          </el-tab-pane>

          <el-tab-pane label="完整SQL" name="full">
            <pre class="full-sql-content">{{ form.content || '请选择 SQL 文件' }}</pre>
          </el-tab-pane>

          <el-tab-pane :label="`异常SQL (${anomalyStatements.length})`" name="anomaly">
            <div class="table-wrap">
              <el-table v-if="visibleStatements.length" :data="visibleStatements" size="small" border height="100%" stripe highlight-current-row @row-click="selectStatement">
                <el-table-column prop="lineNumber" label="行号" width="70" align="center" />
                <el-table-column prop="executeStatus" label="状态" width="110" align="center">
                  <template #default="{ row }">
                    <el-tag :type="statusTag(row.executeStatus)" size="small">{{ statementStatusText(row.executeStatus) }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column prop="sqlType" label="类型" width="150" />
                <el-table-column label="SQL摘要" min-width="420" show-overflow-tooltip>
                  <template #default="{ row }">{{ formatSQLSingleLine(row.sqlContent) }}</template>
                </el-table-column>
                <el-table-column label="原因" min-width="300" show-overflow-tooltip>
                  <template #default="{ row }">{{ reasonText(row) }}</template>
                </el-table-column>
                <el-table-column label="操作" width="150" fixed="right" align="center">
                  <template #default="{ row }">
                    <el-button size="small" link type="primary" @click.stop="handleRetryCurrentFile">重试</el-button>
                    <el-button size="small" link type="warning" @click.stop="handleSkipStatement(row)">跳过</el-button>
                    <el-button size="small" link type="primary" @click.stop="handleExportSelectedSQL(row)">导出</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <el-empty v-else :image-size="60" description="当前文件没有异常 SQL" />
            </div>
          </el-tab-pane>
        </el-tabs>

        <section v-if="selectedStatement" class="sql-detail-panel">
          <div class="detail-header">
            <span>SQL详情</span>
            <div class="detail-actions">
              <el-button size="small" @click="handleCopySelectedSQL">复制SQL</el-button>
              <el-button size="small" @click="handleExportSelectedSQL()">导出本条</el-button>
            </div>
          </div>
          <div class="detail-meta">
            <el-tag :type="statusTag(selectedStatement.executeStatus)" size="small">{{ statementStatusText(selectedStatement.executeStatus) }}</el-tag>
            <span>行号 {{ selectedStatement.lineNumber }}</span>
            <span>{{ selectedStatement.sqlType }}</span>
            <span v-if="selectedStatement.durationMs">耗时 {{ formatDuration(selectedStatement.durationMs) }}</span>
          </div>
          <div class="detail-reason" v-if="reasonText(selectedStatement)">原因：{{ reasonText(selectedStatement) }}</div>
          <pre class="detail-sql">{{ selectedStatement.sqlContent }}</pre>
        </section>
      </main>
    </section>

    <el-dialog v-model="optionsDialogVisible" title="执行选项" width="520px">
      <div class="options-dialog">
        <el-checkbox v-model="form.overwrite">同名覆盖</el-checkbox>
        <el-checkbox v-model="options.skipExistsColumn">字段已存在跳过</el-checkbox>
        <el-checkbox v-model="options.skipExistsTable">对象已存在跳过</el-checkbox>
        <el-checkbox v-model="options.skipUniqueConstraint">唯一冲突跳过</el-checkbox>
        <el-checkbox v-model="options.forceBlockedSql">强制执行阻断 SQL</el-checkbox>
      </div>
      <template #footer>
        <el-button @click="optionsDialogVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="whitelistDialogVisible" title="强制执行白名单配置" width="560px">
      <el-checkbox-group v-model="forceWhitelistEnabled" class="whitelist-options">
        <el-checkbox v-for="item in forceWhitelistOptions" :key="item.sqlType" :label="item.sqlType">
          <span class="whitelist-type">{{ item.sqlType }}</span>
          <span class="whitelist-label">{{ item.label }}</span>
        </el-checkbox>
      </el-checkbox-group>
      <template #footer>
        <el-button @click="whitelistDialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="whitelistSaving" @click="handleSaveSQLForceWhitelist">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.sql-execution-page { display: flex; flex-direction: column; gap: 8px; height: calc(100vh - 96px); overflow: hidden; }
.sql-command-bar, .sql-workbench { background: #fff; border: 1px solid #e4e7ed; border-radius: 6px; }
.sql-command-bar { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 8px 12px; flex-shrink: 0; min-height: 48px; }
.command-status { min-width: 0; display: flex; flex-direction: column; gap: 4px; }
.command-title { display: flex; align-items: center; gap: 10px; min-width: 0; color: #303133; }
.command-title strong { font-size: 16px; }
.command-title span { color: #606266; font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.summary-tags { display: flex; flex-wrap: wrap; gap: 10px; color: #909399; font-size: 12px; line-height: 1; }
.summary-tags span { white-space: nowrap; }
.summary-tags .ok { color: #67c23a; }
.summary-tags .danger { color: #f56c6c; }
.summary-tags .warn { color: #e6a23c; }
.command-actions { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
.upload-btn input { display: none; }
.sql-workbench { flex: 1; min-height: 0; display: grid; grid-template-columns: 280px minmax(0, 1fr); overflow: hidden; }
.sql-file-panel { border-right: 1px solid #e4e7ed; padding: 10px; display: flex; flex-direction: column; min-height: 0; background: #fbfcfe; }
.panel-title-row { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.panel-title { font-size: 14px; font-weight: 600; color: #303133; }
.file-mode-tabs { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: 10px; }
.file-mode-tabs :deep(.el-radio-button__inner) { padding: 5px 8px; border-radius: 4px; border-left: 1px solid #dcdfe6; }
.file-list { flex: 1; min-height: 0; overflow-y: auto; }
.file-item { padding: 8px 10px; border: 1px solid #edf1f7; border-radius: 4px; background: #fff; margin-bottom: 6px; cursor: pointer; }
.file-item:hover { border-color: #b3d8ff; background: #f8fbff; }
.file-item.active { background: #ecf5ff; border-color: #409eff; }
.file-name { font-size: 13px; font-weight: 600; color: #303133; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.file-meta { display: flex; align-items: center; gap: 8px; margin-top: 6px; font-size: 12px; color: #909399; }
.file-actions { display: flex; gap: 8px; margin-top: 6px; }
.sql-main-panel { display: flex; flex-direction: column; min-width: 0; min-height: 0; padding: 10px 12px 12px; overflow: hidden; }
.sql-context-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 32px; margin-bottom: 4px; flex-shrink: 0; }
.current-file-title { font-size: 14px; font-weight: 600; color: #303133; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.option-chips { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; justify-content: flex-end; color: #909399; font-size: 12px; }
.option-label { color: #303133; font-weight: 600; }
.sql-content-tabs { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.sql-content-tabs :deep(.el-tabs__header) { margin-bottom: 8px; }
.sql-content-tabs :deep(.el-tabs__content) { flex: 1; min-height: 0; overflow: hidden; }
.sql-content-tabs :deep(.el-tab-pane) { height: 100%; min-height: 0; }
.table-wrap { height: 100%; min-height: 0; overflow: hidden; }
.full-sql-content, .detail-sql { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; margin: 0; color: #303133; }
.full-sql-content { height: 100%; overflow: auto; padding: 14px; background: #f8fafc; border: 1px solid #e4e7ed; border-radius: 4px; }
.sql-detail-panel { margin-top: 8px; border: 1px solid #e4e7ed; border-radius: 6px; background: #f8fafc; padding: 10px 12px; flex-shrink: 0; max-height: 190px; overflow: auto; }
.detail-header { display: flex; align-items: center; justify-content: space-between; font-size: 14px; font-weight: 600; color: #303133; margin-bottom: 8px; }
.detail-actions { display: flex; gap: 8px; }
.detail-meta { display: flex; align-items: center; gap: 10px; color: #606266; font-size: 12px; margin-bottom: 8px; }
.detail-reason { color: #f56c6c; font-size: 13px; margin-bottom: 8px; }
.detail-sql { background: #fff; border: 1px solid #e4e7ed; border-radius: 4px; padding: 10px; max-height: 110px; overflow: auto; }
.options-dialog { display: flex; flex-direction: column; gap: 10px; }
.whitelist-options { display: flex; flex-direction: column; gap: 8px; }
.whitelist-options :deep(.el-checkbox) { height: auto; align-items: flex-start; margin-right: 0; }
.whitelist-type { display: inline-block; min-width: 190px; font-family: 'JetBrains Mono', Consolas, monospace; font-size: 12px; color: #303133; }
.whitelist-label { font-size: 12px; color: #606266; }

@media (max-width: 1280px) {
  .sql-execution-page { height: auto; overflow: visible; }
  .sql-command-bar { align-items: flex-start; flex-direction: column; }
  .command-actions { justify-content: flex-start; }
  .sql-workbench { grid-template-columns: 1fr; }
  .sql-file-panel { border-right: 0; border-bottom: 1px solid #e4e7ed; max-height: 360px; }
  .sql-main-panel { min-height: 640px; }
}
</style>

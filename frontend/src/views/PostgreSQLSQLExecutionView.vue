<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import JSZip from 'jszip'
import {
  cancelSQLFile, deleteSQLFile, executeSQLFileWithOptions, getSQLFile, importServerSQL,
  listDoneSQLFiles, listTodoSQLFiles, parseSQLBatch, parseSQLFile, skipSQLFile, skipSQLStatement,
  type SQLChangeFile, type SQLChangeStatement, type SQLExecuteOptions,
} from '../api/postgresql'
import { getSettings } from '../api/settings'
import { formatTime } from '../utils/time'

const files = ref<SQLChangeFile[]>([])
const currentFile = ref<SQLChangeFile | null>(null)
const statements = ref<SQLChangeStatement[]>([])
const selectedStatement = ref<SQLChangeStatement | null>(null)
const group = ref<'todo' | 'done'>('todo')
const page = ref(1)
const total = ref(0)
const loading = ref(false)
const submitting = ref(false)
const stopping = ref(false)
const viewMode = ref<'list' | 'full'>('list')
const filter = ref({ version: '', schemaName: '', fileName: '' })
const database = ref('')
const options = ref<SQLExecuteOptions>({ skipExistsColumn: false, skipExistsTable: false, skipUniqueConstraint: false })
const importVisible = ref(false)
const importMode = ref<'local' | 'text' | 'server'>('local')
const importing = ref(false)
const reading = ref(false)
const overwrite = ref(false)
const localFiles = ref<{ fileName: string; content: string }[]>([])
const draft = ref({ fileName: '', content: '' })
const serverPath = ref('')
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
let selection = 0
const running = computed(() => currentFile.value?.executeStatus === 'RUNNING')
const busy = computed(() => running.value || submitting.value)
const hasUncertainResult = computed(() => statements.value.some(s => ['RUNNING', 'UNKNOWN'].includes(s.executeStatus)))
const canExecute = computed(() => currentFile.value && !hasUncertainResult.value && !busy.value && !['SUCCESS', 'SKIPPED'].includes(currentFile.value.executeStatus))
const count = (status: string) => statements.value.filter(s => s.executeStatus === status).length
const statusText = (status: string) => ({ PENDING: '待执行', RUNNING: '执行中', SUCCESS: '成功', FAILED: '失败', PARTIAL_FAILED: '失败', SKIPPED: '跳过', CANCELED: '已停止', UNKNOWN: '结果待核实', NOT_EXECUTABLE: '待执行', BLOCKED: '待执行', TIMEOUT: '超时' }[status] || status)
const statusType = (status: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' => status === 'SUCCESS' ? 'success' : ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN', 'TIMEOUT'].includes(status) ? 'danger' : status === 'RUNNING' ? 'primary' : status === 'SKIPPED' ? 'warning' : 'info'

async function loadFiles() {
  loading.value = true
  try {
    const result = await (group.value === 'todo' ? listTodoSQLFiles : listDoneSQLFiles)(page.value, 20, filter.value)
    files.value = result.list || []; total.value = result.total
  } finally { loading.value = false }
}
async function searchFiles() { page.value = 1; await loadFiles() }
function clearPolling() { if (timer) clearTimeout(timer); timer = undefined }
function applyFile(file: SQLChangeFile, rows: SQLChangeStatement[]) {
  currentFile.value = file; statements.value = rows
  if (selectedStatement.value) selectedStatement.value = rows.find(s => s.id === selectedStatement.value?.id) || null
}
async function openFile(file: SQLChangeFile) {
  const selected = ++selection; clearPolling()
  const data = await getSQLFile(file.id)
  if (disposed || selected !== selection) return
  selectedStatement.value = null; applyFile(data.file, data.statements || [])
  if (running.value) schedulePoll(file.id, selected)
}
function schedulePoll(id: number, selected: number) {
  clearPolling()
  timer = setTimeout(async () => {
    if (disposed || selected !== selection) return
    try {
      const data = await getSQLFile(id)
      if (disposed || selected !== selection) return
      applyFile(data.file, data.statements || [])
      if (data.file.executeStatus === 'RUNNING') schedulePoll(id, selected)
      else { showResult(data.file); await loadFiles() }
    } catch { if (!disposed && selected === selection) schedulePoll(id, selected) }
  }, 1000)
}
function showResult(file: SQLChangeFile) {
  const message = file.executeMessage || statusText(file.executeStatus)
  if (file.executeStatus === 'SUCCESS') ElMessage.success(message)
  else if (['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(file.executeStatus)) ElMessage.error('执行失败，请查看文件结果和失败语句详情')
  else ElMessage.info(message)
}
async function executeFile() {
  if (!currentFile.value || !canExecute.value) return
  submitting.value = true
  try {
    const data = await executeSQLFileWithOptions(currentFile.value.id, { ...options.value })
    applyFile(data.file, data.statements || [])
    if (running.value) { schedulePoll(data.file.id, selection); ElMessage.info('文件已提交后台执行') }
    else showResult(data.file)
    await loadFiles()
  } finally { submitting.value = false }
}
async function stopFile() {
  if (!currentFile.value || !running.value) return
  stopping.value = true
  try { await cancelSQLFile(currentFile.value.id); ElMessage.info('停止请求已提交，当前语句结束后停止后续执行') }
  finally { stopping.value = false }
}
async function skipFile() {
  if (!currentFile.value || busy.value) return
  try { await ElMessageBox.confirm(`将 ${currentFile.value.fileName} 标记为跳过，未执行的 SQL 将不再执行。${hasUncertainResult.value ? '存在结果待核实的语句，请先核实全部数据库执行结果。' : ''}`, '跳过文件') } catch { return }
  await skipSQLFile(currentFile.value.id); await openFile(currentFile.value); await loadFiles()
}
async function skipStatement(row: SQLChangeStatement) {
  if (busy.value) return
  try { await ElMessageBox.confirm(`将第 ${row.lineNumber} 行标记为跳过。${row.executeStatus === 'UNKNOWN' ? '请先核实这句 SQL 的数据库执行结果。' : ''}`, '跳过语句') } catch { return }
  await skipSQLStatement(row.id)
  if (currentFile.value) await openFile(currentFile.value)
  await loadFiles()
}
async function removeFile() {
  if (!currentFile.value || busy.value) return
  try { await ElMessageBox.confirm(`删除 ${currentFile.value.fileName} 的文件记录？不会撤销已执行的 SQL。`, '删除文件') } catch { return }
  await deleteSQLFile(currentFile.value.id); clearPolling(); selection++; currentFile.value = null; statements.value = []; selectedStatement.value = null
  await loadFiles()
}
async function readLocalFiles(event: Event) {
  const input = event.target as HTMLInputElement
  const selected = Array.from(input.files || [])
  input.value = ''; localFiles.value = []; reading.value = true
  try {
    const result: { fileName: string; content: string }[] = []; const names = new Set<string>(); let size = 0
    const add = (fileName: string, content: string) => {
      if (names.has(fileName)) throw new Error(`存在同名 SQL 文件：${fileName}`)
      names.add(fileName); size += new TextEncoder().encode(content).length
      if (result.length >= 500 || size > 50 * 1024 * 1024) throw new Error('最多导入 500 个 SQL 文件，总大小不超过 50MB')
      result.push({ fileName, content })
    }
    for (const file of selected) {
      if (file.size > 50 * 1024 * 1024) throw new Error(`${file.name} 超过 50MB`)
      if (file.name.toLowerCase().endsWith('.zip')) {
        const zip = await JSZip.loadAsync(file)
        for (const entry of Object.values(zip.files)) {
          if (entry.dir || !entry.name.toLowerCase().endsWith('.sql')) continue
          add(entry.name.split('/').pop() || entry.name, await entry.async('string'))
        }
      } else if (file.name.toLowerCase().endsWith('.sql')) add(file.name, await file.text())
      else throw new Error('仅支持 SQL 或 ZIP 文件')
    }
    localFiles.value = result
    if (!result.length) ElMessage.warning('没有找到 SQL 文件')
  } catch (err) { ElMessage.error(err instanceof Error ? err.message : '读取 SQL 文件失败') }
  finally { reading.value = false }
}
async function importFiles() {
  importing.value = true
  try {
    let first: SQLChangeFile | undefined
    if (importMode.value === 'server') {
      if (!serverPath.value.trim()) { ElMessage.warning('请输入服务器文件路径'); return }
      const result = await importServerSQL(serverPath.value.trim(), overwrite.value)
      ElMessage.success(`已导入 ${result.count} 个 SQL 文件`)
    } else if (importMode.value === 'text') {
      if (!draft.value.fileName.trim() || !draft.value.content.trim()) { ElMessage.warning('请输入文件名和 SQL 内容'); return }
      const result = await parseSQLFile({ ...draft.value, fileName: draft.value.fileName.trim(), overwrite: overwrite.value }); first = result.file
    } else {
      if (!localFiles.value.length) { ElMessage.warning('请选择 SQL 或 ZIP 文件'); return }
      const result = await parseSQLBatch({ files: localFiles.value, overwrite: overwrite.value }); first = result.files[0]
    }
    importVisible.value = false; group.value = 'todo'; page.value = 1
    filter.value = { version: '', schemaName: '', fileName: '' }; await loadFiles()
    if (first) await openFile(first)
  } finally { importing.value = false }
}
onMounted(async () => {
  await loadFiles()
  const settings = await getSettings()
  database.value = settings.postgresql_database || '未配置'
  options.value = {
    skipExistsColumn: settings.sql_execute_skip_exists_column === 'true',
    skipExistsTable: settings.sql_execute_skip_exists_table === 'true',
    skipUniqueConstraint: settings.sql_execute_skip_unique_constraint === 'true',
  }
})
onBeforeUnmount(() => { disposed = true; clearPolling() })
</script>

<template>
  <div class="sql-page">
    <div class="toolbar">
      <strong>SQL 执行</strong>
      <span class="muted">数据库：{{ database }} · 按文件执行，普通错误立即停止</span>
      <el-button type="primary" :disabled="busy" @click="importVisible = true">导入 SQL</el-button>
      <el-button @click="loadFiles">刷新</el-button>
      <router-link to="/settings/sql-execution">SQL执行配置</router-link>
    </div>
    <div class="sql-workbench">
      <aside class="sql-file-panel">
        <el-radio-group v-model="group" @change="searchFiles">
          <el-radio-button value="todo">待执行</el-radio-button><el-radio-button value="done">已完成</el-radio-button>
        </el-radio-group>
        <div class="filters">
          <el-input v-model="filter.version" placeholder="版本" clearable @keyup.enter="searchFiles" />
          <el-input v-model="filter.schemaName" placeholder="schema" clearable @keyup.enter="searchFiles" />
          <el-input v-model="filter.fileName" placeholder="文件名" clearable @keyup.enter="searchFiles" />
          <el-button @click="searchFiles">查询</el-button>
        </div>
        <div v-loading="loading" class="files">
          <button v-for="file in files" :key="file.id" class="file-item" :class="{ active: currentFile?.id === file.id }" :disabled="submitting" @click="openFile(file)">
            <span class="file-name">{{ file.fileName }}</span>
            <span class="file-meta">{{ file.schemaName }} · {{ statusText(file.executeStatus) }}</span>
          </button>
          <el-empty v-if="!loading && !files.length" description="暂无文件" :image-size="50" />
        </div>
        <el-pagination v-model:current-page="page" small layout="prev, pager, next" :page-size="20" :total="total" @current-change="loadFiles" />
      </aside>
      <main class="sql-content">
        <template v-if="currentFile">
          <div class="file-heading">
            <div><strong>{{ currentFile.fileName }}</strong><div class="muted">版本 {{ currentFile.version }} · 顺序 {{ currentFile.groupSortNo }} · 执行账号 / schema：{{ currentFile.schemaName }}</div></div>
            <el-tag :type="statusType(currentFile.executeStatus)">{{ statusText(currentFile.executeStatus) }}</el-tag>
          </div>
          <div class="options">
            <el-checkbox v-model="options.skipExistsColumn" :disabled="busy">字段已存在时跳过</el-checkbox>
            <el-checkbox v-model="options.skipExistsTable" :disabled="busy">对象已存在时跳过</el-checkbox>
            <el-checkbox v-model="options.skipUniqueConstraint" :disabled="busy">唯一约束冲突时跳过</el-checkbox>
          </div>
          <div class="actions">
            <el-button type="primary" :disabled="!canExecute" :loading="submitting" @click="executeFile">{{ ['FAILED', 'PARTIAL_FAILED', 'CANCELED'].includes(currentFile.executeStatus) ? '重试当前文件' : '执行当前文件' }}</el-button>
            <el-button v-if="running" type="warning" :loading="stopping" @click="stopFile">停止执行</el-button>
            <el-button :disabled="busy || ['SUCCESS', 'SKIPPED'].includes(currentFile.executeStatus)" @click="skipFile">跳过文件</el-button>
            <el-button type="danger" plain :disabled="busy || hasUncertainResult || currentFile.executeStatus === 'SUCCESS'" @click="removeFile">删除文件</el-button>
            <span class="muted">共 {{ statements.length }} 句 · 成功 {{ count('SUCCESS') }} · 失败 {{ count('FAILED') }} · 跳过 {{ count('SKIPPED') }}</span>
          </div>
          <p class="muted">每句最长执行 60 秒。重试原文件时跳过已成功和已跳过的语句；已经提交的 SQL 不自动撤销。</p>
          <el-alert v-if="hasUncertainResult && !running" title="执行结果待核实，不能重试、覆盖或删除；请先核实目标库结果，确认已生效后再手工跳过。" type="warning" :closable="false" />
          <pre v-if="currentFile.executeMessage" class="execution-result" :class="{ error: ['FAILED', 'PARTIAL_FAILED'].includes(currentFile.executeStatus) }">{{ currentFile.executeMessage }}</pre>
          <div v-if="currentFile.executeTime" class="muted">执行人：{{ currentFile.executeUser || '—' }} · {{ formatTime(currentFile.executeTime) }}</div>
          <el-radio-group v-model="viewMode" class="view-tabs"><el-radio-button value="list">SQL列表</el-radio-button><el-radio-button value="full">完整SQL</el-radio-button></el-radio-group>
          <pre v-if="viewMode === 'full'" class="sql-source">{{ currentFile.fileContent }}</pre>
          <el-table v-else :data="statements" stripe row-key="id">
            <el-table-column prop="lineNumber" label="行号" width="65" />
            <el-table-column label="SQL" min-width="220" show-overflow-tooltip><template #default="{ row }">{{ row.sqlContent.replace(/\s+/g, ' ') }}</template></el-table-column>
            <el-table-column label="状态" width="110"><template #default="{ row }"><el-tag :type="statusType(row.executeStatus)">{{ statusText(row.executeStatus) }}</el-tag></template></el-table-column>
            <el-table-column prop="executeMessage" label="执行信息" min-width="180" show-overflow-tooltip />
            <el-table-column prop="affectedRows" label="影响行数" width="90" />
            <el-table-column label="操作" width="120"><template #default="{ row }"><el-button link type="primary" @click="selectedStatement = row">详情</el-button><el-button link type="warning" :disabled="busy || ['SUCCESS', 'SKIPPED', 'RUNNING'].includes(row.executeStatus) || currentFile.executeStatus === 'SUCCESS'" @click="skipStatement(row)">跳过</el-button></template></el-table-column>
          </el-table>
          <section v-if="selectedStatement" class="detail">
            <div class="file-heading"><strong>第 {{ selectedStatement.lineNumber }} 行 · {{ statusText(selectedStatement.executeStatus) }}</strong><el-button link @click="selectedStatement = null">收起</el-button></div>
            <pre class="sql-source">{{ selectedStatement.sqlContent }}</pre><pre class="execution-result">{{ selectedStatement.executeMessage || '待执行' }}</pre>
            <p class="muted">SQLSTATE：{{ selectedStatement.sqlState || '—' }} · 耗时 {{ selectedStatement.durationMs }} ms · 执行人 {{ selectedStatement.executeUser || '—' }} · {{ selectedStatement.executeTime ? formatTime(selectedStatement.executeTime) : '未执行' }}</p>
          </section>
        </template>
        <el-empty v-else description="导入或选择一个 SQL 文件" />
      </main>
    </div>
    <el-dialog v-model="importVisible" title="导入 SQL 文件" width="760px" :close-on-click-modal="!importing" :show-close="!importing">
      <p>文件名：版本__排序号__schema.sql，例如 <code>V2.0.0__001__df_zhushuju.sql</code>。schema 同时作为执行账号。</p>
      <el-radio-group v-model="importMode" :disabled="importing || reading"><el-radio-button value="local">本地 SQL / ZIP</el-radio-button><el-radio-button value="text">粘贴 SQL</el-radio-button><el-radio-button value="server">服务器文件</el-radio-button></el-radio-group>
      <div v-if="importMode === 'local'" class="import-body"><input type="file" accept=".sql,.zip" multiple :disabled="importing || reading" @change="readLocalFiles" /><p>{{ reading ? '正在读取文件…' : `已选择 ${localFiles.length} 个 SQL 文件` }}</p><div class="import-list"><div v-for="file in localFiles" :key="file.fileName">{{ file.fileName }}</div></div></div>
      <div v-else-if="importMode === 'text'" class="import-body"><el-input v-model="draft.fileName" placeholder="V2.0.0__001__df_zhushuju.sql" :disabled="importing" /><el-input v-model="draft.content" type="textarea" :rows="12" placeholder="输入完整 SQL 脚本" :disabled="importing" /></div>
      <div v-else class="import-body"><el-input v-model="serverPath" placeholder="/opt/df-build-server/download/V2.0.0__001__df_zhushuju.sql" :disabled="importing" /><p class="muted">支持服务器 /opt/df-build-server、/root、/tmp 目录下的 SQL 或 ZIP 文件。</p></div>
      <el-checkbox v-model="overwrite" :disabled="importing">覆盖同名文件，保留相同语句的成功和跳过记录</el-checkbox>
      <template #footer><el-button :disabled="importing" @click="importVisible = false">取消</el-button><el-button type="primary" :loading="importing" :disabled="reading" @click="importFiles">保存文件</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.sql-page { display: flex; flex-direction: column; gap: 16px; }
.toolbar, .file-heading, .actions { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.toolbar { padding: 16px; background: white; border-radius: 8px; }
.toolbar .muted { flex: 1; }
.muted, .file-meta { color: #64748b; font-size: 13px; line-height: 1.7; }
.sql-workbench { display: grid; grid-template-columns: 280px minmax(0, 1fr); gap: 16px; }
.sql-file-panel, .sql-content { background: white; padding: 16px; border-radius: 8px; min-width: 0; }
.filters { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; margin: 12px 0; }
.files { min-height: 180px; max-height: 65vh; overflow: auto; }
.file-item { display: block; width: 100%; padding: 12px 8px; text-align: left; border: 0; border-bottom: 1px solid #e2e8f0; background: transparent; cursor: pointer; }
.file-item.active { background: #eff6ff; }
.file-name, .file-meta { display: block; overflow-wrap: anywhere; }
.file-heading { justify-content: space-between; margin-bottom: 12px; }
.options, .actions, .view-tabs { margin: 12px 0; }
.execution-result, .sql-source { white-space: pre-wrap; overflow-wrap: anywhere; font: 13px/1.6 ui-monospace, monospace; background: #f8fafc; border: 1px solid #e2e8f0; padding: 12px; border-radius: 6px; max-height: 420px; overflow: auto; }
.execution-result.error { color: #b91c1c; background: #fef2f2; }
.detail { margin-top: 16px; border-top: 1px solid #e2e8f0; padding-top: 12px; }
.import-body { display: grid; gap: 12px; margin: 18px 0; }
.import-list { max-height: 180px; overflow: auto; }
@media (max-width: 900px) { .sql-workbench { grid-template-columns: 1fr; } .files { max-height: 220px; } }
</style>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listManagedServers, type ManagedServer } from '../api/server-mgmt'
import {
  listLogSources, saveLogSource, deleteLogSource, listLogFiles, startLogCollection,
  listLogCollections, getLogCollection, cancelLogCollection, deleteLogCollection, downloadLogCollection,
  type LogSource, type LogFile, type CollectionJob,
} from '../api/log-collection'

const categories = [{ value: 'microservice', label: '微服务' }, { value: 'database', label: '数据库' },
  { value: 'rabbitmq', label: 'RabbitMQ' }, { value: 'redis', label: 'Redis' }, { value: 'other', label: '其他' }]
const statusLabels: Record<string, string> = { running: '收集中', pending: '等待收集', completed: '全部成功', partial: '部分成功', failed: '失败', canceled: '已停止', success: '成功' }
function categoryName(value: string) { return categories.find(c => c.value === value)?.label || value }
function statusType(value: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' {
  return ['completed', 'success'].includes(value) ? 'success' : value === 'failed' ? 'danger' : value === 'partial' ? 'warning' : value === 'running' ? 'primary' : 'info'
}
function size(bytes: number) {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']; const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), 4)
  return `${(bytes / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`
}
function time(value?: string | null) { return value && !value.startsWith('0001') ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—' }

const tab = ref('sources')
const sources = ref<LogSource[]>([])
const hosts = ref<ManagedServer[]>([])
const selectedSources = ref<LogSource[]>([])
const sourceHostCount = computed(() => new Set(sources.value.map(source => source.serverId)).size)
const sourceTypeCount = computed(() => new Set(sources.value.map(source => source.category)).size)
const categoryIcons: Record<string, string> = { microservice: 'Box', database: 'Coin', rabbitmq: 'Connection', redis: 'DataLine', other: 'Document' }
const sourceSearch = ref('')
const categoryFilter = ref('')
const sourcesLoading = ref(false)
const displayedSources = computed(() => sources.value.filter(s => (!categoryFilter.value || s.category === categoryFilter.value)
  && `${s.name} ${s.directory} ${hostName(s.serverId)}`.toLowerCase().includes(sourceSearch.value.toLowerCase())))
function hostName(id: number) { const host = hosts.value.find(h => h.id === id); return host ? `${host.host}${host.remark ? ` (${host.remark})` : ''}` : `主机 #${id}（已删除）` }
async function loadSources() {
  sourcesLoading.value = true
  try { [sources.value, hosts.value] = await Promise.all([listLogSources(), listManagedServers()]) }
  finally { sourcesLoading.value = false }
}
const sourceDialog = ref(false)
const saving = ref(false)
const blankSource = (): LogSource => ({ id: 0, name: '', category: 'microservice', serverId: 0, directory: '', pattern: '*.log,*.log.*,*.gz', recursive: false })
const form = ref<LogSource>(blankSource())
function editSource(source?: LogSource) { form.value = source ? { ...source } : blankSource(); sourceDialog.value = true }
async function saveSource() {
  if (!form.value.name.trim() || !form.value.serverId || !form.value.directory.trim()) { ElMessage.warning('请填写名称、主机和日志目录'); return }
  saving.value = true
  try { await saveLogSource(form.value); sourceDialog.value = false; await loadSources(); ElMessage.success('日志来源已保存') }
  catch { /* API displays details. */ } finally { saving.value = false }
}
async function removeSource(source: LogSource) {
  try { await ElMessageBox.confirm(`删除日志来源“${source.name}”？已有收集记录和主机上的日志文件会保留。`, '删除来源', { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' }); await deleteLogSource(source.id); await loadSources() }
  catch { /* Cancel or API error. */ }
}

type PreviewFile = LogFile & { key: string; source: LogSource }
const previewDialog = ref(false)
const scanning = ref(false)
const scanMessage = ref('')
const scanErrors = ref<string[]>([])
const previewFiles = ref<PreviewFile[]>([])
const selectedFiles = ref<PreviewFile[]>([])
const fileSearch = ref('')
const modifiedRange = ref<Date[] | null>(null)
const fileTable = ref<any>()
let scanController: AbortController | undefined
const filteredFiles = computed(() => previewFiles.value.filter(f => {
  const modified = new Date(f.modifiedAt).getTime()
  return f.path.toLowerCase().includes(fileSearch.value.toLowerCase()) && (!modifiedRange.value ||
    (modified >= modifiedRange.value[0]!.getTime() && modified <= modifiedRange.value[1]!.getTime()))
}))
const selectedSize = computed(() => selectedFiles.value.reduce((sum, file) => sum + file.size, 0))
async function preview(list: LogSource[]) {
  scanController?.abort(); const controller = new AbortController(); scanController = controller
  previewDialog.value = true; scanning.value = true; previewFiles.value = []; scanErrors.value = []; selectedFiles.value = []
  fileSearch.value = ''; modifiedRange.value = null
  try {
    for (const source of list) {
      if (controller.signal.aborted) break
      scanMessage.value = `读取 ${source.name} · ${hostName(source.serverId)} · ${source.directory}`
      try {
        const files = await listLogFiles(source.id, controller.signal)
        if (controller.signal.aborted) break
        previewFiles.value.push(...files.map(f => ({ ...f, source, key: `${source.id}/${f.path}` })))
        if (!files.length) scanErrors.value.push(`${source.name} · ${hostName(source.serverId)} · ${source.directory}：没有匹配的普通日志文件`)
      } catch (e: any) {
        if (!controller.signal.aborted) scanErrors.value.push(`${source.name} · ${hostName(source.serverId)} · ${source.directory}：${e.message}`)
      }
    }
  } finally { if (scanController === controller) { scanning.value = false; scanMessage.value = '' } }
}
function closePreview() { scanController?.abort() }
async function selectAllFiles() {
  await nextTick(); fileTable.value?.clearSelection()
  for (const file of filteredFiles.value) fileTable.value?.toggleRowSelection(file, true)
}
const starting = ref(false)
async function startCollection() {
  if (!selectedFiles.value.length) return
  const grouped = new Map<number, string[]>()
  for (const file of selectedFiles.value) { const paths = grouped.get(file.source.id) || []; paths.push(file.path); grouped.set(file.source.id, paths) }
  starting.value = true
  try {
    const job = await startLogCollection([...grouped].map(([sourceId, files]) => ({ sourceId, files })))
    previewDialog.value = false; tab.value = 'jobs'; detail.value = job; detailDialog.value = true; page.value = 1
    await loadJobs(); ElMessage.success('日志收集已开始')
  } catch { /* API displays details. */ } finally { starting.value = false }
}

const jobs = ref<CollectionJob[]>([])
const page = ref(1)
const total = ref(0)
const detail = ref<CollectionJob | null>(null)
const detailDialog = ref(false)
const stopping = ref(false)
async function loadJobs() { const result = await listLogCollections(page.value); jobs.value = result.list; total.value = result.total }
async function openJob(job: CollectionJob) { detail.value = await getLogCollection(job.id); detailDialog.value = true }
async function stopJob() {
  if (!detail.value) return
  stopping.value = true
  try { await cancelLogCollection(detail.value.id); ElMessage.info('已请求停止；压缩中的文件完成后结束'); detail.value = await getLogCollection(detail.value.id) }
  catch { /* API displays details. */ } finally { stopping.value = false }
}
async function removeJob(job: CollectionJob) {
  try {
    await ElMessageBox.confirm(`删除收集任务 #${job.id} 及生成的 ZIP？主机上的原始日志文件会保留。`, '删除收集包', { confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning' })
    await deleteLogCollection(job.id)
    if (detail.value?.id === job.id) detailDialog.value = false
    await loadJobs()
  } catch { /* Cancel or API error. */ }
}
let pollTimer: ReturnType<typeof setTimeout> | undefined
let disposed = false
async function poll() {
  try {
    if (tab.value === 'jobs' || jobs.value.some(j => j.status === 'running')) await loadJobs()
    if (detailDialog.value && detail.value?.status === 'running') {
      const id = detail.value.id; const updated = await getLogCollection(id)
      if (detail.value?.id === id) detail.value = updated
    }
  } catch { /* Retry on next poll. */ }
  if (!disposed) pollTimer = setTimeout(poll, 3000)
}
onMounted(async () => { try { await Promise.all([loadSources(), loadJobs()]) } catch { /* API displays details. */ } if (!disposed) pollTimer = setTimeout(poll, 3000) })
onUnmounted(() => { disposed = true; clearTimeout(pollTimer); scanController?.abort() })
</script>

<template>
  <div class="log-collection">
    <header class="collection-heading">
      <div><h1 class="console-title">日志收集</h1></div>
      <div class="heading-actions" v-if="tab === 'sources'"><el-button @click="editSource()"><el-icon><Plus /></el-icon><span>新增来源</span></el-button><el-button type="primary" :disabled="!selectedSources.length" @click="preview([...selectedSources])"><el-icon><Files /></el-icon><span>选择日志文件（{{ selectedSources.length }} 个来源）</span></el-button></div>
    </header>
    <div class="source-overview">
      <div class="overview-metric"><span class="metric-symbol"><Files /></span><div><span>日志来源</span><strong>{{ sources.length }}<small>个</small></strong></div></div>
      <div class="overview-metric"><span class="metric-symbol cyan"><Platform /></span><div><span>覆盖主机</span><strong>{{ sourceHostCount }}<small>台</small></strong></div></div>
      <div class="overview-metric"><span class="metric-symbol violet"><Grid /></span><div><span>日志类型</span><strong>{{ sourceTypeCount }}<small>类</small></strong></div></div>
    </div>
    <div class="console-panel collection-panel">
    <el-tabs v-model="tab">
      <el-tab-pane label="日志来源" name="sources">
        <div class="toolbar">
          <el-input v-model="sourceSearch" placeholder="名称、主机或目录" prefix-icon="Search" clearable style="width: 260px" />
          <el-select v-model="categoryFilter" clearable placeholder="全部类型" style="width: 145px"><el-option v-for="c in categories" :key="c.value" :label="c.label" :value="c.value" /></el-select>
          <span class="toolbar-spacer"></span><span class="result-count">共 {{ displayedSources.length }} 个来源</span><el-button @click="loadSources" aria-label="刷新日志来源"><el-icon><Refresh /></el-icon></el-button>
        </div>
        <el-table :data="displayedSources" v-loading="sourcesLoading" row-key="id" @selection-change="selectedSources = $event" empty-text="暂无日志来源">
          <el-table-column type="selection" width="42" />
          <el-table-column prop="name" label="来源名称" min-width="190"><template #default="{ row }"><div class="source-identity"><span class="source-symbol" :class="row.category"><el-icon><component :is="categoryIcons[row.category] || 'Document'" /></el-icon></span><strong>{{ row.name }}</strong></div></template></el-table-column>
          <el-table-column label="类型" width="105"><template #default="{ row }"><span class="category-label" :class="row.category">{{ categoryName(row.category) }}</span></template></el-table-column>
          <el-table-column label="主机" min-width="160"><template #default="{ row }"><span class="host-label">{{ hostName(row.serverId) }}</span></template></el-table-column>
          <el-table-column prop="directory" label="日志目录" min-width="210" show-overflow-tooltip><template #default="{ row }"><span class="console-code directory-text">{{ row.directory }}</span></template></el-table-column>
          <el-table-column prop="pattern" label="文件规则" min-width="140" show-overflow-tooltip><template #default="{ row }"><span class="pattern-label console-code">{{ row.pattern }}</span></template></el-table-column>
          <el-table-column label="子目录" width="75"><template #default="{ row }">{{ row.recursive ? '包含' : '不包含' }}</template></el-table-column>
          <el-table-column label="操作" width="175"><template #default="{ row }"><el-button link type="primary" @click="preview([row])">收集</el-button><el-button link @click="editSource(row)">编辑</el-button><el-button link type="danger" @click="removeSource(row)">删除</el-button></template></el-table-column>
        </el-table>

      </el-tab-pane>
      <el-tab-pane label="收集记录" name="jobs">
        <div class="toolbar"><el-button @click="loadJobs">刷新</el-button></div>
        <el-table :data="jobs" empty-text="暂无收集记录">
          <el-table-column prop="id" label="编号" width="80" />
          <el-table-column label="开始时间" width="185"><template #default="{ row }">{{ time(row.createdAt) }}</template></el-table-column>
          <el-table-column prop="operator" label="操作人" width="100" />
          <el-table-column label="结果" width="105"><template #default="{ row }"><el-tag :type="statusType(row.status)">{{ statusLabels[row.status] }}</el-tag></template></el-table-column>
          <el-table-column label="文件" width="190"><template #default="{ row }">成功 {{ row.successFiles }} / {{ row.totalFiles }}，失败 {{ row.failedFiles }}</template></el-table-column>
          <el-table-column prop="message" label="详情" min-width="180" show-overflow-tooltip />
          <el-table-column label="ZIP 大小" width="105"><template #default="{ row }">{{ row.archiveSize ? size(row.archiveSize) : '—' }}</template></el-table-column>
          <el-table-column label="操作" width="195"><template #default="{ row }"><el-button link type="primary" @click="openJob(row)">详情</el-button><el-button link type="primary" :disabled="row.status === 'running' || !row.archiveSize" @click="downloadLogCollection(row.id)">下载</el-button><el-button link type="danger" :disabled="row.status === 'running'" @click="removeJob(row)">删除</el-button></template></el-table-column>
        </el-table>
        <el-pagination v-model:current-page="page" :page-size="20" :total="total" layout="total, prev, pager, next" @current-change="loadJobs" />
      </el-tab-pane>
    </el-tabs>
    </div>

    <el-dialog v-model="sourceDialog" :title="form.id ? '编辑日志来源' : '新增日志来源'" width="640px">
      <el-form label-width="105px" @submit.prevent>
        <el-form-item label="类型" required><el-select v-model="form.category"><el-option v-for="c in categories" :key="c.value" :label="c.label" :value="c.value" /></el-select></el-form-item>
        <el-form-item label="来源名称" required><el-input v-model="form.name" placeholder="例如：计费服务、PostgreSQL、RabbitMQ" /></el-form-item>
        <el-form-item label="主机" required><el-select v-model="form.serverId" filterable placeholder="选择已配置的主机"><el-option v-for="h in hosts" :key="h.id" :label="hostName(h.id)" :value="h.id" /></el-select><span v-if="!hosts.length" class="hint">请先在服务器管理中新增主机。</span></el-form-item>
        <el-form-item label="日志目录" required><el-input v-model="form.directory" placeholder="主机上的实际绝对路径，例如 /var/log/rabbitmq" /></el-form-item>
        <el-form-item label="文件规则" required><el-input v-model="form.pattern" placeholder="*.log,*.log.*,*.gz" /></el-form-item>
        <el-form-item label="包含子目录"><el-switch v-model="form.recursive" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="sourceDialog = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveSource">保存</el-button></template>
    </el-dialog>

    <el-dialog v-model="previewDialog" title="选择要收集的日志文件" width="min(1200px, 96vw)" @close="closePreview">

      <div v-if="scanning" class="scan-message">{{ scanMessage }}…</div>
      <div v-if="scanErrors.length" class="scan-errors"><div v-for="error in scanErrors" :key="error">{{ error }}</div></div>
      <div class="toolbar">
        <el-input v-model="fileSearch" placeholder="筛选文件路径" clearable style="width: 230px" />
        <el-date-picker v-model="modifiedRange" type="datetimerange" start-placeholder="文件修改时间起" end-placeholder="文件修改时间止" style="width: 380px" />
        <el-button :disabled="scanning" @click="selectAllFiles">全选当前结果</el-button><el-button @click="fileTable?.clearSelection()">清空选择</el-button>
      </div>
      <el-table ref="fileTable" :data="filteredFiles" row-key="key" max-height="390" @selection-change="selectedFiles = $event" empty-text="没有匹配的文件">
        <el-table-column type="selection" width="42" />
        <el-table-column label="来源" min-width="135"><template #default="{ row }">{{ categoryName(row.source.category) }} · {{ row.source.name }}</template></el-table-column>
        <el-table-column label="主机" min-width="140"><template #default="{ row }">{{ hostName(row.source.serverId) }}</template></el-table-column>
        <el-table-column prop="path" label="相对日志目录的路径" min-width="240" show-overflow-tooltip />
        <el-table-column label="大小" width="100"><template #default="{ row }">{{ size(row.size) }}</template></el-table-column>
        <el-table-column label="修改时间" width="180"><template #default="{ row }">{{ time(row.modifiedAt) }}</template></el-table-column>
      </el-table>
      <template #footer><span class="selection-info">已选 {{ selectedFiles.length }} 个文件，约 {{ size(selectedSize) }}（单次最多 5000 个）</span><el-button @click="previewDialog = false">关闭</el-button><el-button type="primary" :disabled="scanning || !selectedFiles.length || selectedFiles.length > 5000" :loading="starting" @click="startCollection">开始收集并打包</el-button></template>
    </el-dialog>

    <el-dialog v-model="detailDialog" :title="`收集详情 #${detail?.id || ''}`" width="min(1250px, 96vw)">
      <template v-if="detail">
        <div class="toolbar"><el-tag :type="statusType(detail.status)">{{ statusLabels[detail.status] }}</el-tag><span>操作人：{{ detail.operator }} · {{ time(detail.createdAt) }}</span><span>成功 {{ detail.successFiles }} / {{ detail.totalFiles }}，失败 {{ detail.failedFiles }}</span></div>
        <el-alert :title="detail.message" :type="detail.status === 'failed' ? 'error' : detail.status === 'partial' ? 'warning' : 'info'" :closable="false" />

        <el-table :data="detail.items" max-height="460">
          <el-table-column label="来源 / 主机" min-width="190"><template #default="{ row }">{{ row.source.name }}<br /><span class="hint">{{ row.host }}</span></template></el-table-column>
          <el-table-column label="日志文件" min-width="270"><template #default="{ row }"><span class="file-path">{{ row.source.directory }}/{{ row.path }}</span></template></el-table-column>
          <el-table-column label="状态" width="95"><template #default="{ row }"><el-tag :type="statusType(row.status)">{{ statusLabels[row.status] }}</el-tag></template></el-table-column>
          <el-table-column label="已读取 / 大小" width="170"><template #default="{ row }">{{ size(row.bytes) }} / {{ size(row.size) }}</template></el-table-column>
          <el-table-column label="错误详情" min-width="280"><template #default="{ row }"><span class="error-text">{{ row.error || '—' }}</span></template></el-table-column>
        </el-table>
      </template>
      <template #footer><el-button v-if="detail?.status === 'running'" type="warning" :loading="stopping" @click="stopJob">停止收集</el-button><el-button @click="detailDialog = false">关闭</el-button><el-button type="primary" :disabled="!detail?.archiveSize || detail?.status === 'running'" @click="detail && downloadLogCollection(detail.id)">下载 ZIP</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.log-collection { max-width: 1700px; margin-inline: auto; }
.collection-heading { display: flex; align-items: center; justify-content: space-between; gap: 24px; padding: 2px 0 26px; position: relative; }
.heading-actions { display: flex; gap: 10px; flex-shrink: 0; }.heading-actions :deep(.el-button + .el-button) { margin-left: 0; }.heading-actions :deep(.el-icon) { margin-right: 6px; }
.source-overview { display: flex; align-items: center; background: var(--ui-panel); border: 1px solid var(--ui-border); border-radius: 12px; padding: 22px 26px; margin-bottom: 24px; box-shadow: var(--ui-shadow); }
.overview-metric { display: flex; align-items: center; gap: 15px; flex: 1; padding-inline: 24px; border-right: 1px solid var(--ui-border); }.overview-metric:first-child { padding-left: 0; }.overview-metric:last-child { border-right: 0; padding-right: 0; }
.overview-metric > div > span { display: block; color: var(--ui-muted); font-size: 12px; }.overview-metric strong { display: block; font-size: 29px; color: var(--ui-heading); line-height: 1.3; font-weight: 600; font-variant-numeric: tabular-nums; letter-spacing: -1px; }.overview-metric small { font-size: 11px; color: var(--ui-muted); font-weight: 400; margin-left: 9px; }
.metric-symbol { width: 43px; height: 43px; padding: 12px; background: var(--ui-tint); color: var(--ui-primary); border-radius: 11px; }.metric-symbol.cyan { color: var(--ui-cyan); background: color-mix(in srgb, var(--ui-cyan) 8%, var(--ui-panel)); }.metric-symbol.violet { color: #7973c3; background: color-mix(in srgb, #7973c3 8%, var(--ui-panel)); }
.collection-panel { overflow: hidden; padding-top: 9px; }
.collection-panel :deep(.el-tabs__header) { margin: 0; }.collection-panel :deep(.el-tabs__nav-scroll) { padding-inline: 24px; }.collection-panel :deep(.el-tabs__item) { height: 51px; font-size: 13px; }
.toolbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin: 20px 0; }.collection-panel .toolbar { margin: 0; padding: 20px 24px; }.toolbar-spacer { flex: 1; }.result-count { color: var(--ui-muted); font-size: 11px; margin-right: 5px; }
.source-identity { display: flex; align-items: center; gap: 11px; }.source-identity strong { font-weight: 550; color: var(--ui-heading); font-size: 13px; }.source-symbol { width: 33px; height: 33px; display: inline-flex; align-items: center; justify-content: center; background: var(--ui-tint); color: var(--ui-primary); border-radius: 8px; flex-shrink: 0; }
.source-symbol.database { color: var(--ui-cyan); background: color-mix(in srgb, var(--ui-cyan) 9%, var(--ui-panel)); }.source-symbol.rabbitmq { color: #c48736; background: color-mix(in srgb, #c48736 9%, var(--ui-panel)); }.source-symbol.redis { color: #cb6778; background: color-mix(in srgb, #cb6778 9%, var(--ui-panel)); }
.category-label { display: inline-block; border: 1px solid var(--ui-border); padding: 2px 7px; border-radius: 4px; font-size: 10px; color: var(--ui-text); background: var(--ui-panel-soft); }
.host-label { font-size: 12px; white-space: nowrap; }.directory-text { color: var(--ui-muted); }.pattern-label { display: inline; font-size: 11px; color: var(--ui-text); }
.hint { color: var(--ui-muted); font-size: 12px; display: inline-block; line-height: 1.7; }.selection-info { margin-right: 16px; color: var(--ui-text); font-size: 12px; }
.scan-message { color: var(--ui-primary); margin-top: 12px; }.scan-errors { margin: 12px 0; padding: 12px; border-radius: 8px; background: var(--el-color-danger-light-9); color: var(--el-color-danger); white-space: pre-wrap; overflow-wrap: anywhere; }
.error-text, .file-path { white-space: pre-wrap; overflow-wrap: anywhere; }.error-text { color: var(--el-color-danger); }.file-path { font-family: var(--ui-font-mono); font-size: 11px; }
.el-pagination { padding: 18px 24px; }.el-form .el-select { width: 100%; }
@media (max-width: 1200px) {.overview-metric:nth-child(3) { border-right: 0; }.collection-heading { flex-wrap: wrap; }.source-overview { padding: 20px; } }
@media (max-width: 700px) { .overview-metric { padding-inline: 12px; gap: 8px; }.metric-symbol { display: none; }.overview-metric strong { font-size: 25px; }.heading-actions { flex-wrap: wrap; }.collection-panel .toolbar { padding: 15px; }.toolbar-spacer { display: none; }.selection-info { display: block; margin-bottom: 10px; } }
</style>

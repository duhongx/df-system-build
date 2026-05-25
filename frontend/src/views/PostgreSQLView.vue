<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  executeSQLFile,
  getPostgreSQLInstance,
  getSQLFile,
  listSQLFiles,
  parseSQLFile,
  skipSQLStatement,
  type PostgreSQLInstanceInfo,
  type SQLChangeFile,
  type SQLChangeStatement,
} from '../api/postgresql'
import { formatTime as formatTimeStr } from '../utils/time'

const router = useRouter()
const activeTab = ref<'instance' | 'sql'>('sql')
const loadingInstance = ref(false)
const parsing = ref(false)
const executing = ref(false)
const historyLoading = ref(false)

const instance = ref<PostgreSQLInstanceInfo | null>(null)
const files = ref<SQLChangeFile[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)

const form = ref({
  systemCode: '',
  environment: '',
  schemaName: 'public',
  version: '',
  fileName: '',
  content: '',
})
const currentFile = ref<SQLChangeFile | null>(null)
const statements = ref<SQLChangeStatement[]>([])

const blockedCount = computed(() => statements.value.filter(s => s.riskLevel === 'BLOCKED').length)
const warnCount = computed(() => statements.value.filter(s => s.riskLevel === 'WARN').length)
const executableCount = computed(() => statements.value.filter(s => s.riskLevel !== 'BLOCKED').length)

onMounted(async () => {
  await Promise.all([loadInstance(), loadHistory()])
})

async function loadInstance() {
  loadingInstance.value = true
  try {
    instance.value = await getPostgreSQLInstance()
  } finally {
    loadingInstance.value = false
  }
}

async function loadHistory() {
  historyLoading.value = true
  try {
    const data = await listSQLFiles(page.value, pageSize.value)
    files.value = data.list || []
    total.value = data.total || 0
  } finally {
    historyLoading.value = false
  }
}

function handleFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  form.value.fileName = file.name
  const reader = new FileReader()
  reader.onload = () => {
    form.value.content = String(reader.result || '')
  }
  reader.readAsText(file)
  input.value = ''
}

async function handleParse() {
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
    await loadHistory()
  } finally {
    parsing.value = false
  }
}

async function handleExecute() {
  if (!currentFile.value) {
    ElMessage.warning('请先解析 SQL')
    return
  }
  if (executableCount.value === 0) {
    ElMessage.warning('没有可执行的 SQL')
    return
  }
  executing.value = true
  try {
    const data = await executeSQLFile(currentFile.value.id)
    currentFile.value = data.file
    statements.value = data.statements || []
    ElMessage.success(data.file.executeMessage || '执行完成')
    await loadHistory()
  } finally {
    executing.value = false
  }
}

async function handleSkip(row: SQLChangeStatement) {
  await skipSQLStatement(row.id)
  row.executeStatus = 'SKIPPED'
  row.executeMessage = '手工跳过'
  ElMessage.success('已跳过')
}

async function handleOpenHistory(row: SQLChangeFile) {
  const data = await getSQLFile(row.id)
  currentFile.value = data.file
  statements.value = data.statements || []
  form.value = {
    systemCode: data.file.systemCode || '',
    environment: data.file.environment || '',
    schemaName: data.file.schemaName || 'public',
    version: data.file.version || '',
    fileName: data.file.fileName || '',
    content: data.file.fileContent || '',
  }
}

function riskTag(level: string) {
  if (level === 'BLOCKED') return 'danger'
  if (level === 'WARN') return 'warning'
  return 'success'
}

function statusTag(status: string) {
  if (status === 'SUCCESS') return 'success'
  if (status === 'FAILED' || status === 'PARTIAL_FAILED') return 'danger'
  if (status === 'BLOCKED') return 'danger'
  if (status === 'RUNNING') return 'primary'
  if (status === 'SKIPPED') return 'info'
  return 'info'
}
</script>

<template>
  <div class="postgres-page">
    <div class="page-header">
      <h4 class="page-title">PostgreSQL 管理</h4>
      <el-button size="small" @click="router.push('/settings/environment')">环境配置</el-button>
    </div>

    <el-tabs v-model="activeTab">
      <el-tab-pane label="SQL 执行" name="sql">
        <div class="sql-layout">
          <section class="sql-editor-panel">
            <div class="section-title">SQL 变更</div>
            <div class="meta-grid">
              <el-input v-model="form.systemCode" placeholder="业务系统" />
              <el-input v-model="form.environment" placeholder="环境" />
              <el-input v-model="form.schemaName" placeholder="Schema" />
              <el-input v-model="form.version" placeholder="版本号" />
            </div>
            <div class="file-row">
              <el-input v-model="form.fileName" placeholder="SQL 文件名" />
              <label class="upload-btn">
                <input type="file" accept=".sql,.txt" @change="handleFileChange" />
                <span>上传 SQL</span>
              </label>
            </div>
            <el-input
              v-model="form.content"
              type="textarea"
              :rows="14"
              placeholder="粘贴 SQL 内容，或上传 .sql 文件"
              class="sql-textarea"
            />
            <div class="action-row">
              <el-button :loading="parsing" @click="handleParse">解析并风险扫描</el-button>
              <el-button type="primary" :loading="executing" :disabled="!currentFile || executableCount === 0" @click="handleExecute">
                执行可执行 SQL
              </el-button>
              <el-tag v-if="statements.length" type="info">{{ statements.length }} 条</el-tag>
              <el-tag v-if="warnCount" type="warning">{{ warnCount }} 条风险提示</el-tag>
              <el-tag v-if="blockedCount" type="danger">{{ blockedCount }} 条已拦截</el-tag>
            </div>
          </section>

          <section class="history-panel">
            <div class="section-title">执行记录</div>
            <el-table :data="files" v-loading="historyLoading" size="small" stripe @row-click="handleOpenHistory">
              <el-table-column prop="fileName" label="文件" min-width="150" show-overflow-tooltip />
              <el-table-column prop="executeStatus" label="状态" width="110">
                <template #default="{ row }">
                  <el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="createdAt" label="创建时间" width="150">
                <template #default="{ row }">{{ formatTimeStr(row.createdAt) }}</template>
              </el-table-column>
            </el-table>
            <div class="pager-row">
              <el-pagination
                v-model:current-page="page"
                v-model:page-size="pageSize"
                small
                layout="prev, pager, next"
                :total="total"
                @current-change="loadHistory"
              />
            </div>
          </section>
        </div>

        <section class="statement-panel">
          <div class="section-title">SQL 明细</div>
          <el-table :data="statements" size="small" border>
            <el-table-column prop="lineNumber" label="行" width="60" />
            <el-table-column prop="sqlType" label="类型" width="150" />
            <el-table-column prop="riskLevel" label="风险" width="110">
              <template #default="{ row }">
                <el-tag :type="riskTag(row.riskLevel)" size="small">{{ row.riskLevel }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="executeStatus" label="执行状态" width="120">
              <template #default="{ row }">
                <el-tag :type="statusTag(row.executeStatus)" size="small">{{ row.executeStatus }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="affectedRows" label="影响行数" width="90" />
            <el-table-column prop="durationMs" label="耗时(ms)" width="90" />
            <el-table-column prop="sqlContent" label="SQL" min-width="260" show-overflow-tooltip />
            <el-table-column prop="riskReason" label="风险/错误" min-width="220" show-overflow-tooltip>
              <template #default="{ row }">{{ row.executeMessage || row.riskReason }}</template>
            </el-table-column>
            <el-table-column label="操作" width="90" fixed="right">
              <template #default="{ row }">
                <el-button
                  size="small"
                  link
                  type="warning"
                  :disabled="row.executeStatus === 'SUCCESS' || row.executeStatus === 'SKIPPED'"
                  @click="handleSkip(row)"
                >跳过</el-button>
              </template>
            </el-table-column>
          </el-table>
        </section>
      </el-tab-pane>

      <el-tab-pane label="实例管理" name="instance">
        <section class="instance-panel" v-loading="loadingInstance">
          <div class="instance-header">
            <div>
              <div class="section-title">当前 PostgreSQL 配置</div>
              <div class="hint">连接信息来自系统设置，不在此页面重复维护。</div>
            </div>
            <el-button size="small" @click="loadInstance">刷新</el-button>
          </div>

          <el-descriptions :column="3" border>
            <el-descriptions-item label="状态">
              <el-tag :type="instance?.status === 'UP' ? 'success' : 'danger'">{{ instance?.status || '-' }}</el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="主机">{{ instance?.config.host || '-' }}</el-descriptions-item>
            <el-descriptions-item label="端口">{{ instance?.config.port || '-' }}</el-descriptions-item>
            <el-descriptions-item label="数据库">{{ instance?.config.database || '-' }}</el-descriptions-item>
            <el-descriptions-item label="用户">{{ instance?.config.user || '-' }}</el-descriptions-item>
            <el-descriptions-item label="密码">{{ instance?.config.password || '-' }}</el-descriptions-item>
            <el-descriptions-item label="角色">{{ instance?.role || '-' }}</el-descriptions-item>
            <el-descriptions-item label="当前库">{{ instance?.currentDb || '-' }}</el-descriptions-item>
            <el-descriptions-item label="当前用户">{{ instance?.currentUser || '-' }}</el-descriptions-item>
            <el-descriptions-item label="服务地址">{{ instance?.serverAddr || '-' }}</el-descriptions-item>
            <el-descriptions-item label="服务端口">{{ instance?.serverPort || '-' }}</el-descriptions-item>
            <el-descriptions-item label="检测时间">{{ formatTimeStr(instance?.checkedAt) }}</el-descriptions-item>
            <el-descriptions-item label="版本" :span="3">{{ instance?.version || instance?.message || '-' }}</el-descriptions-item>
          </el-descriptions>

          <div class="section-title sub-title">关键参数</div>
          <el-table :data="Object.entries(instance?.settings || {}).map(([key, value]) => ({ key, value }))" size="small" border>
            <el-table-column prop="key" label="参数" width="240" />
            <el-table-column prop="value" label="值" />
          </el-table>

          <div class="section-title sub-title">复制状态</div>
          <el-table :data="instance?.replications || []" size="small" border>
            <el-table-column prop="clientAddr" label="Standby 地址" />
            <el-table-column prop="state" label="状态" />
            <el-table-column prop="syncState" label="同步状态" />
            <el-table-column prop="writeLag" label="Write Lag" />
            <el-table-column prop="flushLag" label="Flush Lag" />
            <el-table-column prop="replayLag" label="Replay Lag" />
          </el-table>
        </section>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<style scoped>
.postgres-page { display: flex; flex-direction: column; gap: 12px; }
.page-header { display: flex; align-items: center; justify-content: space-between; }
.page-title { font-size: 15px; font-weight: 600; margin: 0; color: #303133; }
.section-title { font-size: 14px; font-weight: 600; color: #303133; margin-bottom: 12px; }
.sub-title { margin-top: 18px; }
.hint { color: #909399; font-size: 12px; margin-top: 4px; }
.sql-layout { display: grid; grid-template-columns: minmax(0, 1fr) 420px; gap: 12px; align-items: start; }
.sql-editor-panel,
.history-panel,
.statement-panel,
.instance-panel {
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 6px;
  padding: 16px;
}
.meta-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin-bottom: 8px; }
.file-row { display: grid; grid-template-columns: minmax(0, 1fr) 96px; gap: 8px; margin-bottom: 8px; }
.upload-btn {
  height: 32px;
  border: 1px solid #dcdfe6;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  color: #606266;
  font-size: 13px;
}
.upload-btn input { display: none; }
.sql-textarea :deep(textarea) { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; line-height: 1.5; }
.action-row { display: flex; align-items: center; gap: 8px; margin-top: 10px; }
.pager-row { display: flex; justify-content: flex-end; margin-top: 8px; }
.statement-panel { margin-top: 12px; }
.instance-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
@media (max-width: 1200px) {
  .sql-layout { grid-template-columns: 1fr; }
  .meta-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>

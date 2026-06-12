<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  listComponents, getEnabled, putEnabled,
  getOverride, putOverride, getComponentDefaults, getComponentTasks,
  getTargets, putTargets,
  type DeploymentComponent, type ComponentTask,
} from '../api/deployment'
import { listManagedServers, type ManagedServer } from '../api/server-mgmt'

const loading = ref(false)
const components = ref<DeploymentComponent[]>([])
const servers = ref<ManagedServer[]>([])
const savingEnabled = ref(false)
let defaultsCache: Record<string, Record<string, any>> | null = null

// Override editor
const overrideVisible = ref(false)
const overrideComponent = ref('')
const overrideRows = ref<{ key: string; value: string }[]>([])
const overrideSaving = ref(false)
const overrideError = ref('')

// Hosts dialog
const hostsVisible = ref(false)
const hostsComponent = ref('')
const hostsSelected = ref<number[]>([])
const hostsSaving = ref(false)

// Tasks (plan) viewer
const tasksVisible = ref(false)
const tasksComponent = ref('')
const tasksItems = ref<ComponentTask[]>([])
const tasksLoading = ref(false)

async function load() {
  loading.value = true
  try {
    const [comps, srvs] = await Promise.all([listComponents(), listManagedServers()])
    components.value = comps
    servers.value = srvs
  } finally {
    loading.value = false
  }
}

function statusTag(s: string) {
  if (s === 'deployed') return 'success'
  if (s === 'failed') return 'danger'
  return 'info'
}
function categoryLabel(c: string) {
  return c === 'k8s' ? 'K8s' : c === 'business' ? '业务' : '其他'
}

// ---- enable / disable ----
async function toggleEnabled(row: DeploymentComponent) {
  savingEnabled.value = true
  try {
    const current = await getEnabled()
    const set = new Set(current)
    if (row.enabled) set.add(row.code)
    else set.delete(row.code)
    const ordered = current.filter(c => set.has(c))
    if (row.enabled && !current.includes(row.code)) ordered.push(row.code)
    await putEnabled(ordered)
    ElMessage.success(`${row.code} 已${row.enabled ? '启用' : '禁用'}`)
  } catch (e: any) {
    row.enabled = !row.enabled
    ElMessage.error(e?.message || '更新失败')
  } finally {
    savingEnabled.value = false
  }
}

// ---- host association ----
async function openHosts(row: DeploymentComponent) {
  hostsComponent.value = row.code
  try {
    const t = await getTargets(row.code)
    hostsSelected.value = t.host_ids || []
  } catch {
    hostsSelected.value = [...(row.hostIds || [])]
  }
  hostsVisible.value = true
}
async function saveHosts() {
  hostsSaving.value = true
  try {
    await putTargets(hostsComponent.value, hostsSelected.value)
    const row = components.value.find(c => c.code === hostsComponent.value)
    if (row) row.hostIds = [...hostsSelected.value]
    ElMessage.success('已保存目标主机')
    hostsVisible.value = false
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败（可能存在主机冲突）')
  } finally {
    hostsSaving.value = false
  }
}

// ---- param editor (key-value form) ----
async function openOverride(row: DeploymentComponent) {
  overrideComponent.value = row.code
  overrideError.value = ''
  let params: Record<string, any> = {}
  try {
    const o = await getOverride(row.code)
    params = o.params || {}
  } catch { /* empty */ }
  overrideRows.value = Object.keys(params).sort().map(k => ({
    key: k,
    value: typeof params[k] === 'object' ? JSON.stringify(params[k]) : String(params[k]),
  }))
  if (overrideRows.value.length === 0) overrideRows.value.push({ key: '', value: '' })
  overrideVisible.value = true
}
function addRow() { overrideRows.value.push({ key: '', value: '' }) }
function removeRow(i: number) { overrideRows.value.splice(i, 1) }

async function restoreDefaults() {
  try {
    if (!defaultsCache) defaultsCache = await getComponentDefaults()
    const d = defaultsCache[overrideComponent.value] || {}
    overrideRows.value = Object.keys(d).sort().map(k => ({
      key: k,
      value: typeof d[k] === 'object' ? JSON.stringify(d[k]) : String(d[k]),
    }))
    if (overrideRows.value.length === 0) {
      overrideRows.value = [{ key: '', value: '' }]
      ElMessage.info('该组件没有出厂默认参数')
    } else {
      ElMessage.success('已恢复出厂默认参数（未保存）')
    }
  } catch (e: any) {
    ElMessage.error(e?.message || '加载默认参数失败')
  }
}

async function saveOverride() {
  overrideError.value = ''
  const params: Record<string, any> = {}
  for (const r of overrideRows.value) {
    const k = r.key.trim()
    if (!k) continue
    if (k in params) { overrideError.value = `重复的参数键：${k}`; return }
    const v = r.value
    if ((v.startsWith('{') && v.endsWith('}')) || (v.startsWith('[') && v.endsWith(']'))) {
      try { params[k] = JSON.parse(v) } catch { params[k] = v }
    } else {
      params[k] = v
    }
  }
  overrideSaving.value = true
  try {
    await putOverride(overrideComponent.value, params)
    ElMessage.success('已保存组件参数')
    overrideVisible.value = false
  } catch (e: any) {
    overrideError.value = e?.message || '保存失败'
  } finally {
    overrideSaving.value = false
  }
}

// ---- tasks (plan with commands) ----
async function openTasks(row: DeploymentComponent) {
  tasksComponent.value = row.code
  tasksVisible.value = true
  tasksLoading.value = true
  try {
    const data = await getComponentTasks(row.code)
    tasksItems.value = data.items || []
  } catch (e: any) {
    tasksItems.value = []
    ElMessage.error(e?.message || '加载任务计划失败')
  } finally {
    tasksLoading.value = false
  }
}
function phaseTag(p: string) {
  if (p === 'deploy') return 'success'
  if (p === 'render') return 'warning'
  if (p === 'rollback' || p === 'residue') return 'danger'
  return 'info'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h4 class="page-title">组件</h4>
    <div class="content-card">
      <p class="hint">启用/禁用组件、关联目标主机、配置参数、查看 Pipeline 任务。部署操作请到「部署运行」页点击「新建部署」。</p>
      <el-table :data="components" v-loading="loading" size="small" border stripe>
        <el-table-column prop="code" label="组件" min-width="150" />
        <el-table-column label="分类" width="80">
          <template #default="{ row }">{{ categoryLabel(row.category) }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-switch v-model="row.enabled" :disabled="savingEnabled" @change="toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column label="目标主机" width="110">
          <template #default="{ row }">
            <span :class="{ 'host-warn': row.enabled && !row.hostIds.length }">
              {{ row.hostIds.length ? `${row.hostIds.length} 台` : '未关联' }}
            </span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.status)" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button size="small" link type="primary" @click="openHosts(row)">目标主机</el-button>
            <el-button size="small" link @click="openOverride(row)">参数</el-button>
            <el-button size="small" link @click="openTasks(row)">Pipeline 任务</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 目标主机 -->
    <el-dialog v-model="hostsVisible" :title="`目标主机 · ${hostsComponent}`" width="560px">
      <p class="dlg-hint">主机来源于「服务器管理」，部署目标仅引用服务器记录。</p>
      <el-select v-model="hostsSelected" multiple filterable placeholder="选择该组件要部署的主机" style="width: 100%;">
        <el-option v-for="s in servers" :key="s.id" :label="`${s.remark || s.host} (${s.host})`" :value="s.id" />
      </el-select>
      <el-empty v-if="!servers.length" :image-size="50" description="请先在「服务器管理」添加服务器" />
      <template #footer>
        <el-button @click="hostsVisible = false">取消</el-button>
        <el-button type="primary" :loading="hostsSaving" @click="saveHosts">保存</el-button>
      </template>
    </el-dialog>

    <!-- 参数 -->
    <el-dialog v-model="overrideVisible" :title="`组件参数 · ${overrideComponent}`" width="620px">
      <el-alert v-if="overrideError" type="error" :closable="false" :title="overrideError" style="margin-bottom: 10px;" />
      <p class="dlg-hint">键值形式配置；密码类字段不能包含 shell 元字符（单引号 双引号 反斜杠 $ 反引号 空格）。</p>
      <div class="kv-head"><span class="kv-k">参数键</span><span class="kv-v">参数值</span><span class="kv-op"></span></div>
      <div v-for="(r, i) in overrideRows" :key="i" class="kv-row">
        <el-input v-model="r.key" placeholder="如 password" class="kv-k" size="small" />
        <el-input v-model="r.value" placeholder="如 cloudhis@2123" class="kv-v" size="small" />
        <el-button class="kv-op" link type="danger" size="small" @click="removeRow(i)">删除</el-button>
      </div>
      <el-button size="small" @click="addRow" style="margin-top: 8px;">+ 添加参数</el-button>
      <template #footer>
        <el-button @click="restoreDefaults" style="float: left;">恢复默认</el-button>
        <el-button @click="overrideVisible = false">取消</el-button>
        <el-button type="primary" :loading="overrideSaving" @click="saveOverride">保存</el-button>
      </template>
    </el-dialog>

    <!-- Pipeline 任务（含命令） -->
    <el-dialog v-model="tasksVisible" :title="`Pipeline 任务 · ${tasksComponent}`" width="900px" top="6vh">
      <div v-loading="tasksLoading" class="tasks-box">
        <div v-for="(t, i) in tasksItems" :key="i" class="task-item">
          <div class="task-head">
            <el-tag size="small" :type="phaseTag(t.phase)">{{ t.phase }}</el-tag>
            <span class="task-name">{{ t.name }}</span>
            <span class="task-comp">{{ t.component }} · {{ t.actions.length }} 个动作</span>
          </div>
          <table class="action-table">
            <thead><tr><th style="width:40px">#</th><th style="width:150px">类型</th><th style="width:220px">名称</th><th>命令 / 参数</th></tr></thead>
            <tbody>
              <tr v-for="(a, j) in t.actions" :key="j">
                <td class="muted">{{ j + 1 }}</td>
                <td><code>{{ a.type }}</code></td>
                <td>{{ a.name }}</td>
                <td><code class="action-summary">{{ a.summary || '—' }}</code></td>
              </tr>
            </tbody>
          </table>
        </div>
        <el-empty v-if="!tasksLoading && !tasksItems.length" :image-size="50" description="该组件暂无 pipeline 任务" />
      </div>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
.hint { font-size: 12px; color: #909399; margin: 0 0 12px; }
.host-warn { color: #e6a23c; }
.dlg-hint { font-size: 12px; color: #909399; margin: 0 0 10px; }
.kv-head { display: flex; gap: 8px; font-size: 12px; color: #909399; margin-bottom: 6px; }
.kv-row { display: flex; gap: 8px; align-items: center; margin-bottom: 8px; }
.kv-k { width: 220px; flex-shrink: 0; }
.kv-v { flex: 1; }
.kv-op { width: 48px; flex-shrink: 0; text-align: center; }
.tasks-box { max-height: 70vh; overflow-y: auto; }
.task-item { border: 1px solid #ebeef5; border-radius: 6px; padding: 10px 12px; margin-bottom: 10px; }
.task-head { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
.task-name { font-size: 13px; font-weight: 500; color: #303133; }
.task-comp { font-size: 12px; color: #909399; margin-left: auto; }
.action-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.action-table th, .action-table td { text-align: left; padding: 4px 8px; border-bottom: 1px solid #ebeef5; vertical-align: top; }
.action-table th { color: #909399; font-weight: 500; background: #fafbfc; }
.action-summary { display: inline-block; word-break: break-all; white-space: pre-wrap; font-size: 12px; color: #303133; }
code { background: #f0f2f5; padding: 1px 4px; border-radius: 3px; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12px; }
.muted { color: #909399; }
</style>

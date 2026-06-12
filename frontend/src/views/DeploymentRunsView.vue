<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  listRuns, runningRuns, listComponents, getTargets, createRun, previewRun,
  type DeploymentRun, type DeploymentComponent,
} from '../api/deployment'
import { listManagedServers, type ManagedServer } from '../api/server-mgmt'
import { formatTime } from '../utils/time'

const router = useRouter()
const loading = ref(false)
const runs = ref<DeploymentRun[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const running = ref<any[]>([])

// dialog
const dialogVisible = ref(false)
const dialogLoading = ref(false)
const submitting = ref(false)
const previewing = ref(false)
const components = ref<DeploymentComponent[]>([])
const servers = ref<ManagedServer[]>([])
const previewResult = ref<{ task_count: number; action_count: number; plan: any[] } | null>(null)
const form = reactive({
  mode: 'deploy',      // deploy | rollback | phase | cleanup
  component: '',
  phase: 'deploy',
  hostIDs: [] as number[],
  dry_run: false,
})

const phaseOptions = ['preflight', 'render', 'deploy', 'test', 'rollback', 'residue']
const showComponent = computed(() => form.mode !== 'cleanup')
const showHostPicker = computed(() =>
  (form.mode === 'deploy' || form.mode === 'rollback' || form.mode === 'phase') &&
  !!selectedComponent.value?.requireHostSelection)
const selectedComponent = computed(() => components.value.find(c => c.name === form.component))
const blockedByState = computed(() =>
  form.mode === 'deploy' && !form.dry_run && selectedComponent.value &&
  selectedComponent.value.deployState !== 'not_deployed')

async function load() {
  loading.value = true
  try {
    const [data, run] = await Promise.all([listRuns(page.value, pageSize), runningRuns()])
    runs.value = data.list || []
    total.value = data.total || 0
    running.value = run || []
  } finally {
    loading.value = false
  }
}

function statusTag(s: string) {
  const v = (s || '').toLowerCase()
  if (v === 'success' || v === 'deployed') return 'success'
  if (v === 'failed') return 'danger'
  if (v === 'running' || v === 'pending') return 'warning'
  if (v === 'canceled') return 'info'
  return 'info'
}
function duration(ms: number) {
  if (!ms) return '-'
  const s = Math.round(ms / 1000)
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m${s % 60}s`
}
function stateLabel(s: string) {
  return s === 'deployed' ? '已部署' : s === 'failed' ? '部署失败' : '未部署'
}
function stateTag(s: string) {
  return s === 'deployed' ? 'success' : s === 'failed' ? 'danger' : 'info'
}

async function openDialog() {
  dialogVisible.value = true
  dialogLoading.value = true
  previewResult.value = null
  Object.assign(form, { mode: 'deploy', component: '', phase: 'deploy', hostIDs: [], dry_run: false })
  try {
    const [comps, srvs] = await Promise.all([listComponents(), listManagedServers()])
    components.value = comps
    servers.value = srvs
  } finally {
    dialogLoading.value = false
  }
}

// When switching component, pre-populate host selection from saved targets.
async function onComponentChange() {
  previewResult.value = null
  form.hostIDs = []
  if (!form.component || form.mode === 'cleanup') return
  try {
    const t = await getTargets(form.component)
    form.hostIDs = t.host_ids || []
  } catch { /* none */ }
}

watch(() => form.mode, () => {
  previewResult.value = null
  form.component = ''
  form.hostIDs = []
})

function buildBody() {
  return {
    mode: form.mode,
    component: form.mode === 'cleanup' ? undefined : form.component,
    phase: form.mode === 'phase' ? form.phase : undefined,
    host_ids: form.mode === 'cleanup' ? undefined : form.hostIDs,
    dry_run: form.dry_run,
  }
}

async function doPreview() {
  previewing.value = true
  previewResult.value = null
  try {
    previewResult.value = await previewRun(buildBody() as any)
  } catch (e: any) {
    ElMessage.error(e?.message || '预览失败')
  } finally {
    previewing.value = false
  }
}

async function doSubmit() {
  submitting.value = true
  try {
    const res = await createRun(buildBody() as any)
    ElMessage.success(`已创建部署任务 #${res.id}`)
    dialogVisible.value = false
    router.push(`/deployment/runs/${res.id}`)
  } catch (e: any) {
    ElMessage.error(e?.message || '提交失败')
  } finally {
    submitting.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <h4 class="page-title">部署运行</h4>
      <div>
        <el-button type="primary" @click="openDialog">新建部署</el-button>
        <el-button @click="load">刷新</el-button>
      </div>
    </div>

    <el-alert v-if="running.length" type="info" :closable="false" :title="`当前有 ${running.length} 个任务正在执行`" style="margin-bottom: 12px;" />

    <div class="content-card">
      <el-table :data="runs" v-loading="loading" size="small" border stripe
        @row-click="(row: DeploymentRun) => router.push(`/deployment/runs/${row.id}`)" style="cursor: pointer;">
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column prop="task_type" label="操作" width="100" />
        <el-table-column prop="target_component" label="组件/范围" min-width="150" />
        <el-table-column prop="phase" label="阶段" width="100" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.status)" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="耗时" width="90">
          <template #default="{ row }">{{ duration(row.duration_ms) }}</template>
        </el-table-column>
        <el-table-column prop="error_summary" label="错误" min-width="180" show-overflow-tooltip />
      </el-table>
      <el-pagination v-if="total > pageSize" v-model:current-page="page" small
        layout="prev, pager, next" :page-size="pageSize" :total="total" @current-change="load"
        style="margin-top: 12px;" />
    </div>

    <el-dialog v-model="dialogVisible" title="新建部署" width="640px">
      <el-form v-loading="dialogLoading" :model="form" label-width="90px">
        <el-form-item label="操作">
          <el-radio-group v-model="form.mode">
            <el-radio value="deploy">部署</el-radio>
            <el-radio value="rollback">回滚</el-radio>
            <el-radio value="phase">单阶段</el-radio>
            <el-radio value="cleanup">清理整个项目</el-radio>
          </el-radio-group>
        </el-form-item>

        <el-alert v-if="form.mode === 'cleanup'" type="warning" :closable="false"
          title="将按反序对所有已启用组件依次执行 rollback + residue，清理项目所有部署产物。该操作不可恢复，请确认。"
          style="margin-bottom: 12px;" />

        <el-form-item v-if="showComponent" label="组件">
          <el-select v-model="form.component" placeholder="选择组件" filterable style="width: 100%;" @change="onComponentChange">
            <el-option v-for="c in components" :key="c.name" :label="c.displayName" :value="c.name">
              <span style="display:inline-flex;align-items:center;gap:8px;width:100%;justify-content:space-between;">
                <span>{{ c.displayName }}</span>
                <el-tag size="small" :type="stateTag(c.deployState)">{{ stateLabel(c.deployState) }}</el-tag>
              </span>
            </el-option>
          </el-select>
          <el-alert v-if="blockedByState && selectedComponent" type="error" :closable="false" style="margin-top: 8px;"
            :title="`组件「${selectedComponent.displayName}」当前为 ${stateLabel(selectedComponent.deployState)}，不能直接重新部署，请先用「回滚」清理后再部署。`" />
        </el-form-item>

        <el-form-item v-if="form.mode === 'phase'" label="阶段">
          <el-select v-model="form.phase" style="width: 100%;">
            <el-option v-for="p in phaseOptions" :key="p" :label="p" :value="p" />
          </el-select>
        </el-form-item>

        <el-form-item v-if="showHostPicker" label="目标主机">
          <el-select v-model="form.hostIDs" multiple filterable placeholder="选择目标主机（来自服务器管理）"
            style="width: 100%;" :disabled="!form.component || !servers.length">
            <el-option v-for="s in servers" :key="s.id" :label="`${s.remark || s.host} (${s.host})`" :value="s.id" />
          </el-select>
          <div v-if="!servers.length" class="hint-warn">还没有服务器，请先到「服务器管理」添加。</div>
          <div v-else-if="form.component && form.hostIDs.length === 0" class="hint-warn">未选择目标主机，本次不会有可执行目标。</div>
        </el-form-item>

        <el-form-item label="模拟运行">
          <el-switch v-model="form.dry_run" active-text="只生成计划，不实际执行" />
        </el-form-item>
      </el-form>

      <template v-if="previewResult">
        <el-divider />
        <div class="preview-summary">
          预览：<strong>{{ previewResult.task_count }}</strong> 个任务 ·
          <strong>{{ previewResult.action_count }}</strong> 个动作
        </div>
        <el-table :data="previewResult.plan" size="small" stripe max-height="240">
          <el-table-column prop="component" label="组件" width="130" />
          <el-table-column prop="phase" label="阶段" width="100" />
          <el-table-column prop="task_name" label="任务" min-width="160" />
          <el-table-column prop="host" label="主机" width="130" />
          <el-table-column prop="actions" label="动作数" width="70" />
        </el-table>
      </template>

      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button :loading="previewing" @click="doPreview">预览</el-button>
        <el-button type="primary" :loading="submitting" :disabled="blockedByState" @click="doSubmit">提交</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
.hint-warn { color: #e6a23c; font-size: 12px; margin-top: 4px; }
.preview-summary { margin-bottom: 8px; color: #606266; font-size: 13px; }
</style>

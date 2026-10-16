<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { getDeploymentConfig, previewDeploymentConfig, saveDeploymentConfig, type ContainerConfigEdit, type DeploymentConfigEdit, type DeploymentConfigResult } from '../api/kubernetes'

const props = defineProps<{ modelValue: boolean; name: string; namespace: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean]; updated: [] }>()
const visible = computed({ get: () => props.modelValue, set: value => emit('update:modelValue', value) })
const loading = ref(false)
const saving = ref(false)
const monitoring = ref(false)
const current = ref<any>(null)
const expectedStrategy = ref('')
const replicas = ref(1)
const viewMode = ref<'editable' | 'all'>('editable')
const containers = computed<any[]>(() => current.value?.spec?.template?.spec?.containers || [])
const resourceRows = [
  { label: 'CPU', request: 'cpuRequest', limit: 'cpuLimit', placeholder: '如 100m 或 1' },
  { label: '内存', request: 'memoryRequest', limit: 'memoryLimit', placeholder: '如 512Mi 或 2G' },
] as const
const probes = [
  { name: '启动探针', key: 'startupProbe', field: 'startupInitialDelay' },
  { name: '就绪探针', key: 'readinessProbe', field: 'readinessInitialDelay' },
  { name: '存活探针', key: 'livenessProbe', field: 'livenessInitialDelay' },
] as const
const expansion = ref<'default' | 'all' | 'none'>('default')
const forms = ref<ContainerConfigEdit[]>([])
const preview = ref<DeploymentConfigResult | null>(null)
const reviewed = ref<DeploymentConfigEdit | null>(null)
const result = ref('')
const recordWarning = ref('')
const resultType = ref<'success' | 'warning' | 'error' | 'info'>('info')
let epoch = 0
let timer: ReturnType<typeof setTimeout> | undefined
const busy = computed(() => loading.value || saving.value || monitoring.value)
type Editor = { index?: number; field: keyof ContainerConfigEdit | 'replicas'; kind: 'number' | 'resource'; label: string; min?: number; max?: number }
type FieldRow = { id: string; key: string; value: string; children?: FieldRow[]; editor?: Editor }
const fieldRows = computed<FieldRow[]>(() => {
  if (!current.value) return []
  const doc = JSON.parse(JSON.stringify(current.value))
  doc.apiVersion ||= 'apps/v1'; doc.kind ||= 'Deployment'; doc.spec.replicas ??= 1
  const editors = new Map<string, Editor>()
  const bind = (path: (string | number)[], editor: Editor) => editors.set(JSON.stringify(path.map(String)), editor)
  bind(['spec', 'replicas'], { field: 'replicas', kind: 'number', label: '副本数', min: 0, max: 2147483647 })
  for (const [index, c] of doc.spec.template.spec.containers.entries()) {
    const base = ['spec', 'template', 'spec', 'containers', index]
    c.resources ||= {}; c.resources.requests ||= {}; c.resources.limits ||= {}
    for (const [group, key, field, label] of [
      ['requests', 'cpu', 'cpuRequest', 'CPU 请求'], ['limits', 'cpu', 'cpuLimit', 'CPU 限制'],
      ['requests', 'memory', 'memoryRequest', '内存请求'], ['limits', 'memory', 'memoryLimit', '内存限制'],
    ] as const) {
      c.resources[group][key] ??= ''
      bind([...base, 'resources', group, key], { index, field, kind: 'resource', label: `${c.name} / ${label}` })
    }
    for (const [key, field, label] of [
      ['startupProbe', 'startupInitialDelay', '启动探针初始延迟'], ['readinessProbe', 'readinessInitialDelay', '就绪探针初始延迟'], ['livenessProbe', 'livenessInitialDelay', '存活探针初始延迟'],
    ] as const) {
      if (!c[key]) continue
      c[key].initialDelaySeconds ??= 0
      bind([...base, key, 'initialDelaySeconds'], { index, field, kind: 'number', label: `${c.name} / ${label}（秒）`, min: 0, max: 86400 })
    }
    if (c.startupProbe) {
      c.startupProbe.failureThreshold ||= 3
      bind([...base, 'startupProbe', 'failureThreshold'], { index, field: 'startupFailureThreshold', kind: 'number', label: `${c.name} / 启动探针连续失败阈值`, min: 1, max: 86400 })
    }
  }
  function rows(value: any, path: string[] = []): FieldRow[] {
    return Object.entries(value).map(([key, child]) => {
      const next = [...path, key], id = JSON.stringify(next), editor = editors.get(id)
      const nested = child !== null && typeof child === 'object'
      let display = nested ? Array.isArray(child) ? `[${child.length}]` : '{}' : child === null ? 'null' : String(child)
      // Keep environment credentials masked, including last-applied annotations.
      if (key === 'value' && /pass|password|secret|token/i.test(value.name || '') || key === 'kubectl.kubernetes.io/last-applied-configuration') display = '••••••••'
      return { id, key: Array.isArray(value) ? `[${key}]${(child as any)?.name ? ' · ' + (child as any).name : ''}` : key, value: display, editor, ...(nested && Object.keys(child).length ? { children: rows(child, next) } : {}) }
    })
  }
  // Stable Kubernetes document order, retaining every returned field.
  return rows({ apiVersion: doc.apiVersion, kind: doc.kind, metadata: doc.metadata, spec: doc.spec, ...Object.fromEntries(Object.entries(doc).filter(([key]) => !['apiVersion', 'kind', 'metadata', 'spec'].includes(key))) })
})
function editValue(editor: Editor): string | number {
  if (editor.field === 'replicas') return replicas.value
  return (forms.value[editor.index!]?.[editor.field] ?? '') as string | number
}
function setEditValue(editor: Editor, value: string | number | undefined) {
  if (editor.field === 'replicas') { replicas.value = Number(value ?? 0); return }
  const form = forms.value[editor.index!] as unknown as Record<string, string | number>
  form[editor.field] = editor.kind === 'number' ? Number(value ?? editor.min ?? 0) : String(value ?? '')
}
function fieldRowClassName({ row }: { row: FieldRow }) { return row.editor ? 'editable-field' : 'readonly-field' }
const expandedFields = computed(() => {
  const ids: string[] = []
  function walk(rows: FieldRow[]) {
    for (const row of rows) {
      if (!row.children || expansion.value === 'none') continue
      if (expansion.value === 'default' && row.id === JSON.stringify(['metadata', 'managedFields'])) continue
      ids.push(row.id); walk(row.children)
    }
  }
  walk(fieldRows.value); return ids
})
function expandFields(expanded: boolean) { expansion.value = expanded ? 'all' : 'none' }

function stop() { epoch++; if (timer) clearTimeout(timer); timer = undefined; monitoring.value = false }
function fill(dep: any) {
  current.value = dep
  replicas.value = dep.spec?.replicas ?? 1
  forms.value = (dep.spec?.template?.spec?.containers || []).map((c: any) => ({
    name: c.name, cpuRequest: c.resources?.requests?.cpu || '', cpuLimit: c.resources?.limits?.cpu || '',
    memoryRequest: c.resources?.requests?.memory || '', memoryLimit: c.resources?.limits?.memory || '',
    ...(c.startupProbe ? { startupInitialDelay: c.startupProbe.initialDelaySeconds || 0, startupFailureThreshold: c.startupProbe.failureThreshold || 3 } : {}),
    ...(c.readinessProbe ? { readinessInitialDelay: c.readinessProbe.initialDelaySeconds || 0 } : {}),
    ...(c.livenessProbe ? { livenessInitialDelay: c.livenessProbe.initialDelaySeconds || 0 } : {}),
  }))
}
async function load() {
  stop(); const ticket = epoch
  expansion.value = 'default'; viewMode.value = 'editable'
  loading.value = true; preview.value = null; reviewed.value = null; result.value = ''; recordWarning.value = ''; current.value = null
  try {
    const data = await getDeploymentConfig(props.name, props.namespace)
    if (ticket !== epoch) return
    fill(data.deployment); expectedStrategy.value = data.expectedStrategy || ''
  } finally { if (ticket === epoch) loading.value = false }
}
watch(() => [props.modelValue, props.name], () => {
  if (props.modelValue && props.name) void load().catch(() => {})
  else { stop(); loading.value = false; saving.value = false }
})
watch(() => props.namespace, () => { visible.value = false; stop() })
onBeforeUnmount(stop)

async function review() {
  const payload: DeploymentConfigEdit = { uid: current.value.metadata.uid, resourceVersion: current.value.metadata.resourceVersion, replicas: replicas.value, containers: structuredClone(forms.value.map(c => ({ ...c }))) }
  const ticket = epoch; saving.value = true
  try {
    const data = await previewDeploymentConfig(props.name, props.namespace, payload)
    if (ticket !== epoch) return
    if (!data.changes?.length) { ElMessage.info('配置没有变化'); return }
    reviewed.value = payload; preview.value = data
  } finally { if (ticket === epoch) saving.value = false }
}
async function apply() {
  if (!reviewed.value) return
  const ticket = epoch; saving.value = true
  try {
    const data = await saveDeploymentConfig(props.name, props.namespace, reviewed.value)
    if (ticket !== epoch) return
    preview.value = null; reviewed.value = null
    recordWarning.value = data.recordWarning || ''
    fill(data.deployment); emit('updated')
    const identity = { uid: data.deployment.metadata.uid, generation: data.deployment.metadata.generation }
    result.value = data.rolloutRequired ? '配置已提交，等待 Pod 更新' : data.replicasChanged ? '配置已提交，等待扩缩容完成' : '配置已提交，等待控制器确认'
    resultType.value = 'info'; monitoring.value = true
    const deadline = Date.now() + data.waitSeconds * 1000
    const poll = async () => {
      if (ticket !== epoch) return
      try {
        const latest = await getDeploymentConfig(props.name, props.namespace)
        if (ticket !== epoch) return
        const dep = latest.deployment, status = dep.status || {}, desired = dep.spec?.replicas ?? 1
        if (dep.metadata.uid !== identity.uid || dep.metadata.generation !== identity.generation) {
          result.value = 'Deployment 已被后续操作修改，请重新加载'; resultType.value = 'warning'; monitoring.value = false; emit('updated'); return
        }
        if ((status.observedGeneration || 0) >= identity.generation) {
          const failed = desired > 0 && (data.rolloutRequired || data.replicasChanged) && (status.conditions || []).find((c: any) => c.type === 'Progressing' && c.status === 'False')
          if (failed) { result.value = `配置已提交，更新异常：${failed.message || failed.reason}`; resultType.value = 'error'; monitoring.value = false; emit('updated'); return }
          const ready = status.replicas === desired && status.updatedReplicas === desired && status.readyReplicas === desired && status.availableReplicas === desired && !(status.unavailableReplicas || 0)
          if (!(data.rolloutRequired || data.replicasChanged) || (desired === 0 && !(status.replicas || 0)) || (desired > 0 && ready)) {
            fill(dep); result.value = desired === 0 ? '配置已保存，当前副本数为 0' : data.rolloutRequired || data.replicasChanged ? '配置已生效，全部副本已就绪' : '配置已生效'
            resultType.value = desired === 0 ? 'warning' : 'success'; monitoring.value = false; emit('updated'); return
          }
          result.value = `配置已提交，等待就绪 ${status.readyReplicas || 0}/${desired}`
        }
      } catch { /* Transient read errors do not undo an accepted configuration. */ }
      if (ticket !== epoch) return
      if (Date.now() >= deadline) { result.value = '等待超时，配置已提交，请查看 Pod 状态'; resultType.value = 'warning'; monitoring.value = false; emit('updated'); return }
      timer = setTimeout(poll, 3000)
    }
    void poll()
  } finally { if (ticket === epoch) saving.value = false }
}
</script>

<template>
  <el-dialog v-model="visible" :title="`配置 · ${name}`" width="min(1100px, 96vw)" top="5vh" :close-on-click-modal="false" destroy-on-close class="deployment-config-dialog">
    <div v-loading="loading" class="deployment-config">
      <el-alert v-if="result" :title="result" :type="resultType" :closable="false" />
      <el-alert v-if="recordWarning" :title="recordWarning" type="warning" :closable="false" />
      <template v-if="current">
        <div class="config-context">
          <el-tag effect="plain">{{ namespace }}</el-tag>
          <span>当前策略：{{ current.spec?.strategy?.type || 'RollingUpdate' }}</span>
          <el-tag v-if="expectedStrategy && expectedStrategy !== current.spec?.strategy?.type" type="warning" effect="plain">规则要求 {{ expectedStrategy }}</el-tag>
          <el-button v-if="expectedStrategy && expectedStrategy !== current.spec?.strategy?.type && !preview" :disabled="busy" type="primary" link @click="review().catch(() => {})">预览策略修正</el-button>
        </div>
        <template v-if="preview">
          <el-table :data="preview.changes" border max-height="60vh">
            <el-table-column prop="field" label="配置项" min-width="200" />
            <el-table-column prop="before" label="修改前" min-width="150" />
            <el-table-column prop="after" label="修改后" min-width="150" />
          </el-table>
          <el-alert class="preview-effect" :title="preview.rolloutRequired ? '应用后将替换 Pod' : preview.replicasChanged ? '应用后按目标副本数扩缩容' : '仅更新 Deployment 配置，不额外重启 Pod'" type="info" :closable="false" />
          <el-alert v-if="reviewed?.replicas === 0" class="preview-effect" title="目标副本数为 0，将停止该服务全部 Pod" type="warning" :closable="false" />
        </template>
        <template v-else>
          <div class="view-toolbar">
            <el-radio-group v-model="viewMode" aria-label="配置视图">
              <el-radio-button value="editable">可修改配置</el-radio-button>
              <el-radio-button value="all">完整 Deployment</el-radio-button>
            </el-radio-group>
            <div v-if="viewMode === 'all'" class="field-actions"><el-button text size="small" @click="expandFields(true)">展开全部</el-button><el-button text size="small" @click="expandFields(false)">收起全部</el-button></div>
          </div>
          <div v-if="viewMode === 'editable'" class="editable-sections">
            <section class="config-section">
              <h3>基本配置</h3>
              <el-form label-width="100px" :disabled="busy" class="basic-form">
                <el-form-item label="副本数"><el-input-number v-model="replicas" aria-label="副本数" :min="0" :max="2147483647" :precision="0" :value-on-clear="0" /></el-form-item>
              </el-form>
              <h4>更新策略</h4>
              <el-descriptions :column="2" border class="strategy-overview">
                <el-descriptions-item label="当前策略">{{ current.spec?.strategy?.type || 'RollingUpdate' }}</el-descriptions-item>
                <el-descriptions-item label="进度期限">{{ current.spec?.progressDeadlineSeconds ?? 600 }} 秒</el-descriptions-item>
                <template v-if="(current.spec?.strategy?.type || 'RollingUpdate') === 'RollingUpdate'">
                  <el-descriptions-item label="maxSurge">{{ current.spec?.strategy?.rollingUpdate?.maxSurge ?? '—' }}</el-descriptions-item>
                  <el-descriptions-item label="maxUnavailable">{{ current.spec?.strategy?.rollingUpdate?.maxUnavailable ?? '—' }}</el-descriptions-item>
                </template>
              </el-descriptions>
            </section>
            <section v-for="(form, index) in forms" :key="form.name" class="config-section">
              <h3>容器 · {{ form.name }}</h3>
              <h4>资源配置</h4>
              <div class="form-grid resource-form">
                <span></span><span class="column-heading">请求（requests）</span><span class="column-heading">限制（limits）</span>
                <template v-for="resource in resourceRows" :key="resource.label">
                  <label>{{ resource.label }}</label>
                  <el-input v-model="form[resource.request]" :aria-label="`${form.name} / ${resource.label}请求`" :disabled="busy" clearable :placeholder="resource.placeholder" />
                  <el-input v-model="form[resource.limit]" :aria-label="`${form.name} / ${resource.label}限制`" :disabled="busy" clearable :placeholder="resource.placeholder" />
                </template>
              </div>
              <h4>健康检查</h4>
              <div class="form-grid probe-form">
                <span></span><span class="column-heading">初始延迟（秒）</span><span class="column-heading">连续失败阈值</span>
                <template v-for="probe in probes" :key="probe.key">
                  <label>{{ probe.name }}</label>
                  <template v-if="containers[index]?.[probe.key]">
                    <el-input-number v-model="form[probe.field]" :aria-label="`${form.name} / ${probe.name}初始延迟（秒）`" :disabled="busy" :min="0" :max="86400" :precision="0" :value-on-clear="0" />
                    <el-input-number v-if="probe.key === 'startupProbe'" v-model="form.startupFailureThreshold" :aria-label="`${form.name} / 启动探针连续失败阈值`" :disabled="busy" :min="1" :max="86400" :precision="0" :value-on-clear="1" />
                    <span v-else class="readonly-value">{{ containers[index][probe.key].failureThreshold || 3 }}</span>
                  </template>
                  <template v-else><span class="readonly-value">未配置</span><span class="readonly-value">—</span></template>
                </template>
              </div>
            </section>
          </div>
        <el-table v-else :data="fieldRows" row-key="id" :expand-row-keys="expandedFields" border height="62vh" :indent="18" :row-class-name="fieldRowClassName">
          <el-table-column label="Deployment 字段" min-width="360">
            <template #default="{ row }"><span class="field-key">{{ row.key }}</span></template>
          </el-table-column>
          <el-table-column label="配置值" min-width="400">
            <template #default="{ row }">
              <div v-if="row.editor" class="editable-value">
                <el-input-number v-if="row.editor.kind === 'number'" :model-value="Number(editValue(row.editor))" :aria-label="row.editor.label" :min="row.editor.min" :max="row.editor.max" :precision="0" :value-on-clear="row.editor.min" :disabled="busy" @update:model-value="setEditValue(row.editor, $event)" />
                <el-input v-else :model-value="String(editValue(row.editor))" :aria-label="row.editor.label" :disabled="busy" clearable placeholder="留空移除" @update:model-value="setEditValue(row.editor, $event)" />
                <span class="editable-label">{{ row.editor.label }}</span>
              </div>
              <span v-else class="readonly-value">{{ row.value }}</span>
            </template>
          </el-table-column>
        </el-table>
        </template>
      </template>
    </div>
    <template #footer>
      <el-button :disabled="saving" @click="visible = false">关闭</el-button>
      <el-button :disabled="busy" @click="load().catch(() => {})">重新加载</el-button>
      <template v-if="preview">
        <el-button :disabled="busy" @click="preview = null; reviewed = null">返回修改</el-button>
        <el-button type="primary" :loading="saving" :disabled="monitoring" @click="apply().catch(() => {})">应用变更</el-button>
      </template>
      <el-button v-else type="primary" :loading="saving" :disabled="busy || !current" @click="review().catch(() => {})">预览变更</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.deployment-config { min-height: 240px; }
.config-context { display: flex; align-items: center; flex-wrap: wrap; gap: 16px; margin: 0 0 16px; }
:deep(.el-table td.el-table__cell) { padding-block: 6px; }
.field-actions { margin-left: auto; display: flex; gap: 4px; }
.view-toolbar { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; margin-bottom: 16px; }
.editable-sections { max-height: 62vh; overflow-y: auto; padding-right: 8px; }
.config-section { border: 1px solid var(--el-border-color-lighter); border-radius: 6px; padding: 20px 24px; margin-bottom: 16px; }
.config-section h3 { margin: 0 0 20px; font-size: 15px; font-weight: 600; }
.config-section h4 { margin: 20px 0 12px; font-size: 14px; font-weight: 500; }
.strategy-overview :deep(.el-descriptions__content) { color: var(--el-text-color-placeholder); }
.basic-form :deep(.el-form-item) { margin-bottom: 0; }
.form-grid { display: grid; grid-template-columns: 100px minmax(180px, 1fr) minmax(180px, 1fr); align-items: center; gap: 12px 24px; max-width: 800px; }
.column-heading { color: var(--el-text-color-secondary); font-size: 13px; }
.probe-form .el-input-number { width: 180px; max-width: 100%; }
@media (max-width: 650px) { .config-section { padding: 16px; } .form-grid { grid-template-columns: 72px minmax(100px, 1fr) minmax(100px, 1fr); gap: 10px; } .probe-form .el-input-number { width: 100%; } }
.field-key, .readonly-value { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; overflow-wrap: anywhere; white-space: pre-wrap; }
:deep(.readonly-field) .field-key, .readonly-value { color: var(--el-text-color-placeholder); }
:deep(.editable-field) { --el-table-tr-bg-color: var(--el-color-primary-light-9); }
.editable-value { display: flex; align-items: center; flex-wrap: wrap; gap: 8px 16px; padding: 4px 0; }
.editable-value .el-input { max-width: 220px; }
.editable-label { color: var(--el-text-color-secondary); font-size: 12px; }
.preview-effect { margin-top: 16px; }
</style>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createRun, listComponents, previewRun, rollbackRun,
  getOverride, putOverride,
  type DeploymentComponent,
} from '../api/deployment'
import { useRouter } from 'vue-router'

const router = useRouter()
const loading = ref(false)
const components = ref<DeploymentComponent[]>([])

// Override editor state
const overrideVisible = ref(false)
const overrideComponent = ref('')
const overrideText = ref('')
const overrideSaving = ref(false)
const overrideError = ref('')

async function load() {
  loading.value = true
  try { components.value = await listComponents() }
  finally { loading.value = false }
}

function statusTag(s: string) {
  if (s === 'deployed') return 'success'
  if (s === 'failed') return 'danger'
  return 'info'
}
function categoryLabel(c: string) {
  return c === 'k8s' ? 'K8s' : c === 'business' ? '业务' : '其他'
}

async function deploy(row: DeploymentComponent) {
  await ElMessageBox.confirm(`确定部署组件 "${row.code}" 吗？`, '确认', { type: 'warning' })
  const res = await createRun({ component: row.code })
  ElMessage.success(`已发起部署，运行 #${res.runId}`)
  router.push(`/deployment/runs/${res.runId}`)
}
async function rollback(row: DeploymentComponent) {
  await ElMessageBox.confirm(`确定回滚组件 "${row.code}" 吗？`, '确认', { type: 'warning' })
  const res = await rollbackRun({ component: row.code })
  ElMessage.success(`已发起回滚，运行 #${res.runId}`)
  router.push(`/deployment/runs/${res.runId}`)
}
async function preview(row: DeploymentComponent) {
  const res = await previewRun({ component: row.code })
  ElMessage.success(`已发起预览（dry-run），运行 #${res.runId}`)
  router.push(`/deployment/runs/${res.runId}`)
}

async function openOverride(row: DeploymentComponent) {
  overrideComponent.value = row.code
  overrideError.value = ''
  try {
    const o = await getOverride(row.code)
    overrideText.value = JSON.stringify(o.params || {}, null, 2)
  } catch {
    overrideText.value = '{}'
  }
  overrideVisible.value = true
}

async function saveOverride() {
  overrideError.value = ''
  let params: Record<string, any>
  try {
    params = JSON.parse(overrideText.value || '{}')
  } catch (e: any) {
    overrideError.value = 'JSON 格式错误：' + (e?.message || '')
    return
  }
  overrideSaving.value = true
  try {
    await putOverride(overrideComponent.value, params)
    ElMessage.success('已保存组件参数覆盖')
    overrideVisible.value = false
  } catch (e: any) {
    // backend rejects shell-metachar passwords with a 400; surface inline.
    overrideError.value = e?.message || '保存失败'
  } finally {
    overrideSaving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h4 class="page-title">组件</h4>
    <div class="content-card">
      <el-table :data="components" v-loading="loading" size="small" border stripe>
        <el-table-column prop="code" label="组件" min-width="160" />
        <el-table-column label="分类" width="100">
          <template #default="{ row }">{{ categoryLabel(row.category) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.status)" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="280" fixed="right">
          <template #default="{ row }">
            <el-button size="small" link type="primary" @click="deploy(row)">部署</el-button>
            <el-button size="small" link @click="preview(row)">预览</el-button>
            <el-button size="small" link type="warning" @click="rollback(row)">回滚</el-button>
            <el-button size="small" link @click="openOverride(row)">参数</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="overrideVisible" :title="`组件参数覆盖 · ${overrideComponent}`" width="560px">
      <el-alert v-if="overrideError" type="error" :closable="false" :title="overrideError" style="margin-bottom: 10px;" />
      <p class="dlg-hint">JSON 格式；密码类字段不能包含 shell 元字符（单引号 双引号 反斜杠 $ 反引号 空格）。</p>
      <el-input v-model="overrideText" type="textarea" :rows="14" class="json-area" />
      <template #footer>
        <el-button @click="overrideVisible = false">取消</el-button>
        <el-button type="primary" :loading="overrideSaving" @click="saveOverride">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
.dlg-hint { font-size: 12px; color: #909399; margin: 0 0 10px; }
.json-area :deep(textarea) { font-family: 'JetBrains Mono', Consolas, monospace; font-size: 13px; }
</style>

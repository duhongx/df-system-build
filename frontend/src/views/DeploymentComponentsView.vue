<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createRun, listComponents, previewRun, rollbackRun,
  type DeploymentComponent,
} from '../api/deployment'
import { useRouter } from 'vue-router'

const router = useRouter()
const loading = ref(false)
const components = ref<DeploymentComponent[]>([])

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
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button size="small" link type="primary" @click="deploy(row)">部署</el-button>
            <el-button size="small" link @click="preview(row)">预览</el-button>
            <el-button size="small" link type="warning" @click="rollback(row)">回滚</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
</style>

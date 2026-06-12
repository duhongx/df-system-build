<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { listRuns, type DeploymentRun } from '../api/deployment'
import { formatTime } from '../utils/time'

const router = useRouter()
const loading = ref(false)
const runs = ref<DeploymentRun[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20

async function load() {
  loading.value = true
  try {
    const data = await listRuns(page.value, pageSize)
    runs.value = data.list || []
    total.value = data.total || 0
  } finally {
    loading.value = false
  }
}

function statusTag(s: string) {
  const v = (s || '').toLowerCase()
  if (v === 'success' || v === 'deployed') return 'success'
  if (v === 'failed') return 'danger'
  if (v === 'running') return 'primary'
  if (v === 'canceled') return 'warning'
  return 'info'
}

function duration(ms: number) {
  if (!ms) return '-'
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  return `${Math.floor(s / 60)}m${s % 60}s`
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h4 class="page-title">部署运行</h4>
    <div class="content-card">
      <el-table :data="runs" v-loading="loading" size="small" border stripe
        @row-click="(row: DeploymentRun) => router.push(`/deployment/runs/${row.id}`)" style="cursor: pointer;">
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column prop="target_component" label="组件/范围" min-width="160" />
        <el-table-column prop="task_type" label="类型" width="100" />
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag :type="statusTag(row.status)" size="small">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="用时" width="90">
          <template #default="{ row }">{{ duration(row.duration_ms) }}</template>
        </el-table-column>
        <el-table-column label="开始时间" width="170">
          <template #default="{ row }">{{ formatTime(row.started_at) }}</template>
        </el-table-column>
        <el-table-column prop="error_summary" label="错误" min-width="200" show-overflow-tooltip />
      </el-table>
      <el-pagination v-if="total > pageSize" v-model:current-page="page" small
        layout="prev, pager, next" :page-size="pageSize" :total="total" @current-change="load"
        style="margin-top: 12px;" />
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
</style>

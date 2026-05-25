<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listPipelines, cancelPipeline, deployPipeline, type Pipeline } from '../api/pipeline'
import { ElMessage, ElMessageBox } from 'element-plus'

const route = useRoute()
const router = useRouter()
const pipelines = ref<Pipeline[]>([])
const loading = ref(true)
const filterStatus = ref('')
const filterType = ref('')
const searchText = ref('')
const filterApp = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

onMounted(async () => {
  if (route.query.app) {
    filterApp.value = route.query.app as string
  }
  await load()
})

async function load() {
  loading.value = true
  try {
    const result = await listPipelines({
      page: page.value,
      pageSize: pageSize.value,
      app: filterApp.value || undefined,
      status: filterStatus.value || undefined,
    })
    let list = result.list || []
    if (searchText.value) {
      const s = searchText.value.toLowerCase()
      list = list.filter(p =>
        p.pipelineNo.toLowerCase().includes(s) || p.appName.toLowerCase().includes(s)
      )
    }
    if (filterType.value) {
      list = list.filter(p => p.appType === filterType.value)
    }
    pipelines.value = list
    total.value = result.total || 0
  } catch (e) {
    pipelines.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function statusType(status: string) {
  const map: Record<string, string> = {
    SUCCESS: 'success', FAILED: 'danger', RUNNING: 'primary',
    PENDING: 'info', CANCELED: 'warning', IMAGE_READY: 'warning',
  }
  return map[status] || 'info'
}

function statusLabel(status: string) {
  const map: Record<string, string> = {
    SUCCESS: '成功', FAILED: '失败', RUNNING: '运行中',
    PENDING: '排队中', CANCELED: '已取消', IMAGE_READY: '等待部署',
  }
  return map[status] || status
}

function goToDetail(p: Pipeline) {
  router.push(`/release/${p.id}`)
}

function clearAppFilter() {
  filterApp.value = ''
  searchText.value = ''
  page.value = 1
  load()
}

function formatTime(time: string | null | undefined) {
  if (!time) return '-'
  // Convert ISO format to readable format
  try {
    const d = new Date(time)
    const y = d.getFullYear()
    const m = String(d.getMonth() + 1).padStart(2, '0')
    const day = String(d.getDate()).padStart(2, '0')
    const h = String(d.getHours()).padStart(2, '0')
    const min = String(d.getMinutes()).padStart(2, '0')
    const s = String(d.getSeconds()).padStart(2, '0')
    return `${y}-${m}-${day} ${h}:${min}:${s}`
  } catch (e) {
    return time
  }
}

async function handleDelete(p: Pipeline) {
  await ElMessageBox.confirm(`确定删除构建记录 "${p.pipelineNo}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await cancelPipeline(p.id) // reuse cancel API to delete/mark
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    // handled
  }
}

async function handleDeploy(p: Pipeline) {
  await ElMessageBox.confirm(`确定开始部署 "${p.pipelineNo}" 吗？`, '确认部署', { type: 'info' })
  try {
    await deployPipeline(p.id)
    ElMessage.success('部署已触发')
    await load()
  } catch (e) {
    // handled
  }
}

function handlePageChange(newPage: number) {
  page.value = newPage
  load()
}

function handleSizeChange(newSize: number) {
  pageSize.value = newSize
  page.value = 1
  load()
}
</script>

<template>
  <div class="page">
    <div class="toolbar-row">
      <div class="toolbar-left">
        <el-tag v-if="filterApp" type="primary" closable @close="clearAppFilter" style="margin-right: 8px;">
          应用: {{ filterApp }}
        </el-tag>
        <el-input
          v-model="searchText"
          placeholder="搜索任务编号 / 应用名"
          clearable
          style="width: 220px;"
          @keyup.enter="load"
          @clear="load"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-select v-model="filterStatus" placeholder="状态" clearable style="width: 110px;" @change="load">
          <el-option label="运行中" value="RUNNING" />
          <el-option label="成功" value="SUCCESS" />
          <el-option label="失败" value="FAILED" />
          <el-option label="排队中" value="PENDING" />
          <el-option label="已取消" value="CANCELED" />
          <el-option label="等待部署" value="IMAGE_READY" />
        </el-select>
        <el-select v-model="filterType" placeholder="应用类型" clearable style="width: 110px;" @change="load">
          <el-option label="Java" value="java" />
          <el-option label="Vue" value="vue" />
        </el-select>
      </div>
    </div>

    <div class="table-wrapper">
      <el-table :data="pipelines" v-loading="loading" stripe @row-click="goToDetail" style="cursor: pointer;">
        <el-table-column prop="pipelineNo" label="任务编号" width="170" />
        <el-table-column prop="appName" label="应用" width="140" />
        <el-table-column prop="gitBranch" label="分支" width="200" show-overflow-tooltip />
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small" effect="dark">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="错误阶段" width="110">
          <template #default="{ row }">
            <span v-if="row.errorStage" style="color: #f56c6c; font-size: 12px;">{{ row.errorStage }}</span>
            <span v-else style="color: #c0c4cc;">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="triggerUser" label="触发人" width="80" />
        <el-table-column label="耗时" width="70">
          <template #default="{ row }">{{ row.durationSeconds ? row.durationSeconds + 's' : '-' }}</template>
        </el-table-column>
        <el-table-column prop="createdAt" label="触发时间" width="170">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="180" fixed="right" align="center">
          <template #default="{ row }">
            <el-button v-if="row.status === 'IMAGE_READY'" type="warning" link size="small" @click.stop="handleDeploy(row)">开始部署</el-button>
            <el-button type="primary" link size="small" @click.stop="goToDetail(row)">详情</el-button>
            <el-button type="danger" link size="small" @click.stop="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="pagination-row">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :page-sizes="[10, 20, 50, 100]"
          :total="total"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="handlePageChange"
          @size-change="handleSizeChange"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.toolbar-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
  background: #fff;
  padding: 12px 16px;
  border-radius: 6px;
  border: 1px solid #ebeef5;
}

.toolbar-left {
  display: flex;
  align-items: center;
  gap: 10px;
}

.table-wrapper {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  overflow: hidden;
}
</style>

.pagination-row {
  display: flex;
  justify-content: flex-end;
  padding: 12px 16px;
  border-top: 1px solid #ebeef5;
}

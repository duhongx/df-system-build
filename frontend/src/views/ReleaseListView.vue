<script setup lang="ts">
import { hasCompletedDeploymentError } from '../utils/pipeline-result'
import { ref, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listPipelines, cancelPipeline, rolloutPipeline, type Pipeline } from '../api/pipeline'
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
      search: searchText.value.trim() || undefined,
      appType: filterType.value || undefined,
    })
    pipelines.value = result.list || []
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
    PENDING: '排队中', CANCELED: '已取消', IMAGE_READY: '镜像制作完成', DEPLOYING: '滚动更新排队中',
  }
  return map[status] || status
}

function goToDetail(p: Pipeline) {
  router.push(`/release/${p.id}`)
}

function handleSearch() {
  page.value = 1
  load()
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

async function handleCancel(p: Pipeline) {
  await ElMessageBox.confirm(`确定取消任务 "${p.pipelineNo}" 吗？`, '确认取消', { type: 'warning' })
  try {
    await cancelPipeline(p.id)
    ElMessage.success('已取消')
    await load()
  } catch (e) {
    // handled
  }
}

async function handleDeploy(p: Pipeline) {
  try {
    await rolloutPipeline(p.id)
    ElMessage.success('滚动更新已提交')
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
  <div class="page release-page">
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
          @keyup.enter="handleSearch"
          @clear="handleSearch"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
        <el-select v-model="filterStatus" placeholder="状态" clearable style="width: 110px;" @change="handleSearch">
          <el-option label="运行中" value="RUNNING" />
          <el-option label="成功" value="SUCCESS" />
          <el-option label="失败" value="FAILED" />
          <el-option label="排队中" value="PENDING" />
          <el-option label="滚动更新排队中" value="DEPLOYING" />
          <el-option label="已取消" value="CANCELED" />
          <el-option label="镜像制作完成" value="IMAGE_READY" />
        </el-select>
        <el-select v-model="filterType" placeholder="应用类型" clearable style="width: 110px;" @change="handleSearch">
          <el-option label="Java" value="java" />
          <el-option label="Vue" value="vue" />
        </el-select>
      </div>
    </div>

    <div class="table-wrapper">
      <div class="history-table">
      <el-table :data="pipelines" height="100%" v-loading="loading" stripe @row-click="goToDetail" style="width: 100%; cursor: pointer;">
        <el-table-column prop="pipelineNo" label="任务编号" min-width="140" show-overflow-tooltip />
        <el-table-column prop="appName" label="应用" min-width="110" show-overflow-tooltip />
        <el-table-column prop="gitBranch" label="分支" min-width="150" show-overflow-tooltip />
        <el-table-column label="状态" min-width="150" align="center">
          <template #default="{ row }">
            <el-tag :type="hasCompletedDeploymentError(row) ? 'warning' : statusType(row.status)" size="small" effect="dark">{{ hasCompletedDeploymentError(row) ? '部署成功，结果记录异常' : statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="错误阶段" min-width="100" show-overflow-tooltip>
          <template #default="{ row }">
            <span v-if="row.errorStage" style="color: #f56c6c; font-size: 12px;">{{ row.errorStage }}</span>
            <span v-else style="color: #c0c4cc;">-</span>
          </template>
        </el-table-column>
        <el-table-column prop="triggerUser" label="触发人" width="70" />
        <el-table-column label="耗时" width="65">
          <template #default="{ row }">{{ row.durationSeconds ? row.durationSeconds + 's' : '-' }}</template>
        </el-table-column>
        <el-table-column prop="createdAt" label="触发时间" min-width="160" show-overflow-tooltip>
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="145" fixed="right" align="center">
          <template #default="{ row }">
            <div class="history-actions">
            <el-button v-if="row.status === 'IMAGE_READY'" type="warning" link size="small" @click.stop="handleDeploy(row)">滚动更新</el-button>
            <el-button type="primary" link size="small" @click.stop="goToDetail(row)">详情</el-button>
            <el-button type="danger" link size="small" v-if="['PENDING', 'RUNNING', 'DEPLOYING', 'IMAGE_READY'].includes(row.status)" @click.stop="handleCancel(row)">取消</el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>
      </div>
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
.release-page { display: flex; flex-direction: column; flex: 1; min-height: 0; min-width: 0; width: 100%; }
.history-table { flex: 1; min-height: 0; min-width: 0; }
.history-actions { display: flex; justify-content: center; gap: 8px; white-space: nowrap; }
.history-actions :deep(.el-button + .el-button) { margin-left: 0; }

.toolbar-row {
  flex-shrink: 0;
  flex-wrap: wrap;
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
  flex-wrap: wrap;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 10px;
}

.table-wrapper {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  overflow: hidden;
}

.pagination-row {
  flex-shrink: 0;
  overflow-x: auto;
  display: flex;
  justify-content: flex-end;
  padding: 12px 16px;
  border-top: 1px solid #ebeef5;
}
</style>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { listArtifacts, type Artifact } from '../api/pipeline'
import { formatTime } from '../utils/time'

const artifacts = ref<Artifact[]>([])
const loading = ref(true)
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)

onMounted(async () => {
  await load()
})

async function load() {
  loading.value = true
  try {
    const result = await listArtifacts({ page: page.value, pageSize: pageSize.value })
    artifacts.value = result.list || []
    total.value = result.total || 0
  } catch (e) {
    artifacts.value = []
    total.value = 0
  } finally {
    loading.value = false
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
  <div class="artifact-list-page">
    <el-card shadow="never" class="main-card">
      <template #header>
        <div class="card-header">
          <span class="card-title">制品记录</span>
          <el-tag type="info" size="small">仅显示构建成功的制品</el-tag>
        </div>
      </template>

      <el-table :data="artifacts" v-loading="loading" stripe border>
        <el-table-column prop="pipelineNo" label="任务编号" width="180" />
        <el-table-column prop="appName" label="应用" width="150" />
        <el-table-column prop="artifactName" label="制品名称" width="160" />
        <el-table-column prop="gitBranch" label="分支" width="200" show-overflow-tooltip />
        <el-table-column label="Git SHA" width="100">
          <template #default="{ row }">{{ row.gitCommitHash?.slice(0, 8) || '-' }}</template>
        </el-table-column>
        <el-table-column prop="uploadPath" label="上传路径" min-width="200" show-overflow-tooltip />
        <el-table-column prop="uploadTargets" label="目标服务器" width="160" />
        <el-table-column label="耗时" width="80">
          <template #default="{ row }">{{ row.durationSeconds }}s</template>
        </el-table-column>
        <el-table-column label="完成时间" width="160">
          <template #default="{ row }">{{ formatTime(row.createdAt) }}</template>
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
    </el-card>
  </div>
</template>

<style scoped>
.artifact-list-page { padding: 0; }
.main-card { border-radius: 8px; }
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.card-title {
  font-size: 15px;
  font-weight: 600;
}

.pagination-row {
  display: flex;
  justify-content: flex-end;
  padding: 12px 16px;
  border-top: 1px solid #ebeef5;
}
</style>

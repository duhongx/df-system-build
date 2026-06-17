<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { listArtifacts, type Artifact } from '../api/pipeline'
import { formatTime } from '../utils/time'

const artifacts = ref<Artifact[]>([])
const loading = ref(true)
const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const onlyLatest = ref(true)

onMounted(load)

async function load() {
  loading.value = true
  try {
    const result = await listArtifacts({ page: page.value, pageSize: onlyLatest.value ? 500 : pageSize.value })
    const list = result.list || []
    artifacts.value = onlyLatest.value ? list.filter(item => item.isLatest) : list
    total.value = onlyLatest.value ? artifacts.value.length : result.total || 0
  } catch (e) {
    artifacts.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function handleLatestChange(value: boolean | string | number) {
  onlyLatest.value = Boolean(value)
  page.value = 1
  load()
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

function sourceLabel(sourceType?: string) {
  switch (sourceType) {
    case 'local_upload': return '本地上传'
    case 'server_download': return '服务器下载'
    case 'artifact_reuse': return '制品库复用'
    case 'build': return '源码构建'
    default: return sourceType || '-'
  }
}
</script>

<template>
  <div class="artifact-management-page">
    <div class="page-header">
      <div>
        <h4>最新制品库</h4>
        <p>每个微服务保留一份 latest 制品，用于客户环境复制、制品比对和后续复用。</p>
      </div>
    </div>

    <el-card shadow="never" class="main-card">
      <div class="latest-header">
        <div>
          <span class="section-title">制品列表</span>
          <div class="section-subtitle">部署成功后才更新 latest；未匹配、不可读、部署失败的制品不会进入 latest。</div>
        </div>
        <el-switch
          v-model="onlyLatest"
          active-text="仅最新"
          inactive-text="历史"
          @change="handleLatestChange"
        />
      </div>

      <div class="artifact-flow">
        <div class="artifact-flow-item">
          <strong>制品导入</strong>
          <span>本地上传、服务器下载只生成更新版本，不直接覆盖 latest。</span>
        </div>
        <div class="artifact-flow-item">
          <strong>部署成功</strong>
          <span>只有部署成功的应用更新 latest，未匹配或失败制品不入库。</span>
        </div>
        <div class="artifact-flow-item">
          <strong>版本回滚</strong>
          <span>回滚依赖版本记录的更新前镜像，不依赖 latest 制品倒推。</span>
        </div>
      </div>

      <el-table :data="artifacts" v-loading="loading" stripe border table-layout="auto">
        <el-table-column prop="appName" label="应用" min-width="140" show-overflow-tooltip />
        <el-table-column prop="artifactName" label="制品名称" min-width="150" show-overflow-tooltip />
        <el-table-column label="来源" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="row.sourceType ? 'primary' : 'info'">{{ sourceLabel(row.sourceType) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最新" width="76">
          <template #default="{ row }">
            <el-tag v-if="row.isLatest" type="success" size="small">最新</el-tag>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column prop="batchId" label="版本" min-width="130" show-overflow-tooltip />
        <el-table-column prop="gitBranch" label="分支" min-width="140" show-overflow-tooltip />
        <el-table-column label="Git SHA" width="100">
          <template #default="{ row }">{{ row.gitCommitHash?.slice(0, 8) || '-' }}</template>
        </el-table-column>
        <el-table-column label="SHA256" width="100">
          <template #default="{ row }">{{ row.sha256?.slice(0, 8) || '-' }}</template>
        </el-table-column>
        <el-table-column prop="storagePath" label="存储路径" min-width="220" show-overflow-tooltip />
        <el-table-column label="耗时" width="76">
          <template #default="{ row }">{{ row.durationSeconds }}s</template>
        </el-table-column>
        <el-table-column label="完成时间" min-width="170">
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
.artifact-management-page { display: flex; flex-direction: column; gap: 12px; }
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #fff;
  border: 1px solid #e5e7eb;
  padding: 16px 18px;
  border-radius: 4px;
}
.page-header h4 { margin: 0 0 6px; font-size: 16px; color: #1f2937; }
.page-header p { margin: 0; color: #8a9099; }
.main-card { border-radius: 4px; }
.latest-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.section-title {
  font-size: 15px;
  font-weight: 600;
}
.section-subtitle {
  margin-top: 4px;
  font-size: 12px;
  color: #909399;
}

.artifact-flow {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 14px;
}

.artifact-flow-item {
  min-height: 70px;
  padding: 12px;
  border: 1px solid #ebeef5;
  border-radius: 8px;
  background: #f8fafc;
}

.artifact-flow-item strong {
  display: block;
  font-size: 13px;
  color: #303133;
  margin-bottom: 6px;
}

.artifact-flow-item span {
  font-size: 12px;
  line-height: 1.6;
  color: #606266;
}

.pagination-row {
  display: flex;
  justify-content: flex-end;
  padding: 12px 16px;
  border-top: 1px solid #ebeef5;
}
</style>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { matchArtifacts, executeBatchDeploy, listLocalDir, type MatchResult } from '../api/batch-deploy'
import { useSettingsStore } from '../stores/settings'

const router = useRouter()
const settingsStore = useSettingsStore()
const step = ref(1)
const loading = ref(false)

// Step 1: Source selection
const sourceType = ref<'upload' | 'local'>('upload')
const localPath = ref('')
const uploadedFiles = ref<string[]>([])
const sourceDir = ref('')
const batchId = ref('')
const namespace = ref('')

// Step 2: Match results
const matchResults = ref<MatchResult[]>([])
const matchedCount = ref(0)

onMounted(async () => {
  if (!settingsStore.loaded) {
    await settingsStore.fetchSettings()
  }
  namespace.value = settingsStore.k8sNamespace
})

// Upload handler
async function handleUpload(event: Event) {
  const input = event.target as HTMLInputElement
  if (!input.files?.length) return

  const formData = new FormData()
  for (let i = 0; i < input.files.length; i++) {
    formData.append('files', input.files[i])
  }

  loading.value = true
  try {
    const resp = await fetch('/api/batch-deploy/upload', {
      method: 'POST',
      headers: { 'Authorization': `Bearer ${localStorage.getItem('df-token') || ''}` },
      body: formData,
    })
    const data = await resp.json()
    if (data.code === 0) {
      uploadedFiles.value = data.data.success || data.data.files || []
      sourceDir.value = data.data.uploadDir
      batchId.value = data.data.batchId || ''
      ElMessage.success(`已上传 ${data.data.count} 个文件`)
      await doMatch(uploadedFiles.value)
    } else {
      ElMessage.error(data.message || '上传失败')
    }
  } catch (e) {
    ElMessage.error('上传失败')
  } finally {
    loading.value = false
    input.value = ''
  }
}

async function handleLoadLocalDir() {
  if (!localPath.value.trim()) { ElMessage.warning('请输入目录路径'); return }
  loading.value = true
  try {
    const result = await listLocalDir(localPath.value, batchId.value)
    uploadedFiles.value = result.files
    sourceDir.value = result.path
    ElMessage.success(`找到 ${result.count} 个制品文件`)
    await doMatch(uploadedFiles.value)
  } catch (e) {
    ElMessage.error('读取目录失败')
  } finally {
    loading.value = false
  }
}

async function doMatch(files: string[]) {
  if (files.length === 0) return
  try {
    const result = await matchArtifacts(files, sourceDir.value, batchId.value)
    matchResults.value = result.results
    matchedCount.value = result.matched
    step.value = 2
    // Show warning for invalid files
    const invalidCount = result.invalid || 0
    if (invalidCount > 0) {
      ElMessage.warning(`${invalidCount} 个文件异常（已标记），请检查后重新上传`)
    }
  } catch (e) { /* handled */ }
}

async function handleExecute() {
  const items = matchResults.value
    .filter(r => r.matched)
    .map(r => ({ fileName: r.fileName, appId: r.appId }))

  if (items.length === 0) {
    ElMessage.warning('没有匹配成功的应用')
    return
  }

  loading.value = true
  try {
    const result = await executeBatchDeploy(sourceDir.value, items, namespace.value.trim(), batchId.value)
    ElMessage.success(`已创建 ${result.pipelines.length} 个构建任务`)
    if (result.errors?.length) {
      ElMessage.warning(`${result.errors.length} 个失败: ${result.errors.join(', ')}`)
    }
    router.push('/build-queue')
  } catch (e) { /* handled */ }
  finally { loading.value = false }
}

function handleReset() {
  step.value = 1
  uploadedFiles.value = []
  matchResults.value = []
  matchedCount.value = 0
  sourceDir.value = ''
  batchId.value = ''
  namespace.value = settingsStore.k8sNamespace
}
</script>

<template>
  <div class="batch-deploy-page">
    <div class="page-header">
      <h4 class="page-title">批量部署</h4>
      <el-button v-if="step === 2" size="small" @click="handleReset">重新选择</el-button>
    </div>

    <!-- Step 1: Upload -->
    <div v-if="step === 1" class="content-card">
      <div class="step-title">选择制品来源</div>
      <el-radio-group v-model="sourceType" style="margin-bottom: 16px;">
        <el-radio value="upload">本地上传</el-radio>
        <el-radio value="local">服务器目录</el-radio>
      </el-radio-group>

      <div v-if="sourceType === 'upload'" class="upload-area">
        <label class="upload-zone">
          <div class="upload-icon"><el-icon :size="40"><Upload /></el-icon></div>
          <div class="upload-text">点击或拖拽上传制品文件</div>
          <div class="upload-hint">支持 .jar / .zip 文件，可多选</div>
          <input type="file" multiple accept=".jar,.zip" style="display: none;" @change="handleUpload" />
        </label>
      </div>

      <div v-if="sourceType === 'local'" class="local-dir-area">
        <div style="display: flex; gap: 8px;">
          <el-input v-model="localPath" placeholder="批量上传工作区内的制品目录路径" style="flex: 1;" />
          <el-button type="primary" :loading="loading" @click="handleLoadLocalDir">读取目录</el-button>
        </div>
        <div class="dir-hint">仅允许读取 df-build-server 工作区 workspaces/batch-upload 下的目录</div>
      </div>
    </div>

    <!-- Step 2: Match Results -->
    <div v-if="step === 2" class="content-card">
      <div class="step-title">
        匹配结果
        <el-tag type="success" size="small" style="margin-left: 8px;">{{ matchedCount }} 个匹配成功</el-tag>
        <el-tag v-if="matchResults.length - matchedCount > 0" type="danger" size="small" style="margin-left: 4px;">
          {{ matchResults.length - matchedCount }} 个未匹配
        </el-tag>
      </div>

      <el-table :data="matchResults" stripe size="small" border>
        <el-table-column label="文件名" min-width="200" prop="fileName" />
        <el-table-column label="匹配应用" width="180">
          <template #default="{ row }">
            <span v-if="row.matched" style="color: #67c23a;">{{ row.appName }}</span>
            <span v-else style="color: #f56c6c;">—</span>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="80">
          <template #default="{ row }">
            <el-tag v-if="row.matched" :type="row.appType === 'java' ? 'danger' : 'success'" size="small">{{ row.appType }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="120">
          <template #default="{ row }">
            <el-tag v-if="!row.valid" type="danger" size="small">文件异常</el-tag>
            <el-tag v-else-if="row.matched" type="success" size="small">匹配成功</el-tag>
            <el-tag v-else type="warning" size="small">未匹配</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="说明" min-width="200" prop="matchReason" />
      </el-table>

      <div class="action-bar">
        <el-input v-model="namespace" placeholder="K8s Namespace（留空使用默认）" class="namespace-input" />
        <el-button type="primary" size="large" :loading="loading" :disabled="matchedCount === 0" @click="handleExecute">
          <el-icon style="margin-right: 4px;"><VideoPlay /></el-icon>
          开始构建镜像 ({{ matchedCount }} 个应用)
        </el-button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.batch-deploy-page { display: flex; flex-direction: column; gap: 12px; }
.page-header { display: flex; align-items: center; justify-content: space-between; }
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 24px; }
.step-title { font-size: 14px; font-weight: 600; color: #303133; margin-bottom: 16px; }

.upload-area { display: flex; justify-content: center; }
.upload-zone {
  display: flex; flex-direction: column; align-items: center; justify-content: center;
  width: 100%; max-width: 500px; height: 200px;
  border: 2px dashed #dcdfe6; border-radius: 8px; cursor: pointer;
  transition: all 0.2s;
}
.upload-zone:hover { border-color: #409eff; background: #f5f7fa; }
.upload-icon { color: #c0c4cc; margin-bottom: 8px; }
.upload-text { font-size: 14px; color: #606266; }
.upload-hint { font-size: 12px; color: #909399; margin-top: 4px; }

.local-dir-area { max-width: 600px; }
.dir-hint { font-size: 12px; color: #909399; margin-top: 8px; }

.action-bar { margin-top: 20px; display: flex; justify-content: center; gap: 12px; align-items: center; }
.namespace-input { width: 240px; }
</style>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listBuildConfigs, createBuildConfig, updateBuildConfig, deleteBuildConfig, type BuildConfig } from '../api/build-config'

const loading = ref(true)
const buildConfigs = ref<BuildConfig[]>([])

const dialogVisible = ref(false)
const editMode = ref(false)
const form = ref<any>({
  name: '',
  category: 'java',
  buildMode: 'docker',
  dockerImage: '',
  cpuLimit: '2',
  memoryLimit: '4g',
  cacheMounts: [] as { hostPath: string; containerPath: string }[],
  installCommand: '',
  buildCommand: '',
  artifactDir: '',
  envVars: '',
  description: '',
  status: 'online',
})

onMounted(async () => {
  await loadData()
})

async function loadData() {
  loading.value = true
  try {
    buildConfigs.value = await listBuildConfigs()
  } catch (e) {
    buildConfigs.value = []
  } finally {
    loading.value = false
  }
}

function handleAdd() {
  editMode.value = false
  form.value = {
    name: '',
    category: 'java',
    buildMode: 'docker',
    dockerImage: '',
    cpuLimit: '2',
    memoryLimit: '4g',
    cacheMounts: [],
    installCommand: '',
    buildCommand: '',
    artifactDir: '',
    envVars: '',
    description: '',
    status: 'online',
  }
  dialogVisible.value = true
}

function handleEdit(row: BuildConfig) {
  editMode.value = true
  let mounts: any[] = []
  try { mounts = JSON.parse(row.cacheMounts || '[]') } catch (_) { mounts = [] }
  form.value = {
    id: row.id,
    name: row.name,
    category: row.category,
    buildMode: row.buildMode,
    dockerImage: row.dockerImage || '',
    cpuLimit: row.cpuLimit || '2',
    memoryLimit: row.memoryLimit || '4g',
    cacheMounts: mounts,
    installCommand: row.installCommand || '',
    buildCommand: row.buildCommand,
    artifactDir: row.artifactDir || '',
    envVars: row.envVars || '',
    description: row.description || '',
    status: row.status || 'online',
  }
  dialogVisible.value = true
}

async function handleSave() {
  if (!form.value.name?.trim()) { ElMessage.warning('请输入名称'); return }
  if (!form.value.buildCommand?.trim()) { ElMessage.warning('请输入构建命令'); return }
  if (form.value.buildMode === 'docker' && !form.value.dockerImage?.trim()) {
    ElMessage.warning('Docker 模式需要填写镜像'); return
  }

  const payload = {
    ...form.value,
    cacheMounts: JSON.stringify(form.value.cacheMounts || []),
  }
  delete payload.id

  try {
    if (editMode.value && form.value.id) {
      await updateBuildConfig(form.value.id, payload)
      ElMessage.success('更新成功')
    } else {
      await createBuildConfig(payload)
      ElMessage.success('创建成功')
    }
    dialogVisible.value = false
    await loadData()
  } catch (e) { /* handled by interceptor */ }
}

async function handleDelete(row: BuildConfig) {
  await ElMessageBox.confirm(`确定删除编译配置 "${row.name}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await deleteBuildConfig(row.id)
    ElMessage.success('删除成功')
    await loadData()
  } catch (e) { /* handled */ }
}

function categoryColor(cat: string) {
  return cat === 'java' ? 'danger' : cat === 'vue' ? 'success' : 'info'
}

function buildModeColor(mode: string) {
  return mode === 'docker' ? '' : 'warning'
}
</script>

<template>
  <div class="page">
    <div class="toolbar-row">
      <h4 class="page-title">编译配置</h4>
      <el-button type="primary" size="small" @click="handleAdd"><el-icon><Plus /></el-icon>新增</el-button>
    </div>

    <div class="content-card">
      <el-table :data="buildConfigs" v-loading="loading" stripe border>
        <el-table-column prop="name" label="名称" min-width="160" />
        <el-table-column label="分类" width="80" align="center">
          <template #default="{ row }">
            <el-tag :type="categoryColor(row.category)" size="small">{{ row.category }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="编译模式" width="100" align="center">
          <template #default="{ row }">
            <el-tag :type="buildModeColor(row.buildMode)" size="small">{{ row.buildMode }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="buildCommand" label="构建命令" min-width="200" show-overflow-tooltip />
        <el-table-column prop="dockerImage" label="Docker 镜像" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">{{ row.dockerImage || '-' }}</template>
        </el-table-column>
        <el-table-column label="状态" width="80" align="center">
          <template #default="{ row }">
            <el-tag :type="row.status === 'online' ? 'success' : 'danger'" size="small">{{ row.status || 'online' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" align="center">
          <template #default="{ row }">
            <el-button type="primary" link size="small" @click="handleEdit(row)">编辑</el-button>
            <el-button type="danger" link size="small" @click="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Add/Edit Dialog -->
    <el-dialog v-model="dialogVisible" :title="editMode ? '编辑编译配置' : '新增编译配置'" width="620px" :close-on-click-modal="false">
      <el-form :model="form" label-width="100px">
        <el-form-item label="名称" required>
          <el-input v-model="form.name" placeholder="如 Java Gradle Docker" />
        </el-form-item>
        <el-form-item label="分类" required>
          <el-radio-group v-model="form.category">
            <el-radio value="java">Java</el-radio>
            <el-radio value="vue">Vue</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="编译模式" required>
          <el-radio-group v-model="form.buildMode">
            <el-radio value="docker">Docker 容器</el-radio>
            <el-radio value="local">本地编译</el-radio>
          </el-radio-group>
        </el-form-item>

        <!-- Docker mode fields -->
        <template v-if="form.buildMode === 'docker'">
          <el-form-item label="Docker 镜像" required>
            <el-input v-model="form.dockerImage" placeholder="ops-builder-gradle:jdk8" />
          </el-form-item>
          <el-form-item label="CPU 限制">
            <el-input v-model="form.cpuLimit" style="width: 100px;" placeholder="2" />
          </el-form-item>
          <el-form-item label="内存限制">
            <el-input v-model="form.memoryLimit" style="width: 100px;" placeholder="4g" />
          </el-form-item>
          <el-form-item label="缓存挂载">
            <div class="cache-mounts">
              <div v-for="(mount, idx) in form.cacheMounts" :key="idx" class="cache-mount-row">
                <el-input v-model="mount.hostPath" placeholder="宿主机路径" style="width: 190px;" />
                <span style="margin: 0 6px; color: #909399;">→</span>
                <el-input v-model="mount.containerPath" placeholder="容器内路径" style="width: 190px;" />
                <el-button type="danger" link @click="form.cacheMounts.splice(idx, 1)">
                  <el-icon><Delete /></el-icon>
                </el-button>
              </div>
              <el-button size="small" @click="form.cacheMounts.push({ hostPath: '', containerPath: '' })">
                <el-icon><Plus /></el-icon>添加挂载
              </el-button>
            </div>
          </el-form-item>
        </template>

        <!-- Local mode fields -->
        <template v-if="form.buildMode === 'local'">
          <el-form-item label="环境变量">
            <el-input v-model="form.envVars" type="textarea" :rows="3" placeholder="KEY=VALUE 格式，每行一个（可选）" />
          </el-form-item>
        </template>

        <!-- Common fields -->
        <el-form-item label="安装命令">
          <el-input v-model="form.installCommand" placeholder="如 yarn install（前端项目填写）" />
        </el-form-item>
        <el-form-item label="构建命令" required>
          <el-input v-model="form.buildCommand" placeholder="如 gradle clean build -x test" />
        </el-form-item>
        <el-form-item label="产物目录">
          <el-input v-model="form.artifactDir" placeholder="如 build/libs 或 dist" />
        </el-form-item>
        <el-form-item label="描述">
          <el-input v-model="form.description" type="textarea" :rows="2" placeholder="可选描述" />
        </el-form-item>
        <el-form-item label="状态">
          <el-radio-group v-model="form.status">
            <el-radio value="online">启用</el-radio>
            <el-radio value="offline">禁用</el-radio>
          </el-radio-group>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSave">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page {
  padding: 0;
}

.toolbar-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.page-title {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
  margin: 0;
}

.content-card {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  padding: 16px;
}

.cache-mounts {
  width: 100%;
}

.cache-mount-row {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
}
</style>

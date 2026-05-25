<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listConfigItems, createConfigItem, updateConfigItem, deleteConfigItem, type ConfigItem } from '../api/config-items'

const items = ref<ConfigItem[]>([])
const filterCategory = ref('')
const dialogVisible = ref(false)
const editMode = ref(false)
const form = ref<Partial<ConfigItem>>({
  name: '', code: '', category: 'dockerfile', contentType: 'text', content: '', description: ''
})

const filteredItems = computed(() => {
  if (!filterCategory.value) return items.value
  return items.value.filter(i => i.category === filterCategory.value)
})

async function loadItems() {
  try {
    items.value = await listConfigItems()
  } catch (e) {
    items.value = []
  }
}

function handleAdd() {
  editMode.value = false
  form.value = { name: '', code: '', category: 'dockerfile', contentType: 'text', content: '', description: '' }
  dialogVisible.value = true
}

function handleEdit(item: ConfigItem) {
  editMode.value = true
  form.value = { ...item }
  dialogVisible.value = true
}

async function handleSave() {
  if (!form.value.name?.trim() || !form.value.code?.trim() || !form.value.content?.trim()) {
    ElMessage.warning('名称、编码和内容不能为空')
    return
  }
  try {
    if (editMode.value && form.value.id) {
      await updateConfigItem(form.value.id, form.value)
      ElMessage.success('配置项已更新')
    } else {
      await createConfigItem(form.value)
      ElMessage.success('配置项已创建')
    }
    dialogVisible.value = false
    await loadItems()
  } catch (e) { /* handled */ }
}

async function handleDelete(item: ConfigItem) {
  await ElMessageBox.confirm(`确定删除配置项 "${item.name}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await deleteConfigItem(item.id)
    ElMessage.success('删除成功')
    await loadItems()
  } catch (e) { /* handled */ }
}

function categoryLabel(cat: string) {
  const map: Record<string, string> = { dockerfile: 'Dockerfile', k8s: 'K8s', script: '脚本' }
  return map[cat] || cat
}

function categoryColor(cat: string) {
  const map: Record<string, string> = { dockerfile: 'primary', k8s: 'success', script: 'warning' }
  return (map[cat] || 'info') as any
}

function contentTypeLabel(ct: string) {
  const map: Record<string, string> = { text: 'Dockerfile', yaml: 'YAML', shell: 'Shell' }
  return map[ct] || ct
}

// Auto-set contentType based on category
function onCategoryChange(cat: string) {
  if (cat === 'dockerfile') form.value.contentType = 'text'
  else if (cat === 'k8s') form.value.contentType = 'yaml'
  else if (cat === 'script') form.value.contentType = 'shell'
}

onMounted(loadItems)
</script>

<template>
  <div class="config-items-page">
    <div class="page-title-row">
      <div>
        <h4 class="page-title">配置项管理</h4>
        <p class="page-desc">管理 Dockerfile 模板、K8s YAML 模板、脚本文件。支持 ${var} 变量替换。</p>
      </div>
      <div class="title-actions">
        <el-select v-model="filterCategory" placeholder="全部分类" clearable size="small" style="width: 120px; margin-right: 8px;">
          <el-option label="全部" value="" />
          <el-option label="Dockerfile" value="dockerfile" />
          <el-option label="K8s" value="k8s" />
          <el-option label="脚本" value="script" />
        </el-select>
        <el-button type="primary" size="small" @click="handleAdd"><el-icon><Plus /></el-icon>新增</el-button>
      </div>
    </div>

    <div class="content-card">
      <el-table :data="filteredItems" stripe border>
        <el-table-column prop="name" label="名称" min-width="160" />
        <el-table-column prop="code" label="编码" width="160" />
        <el-table-column label="分类" width="110">
          <template #default="{ row }">
            <el-tag :type="categoryColor(row.category)" size="small">{{ categoryLabel(row.category) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="100">
          <template #default="{ row }">
            <span class="content-type">{{ contentTypeLabel(row.contentType) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="description" label="描述" min-width="200" show-overflow-tooltip />
        <el-table-column label="操作" width="150">
          <template #default="{ row }">
            <el-button type="primary" link size="small" @click="handleEdit(row)">编辑</el-button>
            <el-button type="danger" link size="small" @click="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Dialog -->
    <el-dialog v-model="dialogVisible" :title="editMode ? '编辑配置项' : '新增配置项'" width="750px" top="5vh">
      <el-form :model="form" label-width="80px">
        <el-row :gutter="16">
          <el-col :span="12">
            <el-form-item label="名称" required>
              <el-input v-model="form.name" placeholder="如 Java Dockerfile" />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="编码" required>
              <el-input v-model="form.code" placeholder="如 dockerfile-java" :disabled="editMode" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-row :gutter="16">
          <el-col :span="12">
            <el-form-item label="分类" required>
              <el-select v-model="form.category" style="width: 100%;" @change="onCategoryChange">
                <el-option label="Dockerfile" value="dockerfile" />
                <el-option label="K8s" value="k8s" />
                <el-option label="脚本" value="script" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="描述">
              <el-input v-model="form.description" placeholder="可选描述" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item label="内容" required>
          <div class="editor-hint">
            <span>支持变量: <code>${registryUrl}</code> <code>${appName}</code> <code>${branch}</code> <code>${artifactName}</code> <code>${version}</code> <code>${namespace}</code> <code>${imageName}</code></span>
          </div>
          <el-input
            v-model="form.content"
            type="textarea"
            :rows="18"
            placeholder="输入模板内容..."
            class="code-editor"
          />
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
.page-title {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
  margin: 0;
}

.page-title-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-desc {
  font-size: 13px;
  color: #909399;
  margin: 4px 0 0 0;
}

.title-actions {
  display: flex;
  align-items: center;
}

.content-card {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  padding: 16px;
}

.content-type {
  font-size: 12px;
  color: #606266;
}

.editor-hint {
  margin-bottom: 8px;
  font-size: 12px;
  color: #909399;
}

.editor-hint code {
  background: #f5f7fa;
  padding: 1px 4px;
  border-radius: 3px;
  font-family: monospace;
  color: #409eff;
}

:deep(.code-editor textarea) {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 13px;
  line-height: 1.5;
}
</style>

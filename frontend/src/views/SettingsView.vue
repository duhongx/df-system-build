<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  listNotifications, createNotification, updateNotification, deleteNotification, testNotification, type NotificationWebhook,
  getSettings, updateSettings,
} from '../api/settings'

const route = useRoute()
const activeTab = computed(() => (route.meta?.settingsTab as string) || 'general')

// General
const generalForm = ref({
  concurrency_limit: '5',
  build_timeout_seconds: '1800',
  log_retention_days: '30',
  default_upload_path: '/root/DFHIS/his-release',
  clean_workspace_after_build: 'true',
  build_mode: 'docker',
  deploy_mode: 'deploy',
  customer_env: '本地',
})



async function loadGeneral() {
  try {
    const s = await getSettings()
    generalForm.value = {
      concurrency_limit: s.concurrency_limit || '5',
      build_timeout_seconds: s.build_timeout_seconds || '1800',
      log_retention_days: s.log_retention_days || '30',
      default_upload_path: s.default_upload_path || '/root/DFHIS/his-release',
      clean_workspace_after_build: s.clean_workspace_after_build || 'true',
      build_mode: s.build_mode || 'docker',
      deploy_mode: s.deploy_mode || 'deploy',
      customer_env: s.customer_env || '本地',
    }
  } catch (e) { /* ignore */ }
}



async function handleSaveGeneral() {
  try {
    await updateSettings(generalForm.value)
    ElMessage.success('设置已保存')
  } catch (e) { /* handled */ }
}



// Notifications
const notifications = ref<NotificationWebhook[]>([])
const notifyDialogVisible = ref(false)
const notifyEditMode = ref(false)
const notifyForm = ref<Partial<NotificationWebhook>>({
  name: '', type: 'dingtalk', webhookUrl: '', secret: '',
  notifyOnSuccess: true, notifyOnFailure: true, enabled: true,
})

async function loadNotifications() {
  try { notifications.value = await listNotifications() } catch (e) { notifications.value = [] }
}

function handleAddNotify() {
  notifyEditMode.value = false
  notifyForm.value = {
    name: '', type: 'dingtalk', webhookUrl: '', secret: '',
    notifyOnSuccess: true, notifyOnFailure: true, enabled: true,
  }
  notifyDialogVisible.value = true
}

function handleEditNotify(n: NotificationWebhook) {
  notifyEditMode.value = true
  notifyForm.value = { ...n }
  notifyDialogVisible.value = true
}

async function handleSaveNotify() {
  if (!notifyForm.value.name?.trim() || !notifyForm.value.webhookUrl?.trim()) {
    ElMessage.warning('请填写名称和 Webhook URL')
    return
  }
  try {
    if (notifyEditMode.value && notifyForm.value.id) {
      await updateNotification(notifyForm.value.id, notifyForm.value)
    } else {
      await createNotification(notifyForm.value)
    }
    ElMessage.success('保存成功')
    notifyDialogVisible.value = false
    await loadNotifications()
  } catch (e) { /* handled */ }
}

async function handleDeleteNotify(n: NotificationWebhook) {
  await ElMessageBox.confirm(`确定删除通知 "${n.name}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await deleteNotification(n.id)
    ElMessage.success('删除成功')
    await loadNotifications()
  } catch (err) { /* handled */ }
}

async function handleTestNotify(n: NotificationWebhook) {
  try {
    await testNotification(n.id)
    ElMessage.success('测试消息已发送')
  } catch (e) { /* handled */ }
}

// Watch tab change to load data
watch(activeTab, (tab) => { loadForTab(tab) }, { immediate: false })

function loadForTab(tab: string) {
  if (tab === 'general') loadGeneral()
  else if (tab === 'notifications') loadNotifications()
}

onMounted(() => {
  loadForTab(activeTab.value)
})
</script>

<template>
  <div class="settings-page">
    <!-- General -->
    <div v-if="activeTab === 'general'">
      <h4 class="page-title">全局参数</h4>
      <div class="content-card">
        <el-form label-width="130px" style="max-width: 580px;">
          <el-form-item label="客户环境">
            <el-select v-model="generalForm.customer_env" style="width: 200px;">
              <el-option label="本地" value="本地" />
              <el-option label="浙江" value="浙江" />
              <el-option label="河南" value="河南" />
              <el-option label="湖南" value="湖南" />
              <el-option label="江西" value="江西" />
              <el-option label="山西" value="山西" />
              <el-option label="新疆" value="新疆" />
              <el-option label="安徽" value="安徽" />
              <el-option label="四川" value="四川" />
              <el-option label="甘肃" value="甘肃" />
              <el-option label="陕西" value="陕西" />
            </el-select>
            <span class="form-hint">当前部署环境，影响应用过滤</span>
          </el-form-item>
          <el-form-item label="部署模式">
            <el-radio-group v-model="generalForm.deploy_mode">
              <el-radio value="deploy">编译 + 部署</el-radio>
              <el-radio value="upload_deploy">上传制品 + 部署</el-radio>
            </el-radio-group>
            <div class="form-hint-block">编译+部署: 源码编译 → 构建镜像 → K8s 部署；上传制品+部署: 上传 jar/zip → 构建镜像 → K8s 部署</div>
          </el-form-item>
          <el-form-item label="制品上传路径" v-if="generalForm.deploy_mode === 'upload_deploy'">
            <el-input v-model="generalForm.default_upload_path" placeholder="/root/DFHIS/his-release" />
            <div class="form-hint-block">制品上传到服务器上的目录</div>
          </el-form-item>
          <el-divider />
          <el-form-item label="全局并发上限">
            <el-input-number
              :model-value="Number(generalForm.concurrency_limit)"
              @update:model-value="(v: any) => generalForm.concurrency_limit = String(v)"
              :min="1" :max="20"
            />
            <span class="form-hint">同时运行的最大构建数</span>
          </el-form-item>
          <el-form-item label="构建超时(秒)">
            <el-input-number
              :model-value="Number(generalForm.build_timeout_seconds)"
              @update:model-value="(v: any) => generalForm.build_timeout_seconds = String(v)"
              :min="60" :max="7200" :step="60"
            />
          </el-form-item>
          <el-form-item label="日志保留天数">
            <el-input-number
              :model-value="Number(generalForm.log_retention_days)"
              @update:model-value="(v: any) => generalForm.log_retention_days = String(v)"
              :min="1" :max="365"
            />
            <span class="form-hint">超过此天数的日志自动清理</span>
          </el-form-item>
          <el-form-item label="构建后清理">
            <el-switch
              :model-value="generalForm.clean_workspace_after_build === 'true'"
              @update:model-value="(v: any) => generalForm.clean_workspace_after_build = String(v)"
            />
            <span class="form-hint">构建完成后自动清理源码和中间产物</span>
          </el-form-item>
          <el-form-item>
            <el-button type="primary" @click="handleSaveGeneral">保存设置</el-button>
          </el-form-item>
        </el-form>
      </div>
    </div>

    <!-- Notifications -->
    <div v-if="activeTab === 'notifications'">
      <div class="page-title-row">
        <h4 class="page-title">通知 Webhook</h4>
        <el-button type="primary" size="small" @click="handleAddNotify"><el-icon><Plus /></el-icon>新增</el-button>
      </div>
      <div class="content-card">
        <el-table :data="notifications" stripe border>
          <el-table-column prop="name" label="名称" width="130" />
          <el-table-column label="类型" width="80">
            <template #default="{ row }">
              <el-tag size="small">{{ row.type === 'dingtalk' ? '钉钉' : '企微' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="webhookUrl" label="Webhook URL" min-width="260" show-overflow-tooltip />
          <el-table-column label="成功" width="60" align="center">
            <template #default="{ row }">
              <el-icon :color="row.notifyOnSuccess ? '#67c23a' : '#c0c4cc'"><CircleCheck /></el-icon>
            </template>
          </el-table-column>
          <el-table-column label="失败" width="60" align="center">
            <template #default="{ row }">
              <el-icon :color="row.notifyOnFailure ? '#f56c6c' : '#c0c4cc'"><CircleClose /></el-icon>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="70">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'" size="small">{{ row.enabled ? '启用' : '禁用' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="200">
            <template #default="{ row }">
              <el-button type="primary" link size="small" @click="handleEditNotify(row)">编辑</el-button>
              <el-button type="success" link size="small" @click="handleTestNotify(row)">测试</el-button>
              <el-button type="danger" link size="small" @click="handleDeleteNotify(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </div>

    <!-- Dialogs -->
    <el-dialog v-model="notifyDialogVisible" :title="notifyEditMode ? '编辑通知' : '新增通知'" width="500px">
      <el-form :model="notifyForm" label-width="100px">
        <el-form-item label="名称" required><el-input v-model="notifyForm.name" /></el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="notifyForm.type">
            <el-radio value="dingtalk">钉钉</el-radio>
            <el-radio value="wecom">企业微信</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="Webhook URL" required><el-input v-model="notifyForm.webhookUrl" /></el-form-item>
        <el-form-item label="Secret"><el-input v-model="notifyForm.secret" placeholder="可选" /></el-form-item>
        <el-form-item label="通知规则">
          <el-checkbox v-model="notifyForm.notifyOnSuccess">成功时通知</el-checkbox>
          <el-checkbox v-model="notifyForm.notifyOnFailure">失败时通知</el-checkbox>
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="notifyForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="notifyDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSaveNotify">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-title {
  font-size: 15px;
  font-weight: 600;
  color: #303133;
  margin: 0 0 16px 0;
}

.page-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}

.page-title-row .page-title { margin: 0; }

.page-desc {
  font-size: 13px;
  color: #909399;
  margin: 0;
}

.content-card {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  padding: 20px;
}

.form-hint {
  margin-left: 12px;
  font-size: 12px;
  color: #909399;
}

.form-hint-block {
  font-size: 12px;
  color: #909399;
  margin-top: 4px;
}

.tpl-card { margin-bottom: 12px; }

.tpl-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.tpl-name {
  font-size: 13px;
  font-weight: 600;
}

.tpl-desc-text {
  font-size: 12px;
  color: #909399;
  margin: 0 0 10px 0;
}

.tpl-actions {
  margin-top: 12px;
  display: flex;
  gap: 8px;
}

.cache-mounts {
  width: 100%;
}

.cache-mount-row {
  display: flex;
  align-items: center;
  margin-bottom: 8px;
}

:deep(.code-editor textarea) {
  font-family: 'JetBrains Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 13px;
  line-height: 1.5;
}
</style>

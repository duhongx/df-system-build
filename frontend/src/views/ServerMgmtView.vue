<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  listManagedServers, createManagedServer, updateManagedServer,
  deleteManagedServer, testManagedServer, type ManagedServer
} from '../api/server-mgmt'

const router = useRouter()
const loading = ref(true)
const searchText = ref('')
const servers = ref<ManagedServer[]>([])

const dialogVisible = ref(false)
const editMode = ref(false)
const form = ref<any>({
  host: '', remark: '', port: 22, username: 'root',
  authType: 'password', credential: '', certPassphrase: '',
  connTimeout: 10, forbiddenCommands: '', sortOrder: 0,
})

async function loadServers() {
  loading.value = true
  try {
    servers.value = await listManagedServers(searchText.value || undefined)
  } catch (e) {
    servers.value = []
  } finally {
    loading.value = false
  }
}

function handleAdd() {
  editMode.value = false
  form.value = {
    host: '', remark: '', port: 22, username: 'root',
    authType: 'password', credential: '', certPassphrase: '',
    connTimeout: 10, forbiddenCommands: '', sortOrder: 0,
  }
  dialogVisible.value = true
}

function handleEdit(s: ManagedServer) {
  editMode.value = true
  form.value = { ...s, credential: '', certPassphrase: '' }
  dialogVisible.value = true
}

async function handleSave() {
  if (!form.value.host?.trim() || !form.value.username?.trim()) {
    ElMessage.warning('请填写主机和用户名')
    return
  }
  try {
    if (editMode.value && form.value.id) {
      await updateManagedServer(form.value.id, form.value)
      ElMessage.success('更新成功')
    } else {
      await createManagedServer(form.value)
      ElMessage.success('创建成功')
    }
    dialogVisible.value = false
    await loadServers()
  } catch (e) { /* handled */ }
}

async function handleDelete(s: ManagedServer) {
  await ElMessageBox.confirm(`确定删除服务器 "${s.host}" 吗？`, '确认删除', { type: 'warning' })
  try {
    await deleteManagedServer(s.id)
    ElMessage.success('删除成功')
    await loadServers()
  } catch (e) { /* handled */ }
}

async function handleTest(s: ManagedServer) {
  try {
    await testManagedServer(s.id)
    ElMessage.success('连接成功')
    await loadServers()
  } catch (e) { /* handled */ }
}

function handleTerminal(s: ManagedServer) {
  router.push(`/servers/${s.id}/terminal`)
}

function handleFiles(s: ManagedServer) {
  router.push(`/servers/${s.id}/files`)
}

function handleLogs(s: ManagedServer) {
  router.push(`/servers/${s.id}/logs`)
}

function handleMonitor(s: ManagedServer) {
  router.push(`/servers/${s.id}/monitor`)
}

function statusType(status: string) {
  if (status === 'online') return 'success'
  if (status === 'offline') return 'danger'
  return 'info'
}

function formatTime(t: string | null) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

async function handleReadKey() {
  try {
    const resp = await fetch('/api/servers/read-key', {
      headers: { 'Authorization': `Bearer ${localStorage.getItem('df-token') || ''}` }
    })
    const data = await resp.json()
    if (data.code === 0 && data.data?.content) {
      form.value.credential = data.data.content
      ElMessage.success(`已读取: ${data.data.path}`)
    } else {
      ElMessage.warning('未找到服务器上的 SSH 私钥文件，请手动输入')
    }
  } catch (e) {
    ElMessage.warning('读取失败，请手动输入')
  }
}

onMounted(loadServers)
</script>

<template>
  <div class="page">
    <div class="toolbar-row">
      <div class="toolbar-left">
        <el-input
          v-model="searchText"
          placeholder="关键字回车搜索"
          clearable
          style="width: 200px;"
          @keyup.enter="loadServers"
          @clear="loadServers"
        >
          <template #prefix><el-icon><Search /></el-icon></template>
        </el-input>
      </div>
      <div class="toolbar-right">
        <el-button @click="loadServers"><el-icon><Refresh /></el-icon></el-button>
        <el-button type="primary" @click="handleAdd"><el-icon><Plus /></el-icon>新增</el-button>
      </div>
    </div>

    <div class="table-wrapper">
      <el-table :data="servers" v-loading="loading" stripe border>
        <el-table-column type="index" label="序号" width="60" />
        <el-table-column prop="host" label="主机" min-width="150" />
        <el-table-column prop="remark" label="主机名" width="100" />
        <el-table-column prop="port" label="端口" width="70" />
        <el-table-column prop="username" label="用户名" width="90" />
        <el-table-column label="认证方式" width="90">
          <template #default="{ row }">{{ row.authType === 'password' ? '密码' : '证书' }}</template>
        </el-table-column>
        <el-table-column label="最近连接时间" min-width="160">
          <template #default="{ row }">
            <span class="time-text">{{ formatTime(row.lastConnTime) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">{{ row.status === 'online' ? '在线' : row.status === 'offline' ? '离线' : '未知' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="320" fixed="right">
          <template #default="{ row }">
            <div class="action-btns">
              <el-button type="primary" link size="small" @click="handleTerminal(row)">
                <el-icon style="margin-right: 2px;"><Monitor /></el-icon>终端
              </el-button>
              <el-button type="primary" link size="small" @click="handleFiles(row)">
                <el-icon style="margin-right: 2px;"><Folder /></el-icon>文件
              </el-button>
              <el-button type="primary" link size="small" @click="handleMonitor(row)">
                <el-icon style="margin-right: 2px;"><DataLine /></el-icon>监控
              </el-button>
              <el-button type="primary" link size="small" @click="handleLogs(row)">
                <el-icon style="margin-right: 2px;"><Document /></el-icon>日志
              </el-button>
              <el-button type="primary" link size="small" @click="handleEdit(row)">编辑</el-button>
              <el-button type="success" link size="small" @click="handleTest(row)">测试</el-button>
              <el-button type="danger" link size="small" @click="handleDelete(row)">删除</el-button>
            </div>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- Dialog -->
    <el-dialog v-model="dialogVisible" :title="editMode ? '编辑服务器' : '新增'" width="520px" :close-on-click-modal="false">
      <el-form :model="form" label-width="120px">
        <el-form-item label="主机" required>
          <el-input v-model="form.host" placeholder="请输入 主机" />
        </el-form-item>
        <el-form-item label="主机名">
          <el-input v-model="form.remark" placeholder="请输入 主机名" />
        </el-form-item>
        <el-form-item label="端口">
          <el-input-number v-model="form.port" :min="1" :max="65535" placeholder="请输入 端口" />
        </el-form-item>
        <el-form-item label="用户名" required>
          <el-input v-model="form.username" placeholder="请输入 用户名" />
        </el-form-item>
        <el-form-item label="认证方式" required>
          <el-radio-group v-model="form.authType">
            <el-radio value="password">密码</el-radio>
            <el-radio value="certificate">证书</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="密码" v-if="form.authType === 'password'">
          <el-input
            v-model="form.credential"
            type="password"
            show-password
            :placeholder="editMode ? '留空则不修改' : '请输入密码'"
          />
        </el-form-item>
        <template v-if="form.authType === 'certificate'">
          <el-form-item label="证书内容">
            <div style="width: 100%;">
              <div style="margin-bottom: 8px;">
                <el-button size="small" @click="handleReadKey">从服务器读取</el-button>
                <span style="font-size: 12px; color: #909399; margin-left: 8px;">默认读取 /root/.ssh/id_rsa</span>
              </div>
              <el-input
                v-model="form.credential"
                type="textarea"
                :rows="4"
                :placeholder="editMode ? '留空则不修改' : '请输入 证书内容'"
              />
            </div>
          </el-form-item>
          <el-form-item label="证书密码">
            <el-input v-model="form.certPassphrase" type="password" show-password placeholder="请输入 证书密码（无密码可留空）" />
          </el-form-item>
        </template>
        <el-form-item label="连接超时(秒)">
          <el-input-number v-model="form.connTimeout" :min="1" :max="60" placeholder="请输入 连接超时(秒)" />
        </el-form-item>
        <el-form-item label="禁止命令">
          <el-input v-model="form.forbiddenCommands" type="textarea" :rows="2" placeholder="请输入 禁止命令(多个命令用逗号分隔)" />
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
.toolbar-left { display: flex; align-items: center; gap: 10px; }
.toolbar-right { display: flex; align-items: center; gap: 8px; }
.table-wrapper {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  overflow: hidden;
}
.time-text { font-size: 12px; color: #606266; }
.action-btns { display: flex; align-items: center; gap: 4px; }
</style>

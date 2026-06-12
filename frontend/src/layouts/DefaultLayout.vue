<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useSettingsStore } from '../stores/settings'
import { getUnreadCount, listNotificationMsgs, markAllRead, type NotificationMsg } from '../api/notification-msg'
import { formatTime as formatNotifyTime } from '../utils/time'
import request from '../api/request'
import { clearToken } from '../api/request'

const settingsStore = useSettingsStore()
settingsStore.fetchSettings()

const route = useRoute()
const router = useRouter()
const isCollapsed = ref(false)

const activeMenu = computed(() => route.path)

const toggleCollapse = () => {
  isCollapsed.value = !isCollapsed.value
}

const sidebarWidth = computed(() => isCollapsed.value ? '54px' : '210px')

// Notifications
const unreadCount = ref(0)
const notifyDrawerVisible = ref(false)
const notifications = ref<NotificationMsg[]>([])

async function loadUnreadCount() {
  try {
    const data = await getUnreadCount()
    unreadCount.value = data.count
  } catch (_) {}
}

async function loadNotifications() {
  try {
    const data = await listNotificationMsgs(1, 30)
    notifications.value = data.list || []
  } catch (_) {}
}

async function handleMarkAllRead() {
  await markAllRead()
  unreadCount.value = 0
  notifications.value.forEach(n => n.read = true)
}

// Poll unread count every 30s
let notifyTimer: any = null
onMountedHook(() => {
  loadUnreadCount()
  notifyTimer = setInterval(loadUnreadCount, 30000)
})
onUnmounted(() => { if (notifyTimer) clearInterval(notifyTimer) })

// Load notifications when drawer opens
import { watch } from 'vue'
watch(notifyDrawerVisible, (v) => { if (v) loadNotifications() })

// Tab system
interface TabItem {
  path: string
  title: string
}

const tabs = ref<TabItem[]>([{ path: '/dashboard', title: '工作台' }])
const activeTab = computed(() => route.path)

router.afterEach((to) => {
  const title = (to.meta?.title as string) || '页面'
  const exists = tabs.value.find(t => t.path === to.path)
  if (!exists) {
    tabs.value.push({ path: to.path, title })
  }
})

function handleTabClick(path: string) {
  router.push(path)
}

function handleTabClose(path: string) {
  const idx = tabs.value.findIndex(t => t.path === path)
  if (idx < 0) return
  tabs.value.splice(idx, 1)
  if (path === route.path) {
    const next = tabs.value[Math.min(idx, tabs.value.length - 1)]
    if (next) router.push(next.path)
  }
}

// Context menu
const contextMenuVisible = ref(false)
const contextMenuX = ref(0)
const contextMenuY = ref(0)
const contextMenuTab = ref<TabItem | null>(null)

function handleTabContextMenu(e: MouseEvent, tab: TabItem) {
  contextMenuVisible.value = true
  contextMenuX.value = e.clientX
  contextMenuY.value = e.clientY
  contextMenuTab.value = tab
}

function closeContextMenu() {
  contextMenuVisible.value = false
}

function handleContextClose() {
  if (contextMenuTab.value) {
    handleTabClose(contextMenuTab.value.path)
  }
  closeContextMenu()
}

function handleContextCloseOthers() {
  if (contextMenuTab.value) {
    tabs.value = tabs.value.filter(t => t.path === contextMenuTab.value!.path)
    router.push(contextMenuTab.value.path)
  }
  closeContextMenu()
}

function handleContextCloseAll() {
  tabs.value = [{ path: '/dashboard', title: '工作台' }]
  router.push('/dashboard')
  closeContextMenu()
}

// Close context menu on click elsewhere
import { onMounted as onMountedHook, onUnmounted } from 'vue'
onMountedHook(() => {
  document.addEventListener('click', closeContextMenu)
})
onUnmounted(() => {
  document.removeEventListener('click', closeContextMenu)
})

// User center
const profileDialogVisible = ref(false)
const passwordDialogVisible = ref(false)

// Theme
const currentTheme = ref<'light' | 'dark' | 'system'>(
  (localStorage.getItem('df-theme') as any) || 'light'
)

function handleThemeCommand(command: 'light' | 'dark' | 'system') {
  currentTheme.value = command
  localStorage.setItem('df-theme', command)
  applyTheme(command)
}

function applyTheme(theme: 'light' | 'dark' | 'system') {
  let isDark = false
  if (theme === 'dark') {
    isDark = true
  } else if (theme === 'system') {
    isDark = window.matchMedia('(prefers-color-scheme: dark)').matches
  }
  document.documentElement.classList.toggle('dark', isDark)
}

// Apply on mount
applyTheme(currentTheme.value)
const passwordForm = ref({ email: '', code: '', newPassword: '', confirmPassword: '' })
const profileForm = ref({
  username: '测试工程师',
  account: 'admin',
  email: 'test@df-his.com',
  phone: '13800138000',
  department: '测试部',
})
const codeSending = ref(false)
const codeCountdown = ref(0)
let countdownTimer: any = null

function handleUserCommand(command: string) {
  switch (command) {
    case 'profile':
      profileDialogVisible.value = true
      loadProfile()
      break
    case 'password':
      passwordForm.value = { email: '', code: '', newPassword: '', confirmPassword: '' }
      passwordDialogVisible.value = true
      break
    case 'logout':
      ElMessageBox.confirm('确定退出登录？', '提示', { type: 'warning' }).then(async () => {
        try {
          await request.post('/auth/logout')
        } catch (_) {}
        clearToken()
        window.location.href = '/login'
      }).catch(() => {})
      break
  }
}

async function loadProfile() {
  try {
    const data: any = await request.get('/auth/profile')
    profileForm.value = {
      username: data.username || '',
      account: data.account || '',
      email: data.email || '',
      phone: data.phone || '',
      department: data.department || '',
    }
  } catch (_) {}
}

async function handleSaveProfile() {
  if (!profileForm.value.username.trim()) { ElMessage.warning('用户名不能为空'); return }
  if (!profileForm.value.email.trim()) { ElMessage.warning('邮箱不能为空'); return }
  try {
    await request.put('/auth/profile', {
      username: profileForm.value.username,
      email: profileForm.value.email,
      phone: profileForm.value.phone,
      department: profileForm.value.department,
    })
    ElMessage.success('个人信息已更新')
    profileDialogVisible.value = false
  } catch (_) {}
}

async function handleSendCode() {
  if (!passwordForm.value.email.trim()) { ElMessage.warning('请输入邮箱'); return }
  try {
    await request.post('/auth/send-code', { email: passwordForm.value.email })
    codeSending.value = true
    codeCountdown.value = 60
    countdownTimer = setInterval(() => {
      codeCountdown.value--
      if (codeCountdown.value <= 0) {
        clearInterval(countdownTimer)
        codeSending.value = false
      }
    }, 1000)
  } catch (_) {}
}

async function handleSavePassword() {
  if (!passwordForm.value.email) { ElMessage.warning('请输入邮箱'); return }
  if (!passwordForm.value.code) { ElMessage.warning('请输入验证码'); return }
  if (!passwordForm.value.newPassword) { ElMessage.warning('请输入新密码'); return }
  if (passwordForm.value.newPassword.length < 6) { ElMessage.warning('密码长度不能少于6位'); return }
  if (passwordForm.value.newPassword !== passwordForm.value.confirmPassword) { ElMessage.warning('两次密码不一致'); return }
  try {
    await request.post('/auth/change-password', {
      email: passwordForm.value.email,
      code: passwordForm.value.code,
      newPassword: passwordForm.value.newPassword,
    })
    ElMessage.success('密码修改成功')
    passwordDialogVisible.value = false
    if (countdownTimer) clearInterval(countdownTimer)
    codeSending.value = false
    codeCountdown.value = 0
    clearToken()
    window.location.href = '/login'
  } catch (_) {}
}
</script>

<template>
  <div class="layout-wrapper">
    <!-- Sidebar -->
    <aside class="sidebar" :style="{ width: sidebarWidth }">
      <div class="sidebar-logo">
        <img src="/logo.svg" alt="logo" class="logo-img" />
        <transition name="fade">
          <span v-show="!isCollapsed" class="logo-text">DF 构建平台</span>
        </transition>
      </div>

      <el-menu
        :default-active="activeMenu"
        :collapse="isCollapsed"
        :collapse-transition="false"
        class="sidebar-menu"
        router
      >
        <el-menu-item index="/dashboard">
          <el-icon><Monitor /></el-icon>
          <template #title>工作台</template>
        </el-menu-item>

        <el-menu-item index="/servers">
          <el-icon><Platform /></el-icon>
          <template #title>服务器管理</template>
        </el-menu-item>

        <el-sub-menu index="k8s-center">
          <template #title>
            <el-icon><Cloudy /></el-icon>
            <span>Kubernetes</span>
          </template>
          <el-menu-item index="/kubernetes/overview">集群概览</el-menu-item>
          <el-menu-item index="/kubernetes/nodes">Nodes</el-menu-item>
          <el-menu-item index="/kubernetes/deployments">Deployments</el-menu-item>
          <el-menu-item index="/kubernetes/pods">Pods</el-menu-item>
          <el-menu-item index="/kubernetes/services">Services</el-menu-item>
          <el-menu-item index="/kubernetes/configmaps">ConfigMaps</el-menu-item>
          <el-menu-item index="/kubernetes/ingresses">Ingress</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="task-center">
          <template #title>
            <el-icon><VideoPlay /></el-icon>
            <span>任务中心</span>
          </template>
          <el-menu-item v-if="settingsStore.showTaskList" index="/tasks">任务列表</el-menu-item>
          <el-menu-item v-if="settingsStore.showBatchDeploy" index="/batch-deploy">批量部署</el-menu-item>
          <el-menu-item index="/build-queue">构建队列</el-menu-item>
          <el-menu-item index="/release">构建历史</el-menu-item>
        </el-sub-menu>

        <el-menu-item index="/artifacts">
          <el-icon><Box /></el-icon>
          <template #title>制品记录</template>
        </el-menu-item>

        <el-sub-menu index="postgresql-center">
          <template #title>
            <el-icon><Coin /></el-icon>
            <span>PostgreSQL 管理</span>
          </template>
          <el-menu-item index="/postgresql/instances">实例管理</el-menu-item>
          <el-menu-item index="/postgresql/sql-execution">SQL 执行</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="settings-center">
          <template #title>
            <el-icon><Setting /></el-icon>
            <span>系统设置</span>
          </template>
          <el-menu-item index="/settings/general">全局参数</el-menu-item>
          <el-menu-item index="/settings/apps">应用管理</el-menu-item>
          <el-menu-item index="/settings/environment">环境配置</el-menu-item>
          <el-menu-item index="/settings/config-items">配置项管理</el-menu-item>
          <el-menu-item index="/settings/build-configs">编译配置</el-menu-item>
          <el-menu-item index="/settings/notifications">通知配置</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="deployment-center">
          <template #title>
            <el-icon><Cpu /></el-icon>
            <span>部署管理</span>
          </template>
          <el-menu-item index="/deployment/components">组件</el-menu-item>
          <el-menu-item index="/deployment/global-config">全局配置</el-menu-item>
          <el-menu-item index="/deployment/runs">部署运行</el-menu-item>
        </el-sub-menu>
      </el-menu>
    </aside>

    <!-- Main Area -->
    <div class="main-area">
      <!-- Header -->
      <header class="main-header">
        <div class="header-left">
          <div class="collapse-btn" @click="toggleCollapse">
            <el-icon :size="18">
              <Fold v-if="!isCollapsed" />
              <Expand v-else />
            </el-icon>
          </div>
        </div>
        <div class="header-right">
          <el-badge :value="unreadCount" :hidden="unreadCount === 0" :max="99" style="margin-right: 16px;">
            <div class="theme-btn" @click="notifyDrawerVisible = true">
              <el-icon :size="18"><Bell /></el-icon>
            </div>
          </el-badge>
          <el-dropdown trigger="click" @command="handleThemeCommand" style="margin-right: 16px;">
            <div class="theme-btn">
              <el-icon :size="18"><Sunny v-if="currentTheme === 'light'" /><Moon v-else-if="currentTheme === 'dark'" /><Monitor v-else /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="light" :class="{ 'is-active-item': currentTheme === 'light' }">
                  <el-icon><Sunny /></el-icon>浅色
                </el-dropdown-item>
                <el-dropdown-item command="dark" :class="{ 'is-active-item': currentTheme === 'dark' }">
                  <el-icon><Moon /></el-icon>深色
                </el-dropdown-item>
                <el-dropdown-item command="system" :class="{ 'is-active-item': currentTheme === 'system' }">
                  <el-icon><Monitor /></el-icon>跟随系统
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-dropdown trigger="click" @command="handleUserCommand">
            <div class="user-info">
              <el-avatar :size="28" style="background: #409eff; font-size: 11px;">Admin</el-avatar>
              <span class="user-name">系统管理员</span>
              <el-icon :size="12" style="margin-left: 4px;"><ArrowDown /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="profile">
                  <el-icon><User /></el-icon>个人信息
                </el-dropdown-item>
                <el-dropdown-item command="password">
                  <el-icon><Lock /></el-icon>修改密码
                </el-dropdown-item>
                <el-dropdown-item command="logout">
                  <el-icon><SwitchButton /></el-icon>退出登录
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </header>

      <!-- Tabs -->
      <div class="tab-bar">
        <div
          v-for="tab in tabs"
          :key="tab.path"
          class="tab-item"
          :class="{ active: activeTab === tab.path }"
          @click="handleTabClick(tab.path)"
          @contextmenu.prevent="handleTabContextMenu($event, tab)"
        >
          <span class="tab-label">{{ tab.title }}</span>
          <el-icon
            v-if="tabs.length > 1"
            class="tab-close"
            :size="12"
            @click.stop="handleTabClose(tab.path)"
          >
            <Close />
          </el-icon>
        </div>
      </div>

      <!-- Tab Context Menu -->
      <div
        v-if="contextMenuVisible"
        class="context-menu"
        :style="{ left: contextMenuX + 'px', top: contextMenuY + 'px' }"
      >
        <div class="context-menu-item" @click="handleContextClose">关闭当前</div>
        <div class="context-menu-item" @click="handleContextCloseOthers">关闭其他</div>
        <div class="context-menu-item" @click="handleContextCloseAll">关闭所有</div>
      </div>

      <!-- Content -->
      <main class="main-content">
        <router-view />
      </main>
    </div>

    <!-- Profile Dialog -->
    <el-dialog v-model="profileDialogVisible" title="个人信息" width="480px" :close-on-click-modal="false">
      <el-form :model="profileForm" label-width="80px">
        <el-form-item label="用户名" required>
          <el-input v-model="profileForm.username" placeholder="请输入用户名" />
        </el-form-item>
        <el-form-item label="账号">
          <el-input v-model="profileForm.account" disabled />
          <div class="form-hint">账号不可修改</div>
        </el-form-item>
        <el-form-item label="邮箱" required>
          <el-input v-model="profileForm.email" placeholder="请输入邮箱" />
        </el-form-item>
        <el-form-item label="手机号">
          <el-input v-model="profileForm.phone" placeholder="请输入手机号" />
        </el-form-item>
        <el-form-item label="部门">
          <el-input v-model="profileForm.department" placeholder="请输入部门" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="profileDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSaveProfile">保存修改</el-button>
      </template>
    </el-dialog>

    <!-- Password Dialog -->
    <el-dialog v-model="passwordDialogVisible" title="修改密码" width="460px" :close-on-click-modal="false">
      <el-form :model="passwordForm" label-width="80px">
        <el-form-item label="邮箱" required>
          <el-input v-model="passwordForm.email" placeholder="接收验证码的邮箱" />
        </el-form-item>
        <el-form-item label="验证码" required>
          <div style="display: flex; gap: 8px; width: 100%;">
            <el-input v-model="passwordForm.code" placeholder="请输入验证码" style="flex: 1;" />
            <el-button
              :disabled="codeSending"
              @click="handleSendCode"
              style="width: 120px;"
            >
              {{ codeSending ? `${codeCountdown}s 后重发` : '发送验证码' }}
            </el-button>
          </div>
        </el-form-item>
        <el-form-item label="新密码" required>
          <el-input v-model="passwordForm.newPassword" type="password" show-password placeholder="请输入新密码（至少6位）" />
        </el-form-item>
        <el-form-item label="确认密码" required>
          <el-input v-model="passwordForm.confirmPassword" type="password" show-password placeholder="再次输入新密码" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="passwordDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSavePassword">确认修改</el-button>
      </template>
    </el-dialog>

    <!-- Notification Drawer -->
    <el-drawer v-model="notifyDrawerVisible" title="通知中心" direction="rtl" size="380px">
      <template #header>
        <div style="display: flex; align-items: center; justify-content: space-between; width: 100%;">
          <span style="font-size: 16px; font-weight: 600;">通知中心</span>
          <el-button size="small" link @click="handleMarkAllRead">全部已读</el-button>
        </div>
      </template>
      <div v-if="notifications.length === 0" style="text-align: center; padding: 40px; color: #909399;">暂无通知</div>
      <div v-for="n in notifications" :key="n.id" class="notify-item" :class="{ unread: !n.read }">
        <div class="notify-title">
          <el-icon v-if="n.level === 'success'" color="#67c23a"><CircleCheck /></el-icon>
          <el-icon v-else-if="n.level === 'error'" color="#f56c6c"><CircleClose /></el-icon>
          <el-icon v-else color="#409eff"><InfoFilled /></el-icon>
          <span>{{ n.title }}</span>
        </div>
        <div class="notify-content">{{ n.content }}</div>
        <div class="notify-time">{{ formatNotifyTime(n.createdAt) }}</div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.layout-wrapper {
  display: flex;
  height: 100vh;
  overflow: hidden;
}

/* Sidebar */
.sidebar {
  background: #fff;
  border-right: 1px solid #f0f0f0;
  display: flex;
  flex-direction: column;
  transition: width 0.28s ease;
  overflow: hidden;
  flex-shrink: 0;
}

.sidebar-logo {
  height: 48px;
  display: flex;
  align-items: center;
  padding: 0 16px;
  border-bottom: 1px solid #f0f0f0;
  overflow: hidden;
}

.logo-img {
  width: 22px;
  height: 22px;
  flex-shrink: 0;
}

.logo-text {
  font-size: 14px;
  font-weight: 700;
  color: #303133;
  margin-left: 10px;
  white-space: nowrap;
}

.sidebar-menu {
  flex: 1;
  border-right: none !important;
  overflow-y: auto;
  overflow-x: hidden;
}

:deep(.el-menu) {
  border-right: none;
}

:deep(.el-menu-item),
:deep(.el-sub-menu__title) {
  height: 44px;
  line-height: 44px;
  font-size: 13px;
  color: #606266;
}

:deep(.el-menu-item.is-active) {
  color: #409eff;
  background: #ecf5ff;
  border-right: 3px solid #409eff;
}

:deep(.el-menu-item:hover),
:deep(.el-sub-menu__title:hover) {
  background: #f5f7fa;
}

:deep(.el-sub-menu .el-menu-item) {
  padding-left: 52px !important;
  height: 40px;
  line-height: 40px;
}

/* Main Area */
.main-area {
  flex: 1;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: #f0f2f5;
}

/* Header */
.main-header {
  height: 48px;
  background: #fff;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 20px;
  border-bottom: 1px solid #f0f0f0;
  flex-shrink: 0;
}

.header-left {
  display: flex;
  align-items: center;
}

.collapse-btn {
  cursor: pointer;
  padding: 6px;
  border-radius: 4px;
  color: #606266;
  transition: all 0.2s;
}

.collapse-btn:hover {
  background: #f5f7fa;
  color: #409eff;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 0;
}

.theme-btn {
  cursor: pointer;
  padding: 6px;
  border-radius: 4px;
  color: #606266;
  transition: all 0.2s;
  display: flex;
  align-items: center;
}

.theme-btn:hover {
  background: #f5f7fa;
  color: #409eff;
}

.header-badge {
  cursor: pointer;
}

.user-info {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
}

.user-name {
  font-size: 13px;
  color: #606266;
}

/* Tab Bar */
.tab-bar {
  height: 34px;
  background: #fff;
  display: flex;
  align-items: center;
  padding: 0 12px;
  gap: 4px;
  border-bottom: 1px solid #f0f0f0;
  overflow-x: auto;
  flex-shrink: 0;
}

.tab-bar::-webkit-scrollbar {
  height: 0;
}

.tab-item {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 12px;
  border-radius: 4px;
  font-size: 12px;
  color: #606266;
  cursor: pointer;
  white-space: nowrap;
  transition: all 0.2s;
  border: 1px solid transparent;
}

.tab-item:hover {
  color: #409eff;
  background: #ecf5ff;
}

.tab-item.active {
  color: #409eff;
  background: #ecf5ff;
  border-color: #d9ecff;
}

.tab-close {
  border-radius: 50%;
  padding: 1px;
  transition: all 0.2s;
}

.tab-close:hover {
  background: #409eff;
  color: #fff;
}

/* Content */
.main-content {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
}

/* Transitions */
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.2s;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

.form-hint {
  font-size: 12px;
  color: #909399;
  margin-top: 2px;
}

/* Context Menu */
.context-menu {
  position: fixed;
  z-index: 9999;
  background: #fff;
  border: 1px solid #ebeef5;
  border-radius: 4px;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.1);
  padding: 4px 0;
  min-width: 120px;
}

.context-menu-item {
  padding: 8px 16px;
  font-size: 13px;
  color: #606266;
  cursor: pointer;
  transition: all 0.15s;
}

.context-menu-item:hover {
  background: #ecf5ff;
  color: #409eff;
}

html.dark .context-menu {
  background: #242424;
  border-color: #3a3a3a;
  box-shadow: 0 2px 12px rgba(0, 0, 0, 0.4);
}

html.dark .context-menu-item {
  color: #ffffffb3;
}

html.dark .context-menu-item:hover {
  background: #1a2c42;
  color: #409eff;
}

.notify-item {
  padding: 12px 0;
  border-bottom: 1px solid #f0f0f0;
}
.notify-item.unread {
  background: #f0f9ff;
  margin: 0 -20px;
  padding: 12px 20px;
}
.notify-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 500;
  color: #303133;
}
.notify-content {
  font-size: 12px;
  color: #606266;
  margin-top: 4px;
  padding-left: 22px;
}
.notify-time {
  font-size: 11px;
  color: #c0c4cc;
  margin-top: 4px;
  padding-left: 22px;
}
</style>

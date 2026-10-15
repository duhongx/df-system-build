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
const isCollapsed = ref(window.innerWidth < 1200)
const pageSection = computed(() => {
  if (/^\/(servers|kubernetes)/.test(route.path)) return '运维管理'
  if (route.path.startsWith('/postgresql')) return '数据库管理'
  if (route.path.startsWith('/settings')) return '平台配置'
  if (route.path.startsWith('/deployment')) return '基础设施'
  return '构建与交付'
})

const activeMenu = computed(() => {
  if (route.path.startsWith('/artifacts/versions/')) return '/artifacts/versions'
  return route.path
})

const toggleCollapse = () => {
  isCollapsed.value = !isCollapsed.value
}

const sidebarWidth = computed(() => isCollapsed.value ? '72px' : '224px')

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
if (route.path !== '/dashboard') tabs.value.push({ path: route.path, title: String(route.meta.title || '页面') })
const activeTab = computed(() => route.path)

const removeTabHook = router.afterEach((to) => {
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
  removeTabHook()
})

// User center
const profileDialogVisible = ref(false)

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
const profileLoading = ref(false)
const profileLoaded = ref(false)
const profileForm = ref({
  username: '',
  email: '',
  phone: '',
  department: '',
})

function handleUserCommand(command: string) {
  switch (command) {
    case 'profile':
      profileDialogVisible.value = true
      loadProfile()
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
  profileLoading.value = true
  profileLoaded.value = false
  try {
    const data: any = await request.get('/auth/profile')
    profileForm.value = {
      username: data.username || '',
      email: data.email || '',
      phone: data.phone || '',
      department: data.department || '',
    }
    profileLoaded.value = true
  } catch (_) {}
  finally { profileLoading.value = false }
}

async function handleSaveProfile() {
  if (!profileLoaded.value || profileLoading.value) return
  if (!profileForm.value.username.trim()) { ElMessage.warning('用户名不能为空'); return }
  try {
    await request.put('/auth/profile', {
      email: profileForm.value.email,
      phone: profileForm.value.phone,
      department: profileForm.value.department,
    })
    ElMessage.success('个人信息已更新')
    profileDialogVisible.value = false
  } catch (_) {}
}

</script>

<template>
  <div class="layout-wrapper" :class="{ 'nav-collapsed': isCollapsed }">
    <!-- Sidebar -->
    <aside class="sidebar" :style="{ width: sidebarWidth }">
      <div class="sidebar-logo" :class="{ collapsed: isCollapsed }">
        <img src="/logo.svg" alt="" class="logo-icon" />
        <div v-if="!isCollapsed" class="brand-wordmark"><strong>CloudHIS</strong></div>
      </div>

      <el-menu
        :default-active="activeMenu"
        :collapse="isCollapsed"
        :collapse-transition="false"
        class="sidebar-menu"
        router
      >
        <li v-if="!isCollapsed" class="nav-section" role="presentation">工作空间</li>
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

        <li v-if="!isCollapsed" class="nav-section" role="presentation">构建与数据</li>
        <el-sub-menu index="task-center">
          <template #title>
            <el-icon><VideoPlay /></el-icon>
            <span>任务中心</span>
          </template>
          <el-menu-item v-if="settingsStore.showTaskList" index="/tasks">任务列表</el-menu-item>
          <el-menu-item index="/batch-deploy">新增更新任务</el-menu-item>
          <el-menu-item index="/build-queue">构建队列</el-menu-item>
          <el-menu-item index="/release">构建历史</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="artifact-center">
          <template #title>
            <el-icon><Box /></el-icon>
            <span>制品管理</span>
          </template>
          <el-menu-item index="/artifacts/versions">更新版本</el-menu-item>
          <el-menu-item index="/artifacts/latest">制品库</el-menu-item>
        </el-sub-menu>

        <el-sub-menu index="postgresql-center">
          <template #title>
            <el-icon><Coin /></el-icon>
            <span>PostgreSQL 管理</span>
          </template>
          <el-menu-item index="/postgresql/instances">实例管理</el-menu-item>
          <el-menu-item index="/postgresql/sql-execution">SQL 执行</el-menu-item>
          <el-menu-item index="/postgresql/view-dependency-change">视图依赖变更</el-menu-item>
        </el-sub-menu>

        <li v-if="!isCollapsed" class="nav-section" role="presentation">平台管理</li>
        <el-sub-menu index="settings-center">
          <template #title>
            <el-icon><Setting /></el-icon>
            <span>系统设置</span>
          </template>
          <el-menu-item index="/settings/general">全局参数</el-menu-item>
          <el-menu-item index="/settings/apps">应用管理</el-menu-item>
          <el-menu-item index="/settings/environment">环境配置</el-menu-item>
          <el-menu-item index="/settings/config-items">配置项管理</el-menu-item>
          <el-menu-item v-if="settingsStore.showTaskList" index="/settings/build-configs">编译配置</el-menu-item>
          <el-menu-item index="/settings/sql-execution">SQL执行配置</el-menu-item>
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
          <button class="collapse-btn" type="button" :aria-label="isCollapsed ? '展开导航' : '收起导航'" @click="toggleCollapse">
            <el-icon :size="18">
              <Fold v-if="!isCollapsed" />
              <Expand v-else />
            </el-icon>
          </button>
          <div class="header-breadcrumb"><span>{{ pageSection }}</span><span class="breadcrumb-slash">/</span><strong>{{ route.meta.title }}</strong></div>
        </div>
        <div class="header-right">
          <el-badge :value="unreadCount" :hidden="unreadCount === 0" :max="99" style="margin-right: 16px;">
            <button class="theme-btn" type="button" aria-label="通知中心" @click="notifyDrawerVisible = true">
              <el-icon :size="18"><Bell /></el-icon>
            </button>
          </el-badge>
          <el-dropdown trigger="click" @command="handleThemeCommand" style="margin-right: 16px;">
            <button class="theme-btn" type="button" aria-label="切换主题">
              <el-icon :size="18"><Sunny v-if="currentTheme === 'light'" /><Moon v-else-if="currentTheme === 'dark'" /><Monitor v-else /></el-icon>
            </button>
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
              <el-avatar :size="32" class="admin-avatar">A</el-avatar>
              <span class="user-name">系统管理员</span>
              <el-icon :size="12" style="margin-left: 4px;"><ArrowDown /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="profile">
                  <el-icon><User /></el-icon>个人信息
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
          role="tab"
          tabindex="0"
          :aria-selected="activeTab === tab.path"
          :class="{ active: activeTab === tab.path }"
          @click="handleTabClick(tab.path)"
          @keydown.enter="handleTabClick(tab.path)"
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
      <main class="main-content" :class="{ 'main-content--fill': route.meta.fillViewport }">
        <router-view :key="route.fullPath" />
      </main>
    </div>

    <!-- Profile Dialog -->
    <el-dialog v-model="profileDialogVisible" title="个人信息" width="480px" :close-on-click-modal="false">
      <el-form :model="profileForm" label-width="80px" v-loading="profileLoading">
        <el-form-item label="用户名">
          <el-input v-model="profileForm.username" readonly />
        </el-form-item>
        <el-form-item label="邮箱">
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
        <el-button type="primary" :disabled="!profileLoaded || profileLoading" @click="handleSaveProfile">保存修改</el-button>
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
.layout-wrapper { display: flex; height: 100vh; height: 100dvh; overflow: hidden; }
.sidebar { background: var(--ui-panel); border-right: 1px solid var(--ui-border); display: flex; flex-direction: column; transition: width .22s ease; overflow: hidden; flex-shrink: 0; }
.sidebar-logo { height: 84px; display: flex; align-items: center; gap: 10px; padding: 0 24px; overflow: hidden; flex-shrink: 0; }
.sidebar-logo.collapsed { padding: 0; justify-content: center; }
.logo-icon { width: 33px; height: 33px; flex-shrink: 0; }
.brand-wordmark { white-space: nowrap; }.brand-wordmark strong { display: block; font-size: 22px; letter-spacing: -.9px; color: var(--ui-heading); font-weight: 700; line-height: 1.2; }
.brand-wordmark strong span { color: var(--ui-cyan); margin-left: 2px; }
.sidebar-menu { flex: 1; border-right: none; overflow-y: auto; overflow-x: hidden; padding: 0 12px 12px; background: transparent; }
.nav-section { font-size: 10px; color: var(--ui-muted); letter-spacing: 1.6px; padding: 22px 14px 10px; list-style: none; }
.nav-section:first-child { padding-top: 12px; }
:deep(.el-menu) { border-right: none; background: transparent; }
:deep(.el-menu-item), :deep(.el-sub-menu__title) { height: 43px; line-height: 43px; border-radius: 7px; margin-bottom: 5px; color: var(--ui-text); font-size: 13px; padding-left: 14px !important; }
:deep(.el-menu-item .el-icon), :deep(.el-sub-menu__title .el-icon) { font-size: 17px; margin-right: 11px; color: var(--ui-muted); }
:deep(.el-menu-item.is-active) { background: var(--ui-tint); color: var(--ui-primary); font-weight: 600; position: relative; }
:deep(.el-menu-item.is-active)::before { content: ''; position: absolute; height: 17px; width: 3px; left: 0; border-radius: 0 3px 3px 0; background: var(--ui-cyan); }
:deep(.el-menu-item.is-active .el-icon) { color: var(--ui-primary); }
:deep(.el-menu-item:hover), :deep(.el-sub-menu__title:hover) { background: var(--ui-panel-soft); color: var(--ui-primary); }
:deep(.el-sub-menu .el-menu-item) { padding-left: 43px !important; height: 36px; line-height: 36px; font-size: 12px; }
:deep(.el-menu--collapse) { width: 71px; padding-inline: 10px; }
:deep(.el-menu--collapse .el-sub-menu__title), :deep(.el-menu--collapse .el-menu-item) { padding-inline: 16px !important; }
.main-area { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; background: var(--ui-bg); }
.main-header { height: 66px; background: var(--ui-panel); display: flex; align-items: center; justify-content: space-between; padding: 0 28px; border-bottom: 1px solid var(--ui-border); flex-shrink: 0; }
.header-left, .header-right { display: flex; align-items: center; }.header-left { gap: 22px; min-width: 0; }.header-right { flex-shrink: 0; }
.collapse-btn, .theme-btn { border: 0; background: transparent; display: inline-flex; align-items: center; justify-content: center; width: 32px; height: 32px; color: var(--ui-muted); border-radius: 7px; cursor: pointer; transition: background .15s, color .15s; }
.collapse-btn:hover, .theme-btn:hover { background: var(--ui-tint); color: var(--ui-primary); }
.header-breadcrumb { display: flex; align-items: center; gap: 12px; font-size: 12px; color: var(--ui-muted); min-width: 0; }.header-breadcrumb strong { color: var(--ui-heading); font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.breadcrumb-slash { color: var(--el-border-color); }
.user-info { display: flex; align-items: center; gap: 10px; cursor: pointer; padding-left: 18px; border-left: 1px solid var(--ui-border); }
.admin-avatar { background: var(--ui-tint); color: var(--ui-primary); font-size: 13px; font-weight: 650; border: 1px solid var(--el-color-primary-light-8); }.user-name { font-size: 12px; color: var(--ui-text); }
.tab-bar { min-height: 43px; background: var(--ui-panel); display: flex; align-items: center; padding: 0 28px; gap: 5px; border-bottom: 1px solid var(--ui-border); overflow-x: auto; flex-shrink: 0; }.tab-bar::-webkit-scrollbar { height: 0; }
.tab-item { display: flex; align-items: center; gap: 14px; height: 31px; padding: 0 13px; border-radius: 5px; font-size: 11px; color: var(--ui-muted); cursor: pointer; white-space: nowrap; transition: background .15s; border: 1px solid transparent; }
.tab-item.active { color: var(--ui-primary); background: var(--ui-tint); border-color: var(--el-color-primary-light-8); }.tab-item:hover { color: var(--ui-primary); background: var(--ui-panel-soft); }
.tab-close { border-radius: 3px; }.tab-close:hover { background: var(--el-color-primary-light-8); }
.main-content { flex: 1; min-width: 0; overflow: auto; padding: 28px 32px 36px; scrollbar-gutter: stable; }
.main-content--fill { display: flex; flex-direction: column; min-height: 0; overflow: hidden; }
.context-menu { position: fixed; z-index: 9999; background: var(--ui-panel); border: 1px solid var(--ui-border); border-radius: 9px; box-shadow: 0 8px 28px rgb(15 35 67 / 12%); padding: 6px; min-width: 140px; }
.context-menu-item { padding: 8px 12px; font-size: 12px; color: var(--ui-text); cursor: pointer; border-radius: 5px; }.context-menu-item:hover { background: var(--ui-tint); color: var(--ui-primary); }
.notify-item { padding: 16px 0; border-bottom: 1px solid var(--ui-border); }.notify-item.unread { background: var(--ui-tint); margin: 0 -20px; padding-inline: 20px; }
.notify-title { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 550; color: var(--ui-heading); }.notify-content { font-size: 12px; color: var(--ui-text); margin-top: 8px; padding-left: 24px; }.notify-time { font-size: 11px; color: var(--ui-muted); margin-top: 8px; padding-left: 24px; }
@media (max-width: 1100px) { .main-content { padding: 23px 22px; }.main-header { padding-inline: 20px; }.tab-bar { padding-inline: 20px; } }
@media (max-width: 700px) { .main-content { padding: 18px 14px; }.main-header { padding-inline: 12px; height: 58px; }.user-name { display: none; }.header-breadcrumb > span:first-child, .breadcrumb-slash { display: none; }.header-left { gap: 8px; }.header-right :deep(.el-badge), .header-right :deep(.el-dropdown) { margin-right: 6px !important; }.user-info { padding-left: 8px; }.tab-bar { padding-inline: 12px; } }
</style>

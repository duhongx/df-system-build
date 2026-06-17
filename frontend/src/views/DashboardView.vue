<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getDashboardStats, getRecentBuilds, type Pipeline } from '../api/pipeline'

const router = useRouter()
const stats = ref<any>({})
const recentTasks = ref<Pipeline[]>([])
const loading = ref(true)

onMounted(async () => {
  loading.value = true
  try {
    const [s, tasks] = await Promise.all([getDashboardStats(), getRecentBuilds()])
    stats.value = s
    recentTasks.value = tasks || []
  } catch (e) {
    stats.value = {}
    recentTasks.value = []
  } finally {
    loading.value = false
  }
})

function statusType(status: string) {
  const map: Record<string, string> = {
    SUCCESS: 'success', FAILED: 'danger', RUNNING: 'primary',
    PENDING: 'info', CANCELED: 'warning',
  }
  return map[status] || 'info'
}

function statusLabel(status: string) {
  const map: Record<string, string> = {
    SUCCESS: '成功', FAILED: '失败', RUNNING: '运行中',
    PENDING: '排队中', CANCELED: '已取消',
  }
  return map[status] || status
}

function goToDetail(task: Pipeline) {
  router.push(`/release/${task.id}`)
}

const quickActions = [
  { icon: 'VideoPlay', label: '新建发布', path: '/tasks', color: '#409eff' },
  { icon: 'Grid', label: '应用管理', path: '/apps', color: '#67c23a' },
  { icon: 'Files', label: '批量发布', path: '/tasks', color: '#e6a23c' },
  { icon: 'Connection', label: '远程同步', path: '/remote-sync', color: '#909399' },
  { icon: 'Box', label: '远程打包', path: '/remote-package', color: '#f56c6c' },
  { icon: 'Setting', label: '系统设置', path: '/settings', color: '#909399' },
]
</script>

<template>
  <div class="dashboard" v-loading="loading">
    <!-- Welcome Banner -->
    <div class="welcome-banner">
      <div class="welcome-left">
        <h2 class="welcome-title">欢迎回来，开始今天的构建任务</h2>
        <p class="welcome-desc">CloudHIS智能管理平台 · 当前共 {{ stats.totalApps || 0 }} 个注册应用，今日已构建 {{ stats.todayReleases || 0 }} 次</p>
      </div>
    </div>

    <!-- Stat Cards -->
    <div class="stat-cards">
      <div class="stat-card">
        <div class="stat-card-inner">
          <div class="stat-label">注册应用</div>
          <div class="stat-value">{{ stats.totalApps || 0 }}</div>
          <div class="stat-footer">
            <span class="stat-trend up">+2</span>
            <span class="stat-footer-text">较上周</span>
          </div>
        </div>
        <div class="stat-icon-box" style="background: #ecf5ff;">
          <el-icon :size="24" color="#409eff"><Grid /></el-icon>
        </div>
      </div>

      <div class="stat-card">
        <div class="stat-card-inner">
          <div class="stat-label">今日构建</div>
          <div class="stat-value">{{ stats.todayReleases || 0 }}</div>
          <div class="stat-footer">
            <span class="stat-trend up">+3</span>
            <span class="stat-footer-text">较昨日</span>
          </div>
        </div>
        <div class="stat-icon-box" style="background: #f0f9eb;">
          <el-icon :size="24" color="#67c23a"><VideoPlay /></el-icon>
        </div>
      </div>

      <div class="stat-card">
        <div class="stat-card-inner">
          <div class="stat-label">构建成功</div>
          <div class="stat-value success-text">{{ stats.successCount || 0 }}</div>
          <div class="stat-footer">
            <span class="stat-footer-text">成功率 {{ stats.totalReleases ? Math.round(stats.successCount / stats.totalReleases * 100) : 67 }}%</span>
          </div>
        </div>
        <div class="stat-icon-box" style="background: #f0f9eb;">
          <el-icon :size="24" color="#67c23a"><CircleCheck /></el-icon>
        </div>
      </div>

      <div class="stat-card">
        <div class="stat-card-inner">
          <div class="stat-label">构建失败</div>
          <div class="stat-value danger-text">{{ stats.failedCount || 0 }}</div>
          <div class="stat-footer">
            <span class="stat-trend down">-1</span>
            <span class="stat-footer-text">较昨日</span>
          </div>
        </div>
        <div class="stat-icon-box" style="background: #fef0f0;">
          <el-icon :size="24" color="#f56c6c"><CircleClose /></el-icon>
        </div>
      </div>
    </div>

    <!-- Main Content Row -->
    <div class="content-row">
      <!-- Recent Tasks -->
      <div class="content-left">
        <div class="section-card">
          <div class="section-header">
            <span class="section-title">最近构建</span>
            <el-button type="primary" link size="small" @click="router.push('/release')">查看全部 →</el-button>
          </div>
          <div class="task-list">
            <div
              v-for="task in recentTasks"
              :key="task.id"
              class="task-item"
              @click="goToDetail(task)"
            >
              <div class="task-item-left">
                <el-tag :type="statusType(task.status)" size="small" effect="dark" class="task-status-tag">
                  {{ statusLabel(task.status) }}
                </el-tag>
                <div class="task-item-info">
                  <span class="task-app-name">{{ task.appName }}</span>
                  <span class="task-branch">{{ task.gitBranch }}</span>
                </div>
              </div>
              <div class="task-item-right">
                <span class="task-duration" v-if="task.durationSeconds">{{ task.durationSeconds }}s</span>
                <span class="task-time">{{ task.createdAt?.slice(11, 16) }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- Quick Actions -->
      <div class="content-right">
        <div class="section-card">
          <div class="section-header">
            <span class="section-title">快捷入口</span>
          </div>
          <div class="quick-actions">
            <div
              v-for="action in quickActions"
              :key="action.path"
              class="quick-action-item"
              @click="router.push(action.path)"
            >
              <div class="action-icon" :style="{ background: action.color + '15', color: action.color }">
                <el-icon :size="20"><component :is="action.icon" /></el-icon>
              </div>
              <span class="action-label">{{ action.label }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dashboard {
  /* no extra padding, main-content already has 16px */
}

/* Welcome Banner */
.welcome-banner {
  background: linear-gradient(135deg, #409eff 0%, #66b1ff 100%);
  border-radius: 6px;
  padding: 24px 28px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.welcome-title {
  font-size: 18px;
  font-weight: 600;
  color: #fff;
  margin-bottom: 6px;
}

.welcome-desc {
  font-size: 13px;
  color: rgba(255, 255, 255, 0.85);
}

.welcome-banner .el-button {
  background: rgba(255, 255, 255, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.4);
  color: #fff;
}

.welcome-banner .el-button:hover {
  background: rgba(255, 255, 255, 0.35);
}

/* Stat Cards */
.stat-cards {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
  margin-bottom: 16px;
}

.stat-card {
  background: #fff;
  border-radius: 6px;
  padding: 20px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border: 1px solid #ebeef5;
}

.stat-card-inner {
  flex: 1;
}

.stat-label {
  font-size: 13px;
  color: #909399;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 26px;
  font-weight: 700;
  color: #303133;
  line-height: 1;
  margin-bottom: 8px;
}

.stat-value.success-text { color: #67c23a; }
.stat-value.danger-text { color: #f56c6c; }

.stat-footer {
  font-size: 12px;
  color: #909399;
}

.stat-trend {
  font-weight: 600;
  margin-right: 4px;
}

.stat-trend.up { color: #67c23a; }
.stat-trend.down { color: #f56c6c; }

.stat-icon-box {
  width: 48px;
  height: 48px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

/* Content Row */
.content-row {
  display: grid;
  grid-template-columns: 1fr 320px;
  gap: 16px;
  align-items: stretch;
}

.content-left,
.content-right {
  display: flex;
}

.content-left .section-card,
.content-right .section-card {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.section-card {
  background: #fff;
  border-radius: 6px;
  border: 1px solid #ebeef5;
  overflow: hidden;
}

.section-header {
  padding: 14px 20px;
  border-bottom: 1px solid #f0f0f0;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
}

/* Task List */
.task-list {
  padding: 8px 0;
  flex: 1;
}

.task-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 20px;
  cursor: pointer;
  transition: background 0.15s;
}

.task-item:hover {
  background: #f5f7fa;
}

.task-item-left {
  display: flex;
  align-items: center;
  gap: 12px;
}

.task-status-tag {
  min-width: 52px;
  text-align: center;
}

.task-item-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.task-app-name {
  font-size: 13px;
  font-weight: 500;
  color: #303133;
}

.task-branch {
  font-size: 12px;
  color: #909399;
}

.task-item-right {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
  color: #909399;
}

.task-duration {
  font-weight: 500;
  color: #606266;
}

/* Quick Actions */
.quick-actions {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
  padding: 16px;
  flex: 1;
  align-content: center;
}

.quick-action-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 14px 8px;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
}

.quick-action-item:hover {
  background: #f5f7fa;
  transform: translateY(-1px);
}

.action-icon {
  width: 40px;
  height: 40px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.action-label {
  font-size: 12px;
  color: #606266;
}
</style>

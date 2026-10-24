<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getDashboardStats, getRecentBuilds, type Pipeline, type DashboardStats } from '../api/pipeline'

const router = useRouter()
const stats = ref<Partial<DashboardStats>>({})
const recentTasks = ref<Pipeline[]>([])
const loading = ref(true)
const loadError = ref('')
async function refresh() {
  loading.value = true; loadError.value = ''
  try {
    const [summary, tasks] = await Promise.all([getDashboardStats(), getRecentBuilds()])
    stats.value = summary; recentTasks.value = tasks || []
  } catch { loadError.value = '暂时无法获取工作台数据，请稍后重试。' }
  finally { loading.value = false }
}
onMounted(refresh)
const today = new Intl.DateTimeFormat('zh-CN', { month: 'long', day: 'numeric', weekday: 'long' }).format(new Date())
const metrics = computed(() => [
  { label: '注册应用', value: stats.value.totalApps, icon: 'Grid', color: 'blue' },
  { label: '今日构建', value: stats.value.todayBuilds, icon: 'VideoPlay', color: 'cyan' },
  { label: '今日成功', value: stats.value.successCount, icon: 'CircleCheck', color: 'green' },
  { label: '今日失败', value: stats.value.failedCount, icon: 'CircleClose', color: 'rose' },
])
const quickActions = computed(() => [
  { icon: 'VideoPlay', label: '新增更新任务', path: '/batch-deploy', color: 'blue' },
  { icon: 'Box', label: '更新记录', path: '/artifacts/versions', color: 'cyan' },
  { icon: 'Coin', label: 'SQL 执行', path: '/postgresql/sql-execution', color: 'green' },
  { icon: 'Platform', label: '服务器管理', path: '/servers', color: 'blue' },
  { icon: 'Cloudy', label: 'Kubernetes', path: '/kubernetes/overview', color: 'cyan' },
])
const statusLabels: Record<string, string> = { SUCCESS: '成功', FAILED: '失败', RUNNING: '运行中', PENDING: '排队中', CANCELED: '已取消', IMAGE_READY: '镜像制作完成', DEPLOYING: '滚动更新排队中' }
function statusType(status: string): 'success' | 'danger' | 'primary' | 'warning' | 'info' {
  return status === 'SUCCESS' ? 'success' : status === 'FAILED' ? 'danger' : status === 'RUNNING' ? 'primary' : status === 'CANCELED' ? 'warning' : 'info'
}
function time(value: string) { return value ? new Date(value).toLocaleString('zh-CN', { hour12: false }) : '—' }
function openBuild(row: Pipeline) { router.push(`/release/${row.id}`) }
</script>

<template>
  <div class="dashboard" v-loading="loading">
    <header class="dashboard-heading"><h1 class="console-title">工作台</h1><div class="dashboard-date"><span>{{ today }}</span><el-button @click="refresh" aria-label="刷新工作台"><el-icon><Refresh /></el-icon></el-button></div></header>
    <el-alert v-if="loadError" :title="loadError" type="error" :closable="false" class="load-error" />
    <div class="stat-cards">
      <div class="console-panel metric-card" v-for="metric in metrics" :key="metric.label"><div class="metric-top"><span>{{ metric.label }}</span><span class="metric-icon" :class="metric.color"><el-icon><component :is="metric.icon" /></el-icon></span></div><strong>{{ loadError ? '—' : (metric.value ?? '—') }}</strong></div>
    </div>
    <section class="console-panel actions-panel"><div class="section-heading"><div><h2>常用操作</h2></div></div><div class="quick-actions"><button v-for="action in quickActions" :key="action.path" class="quick-action" @click="router.push(action.path)"><span class="action-icon" :class="action.color"><el-icon><component :is="action.icon" /></el-icon></span><span class="action-copy"><strong>{{ action.label }}</strong></span><span class="action-arrow">↗</span></button></div></section>
    <section class="console-panel recent-panel"><div class="section-heading"><div><h2>最近构建</h2></div><el-button link type="primary" @click="router.push('/release')">查看全部记录 <span class="link-arrow">→</span></el-button></div>
      <el-table :data="recentTasks" empty-text="暂无构建记录" @row-click="openBuild" class="recent-table">
        <el-table-column label="应用" min-width="200"><template #default="{ row }"><div class="build-app"><span class="build-icon"><Box /></span><strong>{{ row.appName || '未命名应用' }}</strong></div></template></el-table-column>
        <el-table-column label="分支" min-width="130"><template #default="{ row }"><span class="branch-label console-code">{{ row.gitBranch || '—' }}</span></template></el-table-column>
        <el-table-column label="状态" width="120"><template #default="{ row }"><el-tag :type="statusType(row.status)" size="small">{{ statusLabels[row.status] || row.status }}</el-tag></template></el-table-column>
        <el-table-column label="耗时" width="110"><template #default="{ row }"><span class="console-code">{{ row.durationSeconds == null ? '—' : `${row.durationSeconds}s` }}</span></template></el-table-column>
        <el-table-column label="提交时间" min-width="190"><template #default="{ row }">{{ time(row.createdAt) }}</template></el-table-column>
        <el-table-column width="80"><template #default="{ row }"><el-button link type="primary" @click.stop="router.push(`/release/${row.id}`)">详情</el-button></template></el-table-column>
      </el-table>
    </section>
  </div>
</template>

<style scoped>
.dashboard { max-width: 1700px; margin-inline: auto; }
.dashboard-heading { display: flex; align-items: center; justify-content: space-between; padding: 2px 0 29px; gap: 24px; position: relative; }
.dashboard-heading::after { content: ''; position: absolute; right: 150px; top: 8px; width: 200px; height: 76px; background-image: radial-gradient(var(--ui-border) 1px, transparent 1px); background-size: 11px 11px; opacity: .75; pointer-events: none; mask-image: linear-gradient(90deg, transparent, #000, transparent); }
.dashboard-date { display: flex; align-items: center; gap: 16px; color: var(--ui-muted); font-size: 12px; z-index: 1; }
.load-error { margin-bottom: 20px; }.stat-cards { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 18px; margin-bottom: 25px; }
.metric-card { padding: 20px 23px; }.metric-top { display: flex; align-items: center; justify-content: space-between; color: var(--ui-text); font-size: 12px; }.metric-icon { width: 31px; height: 31px; display: inline-flex; align-items: center; justify-content: center; border-radius: 8px; }.metric-card > strong { display: block; font-size: 34px; line-height: 1.25; letter-spacing: -1px; font-weight: 600; color: var(--ui-heading); margin: 9px 0; font-variant-numeric: tabular-nums; }
.blue { color: var(--ui-primary); background: var(--ui-tint); }.cyan { color: var(--ui-cyan); background: color-mix(in srgb, var(--ui-cyan) 9%, var(--ui-panel)); }.green { color: var(--el-color-success); background: var(--el-color-success-light-9); }.rose { color: var(--el-color-danger); background: var(--el-color-danger-light-9); }.violet { color: #8c7bc7; background: color-mix(in srgb, #8c7bc7 9%, var(--ui-panel)); }
.actions-panel, .recent-panel { margin-bottom: 24px; overflow: hidden; }.section-heading { display: flex; align-items: center; justify-content: space-between; padding: 22px 25px; gap: 18px; }.section-heading h2 { font-size: 16px; font-weight: 600; color: var(--ui-heading); }
.quick-actions { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); padding: 0 25px 23px; gap: 12px; }.quick-action { display: flex; align-items: center; text-align: left; gap: 13px; padding: 17px 16px; border: 1px solid var(--ui-border); background: var(--ui-panel); border-radius: 8px; cursor: pointer; transition: background .16s, border-color .16s; min-width: 0; }.quick-action:hover { border-color: var(--el-color-primary-light-5); background: var(--ui-panel-soft); }.action-icon { width: 38px; height: 38px; display: inline-flex; align-items: center; justify-content: center; border-radius: 9px; flex-shrink: 0; font-size: 18px; }.action-copy { display: block; min-width: 0; }.action-copy strong { display: block; font-size: 12px; font-weight: 550; color: var(--ui-heading); }.action-arrow { margin-left: auto; color: var(--ui-muted); font-size: 17px; }.link-arrow { margin-left: 8px; }
.recent-table :deep(.el-table__row) { cursor: pointer; }.build-app { display: flex; align-items: center; gap: 10px; }.build-app strong { font-weight: 500; font-size: 12px; color: var(--ui-heading); }.build-icon { width: 28px; height: 28px; border-radius: 7px; padding: 7px; background: var(--ui-panel-soft); color: var(--ui-muted); }.branch-label { background: var(--ui-panel-soft); border: 1px solid var(--ui-border); border-radius: 4px; padding: 3px 7px; font-size: 10px; }
@media (max-width: 1100px) { .quick-actions { grid-template-columns: repeat(2, minmax(0, 1fr)); }.metric-card { padding: 18px; }.stat-cards { gap: 12px; }.dashboard-heading::after { display: none; } }
@media (max-width: 700px) { .stat-cards { grid-template-columns: repeat(2, minmax(0, 1fr)); }.dashboard-heading { align-items: flex-start; flex-direction: column; gap: 15px; }.quick-actions { grid-template-columns: 1fr; padding-inline: 16px; }.section-heading { padding: 18px 16px; } }
</style>

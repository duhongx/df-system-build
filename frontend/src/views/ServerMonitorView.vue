<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getManagedServer, getServerMetrics, type ManagedServer } from '../api/server-mgmt'

const route = useRoute()
const router = useRouter()
const serverId = Number(route.params.id)
const server = ref<ManagedServer | null>(null)
const metrics = ref<any>(null)
const loading = ref(true)
const error = ref('')
let timer: any = null

onMounted(async () => {
  try {
    server.value = await getManagedServer(serverId)
  } catch (e) { /* handled */ }
  await fetchMetrics()
  // Auto refresh every 10 seconds
  timer = setInterval(fetchMetrics, 10000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})

async function fetchMetrics() {
  try {
    metrics.value = await getServerMetrics(serverId)
    error.value = ''
  } catch (e: any) {
    error.value = e?.message || '获取监控数据失败'
  } finally {
    loading.value = false
  }
}

function formatBytes(bytes: number) {
  if (!bytes) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let val = bytes
  while (val >= 1024 && i < units.length - 1) { val /= 1024; i++ }
  return `${val.toFixed(1)} ${units[i]}`
}

function formatUptime(seconds: number) {
  if (!seconds) return '-'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const mins = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}天 ${hours}小时`
  if (hours > 0) return `${hours}小时 ${mins}分钟`
  return `${mins}分钟`
}

function cpuColor(pct: number) {
  if (pct > 80) return '#f56c6c'
  if (pct > 50) return '#e6a23c'
  return '#67c23a'
}

function memColor(pct: number) {
  if (pct > 85) return '#f56c6c'
  if (pct > 60) return '#e6a23c'
  return '#409eff'
}

function handleBack() {
  router.push('/servers')
}
</script>

<template>
  <div class="monitor-page">
    <div class="monitor-header">
      <div class="header-left">
        <el-button size="small" @click="handleBack"><el-icon><ArrowLeft /></el-icon>返回</el-button>
        <span class="server-info" v-if="server">
          {{ server.host }} ({{ server.remark || server.host }})
          <el-tag type="success" size="small" style="margin-left: 8px;">监控中</el-tag>
        </span>
      </div>
      <div class="header-right">
        <span style="font-size: 12px; color: #909399;">每 10 秒自动刷新</span>
        <el-button size="small" @click="fetchMetrics" :loading="loading">刷新</el-button>
      </div>
    </div>

    <div v-if="error" class="error-card">
      <el-alert :title="error" type="error" show-icon :closable="false" />
    </div>

    <template v-if="metrics">
      <!-- Overview Cards -->
      <div class="metric-cards">
        <div class="metric-card">
          <div class="card-title">CPU 使用率</div>
          <div class="card-value">
            <el-progress type="dashboard" :percentage="metrics.cpuUsagePercent" :color="cpuColor(metrics.cpuUsagePercent)" :width="100" />
          </div>
          <div class="card-extra">{{ metrics.cpuCores }} 核</div>
        </div>

        <div class="metric-card">
          <div class="card-title">内存使用率</div>
          <div class="card-value">
            <el-progress type="dashboard" :percentage="metrics.memUsagePercent" :color="memColor(metrics.memUsagePercent)" :width="100" />
          </div>
          <div class="card-extra">{{ formatBytes(metrics.memUsedBytes) }} / {{ formatBytes(metrics.memTotalBytes) }}</div>
        </div>

        <div class="metric-card">
          <div class="card-title">系统负载</div>
          <div class="card-value load-values">
            <div><span class="load-label">1m</span><span class="load-num">{{ metrics.load1 }}</span></div>
            <div><span class="load-label">5m</span><span class="load-num">{{ metrics.load5 }}</span></div>
            <div><span class="load-label">15m</span><span class="load-num">{{ metrics.load15 }}</span></div>
          </div>
          <div class="card-extra">核心数: {{ metrics.cpuCores }}</div>
        </div>

        <div class="metric-card">
          <div class="card-title">运行时间</div>
          <div class="card-value uptime-value">{{ formatUptime(metrics.uptimeSeconds) }}</div>
          <div class="card-extra">启动于 {{ metrics.bootTime }}</div>
        </div>
      </div>

      <!-- Disk Usage -->
      <div class="section-card">
        <h4 class="section-title">磁盘使用</h4>
        <el-table :data="metrics.disks" stripe size="small" border>
          <el-table-column prop="mountpoint" label="挂载点" min-width="150" />
          <el-table-column prop="device" label="设备" width="150" />
          <el-table-column label="总容量" width="100">
            <template #default="{ row }">{{ formatBytes(row.totalBytes) }}</template>
          </el-table-column>
          <el-table-column label="已使用" width="100">
            <template #default="{ row }">{{ formatBytes(row.usedBytes) }}</template>
          </el-table-column>
          <el-table-column label="可用" width="100">
            <template #default="{ row }">{{ formatBytes(row.availBytes) }}</template>
          </el-table-column>
          <el-table-column label="使用率" width="180">
            <template #default="{ row }">
              <el-progress :percentage="row.usagePercent" :color="row.usagePercent > 85 ? '#f56c6c' : row.usagePercent > 70 ? '#e6a23c' : '#409eff'" :stroke-width="14" :text-inside="true" />
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- Network -->
      <div class="section-card">
        <h4 class="section-title">网络接口</h4>
        <el-table :data="metrics.networks" stripe size="small" border>
          <el-table-column prop="interface" label="接口" width="120" />
          <el-table-column label="接收总量">
            <template #default="{ row }">{{ formatBytes(row.receiveBytes) }}</template>
          </el-table-column>
          <el-table-column label="发送总量">
            <template #default="{ row }">{{ formatBytes(row.transmitBytes) }}</template>
          </el-table-column>
        </el-table>
      </div>
    </template>
  </div>
</template>

<style scoped>
.monitor-page { display: flex; flex-direction: column; gap: 16px; }
.monitor-header {
  display: flex; align-items: center; justify-content: space-between;
  background: #fff; padding: 12px 16px; border-radius: 6px; border: 1px solid #ebeef5;
}
.header-left { display: flex; align-items: center; gap: 12px; }
.header-right { display: flex; align-items: center; gap: 12px; }
.server-info { font-size: 14px; font-weight: 500; color: #303133; }
.error-card { margin: 0; }

.metric-cards {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}

.metric-card {
  background: #fff;
  border-radius: 8px;
  border: 1px solid #ebeef5;
  padding: 20px;
  text-align: center;
}

.card-title {
  font-size: 13px;
  color: #909399;
  margin-bottom: 12px;
}

.card-value {
  display: flex;
  justify-content: center;
  align-items: center;
  min-height: 100px;
}

.card-extra {
  font-size: 12px;
  color: #909399;
  margin-top: 8px;
}

.load-values {
  display: flex;
  gap: 20px;
  flex-direction: row;
}

.load-label {
  font-size: 11px;
  color: #909399;
  display: block;
}

.load-num {
  font-size: 22px;
  font-weight: 600;
  color: #303133;
}

.uptime-value {
  font-size: 22px;
  font-weight: 600;
  color: #303133;
  min-height: 100px;
  display: flex;
  align-items: center;
  justify-content: center;
}

.section-card {
  background: #fff;
  border-radius: 8px;
  border: 1px solid #ebeef5;
  padding: 16px;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  color: #303133;
  margin: 0 0 12px 0;
}
</style>

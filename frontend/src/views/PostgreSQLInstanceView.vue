<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getPostgreSQLInstance, type PostgreSQLInstanceInfo } from '../api/postgresql'
import { formatTime as formatTimeStr } from '../utils/time'

const router = useRouter()
const loading = ref(false)
const instance = ref<PostgreSQLInstanceInfo | null>(null)

async function loadInstance() {
  loading.value = true
  try {
    instance.value = await getPostgreSQLInstance()
  } finally {
    loading.value = false
  }
}

onMounted(loadInstance)
</script>

<template>
  <div class="postgres-instance-page" v-loading="loading">
    <div class="page-title-row">
      <div>
        <h4 class="page-title">实例管理</h4>
        <p class="page-desc">展示系统设置中已维护的 PostgreSQL 连接和运行状态。</p>
      </div>
      <div class="title-actions">
        <el-button size="small" @click="loadInstance">刷新</el-button>
        <el-button size="small" type="primary" @click="router.push('/settings/environment')">系统设置</el-button>
      </div>
    </div>

    <div class="content-card">
      <el-descriptions :column="3" border>
        <el-descriptions-item label="连接状态">
          <el-tag :type="instance?.status === 'UP' ? 'success' : 'danger'">{{ instance?.status || '-' }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="主机">{{ instance?.config.host || '-' }}</el-descriptions-item>
        <el-descriptions-item label="端口">{{ instance?.config.port || '-' }}</el-descriptions-item>
        <el-descriptions-item label="数据库">{{ instance?.config.database || '-' }}</el-descriptions-item>
        <el-descriptions-item label="用户">{{ instance?.config.user || '-' }}</el-descriptions-item>
        <el-descriptions-item label="密码">{{ instance?.config.password || '-' }}</el-descriptions-item>
        <el-descriptions-item label="角色">{{ instance?.role || '-' }}</el-descriptions-item>
        <el-descriptions-item label="当前库">{{ instance?.currentDb || '-' }}</el-descriptions-item>
        <el-descriptions-item label="当前用户">{{ instance?.currentUser || '-' }}</el-descriptions-item>
        <el-descriptions-item label="服务地址">{{ instance?.serverAddr || '-' }}</el-descriptions-item>
        <el-descriptions-item label="服务端口">{{ instance?.serverPort || '-' }}</el-descriptions-item>
        <el-descriptions-item label="检测时间">{{ formatTimeStr(instance?.checkedAt) }}</el-descriptions-item>
        <el-descriptions-item label="版本" :span="3">{{ instance?.version || instance?.message || '-' }}</el-descriptions-item>
      </el-descriptions>
    </div>

    <div class="data-grid">
      <div class="content-card">
        <div class="section-title">关键参数</div>
        <el-table :data="Object.entries(instance?.settings || {}).map(([key, value]) => ({ key, value }))" size="small" border>
          <el-table-column prop="key" label="参数" width="240" />
          <el-table-column prop="value" label="值" />
        </el-table>
      </div>

      <div class="content-card">
        <div class="section-title">复制状态</div>
        <el-table :data="instance?.replications || []" size="small" border>
          <el-table-column prop="clientAddr" label="Standby 地址" />
          <el-table-column prop="state" label="状态" />
          <el-table-column prop="syncState" label="同步状态" />
          <el-table-column prop="writeLag" label="Write Lag" />
          <el-table-column prop="flushLag" label="Flush Lag" />
          <el-table-column prop="replayLag" label="Replay Lag" />
        </el-table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.postgres-instance-page { display: flex; flex-direction: column; gap: 16px; }
.page-title-row { display: flex; align-items: flex-start; justify-content: space-between; }
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0; }
.page-desc { font-size: 13px; color: #909399; margin: 4px 0 0; }
.title-actions { display: flex; gap: 8px; }
.content-card { background: #fff; border: 1px solid #ebeef5; border-radius: 6px; padding: 16px; }
.section-title { font-size: 14px; font-weight: 600; color: #303133; margin-bottom: 12px; }
.data-grid { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; }
</style>

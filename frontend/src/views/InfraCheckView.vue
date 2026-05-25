<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'

const packageDir = ref('/opt/deploy-packages')
const checking = ref(false)
const packageResult = ref<any>(null)

async function handleCheckPackages() {
  checking.value = true
  try {
    const resp = await fetch('/api/deploy/check-packages', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Authorization': `Bearer ${localStorage.getItem('df-token')}` },
      body: JSON.stringify({ packageDir: packageDir.value }),
    })
    const data = await resp.json()
    if (data.code === 0) {
      packageResult.value = data.data
    } else {
      ElMessage.error(data.message)
    }
  } catch (e) {
    ElMessage.error('检测失败')
  } finally {
    checking.value = false
  }
}
</script>

<template>
  <div class="page">
    <h4 class="page-title">部署前检测</h4>

    <div class="content-card">
      <h5>离线包检测</h5>
      <div style="display: flex; gap: 8px; margin-bottom: 16px;">
        <el-input v-model="packageDir" placeholder="离线包目录路径" style="flex: 1;" />
        <el-button type="primary" :loading="checking" @click="handleCheckPackages">开始检测</el-button>
      </div>

      <div v-if="packageResult">
        <el-tag :type="packageResult.passed ? 'success' : 'danger'" style="margin-bottom: 12px;">
          {{ packageResult.passed ? '检测通过' : '检测未通过' }}
        </el-tag>
        <el-table :data="packageResult.items" stripe size="small" border>
          <el-table-column prop="name" label="检测项" min-width="200" />
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="row.status === 'pass' ? 'success' : row.status === 'warn' ? 'warning' : 'danger'" size="small">
                {{ row.status === 'pass' ? '通过' : row.status === 'warn' ? '警告' : '失败' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="message" label="说明" min-width="250" />
        </el-table>
      </div>
    </div>

    <div class="content-card" style="margin-top: 16px;">
      <h5>服务器前置条件检测</h5>
      <p style="color: #909399; font-size: 13px;">选择服务器后检测 firewalld/SELinux/swap/磁盘空间/时间同步等前置条件</p>
      <el-button size="small" disabled>检测所有服务器 (开发中)</el-button>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 20px; }
h5 { font-size: 14px; margin: 0 0 12px 0; }
</style>

<script setup lang="ts">
import { ref } from 'vue'

// Offline bundle install endpoints land in Task 10 (backend). This page shows
// the current bundle status and the upload/install entrypoints; install wiring
// is enabled once the backend offline API is exposed.
const status = ref<{ bundleVersion?: string; installedAt?: string; fileCount?: number } | null>(null)
</script>

<template>
  <div class="page">
    <h4 class="page-title">离线包</h4>
    <div class="content-card">
      <div v-if="status">
        <p>当前版本：<strong>{{ status.bundleVersion }}</strong></p>
        <p>安装时间：{{ status.installedAt }}</p>
        <p>文件数：{{ status.fileCount }}</p>
      </div>
      <el-empty v-else :image-size="60" description="离线包管理（上传 / 校验 / 安装）即将开放" />
      <p class="hint">离线资源树（约 4.5GB）保存在服务器本地 Resource_Dir，由离线包安装填充；不入库、不嵌入二进制。</p>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 24px; }
.hint { font-size: 12px; color: #909399; margin-top: 16px; }
</style>

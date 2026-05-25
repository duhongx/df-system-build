<script setup lang="ts">
</script>
<template>
  <div class="page">
    <h4 class="page-title">{{ $route.meta.title }}</h4>
    <div class="content-card">
      <el-empty description="功能开发中..." :image-size="100" />
    </div>
  </div>
</template>
<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 40px; }
</style>

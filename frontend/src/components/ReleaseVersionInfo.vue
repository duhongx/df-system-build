<script setup lang="ts">
import { computed, ref } from 'vue'
import { releaseVersionContent, type ReleaseVersionSource } from '../utils/release-version'

const props = defineProps<{ version: ReleaseVersionSource }>()
const content = computed(() => releaseVersionContent(props.version))
const rawVisible = ref(false)
const rawText = computed(() => {
  const raw = props.version.releaseManifestJson || ''
  try { return JSON.stringify(JSON.parse(raw), null, 2) } catch { return raw }
})
</script>

<template>
  <el-card shadow="never" class="release-version-info">
    <template #header>
      <div class="release-header">
        <strong>业务版本</strong>
        <el-tag v-if="version.appVersion" size="large">{{ version.appVersion }}</el-tag>
        <span v-else class="release-muted">{{ version.releaseManifestError ? '版本信息异常' : '未提供业务版本信息' }}</span>
        <span v-if="version.releaseTime">发布时间：{{ version.releaseTime }}</span>
        <el-button v-if="version.releaseManifestJson" link type="primary" @click="rawVisible = true">查看 version.json</el-button>
      </div>
    </template>
    <el-alert v-if="version.releaseManifestError" :title="version.releaseManifestError" type="warning" :closable="false" show-icon />
    <div v-if="version.releaseReadme" class="release-section">
      <strong>更新说明</strong>
      <div class="release-text">{{ version.releaseReadme }}</div>
    </div>
    <div v-if="content.features.length" class="release-section">
      <strong>功能变更</strong>
      <ul><li v-for="(feature, index) in content.features" :key="index" class="release-text">{{ feature }}</li></ul>
    </div>
    <div v-if="content.parameters.length" class="release-section">
      <strong>参数说明</strong>
      <el-table :data="content.parameters" border max-height="320">
        <el-table-column prop="name" label="参数名称" min-width="160" />
        <el-table-column prop="description" label="参数说明" min-width="280" />
        <el-table-column v-if="content.parameters.some(item => item.value)" prop="value" label="参数值" min-width="160" />
      </el-table>
    </div>
    <el-descriptions v-if="content.businessFields.length" :column="1" border class="release-section">
      <el-descriptions-item v-for="field in content.businessFields" :key="field.name" :label="field.name">
        <div class="release-text">{{ field.value }}</div>
      </el-descriptions-item>
    </el-descriptions>
    <el-dialog v-model="rawVisible" title="业务版本原始内容 · version.json" width="760px" append-to-body>
      <pre class="release-json">{{ rawText }}</pre>
    </el-dialog>
  </el-card>
</template>

<style scoped>
.release-header { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.release-muted { color: #909399; }
.release-section { margin-top: 12px; }
.release-section > strong { display: block; margin-bottom: 8px; }
.release-text { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 360px; overflow: auto; line-height: 1.7; }
.release-json { white-space: pre-wrap; overflow-wrap: anywhere; max-height: 65vh; overflow: auto; }
ul { margin: 0; padding-left: 22px; }
</style>

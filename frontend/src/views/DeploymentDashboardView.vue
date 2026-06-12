<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { listComponents, listRuns, runningRuns, type DeploymentComponent } from '../api/deployment'

const components = ref<DeploymentComponent[]>([])
const runningCount = ref(0)
const totalRuns = ref(0)

async function load() {
  const [comps, running, runs] = await Promise.all([
    listComponents(), runningRuns(), listRuns(1, 1),
  ])
  components.value = comps
  runningCount.value = (running || []).length
  totalRuns.value = runs.total || 0
}

const deployedCount = () => components.value.filter(c => c.status === 'deployed').length

onMounted(load)
</script>

<template>
  <div class="page">
    <h4 class="page-title">部署管理 · 概览</h4>
    <div class="tiles">
      <div class="tile"><div class="num">{{ runningCount }}</div><div class="lbl">进行中</div></div>
      <div class="tile"><div class="num">{{ deployedCount() }}</div><div class="lbl">已部署组件</div></div>
      <div class="tile"><div class="num">{{ components.length }}</div><div class="lbl">组件总数</div></div>
      <div class="tile"><div class="num">{{ totalRuns }}</div><div class="lbl">历史运行</div></div>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.tiles { display: flex; gap: 16px; flex-wrap: wrap; }
.tile { background: #fff; border: 1px solid #ebeef5; border-radius: 8px; padding: 24px 32px; min-width: 140px; text-align: center; }
.num { font-size: 28px; font-weight: 700; color: #409eff; }
.lbl { font-size: 13px; color: #909399; margin-top: 6px; }
</style>

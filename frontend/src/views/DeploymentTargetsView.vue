<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  listComponents, listTargets, putTargets,
  type DeploymentComponent,
} from '../api/deployment'
import { listManagedServers, type ManagedServer } from '../api/server-mgmt'

const components = ref<DeploymentComponent[]>([])
const servers = ref<ManagedServer[]>([])
const targetsMap = ref<Record<string, number[]>>({})
const selected = ref<string>('')
const checked = ref<number[]>([])
const saving = ref(false)
const conflictMsg = ref('')

const currentCategory = computed(() =>
  components.value.find(c => c.code === selected.value)?.category || '')

async function load() {
  const [comps, srvs, targets] = await Promise.all([
    listComponents(), listManagedServers(), listTargets(),
  ])
  components.value = comps
  servers.value = srvs
  const map: Record<string, number[]> = {}
  for (const t of targets) map[t.component_name] = t.host_ids || []
  targetsMap.value = map
  if (!selected.value && comps.length) select(comps[0].code)
}

function select(code: string) {
  selected.value = code
  checked.value = [...(targetsMap.value[code] || [])]
  conflictMsg.value = ''
}

async function save() {
  if (!selected.value) return
  saving.value = true
  conflictMsg.value = ''
  try {
    await putTargets(selected.value, checked.value)
    targetsMap.value[selected.value] = [...checked.value]
    ElMessage.success('已保存主机绑定')
  } catch (e: any) {
    // The unified interceptor surfaces the message; capture for inline display.
    conflictMsg.value = e?.message || '保存失败（可能存在主机冲突）'
  } finally {
    saving.value = false
  }
}

function categoryLabel(c: string) {
  return c === 'k8s' ? 'K8s' : c === 'business' ? '业务' : '其他'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <h4 class="page-title">主机绑定</h4>
    <div class="split">
      <div class="content-card left">
        <div class="left-head">组件</div>
        <div class="comp-list">
          <div v-for="c in components" :key="c.code" class="comp-item"
            :class="{ active: selected === c.code }" @click="select(c.code)">
            <span class="comp-name">{{ c.code }}</span>
            <span class="comp-tag">{{ categoryLabel(c.category) }}</span>
            <span class="comp-count">{{ (targetsMap[c.code] || []).length }}</span>
          </div>
        </div>
      </div>

      <div class="content-card right">
        <div class="right-head">
          <span>目标主机 <em v-if="selected">· {{ selected }}（{{ categoryLabel(currentCategory) }}）</em></span>
          <el-button type="primary" size="small" :loading="saving" :disabled="!selected" @click="save">保存</el-button>
        </div>
        <el-alert v-if="conflictMsg" type="error" :closable="false" :title="conflictMsg" style="margin-bottom: 10px;" />
        <p class="hint">主机来源于「服务器管理」，部署目标仅引用服务器记录，不重复维护主机信息。</p>
        <el-checkbox-group v-model="checked" class="srv-group">
          <el-checkbox v-for="s in servers" :key="s.id" :value="s.id" class="srv-item">
            {{ s.remark || s.host }} <span class="srv-host">({{ s.host }})</span>
          </el-checkbox>
        </el-checkbox-group>
        <el-empty v-if="!servers.length" :image-size="50" description="请先在「服务器管理」添加服务器" />
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-title { font-size: 15px; font-weight: 600; color: #303133; margin: 0 0 16px 0; }
.split { display: flex; gap: 12px; }
.content-card { background: #fff; border-radius: 8px; border: 1px solid #ebeef5; padding: 16px; }
.left { width: 280px; flex-shrink: 0; }
.right { flex: 1; }
.left-head, .right-head { font-size: 13px; font-weight: 600; color: #303133; margin-bottom: 10px; display: flex; align-items: center; justify-content: space-between; }
.right-head em { color: #909399; font-style: normal; font-weight: 400; }
.comp-item { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border-radius: 4px; cursor: pointer; margin-bottom: 4px; }
.comp-item:hover { background: #f5f7fa; }
.comp-item.active { background: #ecf5ff; }
.comp-name { flex: 1; font-size: 13px; }
.comp-tag { font-size: 11px; color: #909399; }
.comp-count { font-size: 12px; color: #409eff; min-width: 18px; text-align: center; }
.hint { font-size: 12px; color: #909399; margin: 0 0 12px; }
.srv-group { display: flex; flex-direction: column; gap: 8px; }
.srv-item { margin-right: 0; }
.srv-host { color: #c0c4cc; font-size: 12px; }
</style>

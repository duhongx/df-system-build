<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { ElMessage, type TableInstance } from 'element-plus'
import {
  previewSQLFileViewTransfer, transferSQLFileToViewTask,
  type SQLFileViewTransferTarget, type SQLFileViewTransferPreview,
  type SQLViewTransferUnit, type SQLViewDependencyTask,
} from '../api/postgresql'

const props = defineProps<{ target: SQLFileViewTransferTarget | null; busy?: boolean }>()
const emit = defineEmits<{ close: []; created: [task: SQLViewDependencyTask] }>()
const preview = ref<SQLFileViewTransferPreview | null>(null)
const table = ref<TableInstance>()
const selection = ref<SQLViewTransferUnit[]>([])
const loading = ref(false), creating = ref(false), error = ref('')
let generation = 0, disposed = false

const changeCount = computed(() => selection.value.reduce((n, unit) => n + unit.changeCount, 0))
const tableCount = computed(() => new Set(selection.value.flatMap(unit => unit.tables)).size)
const transactionConflict = computed(() => selection.value.length > 1 && selection.value.some(unit => unit.transaction))
const contextConflict = computed(() => new Set(selection.value.map(unit => unit.contextKey)).size > 1)
const canCreate = computed(() => !!preview.value && selection.value.length > 0 && !transactionConflict.value && !contextConflict.value && !loading.value && !creating.value && !props.busy && !error.value)

async function load() {
  const current = ++generation
  preview.value = null
  selection.value = []
  error.value = ''
  if (!props.target) { loading.value = false; return }
  const target = { ...props.target, statementIds: props.target.statementIds?.slice() }
  loading.value = true
  try {
    const result = await previewSQLFileViewTransfer(target)
    if (disposed || current !== generation) return
    preview.value = result
    await nextTick()
    if (!disposed && current === generation) table.value?.toggleAllSelection()
  } catch (err) {
    if (!disposed && current === generation) error.value = err instanceof Error ? err.message : '转入预览失败'
  } finally {
    if (current === generation) loading.value = false
  }
}

async function create() {
  if (!canCreate.value || !props.target || !preview.value) return
  const target = { ...props.target, statementIds: props.target.statementIds?.slice() }
  const current = generation
  creating.value = true
  try {
    const task = await transferSQLFileToViewTask(target, selection.value.map(unit => unit.id), preview.value.previewHash)
    if (!disposed && current === generation) {
      ElMessage.success(`已创建视图依赖任务 #${task.id}，请分析依赖后执行`)
      emit('created', task)
    }
  } catch (err) {
    if (!disposed && current === generation) error.value = err instanceof Error ? err.message : '创建视图依赖任务失败'
  } finally { creating.value = false }
}

function close() { if (!creating.value) emit('close') }
function lineRange(unit: { startLine: number; endLine: number }) {
  return unit.startLine === unit.endLine ? `${unit.startLine}` : `${unit.startLine}–${unit.endLine}`
}
watch(() => props.target, load, { immediate: true })
onBeforeUnmount(() => { disposed = true; generation++ })
</script>

<template>
  <el-dialog :model-value="!!target" title="转入视图依赖" width="1100px" append-to-body
    :close-on-click-modal="false" :close-on-press-escape="!creating" :show-close="!creating" @close="close">
    <div v-loading="loading" class="transfer-content">
      <p class="transfer-note">同一文件的字段类型变更可合并为一个任务。转入只创建待分析任务，选中的原 SQL 会暂缓普通执行；任务执行、视图恢复及校验全部成功后，逐条回写成功结果。</p>
      <el-alert v-if="error" :title="error" type="error" :closable="false" />
      <template v-if="preview">
        <strong class="transfer-file">{{ preview.fileName }}</strong>
        <p class="transfer-note">已选择 {{ changeCount }} 条字段变更，涉及 {{ tableCount }} 张表。可取消勾选部分变更；原 BEGIN/COMMIT 事务组作为一个整体选择。</p>
        <el-alert v-if="transactionConflict" title="请选择一个完整原事务组，或仅选择组外语句。不同原事务组及组外语句须分别转入，保留原提交边界。" type="warning" :closable="false" />
        <el-alert v-if="contextConflict" title="选中的 SQL 使用不同 search_path 或会话配置，请按原上下文分别转入。" type="warning" :closable="false" />
        <el-table ref="table" :data="preview.units" row-key="id" max-height="390" stripe
          @selection-change="selection=$event">
          <el-table-column type="selection" width="44" :selectable="() => !creating" />
          <el-table-column type="expand"><template #default="{row}"><div class="transfer-source"><template v-for="stmt in row.statements" :key="stmt.id"><span>第 {{stmt.lineNumber}} 行 · {{stmt.executeStatus==='PENDING'?'未执行':stmt.executeStatus==='FAILED'?'失败／暂缓':stmt.executeStatus}}</span><pre>{{stmt.sqlContent}}</pre></template></div></template></el-table-column>
          <el-table-column label="源文件行号" width="125"><template #default="{row}">{{ lineRange(row) }}</template></el-table-column>
          <el-table-column label="处理范围" width="145"><template #default="{row}"><el-tag :type="row.transaction?'warning':'info'">{{row.transaction?'完整事务组':'单条 SQL'}}</el-tag></template></el-table-column>
          <el-table-column prop="changeCount" label="字段变更数" width="105" />
          <el-table-column label="实际目标字段" min-width="410"><template #default="{row}"><div class="transfer-targets"><span v-for="(targetName,index) in row.targets" :key="index">{{targetName}}</span></div></template></el-table-column>
        </el-table>
        <el-empty v-if="!preview.units.length" :description="preview.excluded.length?'没有可转入的字段类型变更，请查看下方原因':'当前范围没有字段类型变更'" :image-size="60" />
        <el-collapse v-if="preview.excluded.length"><el-collapse-item :title="`未纳入 ${preview.excluded.length} 项 · 查看原因`" name="excluded"><el-table :data="preview.excluded" max-height="240"><el-table-column label="行号" width="125"><template #default="{row}">{{lineRange(row)}}</template></el-table-column><el-table-column prop="reason" label="原因" min-width="500" /></el-table></el-collapse-item></el-collapse>
      </template>
    </div>
    <template #footer><el-button :disabled="creating" @click="close">取消</el-button><el-button :disabled="creating||loading" @click="load">重新预览</el-button><el-button type="primary" :disabled="!canCreate" :loading="creating" @click="create">创建一个视图依赖任务（{{changeCount}} 条变更）</el-button></template>
  </el-dialog>
</template>

<style scoped>
.transfer-content{display:flex;flex-direction:column;gap:12px;min-height:140px}.transfer-note{margin:0;color:var(--el-text-color-secondary);font-size:13px;line-height:1.7}.transfer-file{overflow-wrap:anywhere}.transfer-targets{display:flex;flex-direction:column;gap:4px;max-height:120px;overflow:auto;overflow-wrap:anywhere}.transfer-source{padding:12px 24px}.transfer-source>span{font-size:12px;color:var(--el-text-color-secondary)}.transfer-source pre{white-space:pre-wrap;overflow-wrap:anywhere;font:13px/1.7 ui-monospace,monospace;max-height:250px;overflow:auto;margin:6px 0 14px}
</style>

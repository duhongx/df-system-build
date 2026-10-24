<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { ElMessage } from 'element-plus';
import { editSQLFile, getSQLFile, getSQLFileEdit, listSQLFileEdits, type SQLChangeFile, type SQLChangeStatement, type SQLFileEdit, type SQLFileEditResult } from '../api/postgresql';
import { formatTime } from '../utils/time';

const props = defineProps<{ fileId: number | null; startEditing?: boolean; busy?: boolean }>();
const emit = defineEmits<{ close: []; saved: [result: SQLFileEditResult] }>();
const file = ref<SQLChangeFile | null>(null);
const rows = ref<SQLChangeStatement[]>([]);
const draft = ref('');
const original = ref('');
const editing = ref(false);
const saving = ref(false);
const loading = ref(false);
const error = ref('');
const historyVisible = ref(false);
const history = ref<SQLFileEdit[]>([]);
const historyPage = ref(1);
const historyTotal = ref(0);
const historyLoading = ref(false);
const revision = ref<{edit: SQLFileEdit; beforeStatements: SQLChangeStatement[]} | null>(null);
const revisionTab = ref('after');
let generation = 0;

const blockedReason = computed(() => {
  if(rows.value.some(row=>row.viewDependencyTaskId&&row.executeStatus!=='SUCCESS'))return '文件包含已转入视图依赖的语句，请先完成关联任务。';
  if (props.busy) return '当前有执行或操作正在进行，请结束后再编辑。';
  if (!file.value || loading.value) return '正在读取文件。';
  if (!['PENDING', 'FAILED', 'CANCELED', 'PARTIAL_FAILED'].includes(file.value.executeStatus)) return '此文件正在执行、排队、已完成或结果待核实，仅可查看全文。';
  if (rows.value.some(row => ['RUNNING', 'UNKNOWN'].includes(row.executeStatus))) return '文件中有正在执行或结果待核实的语句，仅可查看全文。';
  return '';
});
const canSave = computed(() => editing.value && !saving.value && !blockedReason.value && draft.value !== original.value && !!draft.value.trim());
const statusText = (value: string) => ({SUCCESS: '成功', FAILED: '失败', SKIPPED: '已跳过', PENDING: '未执行', CANCELED: '未完成', UNKNOWN: '结果待核实', RUNNING: '执行中'}[value] || value);

watch(() => props.fileId, async id => {
  const current = ++generation;
  file.value = null;
  rows.value = [];
  draft.value = original.value = '';
  editing.value = false;
  error.value = '';
  historyVisible.value = false;
  revision.value = null;
  if (!id) { loading.value = false; return; }
  loading.value = true;
  try {
    const result = await getSQLFile(id);
    if (current !== generation) return;
    file.value = result.file;
    rows.value = result.statements || [];
    draft.value = original.value = result.file.fileContent ?? '';
  } catch (cause) {
    if (current === generation) error.value = cause instanceof Error ? cause.message : '读取 SQL 文件失败';
  } finally {
    if (current === generation) {
      loading.value = false;
      if (props.startEditing && !blockedReason.value) editing.value = true;
    }
  }
}, {immediate: true});
onBeforeUnmount(() => { generation++; });

function close() { if (!saving.value && !editing.value) emit('close'); }
function startEdit() { if (!blockedReason.value) { draft.value = original.value; editing.value = true; error.value = ''; } }
function cancelEdit() { if (!saving.value) { draft.value = original.value; editing.value = false; error.value = ''; } }
async function save() {
  if (!canSave.value || !file.value) return;
  saving.value = true;
  error.value = '';
  try {
    const result = await editSQLFile(file.value.id, draft.value, original.value);
    editing.value = false;
    draft.value = original.value = result.file.fileContent ?? '';
    emit('saved', result);
    ElMessage.success(`全文已保存：保留 ${result.edit.preservedStatements} 条原状态，${result.edit.newStatements} 条调整为待执行；保存未执行 SQL`);
    emit('close');
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '保存失败，草稿已保留';
  } finally { saving.value = false; }
}
async function loadHistory() {
  if (!props.fileId) return;
  const id = props.fileId;
  historyLoading.value = true;
  try {
    const result = await listSQLFileEdits(id, historyPage.value, 20);
    if (props.fileId === id) { history.value = result.list || []; historyTotal.value = result.total; }
  } finally { historyLoading.value = false; }
}
async function showHistory() { historyPage.value = 1; historyVisible.value = true; await loadHistory(); }
async function showRevision(edit: SQLFileEdit) {
  const id = props.fileId;
  if (!id) return;
  const result = await getSQLFileEdit(id, edit.id);
  if (props.fileId === id) { revision.value = result; revisionTab.value = 'after'; }
}
</script>

<template>
  <el-dialog :model-value="!!fileId" :title="editing ? '编辑整个 SQL 文件' : '完整 SQL 文件'" width="min(1200px, 94vw)" append-to-body
    :show-close="!editing&&!saving" :close-on-click-modal="false" :close-on-press-escape="!editing&&!saving" @close="close">
    <div v-loading="loading" class="file-editor-body">
      <div v-if="file" class="file-editor-context"><strong>{{file.fileName}}</strong><span>schema：{{file.schemaName || '未配置'}} · {{rows.length}} 条 SQL</span></div>
      <p v-if="editing" class="file-editor-note">可以编辑全文。成功／已跳过的 SQL 及其事务组请保持原样；未改动的语句保留状态，修改或新增的语句保存后为待执行。保存会保留修改前的全文与状态，不执行 SQL。</p>
      <p v-else-if="blockedReason&&file" class="file-editor-note">{{blockedReason}}</p>
      <el-input v-if="editing" v-model="draft" type="textarea" :rows="23" :disabled="saving" spellcheck="false" aria-label="完整 SQL 文件内容" class="file-editor-textarea" />
      <pre v-else-if="file" class="file-source">{{original}}</pre>
      <el-alert v-if="error" :title="error" type="error" :closable="false" class="file-editor-error" />
    </div>
    <template #footer>
      <template v-if="editing"><el-button :disabled="saving" @click="cancelEdit">取消编辑</el-button><el-button type="primary" :loading="saving" :disabled="!canSave" @click="save">保存全文</el-button></template>
      <template v-else><el-button :disabled="!file||loading" @click="showHistory">修改历史</el-button><el-button @click="close">关闭</el-button><el-button type="primary" :disabled="!!blockedReason" @click="startEdit">编辑全文</el-button></template>
    </template>
  </el-dialog>

  <el-dialog v-model="historyVisible" title="整文件修改历史" width="850px" append-to-body>
    <el-table :data="history" v-loading="historyLoading" max-height="420">
      <el-table-column prop="id" label="修改" width="80"/><el-table-column label="保存时间" width="180"><template #default="{row}">{{formatTime(row.createdAt)}}</template></el-table-column>
      <el-table-column prop="editor" label="保存人" width="110"/><el-table-column prop="preservedStatements" label="保留状态" width="100"/>
      <el-table-column prop="newStatements" label="调整待执行" width="110"/><el-table-column prop="removedStatements" label="替换／移除" width="110"/>
      <el-table-column label="操作"><template #default="{row}"><el-button link type="primary" @click="showRevision(row)">查看</el-button></template></el-table-column>
    </el-table>
    <el-pagination v-if="historyTotal>20" v-model:current-page="historyPage" :page-size="20" :total="historyTotal" layout="prev,pager,next" @current-change="loadHistory"/>
  </el-dialog>
  <el-dialog :model-value="!!revision" :title="`整文件修改 #${revision?.edit.id || ''}`" width="min(1200px, 94vw)" append-to-body @close="revision=null">
    <template v-if="revision">
      <p>{{revision.edit.fileName}} · {{revision.edit.editor}} · {{formatTime(revision.edit.createdAt)}}</p>
      <el-tabs v-model="revisionTab">
        <el-tab-pane label="修改后全文" name="after"><pre class="file-source">{{revision.edit.afterContent}}</pre></el-tab-pane>
        <el-tab-pane label="修改前全文" name="before"><pre class="file-source">{{revision.edit.beforeContent}}</pre></el-tab-pane>
        <el-tab-pane label="保存前执行状态" name="states">
          <el-table :data="revision.beforeStatements" max-height="500" stripe><el-table-column prop="lineNumber" label="行" width="70"/>
            <el-table-column label="状态" width="120"><template #default="{row}">{{statusText(row.executeStatus)}}</template></el-table-column>
            <el-table-column prop="databaseUser" label="连接账号" width="150"/><el-table-column prop="sqlContent" label="SQL" min-width="330" show-overflow-tooltip/>
            <el-table-column prop="executeMessage" label="执行信息" min-width="330" show-overflow-tooltip/>
          </el-table>
        </el-tab-pane>
      </el-tabs>
    </template>
  </el-dialog>
</template>

<style scoped>
.file-editor-body{min-height:150px}.file-editor-context{display:flex;flex-direction:column;gap:6px;margin-bottom:12px;overflow-wrap:anywhere}.file-editor-context span,.file-editor-note{color:var(--el-text-color-secondary);font-size:13px;line-height:1.7}.file-source{white-space:pre-wrap;overflow-wrap:anywhere;max-height:65vh;overflow:auto;background:var(--el-fill-color-light);padding:14px;font:13px/1.65 ui-monospace,SFMono-Regular,Consolas,monospace;tab-size:4}.file-editor-textarea :deep(textarea){font:13px/1.65 ui-monospace,SFMono-Regular,Consolas,monospace;tab-size:4;max-height:65vh;resize:vertical}.file-editor-error{margin-top:12px}
</style>

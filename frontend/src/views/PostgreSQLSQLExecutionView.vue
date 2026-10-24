<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { ElMessage, ElMessageBox, type TableInstance } from 'element-plus';
import JSZip from 'jszip';
import SQLExecutionHistory from '../components/SQLExecutionHistory.vue';
import SQLCurrentFailures from '../components/SQLCurrentFailures.vue';
import {useRoute,useRouter} from 'vue-router';
import {useSettingsStore} from '../stores/settings';
const router=useRouter(),route=useRoute(),settingsStore=useSettingsStore();
const failureList=ref<InstanceType<typeof SQLCurrentFailures>>();
import SQLRetryAccountDialog from '../components/SQLRetryAccountDialog.vue';
import SQLFileEditorDialog from '../components/SQLFileEditorDialog.vue';
import SQLViewDependencyTransferDialog from '../components/SQLViewDependencyTransferDialog.vue';
import type { SQLFileViewTransferTarget, SQLViewDependencyTask } from '../api/postgresql';
import { cancelSQLFile, cancelSQLFileSequence, setSQLFileExecutionConfig, deleteSQLFile, editSQLStatement, executeFailedSQLStatement, executeSelectedSQLFiles, getSQLBatch, getSQLFile, importServerSQL, listDoneSQLFiles, listTodoSQLFiles, markSQLStatementFailed, markSQLFileFailed, parseSQLBatch, parseSQLFile, skipSQLFile, skipSQLStatement, type SQLExecutionFileFilter, type SQLChangeBatch, type SQLChangeFile, type SQLChangeStatement, type SQLExecuteOptions, type SQLRetryTarget, type SQLFileEditResult, } from '../api/postgresql';
import { getSettings } from '../api/settings';
import { formatTime } from '../utils/time';
import { compareSQLFiles } from '../utils/sql-file-order';
import { compactSQLExecutionMessage, describeSQLExecution, type SQLExecutionSummary } from '../utils/sql-execution-summary';
import { transactionScope, transactionStatementStatus } from '../utils/sql-transaction';
const files = ref<SQLChangeFile[]>([]);
const currentFile = ref<SQLChangeFile | null>(null);
const statements = ref<SQLChangeStatement[]>([]);
const statementTable = ref<TableInstance>();
const selectedTransferStatements = ref<SQLChangeStatement[]>([]);
const viewTransferTarget = ref<SQLFileViewTransferTarget | null>(null);
watch(() => currentFile.value?.id, () => { selectedTransferStatements.value = []; });
let focusedError = '';
const isStatementError = (row: SQLChangeStatement) => row.executeStatus==='UNKNOWN' || (['FAILED', 'PARTIAL_FAILED'].includes(row.executeStatus) && !['ROLLED_BACK','NOT_EXECUTED'].includes(row.transactionOutcome||''));
const failedStatement = computed(() => statements.value.find(row=>row.transactionOutcome==='FAILED'&&isStatementError(row)) || statements.value.find(isStatementError));
const transactionGroupCount = computed(() => new Set(statements.value.map(row=>row.transactionStartLine).filter(Boolean)).size);
const failedExecutionFile = computed(() => executionFiles.value.find(file => ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(file.executeStatus)));
const statementRowClass = ({ row }: { row: SQLChangeStatement }) => `sql-statement-${row.id}${isStatementError(row) ? ' statement-error-row' : ''}`;
async function locateStatementFailure(force = false, lineNumber?: number) {
    const fileId = currentFile.value?.id;
    const failure = (lineNumber ? statements.value.find(row => row.lineNumber === lineNumber) : undefined) || failedStatement.value;
    if (!failure || !fileId || !executionVisible.value) { focusedError = ''; return; }
    const key = `${fileId}:${failure.id}:${failure.executeStatus}`;
    if (!force && key === focusedError) return;
    await nextTick();
    if (disposed || currentFile.value?.id !== fileId || !executionVisible.value) return;
    const table = statementTable.value;
    const body = (table?.$el as HTMLElement | undefined)?.querySelector('.el-table__body-wrapper');
    const row = body?.querySelector<HTMLElement>(`tr.sql-statement-${failure.id}`);
    const scroll = body?.querySelector<HTMLElement>('.el-scrollbar__wrap');
    if (!row || !scroll || !table) return;
    const top = scroll.scrollTop + row.getBoundingClientRect().top - scroll.getBoundingClientRect().top - Math.max(0, (scroll.clientHeight - row.offsetHeight) / 2);
    table.setScrollTop(Math.max(0, top));
    focusedError = key;
}
async function locateExecutionFailure() {
    const stoppedFile = executionFiles.value.find(file => file.id === executionBatch.value?.stoppedFileId);
    const file = stoppedFile || (failedStatement.value ? currentFile.value : failedExecutionFile.value);
    if (!file) return;
    if (file.id !== currentFile.value?.id) await selectExecutionFile(file, true);
    await locateSidebarFile();
    await locateStatementFailure(true);
}
const selectedStatement = ref<SQLChangeStatement | null>(null);
const retryAccountTarget = ref<SQLRetryTarget | null>(null);
const editingStatement = ref(false);
const savingStatement = ref(false);
const statementDraft = ref('');
const originalStatement = ref('');
const group = ref<'todo' | 'done' | 'failed' | 'history'>('todo');
const mutating = ref(false);
const detailTab = ref('sql');
const configFile = ref<SQLChangeFile | null>(null);
const schemaDraft = ref('');
const savingConfig = ref(false);
const page = ref(1);
const total = ref(0);
const loading = ref(false);
const submitting = ref(false);
const stopping = ref(false);
const executionVisible = ref(false);
const fileEditorFileId = ref<number | null>(null);
const fileEditorStartEditing = ref(false);
const filteredFiles = ref<SQLExecutionFileFilter[]>([]);
const filteredFilesVisible = ref(false);
const executionFileList = ref<HTMLElement>();
const fullscreen = ref(true);
const executionFiles = ref<SQLChangeFile[]>([]);
const executionFileGroups = computed(() => [
    { key: 'success', label: '执行成功', type: 'success' as const, files: executionFiles.value.filter(file => file.executeStatus === 'SUCCESS') },
    { key: 'failed', label: '执行失败', type: 'danger' as const, files: executionFiles.value.filter(file => ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(file.executeStatus)) },
    { key: 'pending', label: '未执行 / 执行中', type: 'warning' as const, files: executionFiles.value.filter(file => !['SUCCESS', 'FAILED', 'PARTIAL_FAILED', 'UNKNOWN', 'SKIPPED'].includes(file.executeStatus)) },
    { key: 'skipped', label: '已跳过', type: 'info' as const, files: executionFiles.value.filter(file => file.executeStatus === 'SKIPPED') },
].filter(item => item.files.length));
const selectedFiles = ref<SQLChangeFile[]>([]);
const executionBatch = ref<SQLChangeBatch | null>(null);
const executionBatchFiles = ref<SQLChangeFile[]>([]);
const executionDetailsVisible = ref(false);
const executionStartError = ref('');
const pollError = ref('');
const lastPollAt = ref('');
const followExecution = ref(false);
async function locateSidebarFile() {
    await nextTick();
    if (!executionVisible.value || !currentFile.value) return;
    const list = executionFileList.value;
    const item = list?.querySelector<HTMLElement>(`[data-file-id="${currentFile.value.id}"]`);
    if (!list || !item) return;
    // Scroll only the file list. The statement table keeps its own position.
    list.scrollTop = Math.max(0, list.scrollTop + item.getBoundingClientRect().top - list.getBoundingClientRect().top - 30);
}
async function executionOpened() {
    await locateSidebarFile();
    await locateStatementFailure(true);
}
async function resumeExecutionFollow() {
    followExecution.value = true;
    const file = executionFiles.value.find(file => file.executeStatus === 'RUNNING');
    if (file) await selectExecutionFile(file);
    await locateSidebarFile();
}
async function viewFilteredFile(file: SQLExecutionFileFilter) {
    filteredFilesVisible.value = false;
    await selectExecutionFile(file, true);
}
watch(() => `${currentFile.value?.id || ''}|${executionFileGroups.value.map(group => `${group.key}:${group.files.map(file => file.id).join(',')}`).join('|')}`, () => { void locateSidebarFile(); }, { flush: 'post' });
watch(executionVisible, visible => { if (!visible) { filteredFilesVisible.value = false; executionDetailsVisible.value = false; } });
const pageSize = ref(30);
const detailLoading = ref(false);
const filtersVisible = ref(false);
const filter = ref({ version: '', schemaName: '', fileName: '', status:'' });
const options = ref<SQLExecuteOptions>({ skipExistsColumn: true, skipExistsTable: true, skipUniqueConstraint: true });
const importVisible = ref(false);
const importMode = ref<'local' | 'text' | 'server'>('local');
const importing = ref(false);
const reading = ref(false);
const overwrite = ref(true);
const fileInput = ref<HTMLInputElement | null>(null);
const localFiles = ref<{
    fileName: string;
    content: string;
}[]>([]);
const draft = ref({ fileName: '', content: '' });
const serverPath = ref('');
let timer: ReturnType<typeof setTimeout> | undefined;
let disposed = false;
let selection = 0;
let executionSession = 0;
let listRequest = 0;
let notifiedCompletedBatch = 0;
let retryAttempt = 0;
let notifiedRetryAttempt = 0;
const isActive = (file: SQLChangeFile) => ['QUEUED', 'RUNNING'].includes(file.executeStatus);
const busy = computed(() => mutating.value || savingConfig.value || submitting.value || executionBatch.value?.executeStatus === 'RUNNING' || executionFiles.value.some(isActive));
const continuationCandidates = computed(() => {
    const excludedIds = new Set(filteredFiles.value.map(file => file.id));
    const completedIds = new Set(executionBatch.value?.outcome === 'COMPLETED' ? executionBatchFiles.value.map(file => file.id) : []);
    return executionFiles.value.filter(file => !['SUCCESS', 'SKIPPED'].includes(file.executeStatus) && !excludedIds.has(file.id) && (!completedIds.has(file.id) || file.executeStatus === 'PENDING'));
});
const canExecute = computed(() => !savingStatement.value && !editingStatement.value && !busy.value && continuationCandidates.value.length > 0);
const executionReport = computed<SQLExecutionSummary | null>(() => {
    if (executionStartError.value) return { type: 'error', title: '本次执行未开始', progress: '执行请求未提交成功，请查看执行详情。', description: compactSQLExecutionMessage(executionStartError.value) };
    if (executionBatch.value) return describeSQLExecution(executionBatch.value, executionBatchFiles.value, filteredFiles.value);
    if (currentFile.value?.executeStatus === 'RUNNING') return { type: 'info', title: '正在执行 SQL', progress: '语句执行进度见下方执行信息。', description: compactSQLExecutionMessage(currentFile.value.executeMessage) };
    return null;
});
const executionCompleted = computed(() => !!executionBatch.value && executionBatch.value.executeStatus !== 'RUNNING' &&
    (executionBatch.value.outcome === 'COMPLETED' || executionBatch.value.executeStatus === 'SUCCESS'));
function showExecutionCompletion(result: Pick<SQLExecutionSummary, 'title' | 'description' | 'type'>) {
    void ElMessageBox.alert(result.description, result.title, {
        type: result.type, confirmButtonText: '知道了', closeOnClickModal: false,
        closeOnPressEscape: true, showClose: true,
    }).catch(() => {});
}
const executionDetailsMessage = computed(() => [
    pollError.value ? `进度刷新失败：${pollError.value}` : '',
    executionStartError.value || executionBatch.value?.executeMessage || currentFile.value?.executeMessage || '',
].filter(Boolean).join('\n\n'));
const executionReportFile = computed(() => {
    const location = executionReport.value?.location;
    if (!location) return undefined;
    return executionFiles.value.find(file => location.fileId ? file.id === location.fileId : file.fileName === location.fileName);
});
async function locateReportFailure() {
    const file = executionReportFile.value;
    const lineNumber = executionReport.value?.location?.lineNumber;
    if (!file) return;
    if (file.id !== currentFile.value?.id) await selectExecutionFile(file, true);
    if (currentFile.value?.id !== file.id) return;
    await locateSidebarFile();
    await locateStatementFailure(true, lineNumber);
}
const canEditStatement = computed(() => !!selectedStatement.value && !selectedStatement.value.viewDependencyTaskId && !busy.value && !detailLoading.value &&
    ['PENDING', 'FAILED', 'CANCELED', 'PARTIAL_FAILED'].includes(currentFile.value?.executeStatus || '') &&
    ['PENDING', 'FAILED'].includes(selectedStatement.value.executeStatus) &&
    !statements.value.some(row => ['RUNNING', 'UNKNOWN'].includes(row.executeStatus)));
function showFileEditor(file: SQLChangeFile, edit = false) {
    fileEditorStartEditing.value = edit;
    fileEditorFileId.value = file.id;
}
async function fileEditSaved(result: SQLFileEditResult) {
    const inSelection = executionFiles.value.some(file => file.id === result.file.id);
    if (currentFile.value?.id === result.file.id) {
        closeStatement();
        applyFile(result.file, result.statements || []);
    }
    if (inSelection) {
        executionBatch.value = null;
        executionStartError.value = pollError.value = '';
        followExecution.value = false;
        mergeExecutionFiles([result.file]);
    }
    if (!result.file.importMessage) filteredFiles.value = filteredFiles.value.filter(file => file.id !== result.file.id);
    await loadFiles(true);
}
function showStatement(row: SQLChangeStatement) {
    editingStatement.value = false;
    detailTab.value = 'sql';
    selectedStatement.value = row;
}
function startStatementEdit() {
    if (!canEditStatement.value || !selectedStatement.value) return;
    originalStatement.value = selectedStatement.value.sqlContent;
    statementDraft.value = originalStatement.value;
    editingStatement.value = true;
}
function closeStatement() {
    selectedStatement.value = null;
    editingStatement.value = false;
    statementDraft.value = '';
    originalStatement.value = '';
}
async function saveStatement() {
    const row = selectedStatement.value;
    if (!row || !canEditStatement.value || savingStatement.value) return;
    if (!statementDraft.value.trim()) { ElMessage.warning('请输入 SQL 内容'); return; }
    savingStatement.value = true;
    try {
        const beforeCount = statements.value.length;
        const result = await editSQLStatement(row.id, statementDraft.value, originalStatement.value);
        const splitCount = Math.max(1, (result.statements || []).length - beforeCount + 1);
        applyFile(result.file, result.statements || []);
        filteredFiles.value = filteredFiles.value.filter(file => file.id !== result.file.id);
        executionFiles.value = executionFiles.value.map(file => file.id === result.file.id ? result.file : file);
        editingStatement.value = false;
        if (splitCount > 1) {
            closeStatement();
            ElMessage.success(`已修正拆分为 ${splitCount} 条 SQL，均为未执行状态；请在列表中点击执行继续`);
        } else ElMessage.success(row.transactionStartLine?'SQL 已保存，所属事务组已重置，点击执行将从事务组开头重试':'SQL 已保存，点击执行可重试');
        await loadFiles(true);
    } finally { savingStatement.value = false; }
}
const statusText = (s: string) => ({ PARTIAL_SUCCESS:'部分执行成功',INCOMPLETE:'执行未完成', PENDING: '未执行', QUEUED: '等待执行', RUNNING: '执行中', SUCCESS: '成功', FAILED: '失败', PARTIAL_FAILED: '部分失败', SKIPPED: '已跳过', CANCELED: '未完成', UNKNOWN: '结果待核实' }[s] || s);
const statusType = (s: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' => s === 'SUCCESS' ? 'success' : s==='PARTIAL_SUCCESS'?'warning':['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(s) ? 'danger' : ['RUNNING', 'QUEUED'].includes(s) ? 'primary' : s === 'PENDING' ? 'warning' : 'info';
const canSelect = (f: SQLChangeFile) => !isActive(f)&&!['SUCCESS','SKIPPED','UNKNOWN'].includes(f.executeStatus)&&f.resultStatus!=='UNKNOWN';
async function loadFiles(quiet = false) {
    if(group.value==='history') return;
 if(group.value==='failed'){await failureList.value?.refresh();return;}
    const request = ++listRequest;
    if (!quiet)
        loading.value = true;
    try {
        const r = await (group.value === 'todo' ? listTodoSQLFiles : listDoneSQLFiles)(page.value, pageSize.value, filter.value);
        if (disposed || request !== listRequest)
            return;
        files.value = r.list || [];
        total.value = r.total;
    }
    finally {
        if (request === listRequest)
            loading.value = false;
    }
}
async function searchFiles() { if(group.value!=='done')filter.value.status=''; page.value = 1; selectedFiles.value = []; await loadFiles(); }
function clearPolling() { if (timer)
    clearTimeout(timer); timer = undefined; }
function applyFile(file: SQLChangeFile, rows: SQLChangeStatement[]) {
    currentFile.value = file;
    statements.value = rows;
    mergeExecutionFiles([file]);
    if (selectedStatement.value) selectedStatement.value = rows.find(s => s.id === selectedStatement.value?.id) || null;
    void locateStatementFailure();
}
function mergeExecutionFiles(updates: SQLChangeFile[]) {
    const byId = new Map(updates.map(file => [file.id, file]));
    const existing = new Set(executionFiles.value.map(file => file.id));
    executionFiles.value = executionFiles.value.map(file => byId.get(file.id) || file)
        .concat(updates.filter(file => !existing.has(file.id))).sort(compareSQLFiles);
}
async function selectExecutionFile(file: Pick<SQLChangeFile, 'id'>, manual = false) {
    if (manual) followExecution.value = false;
    const selected = ++selection;
    focusedError = '';
    statementTable.value?.setScrollTop(0);
    detailLoading.value = true;
    try {
        const r = await getSQLFile(file.id);
        if (disposed || selected !== selection)
            return;
        selectedStatement.value = null;
        applyFile(r.file, r.statements || []);
    }
    finally {
        if (selected === selection)
            detailLoading.value = false;
    }
}
async function openExecution(rows: SQLChangeFile[]) {
    if (submitting.value||mutating.value||savingConfig.value)
        return;
    if (!rows.length) {
        ElMessage.warning('请选择 SQL 文件');
        return;
    }
    clearPolling();
    executionSession++;
    selection++;
    currentFile.value = null;
    statements.value = [];
    executionBatch.value = null;
    executionBatchFiles.value = [];
    executionStartError.value = '';
    pollError.value = '';
    lastPollAt.value = '';
    followExecution.value = false;
    filteredFiles.value = [];
    filteredFilesVisible.value = false;
    executionDetailsVisible.value = false;
    executionFiles.value = [...rows].sort(compareSQLFiles);
    executionVisible.value = true;
    const active = rows.find(isActive);
    const associated = active || (rows.length === 1 ? rows[0] : undefined);
    if (associated?.executionBatchId) {
        const r = await getSQLBatch(associated.executionBatchId,true);
        executionBatch.value = r.batch;
        executionBatchFiles.value = r.files;
        mergeExecutionFiles(r.files);
        filteredFiles.value = r.batch.filteredFiles || [];
        followExecution.value = r.batch.executeStatus === 'RUNNING';
    }
    await selectExecutionFile(executionFiles.value.find(file => file.executeStatus === 'RUNNING') || active || associated || executionFiles.value[0]!);
    if (busy.value)
        schedulePoll();
}
async function refreshExecution() {
    const session = executionSession;
    const retry = retryAttempt;
    if (executionBatch.value) {
        const wasRunning = executionBatch.value.executeStatus === 'RUNNING';
        const r = await getSQLBatch(executionBatch.value.id,true);
        if (disposed || session !== executionSession)
            return;
        executionBatch.value = r.batch;
        executionBatchFiles.value = r.files;
        if (r.batch.filteredFiles && wasRunning) filteredFiles.value = r.batch.filteredFiles;
        mergeExecutionFiles(r.files);
        // Follow the active file, then the last file reached by this run.
        // An older deferred failure must not pull focus back to the start.
        const reached = r.files.filter(file => !['QUEUED', 'PENDING', 'CANCELED'].includes(file.executeStatus));
        const next = r.files.find(file => file.executeStatus === 'RUNNING') ||
            (r.batch.executeStatus !== 'RUNNING' ? r.files.find(file => file.id === r.batch.stoppedFileId) || reached[reached.length - 1] : undefined);
        if (followExecution.value && next && next.id !== currentFile.value?.id && executionVisible.value && !selectedStatement.value && !editingStatement.value) {
            await selectExecutionFile(next);
        }
        if (wasRunning && r.batch.executeStatus !== 'RUNNING') {
            followExecution.value = false;
            const result = describeSQLExecution(r.batch, r.files, filteredFiles.value);
            if (executionCompleted.value) {
                if (notifiedCompletedBatch !== r.batch.id) {
                    notifiedCompletedBatch = r.batch.id;
                    showExecutionCompletion(result);
                }
            } else ElMessage({ message: result.title, type: result.type, duration: 6000 });
        }
    }
    else {
        const rows = await Promise.all(executionFiles.value.map(f => getSQLFile(f.id)));
        if (disposed || session !== executionSession)
            return;
        mergeExecutionFiles(rows.map(r => r.file));
    }
    const id = currentFile.value?.id;
    const selected = selection;
    if (id) {
        const wasRunning = currentFile.value?.executeStatus === 'RUNNING';
        const unfinished = new Set(statements.value.filter(row => !['SUCCESS', 'SKIPPED'].includes(row.executeStatus)).map(row => row.id));
        const r = await getSQLFile(id);
        if (!disposed && session === executionSession && selected === selection) {
            applyFile(r.file, r.statements || []);
            // A single-statement/group retry can finish while other statements
            // in the file remain pending or deferred. Report only new results.
            if (!executionBatch.value && wasRunning && !isActive(r.file) && retry === retryAttempt && retry !== notifiedRetryAttempt) {
                const completed = (r.statements || []).filter(row => unfinished.has(row.id));
                const succeeded = completed.filter(row => row.executeStatus === 'SUCCESS').length;
                const skipped = completed.filter(row => row.executeStatus === 'SKIPPED').length;
                if (succeeded || skipped) {
                    notifiedRetryAttempt = retry;
                    showExecutionCompletion({
                        type: 'success', title: 'SQL 重试完成',
                        description: `本次成功 ${succeeded} 条 SQL，跳过 ${skipped} 条。文件中其他语句的状态请查看执行列表。`,
                    });
                }
            }
        }
    }
    await loadFiles(true);
    pollError.value = '';
    lastPollAt.value = new Date().toISOString();
}
function schedulePoll() {
    clearPolling();
    const session = executionSession;
    timer = setTimeout(async () => { if (disposed || session !== executionSession)
        return; try {
        await refreshExecution();
        if (!disposed && session === executionSession && busy.value)
            schedulePoll();
    }
    catch (error) {
        pollError.value = error instanceof Error ? error.message : '请求失败';
        if (!disposed && session === executionSession)
            schedulePoll();
    } }, 1000);
}
async function executeFiles() {
    if (!canExecute.value)
        return;
    submitting.value = true;
    executionStartError.value = '';
    pollError.value = '';
    try {
        const ids = executionFiles.value.filter(f => !['SUCCESS', 'SKIPPED'].includes(f.executeStatus)).map(f => f.id);
        const r = await executeSelectedSQLFiles(ids, { ...options.value });
        executionBatch.value = r.batch;
        executionBatchFiles.value = r.files;
        mergeExecutionFiles(r.files);
        followExecution.value = true;
        filteredFiles.value = r.batch.filteredFiles || [];
        if (filteredFiles.value.length) ElMessage.warning(`已暂缓 ${filteredFiles.value.length} 个文件，其余文件继续执行`);
        if (r.files[0]) await selectExecutionFile(r.files[0]);
        ElMessage.success('已开始执行未执行语句，已有失败保留供单独重试');
        schedulePoll();
        await loadFiles(true);
    }
    catch (error) {
        executionStartError.value = error instanceof Error ? error.message : '提交执行请求失败';
    }
    finally {
        submitting.value = false;
    }
}
async function stopFile() {
    if(stopping.value) return;
    stopping.value=true;
    try {
        if(executionBatch.value) executionBatch.value=await cancelSQLFileSequence(executionBatch.value.id);
        else {const file=executionFiles.value.find(f=>f.executeStatus==='RUNNING');if(!file)return;await cancelSQLFile(file.id);}
        ElMessage.info('停止请求已保存；当前语句结束后停止，尚未提交的显式事务组将整体回滚');
    } finally {stopping.value=false;}
}
function showExecutionConfig(file:SQLChangeFile){configFile.value=file;schemaDraft.value=file.schemaName||'';}
async function configureFilteredFile(id:number){
    const result=await getSQLFile(id);
    filteredFilesVisible.value=false;
    showExecutionConfig(result.file);
}
async function saveExecutionConfig(){
    const file=configFile.value;if(!file||savingConfig.value||mutating.value)return;
    if(!schemaDraft.value.trim()){ElMessage.warning('请输入执行账号/schema');return;}
    savingConfig.value=true;
    try {
        const result=await setSQLFileExecutionConfig(file.id,schemaDraft.value.trim(),file.schemaName||'');
        if(currentFile.value?.id===file.id)applyFile(result.file,result.statements||[]);
        executionFiles.value=executionFiles.value.map(f=>f.id===file.id?result.file:f);
        if(!result.file.importMessage)filteredFiles.value=filteredFiles.value.filter(f=>f.id!==file.id);
        configFile.value=null;
        await loadFiles(true);
        ElMessage.success('执行配置已保存，账号连接和 schema 权限校验通过');
    }finally{savingConfig.value=false;}
}
async function skipFile(file: SQLChangeFile) {
    if (isActive(file)||mutating.value||savingConfig.value)
        return;
    try {
        await ElMessageBox.confirm(`将 ${file.fileName} 标记为不执行？`, '标记为不执行', { type: 'warning' });
    }
    catch {
        return;
    }
    mutating.value=true;
    try {await skipSQLFile(file.id);await loadFiles();if(executionVisible.value)await refreshExecution();}
    finally{mutating.value=false;}
}
async function skipStatement(row: SQLChangeStatement) {
    if (busy.value)
        return;
    try {
        await ElMessageBox.confirm(`${row.transactionStartLine?`将${transactionScope(row)}整体标记为不执行？组内所有语句都会跳过。`:`将第 ${row.lineNumber} 行标记为不执行？`}${row.executeStatus === 'UNKNOWN' ? '请先核实整个事务的数据库执行结果。' : ''}`, '标记为不执行', { type: 'warning' });
    }
    catch {
        return;
    }
    mutating.value=true;
    try{await skipSQLStatement(row.id);await refreshExecution();}
    finally{mutating.value=false;}
}
function canMarkFileFailed(file: SQLChangeFile) {
    return !isActive(file) && !['SUCCESS', 'SKIPPED', 'UNKNOWN'].includes(file.executeStatus) && !file.unknownStatements;
}
async function markFileFailed(file: SQLChangeFile) {
    if (busy.value || !canMarkFileFailed(file)) return;
    try {
        await ElMessageBox.confirm(
            `将 ${file.fileName} 中尚未完成的 SQL 一次性标记失败并暂缓？原有错误、成功、已跳过及已关联视图任务的记录保留。此次不会执行 SQL，也不会自动继续后续文件；标记后的语句可在“失败 SQL”中处理。`,
            '整文件标记失败', { type: 'warning', confirmButtonText: '标记失败并暂缓' },
        );
    } catch { return; }
    if (busy.value) return;
    mutating.value = true;
    try {
        const result = await markSQLFileFailed(file.id);
        if (disposed) return;
        if (executionFiles.value.some(item => item.id === file.id)) {
            clearPolling();
            executionSession++;
            executionBatch.value = null;
            executionBatchFiles.value = [];
            executionStartError.value = pollError.value = '';
            followExecution.value = false;
        }
        if (currentFile.value?.id === file.id) {
            selection++;
            detailLoading.value = false;
            applyFile(result.file, result.statements || []);
        } else mergeExecutionFiles([result.file]);
        await loadFiles(true);
        ElMessage.success(`已暂缓 ${result.deferredCount} 条 SQL（本次新标记 ${result.markedCount} 条），可点击“查看本文件失败 SQL”继续处理`);
    } finally { mutating.value = false; }
}
async function showFileFailures(file: SQLChangeFile) {
    if (busy.value) return;
    executionVisible.value = false;
    group.value = 'failed';
    await nextTick();
    await failureList.value?.showFile(file.id, file.fileName);
}
function canSelectViewTransferStatement(row: SQLChangeStatement) {
    return !busy.value && !row.viewDependencyTaskId &&
        ['', 'PENDING', 'FAILED', 'PARTIAL_FAILED', 'TIMEOUT', 'CANCELED', 'NOT_EXECUTABLE', 'BLOCKED'].includes(row.executeStatus);
}
function previewViewTransfer(all = false) {
    if (busy.value || detailLoading.value || !currentFile.value) return;
    if (!all && !selectedTransferStatements.value.length) return;
    viewTransferTarget.value = { fileId: currentFile.value.id, all,
        statementIds: all ? undefined : selectedTransferStatements.value.map(row => row.id) };
}
async function viewTransferCreated(task: SQLViewDependencyTask) {
    viewTransferTarget.value = null;
    executionVisible.value = false;
    await router.push({name:'PostgreSQLViewDependencyChange',query:{taskId:String(task.id)}});
}
async function markStatementFailed(row: SQLChangeStatement) {
    if (busy.value || row.manualFailure || !['PENDING', 'FAILED', 'CANCELED', 'PARTIAL_FAILED', 'NOT_EXECUTABLE'].includes(row.executeStatus)) return;
    try {
        await ElMessageBox.confirm(`${row.transactionStartLine?`将${transactionScope(row)}整体标记为失败并暂缓？`:`将第 ${row.lineNumber} 行标记为失败并暂缓？`}确认后会自动继续事务组之后的 SQL 和后续文件；暂缓的内容可稍后${row.transactionStartLine?'整组':'单独'}重试。`, '标记失败并继续', { type: 'warning' });
    } catch { return; }
    mutating.value = true;
    try {
        await markSQLStatementFailed(row.id);
        await refreshExecution();
        ElMessage.success('已加入失败清单，正在自动继续后续 SQL');
    } finally { mutating.value = false; }
    await executeFiles();
}
function showRetryAccount(row: SQLChangeStatement) {
    if (busy.value || row.executeStatus !== 'FAILED' || !currentFile.value) return;
    retryAccountTarget.value = {
        statementId: row.id, fileId: row.fileId, fileName: currentFile.value.fileName,
        lineNumber: row.lineNumber, schemaName: currentFile.value.schemaName,
        originalAccount: row.databaseUser || currentFile.value.schemaName,
        transactionLabel: row.transactionStartLine ? transactionScope(row) : undefined,
    };
}
function applyRetryResult(result: {file: SQLChangeFile; statements: SQLChangeStatement[]}) {
    clearPolling();executionSession++;selection++;executionStartError.value='';pollError.value='';executionBatchFiles.value=[];
    retryAttempt++;
    executionBatch.value = null;
    followExecution.value = false;
    applyFile(result.file, result.statements || []);
    executionFiles.value = executionFiles.value.map(file => file.id === result.file.id ? result.file : file);
    schedulePoll();
}
async function executeFailedStatement(row: SQLChangeStatement) {
    if (busy.value || row.executeStatus !== 'FAILED' || !currentFile.value) return;
    try {
        await ElMessageBox.confirm(row.transactionStartLine?`重新执行${transactionScope(row)}？将从 BEGIN 开始执行整个事务组，不执行组外语句。`:`单独重试第 ${row.lineNumber} 行 SQL？若属于同名视图的 DROP + CREATE 组合，将在事务中重试整组。`, row.transactionStartLine?'整组重试':'单独重试 SQL', { type: 'warning' });
    } catch { return; }
    submitting.value = true;
    try {
        const result = await executeFailedSQLStatement(row.id, { ...options.value });
        applyRetryResult(result);
    } finally { submitting.value = false; }
}
async function openFailureFile(id:number){const r=await getSQLFile(id);await openExecution([r.file]);}
function failureRetryStarted(result:{file:SQLChangeFile;statements:SQLChangeStatement[]}){executionFiles.value=[result.file];executionVisible.value=true;applyRetryResult(result);}
function transferStatement(row: SQLChangeStatement) {
    if (busy.value) return;
    viewTransferTarget.value = {fileId: row.fileId, statementIds: [row.id]};
}
async function removeFile(file: SQLChangeFile) { try {
    await ElMessageBox.confirm(`删除 ${file.fileName} 的文件记录？`, '删除文件', { type: 'warning' });
}
catch {
    return;
} await deleteSQLFile(file.id); await loadFiles(); }
function showImport(mode: 'local' | 'text' | 'server') { importMode.value = mode; importVisible.value = true; }
async function readLocalFiles(event: Event) {
    const input = event.target as HTMLInputElement;
    const selected = Array.from(input.files || []);
    input.value = '';
    localFiles.value = [];
    reading.value = true;
    try {
        const result: {
            fileName: string;
            content: string;
        }[] = [];
        const names = new Set<string>();
        let size = 0;
        const add = (fileName: string, content: string) => {
            if (names.has(fileName))
                throw new Error(`存在同名 SQL 文件：${fileName}`);
            names.add(fileName);
            size += new TextEncoder().encode(content).length;
            if (result.length >= 500 || size > 50 * 1024 * 1024)
                throw new Error('最多导入 500 个 SQL 文件，总大小不超过 50MB');
            result.push({ fileName, content });
        };
        for (const file of selected) {
            if (file.size > 50 * 1024 * 1024)
                throw new Error(`${file.name} 超过 50MB`);
            if (file.name.toLowerCase().endsWith('.zip')) {
                const zip = await JSZip.loadAsync(file);
                for (const entry of Object.values(zip.files)) {
                    if (entry.dir || !entry.name.toLowerCase().endsWith('.sql'))
                        continue;
                    add(entry.name.split('/').pop() || entry.name, await entry.async('string'));
                }
            }
            else if (file.name.toLowerCase().endsWith('.sql'))
                add(file.name, await file.text());
            else
                throw new Error('仅支持 SQL 或 ZIP 文件');
        }
        localFiles.value = result;
        if (!result.length)
            ElMessage.warning('没有找到 SQL 文件');
    }
    catch (err) {
        ElMessage.error(err instanceof Error ? err.message : '读取 SQL 文件失败');
    }
    finally {
        reading.value = false;
    }
}
async function importFiles() {
    importing.value = true;
    try {
        let importedCount = 0;
        if (importMode.value === 'server') {
            if (!serverPath.value.trim()) {
                ElMessage.warning('请输入服务器文件路径');
                return;
            }
            const result = await importServerSQL(serverPath.value.trim(), overwrite.value);
            importedCount = result.count;
        }
        else if (importMode.value === 'text') {
            if (!draft.value.fileName.trim() || !draft.value.content.trim()) {
                ElMessage.warning('请输入文件名和 SQL 内容');
                return;
            }
            await parseSQLFile({ ...draft.value, fileName: draft.value.fileName.trim(), overwrite: overwrite.value });
            importedCount = 1;
        }
        else {
            if (!localFiles.value.length) {
                ElMessage.warning('请选择 SQL 或 ZIP 文件');
                return;
            }
            const result = await parseSQLBatch({ files: localFiles.value, overwrite: overwrite.value });
            importedCount = result.files.length;
        }
        importVisible.value = false;
        group.value = 'todo';
        page.value = 1;
        filter.value = { version: '', schemaName: '', fileName: '', status:'' };
        await loadFiles();
        ElMessage.success(`已上传 ${importedCount} 个 SQL 文件`);
    }
    finally {
        importing.value = false;
    }
}
onMounted(async () => { await loadFiles(); const settings = await getSettings();settingsStore.customerEnv=(settings.customer_env||'').trim();settingsStore.loaded=true; if(route.query.fileId){group.value='done';await loadFiles();await openFailureFile(Number(route.query.fileId));} options.value = { skipExistsColumn: settings.sql_execute_skip_exists_column !== 'false', skipExistsTable: settings.sql_execute_skip_exists_table !== 'false', skipUniqueConstraint: settings.sql_execute_skip_unique_constraint !== 'false' }; });
onBeforeUnmount(() => { disposed = true; selection++; clearPolling(); });
</script>
<template>
 <div class="sql-page viewport-table-page">
  <el-tabs v-model="group" class="file-tabs" @tab-change="searchFiles"><el-tab-pane label="未执行文件" name="todo"/><el-tab-pane label="已执行文件" name="done"/><el-tab-pane label="失败 SQL" name="failed"/><el-tab-pane label="执行记录" name="history"/></el-tabs>
  <SQLExecutionHistory v-if="group==='history'" @show-current-failures="group='failed'"/>
 <SQLCurrentFailures v-else-if="group==='failed'" ref="failureList" :options="options" :busy="busy" @open-file="openFailureFile" @started="failureRetryStarted" @changed="loadFiles(true)"/>
  <template v-else>
  <div class="file-toolbar">
   <el-button v-if="group==='todo'&&settingsStore.isLocal" type="primary" @click="showImport('server')">导入服务器文件</el-button><el-button v-if="group==='todo'" type="primary" @click="showImport('local')">上传SQL文件</el-button><el-button type="danger" @click="openExecution(selectedFiles)">{{group==='done'?'继续未执行 SQL':'执行SQL文件'}}</el-button><span v-if="group==='todo'" class="order-note">从前往后按顺序执行</span><div class="toolbar-spacer"/><el-button @click="loadFiles()">刷新</el-button><el-button :type="filtersVisible?'primary':'default'" @click="filtersVisible=!filtersVisible">筛选</el-button>
  </div>
  <div v-if="filtersVisible" class="filters"><el-input v-model="filter.version" placeholder="版本号" clearable @keyup.enter="searchFiles"/><el-input v-model="filter.schemaName" placeholder="数据库 / schema" clearable @keyup.enter="searchFiles"/><el-input v-model="filter.fileName" placeholder="文件名称" clearable @keyup.enter="searchFiles"/><el-select v-if="group==='done'" v-model="filter.status" placeholder="执行结果" clearable style="width:170px"><el-option v-for="value in ['SUCCESS','PARTIAL_SUCCESS','FAILED','INCOMPLETE','RUNNING','QUEUED','CANCELED','UNKNOWN','SKIPPED']" :key="value" :value="value" :label="statusText(value)"/></el-select><el-button type="primary" @click="searchFiles">查询</el-button></div>
  <div class="table-wrapper" v-loading="loading">
   <el-table :data="files" row-key="id" height="100%" stripe @selection-change="selectedFiles=$event">
    <el-table-column type="selection" width="44" :selectable="canSelect"/><el-table-column type="index" label="#" width="60" :index="(i:number)=>(page-1)*pageSize+i+1"/>
    <el-table-column prop="version" label="版本号" width="165" show-overflow-tooltip/><el-table-column label="数据库" width="140" show-overflow-tooltip><template #default="{row}">{{row.schemaName||'未识别'}}</template></el-table-column>
    <el-table-column label="文件名称" min-width="330"><template #default="{row}"><el-button link type="primary" class="file-link" @click="openExecution([row])">{{row.fileName}}</el-button></template></el-table-column>
    <el-table-column label="文件上传时间" width="170"><template #default="{row}">{{formatTime(row.createdAt)}}</template></el-table-column><el-table-column label="上传人" width="90"><template #default="{row}">{{row.createdUser||'—'}}</template></el-table-column>
    <el-table-column label="执行时间" width="170"><template #default="{row}">{{row.executeTime?formatTime(row.executeTime):'—'}}</template></el-table-column><el-table-column label="执行人" width="90"><template #default="{row}">{{row.executeUser||'—'}}</template></el-table-column>
    <el-table-column label="状态" width="105"><template #default="{row}"><el-tooltip :disabled="!row.importMessage||row.executeStatus!=='PENDING'" :content="row.importMessage"><el-tag :type="statusType(row.resultStatus||row.executeStatus)" size="small">{{group==='todo'&&row.executeStatus==='PENDING'&&row.importMessage?'待处理':(row.resultStatus==='SUCCESS'?'执行成功':row.resultStatus==='FAILED'?'执行失败':statusText(row.resultStatus||row.executeStatus))}}</el-tag></el-tooltip></template></el-table-column>
    <el-table-column label="语句结果" width="290"><template #default="{row}">成功 {{row.successStatements||0}} / 失败 {{row.failedStatements||0}} / 未执行 {{row.pendingStatements||0}} / 跳过 {{row.skippedStatements||0}}<span v-if="row.unknownStatements"> / 待核实 {{row.unknownStatements}}</span></template></el-table-column>
    <el-table-column label="操作" width="430" fixed="right"><template #default="{row}"><div class="row-actions"><el-button v-if="group==='done'" link type="primary" @click="openExecution([row])">查看／继续</el-button><el-button link type="primary" :disabled="busy||isActive(row)||!['PENDING','FAILED','CANCELED','PARTIAL_FAILED'].includes(row.executeStatus)" @click="showFileEditor(row,true)">编辑文件</el-button><el-button v-if="!row.schemaName||(row.schemaConfigured&&row.executeStatus==='PENDING')" link type="primary" :disabled="isActive(row)||mutating||savingConfig" @click="showExecutionConfig(row)">执行配置</el-button><el-button link type="primary" :disabled="mutating||savingConfig||isActive(row)||row.executeStatus==='SKIPPED'" @click="skipFile(row)">标记为不执行</el-button><el-button link type="danger" :disabled="mutating||savingConfig||isActive(row)||row.executeStatus==='UNKNOWN'" @click="removeFile(row)">删除</el-button></div></template></el-table-column>
   </el-table>
  </div>
  <div class="pagination-row"><el-pagination v-model:current-page="page" v-model:page-size="pageSize" :page-sizes="[20,30,50,100]" :total="total" layout="total, sizes, prev, pager, next, jumper" @size-change="searchFiles" @current-change="loadFiles()"/></div>
  </template>
  <el-dialog v-model="executionVisible" :fullscreen="fullscreen" width="92%" class="sql-execution-dialog" :show-close="false" :close-on-click-modal="false" destroy-on-close @opened="executionOpened">
   <template #header><div class="execution-header"><strong>SQL执行</strong><div><el-button v-if="failedStatement||failedExecutionFile" link type="danger" @click="locateExecutionFailure">定位错误</el-button><el-button v-if="executionCompleted" link @click="executionDetailsVisible=true">查看执行结果</el-button><el-button link :disabled="!currentFile" @click="currentFile&&showFileEditor(currentFile)">完整SQL / 编辑</el-button><el-button link :aria-label="fullscreen?'还原窗口':'最大化窗口'" @click="fullscreen=!fullscreen">{{fullscreen?'还原':'最大化'}}</el-button><el-button link aria-label="关闭SQL执行" @click="executionVisible=false">关闭</el-button></div></div></template>
   <section v-if="pollError" class="execution-status-bar" aria-label="执行进度刷新状态">
    <div class="execution-status-content"><strong>进度刷新失败</strong><span>后台可能仍在执行，当前状态可能已过期。<template v-if="lastPollAt">上次刷新：{{formatTime(lastPollAt)}}</template></span></div>
    <div class="execution-status-actions"><el-button link type="primary" @click="executionDetailsVisible=true">查看执行详情</el-button><el-button link type="primary" @click="refreshExecution">重试刷新</el-button></div>
   </section>
   <section v-if="executionReport&&(!executionCompleted||executionStartError)" class="execution-status-bar" aria-label="SQL执行状态">
    <div class="execution-status-content"><strong :title="executionReport.title">{{executionReport.title}}</strong><span :title="executionReport.progress">{{executionReport.progress}}</span></div>
    <div class="execution-status-actions"><el-button v-if="executionReportFile" link type="primary" @click="locateReportFailure">定位上次停止位置</el-button><el-button link type="primary" @click="executionDetailsVisible=true">查看执行详情</el-button></div>
   </section>
   <div class="execution-workbench">
    <aside class="execution-files">
     <div class="sidebar-section-title">SQL 文件</div>
     <div v-if="currentFile" class="current-file-card" aria-label="右侧当前 SQL 文件">
      <div class="current-file-label"><strong>{{currentFile.executeStatus==='RUNNING'?'当前执行文件':'当前查看文件'}}</strong><el-tag :type="statusType(currentFile.executeStatus)" size="small">{{statusText(currentFile.executeStatus)}}</el-tag></div>
      <button class="current-file-name" :title="currentFile.fileName" @click="locateSidebarFile">{{currentFile.fileName}}</button>
      <el-button v-if="executionBatch?.executeStatus==='RUNNING'&&!followExecution" link type="primary" @click="resumeExecutionFollow">跟随正在执行的文件</el-button>
     </div>
     <div ref="executionFileList" class="execution-file-list">
      <section v-for="groupItem in executionFileGroups" :key="groupItem.key" class="execution-file-group">
       <div class="sidebar-section-title statement-group-title"><span>{{groupItem.label}}</span><el-tag size="small" :type="groupItem.type">{{groupItem.files.length}}</el-tag></div>
       <button v-for="file in groupItem.files" :key="file.id" class="execution-file" :class="{active:currentFile?.id===file.id}" :data-file-id="file.id" :aria-current="currentFile?.id===file.id?'true':undefined" :title="file.fileName" @click="selectExecutionFile(file,true)"><span>{{file.fileName}}</span><el-tag :type="statusType(file.executeStatus)" size="small">{{file.executeStatus==='PENDING'&&file.importMessage?'待处理':statusText(file.executeStatus)}}</el-tag></button>
      </section>
     </div>
    </aside>
    <main class="statement-panel" v-loading="detailLoading">
     <div class="statement-file-header">
      <div v-if="currentFile" class="statement-file-context"><span class="statement-file-label">{{currentFile.executeStatus==='RUNNING'?'正在执行 SQL 文件':'当前 SQL 文件'}}</span><strong>{{currentFile.fileName}}</strong><span class="statement-file-schema">默认账号 / schema：{{currentFile.schemaName||'未配置'}}<template v-if="transactionGroupCount"> · {{transactionGroupCount}} 个事务组，按整组提交或回滚</template></span></div>
      <div v-if="currentFile" class="file-handling-actions"><el-button type="primary" plain size="small" :disabled="busy||detailLoading||!canMarkFileFailed(currentFile)" @click="previewViewTransfer(true)">整文件转入视图依赖</el-button><el-button type="primary" size="small" :disabled="busy||detailLoading||!selectedTransferStatements.length" @click="previewViewTransfer()">选中 {{selectedTransferStatements.length}} 条转入</el-button><el-button type="warning" plain size="small" :disabled="busy||detailLoading||!canMarkFileFailed(currentFile)" @click="markFileFailed(currentFile)">整文件标记失败</el-button><el-button size="small" :disabled="busy||detailLoading||!statements.some(isStatementError)" @click="showFileFailures(currentFile)">查看本文件失败 SQL</el-button></div>
      <el-button v-if="filteredFiles.length" type="warning" plain size="small" @click="filteredFilesVisible=true">未入队 {{filteredFiles.length}} 个 · 查看原因</el-button>
     </div>
     <el-alert v-if="currentFile?.importMessage" :title="currentFile.importMessage" type="warning" :closable="false" class="file-error"><el-button v-if="!currentFile.schemaName" link type="primary" :disabled="savingConfig||mutating" @click="showExecutionConfig(currentFile)">配置执行账号/schema</el-button></el-alert>

     <el-alert v-if="currentFile&&!failedStatement&&['FAILED','UNKNOWN','PARTIAL_FAILED'].includes(currentFile.executeStatus)" :title="currentFile.executeMessage||'执行失败，请查看语句结果'" type="error" :closable="false" class="file-error"/>
     <el-table ref="statementTable" :data="statements" :row-class-name="statementRowClass" stripe height="100%" row-key="id" @row-dblclick="showStatement" @selection-change="selectedTransferStatements=$event">
      <el-table-column type="selection" width="44" :selectable="canSelectViewTransferStatement" />
      <el-table-column type="index" label="#" width="60"/><el-table-column prop="lineNumber" label="line" width="55"/><el-table-column v-if="transactionGroupCount" label="事务组" width="130"><template #default="{row}"><el-tag v-if="row.transactionStartLine" size="small" type="info">{{row.transactionStartLine}}–{{row.transactionEndLine}} 行</el-tag><span v-else>—</span></template></el-table-column><el-table-column label="状态" width="110"><template #default="{row}"><el-tag :type="statusType(row.executeStatus)" size="small">{{(row.viewDependencyTaskId&&row.executeStatus==='FAILED'?'待视图依赖处理':transactionStatementStatus(row))||(row.executeStatus==='SKIPPED'?(row.skipKind==='MANUAL'?'手动跳过':row.skipKind==='AUTO'?'自动跳过':'已跳过'):statusText(row.executeStatus))}}</el-tag></template></el-table-column>
      <el-table-column v-if="statements.some(row=>row.databaseUser&&row.databaseUser!==currentFile?.schemaName)" prop="databaseUser" label="连接账号" width="145"/><el-table-column label="sql" min-width="380" show-overflow-tooltip><template #default="{row}"><button class="sql-preview" @click="showStatement(row)">{{row.sqlContent.replace(/\s+/g,' ')}}</button></template></el-table-column><el-table-column label="执行信息" min-width="350"><template #default="{row}"><button v-if="isStatementError(row)" class="statement-error-message" :aria-label="`查看第 ${row.lineNumber} 行错误`" @click="showStatement(row)">{{row.executeMessage||'执行结果异常，请查看详情'}}</button><span v-else class="statement-result-text" :title="row.executeMessage">{{row.executeMessage}}</span></template></el-table-column>
      <el-table-column label="操作" width="500" fixed="right"><template #default="{row}"><div class="row-actions"><el-button link type="primary" @click="showStatement(row)">详情</el-button><template v-if="!row.viewDependencyTaskId"><el-button link type="warning" :disabled="busy||row.manualFailure||!['PENDING','FAILED','CANCELED','PARTIAL_FAILED','NOT_EXECUTABLE'].includes(row.executeStatus)" @click="markStatementFailed(row)">{{row.manualFailure?'已标记失败':row.transactionStartLine?'整组标记失败':'标记失败并继续'}}</el-button><el-button v-if="row.executeStatus==='FAILED'" link type="primary" :disabled="busy" @click="executeFailedStatement(row)">{{row.transactionStartLine?'整组重试':'单独重试'}}</el-button><el-button v-if="row.executeStatus==='FAILED'" link type="primary" :disabled="busy" @click="showRetryAccount(row)">切换账号重试</el-button><el-button link type="primary" :disabled="busy||['SUCCESS','SKIPPED','RUNNING'].includes(row.executeStatus)||currentFile?.executeStatus==='SUCCESS'" @click="skipStatement(row)">{{row.transactionStartLine?'整组不执行':'标记为不执行'}}</el-button><el-button v-if="row.executeStatus==='FAILED'&&/^ALTER\s+TABLE\b/i.test(row.sqlContent)" link type="warning" :disabled="busy" @click="transferStatement(row)">转入视图依赖</el-button></template><el-button v-else link type="primary" @click="router.push({name:'PostgreSQLViewDependencyChange',query:{taskId:String(row.viewDependencyTaskId)}})">查看关联视图任务 #{{row.viewDependencyTaskId}}</el-button></div></template></el-table-column>
     </el-table>
    </main>
   </div>
   <template #footer><div class="execution-footer"><span class="continuation-note">只执行未执行语句；已有失败保留，需单独重试</span><span>跳过：</span><el-checkbox v-model="options.skipExistsTable" :disabled="busy">已存在的表 / 索引 / 约束</el-checkbox><el-checkbox v-model="options.skipExistsColumn" :disabled="busy">已存在的字段</el-checkbox><el-checkbox v-model="options.skipUniqueConstraint" :disabled="busy">唯一约束冲突</el-checkbox><el-button type="primary" :disabled="!canExecute" :loading="submitting" @click="executeFiles">执行 / 继续后续 SQL</el-button><el-button v-if="executionBatch?.executeStatus==='RUNNING'||executionFiles.some(f=>f.executeStatus==='RUNNING')" type="warning" :disabled="executionBatch?.stopRequested" :loading="stopping" @click="stopFile">停止</el-button><el-button @click="refreshExecution">刷新</el-button><el-button @click="executionVisible=false">关闭</el-button></div></template>
  </el-dialog>
  <el-dialog v-model="executionDetailsVisible" title="执行结果详情" width="900px" append-to-body>
   <div class="execution-result-details">
    <p class="execution-record-note">这是该批次执行时保存的记录。已暂缓、编辑或重试的语句，请以表格中的当前状态和语句执行记录为准。</p>
    <p v-if="executionReport">{{executionReport.title}}。{{executionReport.description}}</p>
    <p v-if="executionBatch?.finishedAt">结束时间：{{formatTime(executionBatch.finishedAt)}}</p>
    <h4>原始执行信息</h4><pre class="execution-message">{{executionDetailsMessage||'该批次未保存原始执行信息。'}}</pre>
   </div>
   <template #footer><el-button v-if="executionReportFile" type="primary" @click="executionDetailsVisible=false;locateReportFailure()">定位上次停止位置</el-button><el-button @click="executionDetailsVisible=false">关闭</el-button></template>
  </el-dialog>
  <el-drawer v-model="filteredFilesVisible" title="未入队文件：查看原因与处理方式" size="min(760px, 92vw)" append-to-body>
   <p class="filtered-explanation">以下文件未加入本轮执行队列，不影响其他文件执行。请按原因处理后重新提交；已有失败的语句保留在失败清单中，可单独重试。</p>
   <div class="filtered-reason-list"><div v-for="file in filteredFiles" :key="file.id" class="filtered-reason-row"><el-button link type="primary" :disabled="mutating||savingConfig" @click="viewFilteredFile(file)">{{file.fileName}}</el-button><span>{{file.reason}}</span><el-button v-if="file.code==='MISSING_SCHEMA'" link type="primary" :disabled="mutating||savingConfig" @click="configureFilteredFile(file.id)">配置执行账号/schema</el-button></div></div>
  </el-drawer>
  <SQLFileEditorDialog :file-id="fileEditorFileId" :start-editing="fileEditorStartEditing" :busy="busy" @close="fileEditorFileId=null" @saved="fileEditSaved"/>
  <SQLViewDependencyTransferDialog :target="viewTransferTarget" :busy="busy" @close="viewTransferTarget=null" @created="viewTransferCreated" />
  <el-dialog :model-value="!!selectedStatement" title="SQL详情" width="900px" append-to-body :close-on-click-modal="!editingStatement&&!savingStatement" :close-on-press-escape="!savingStatement" :show-close="!savingStatement" @close="closeStatement"><template v-if="selectedStatement"><p v-if="editingStatement" class="statement-edit-note">可补充分号，将当前错误片段修正为多条 SQL；保存后分别列出并等待执行。</p><el-alert v-if="selectedStatement.transactionStartLine" :title="`${transactionScope(selectedStatement)}：重试、标记失败和不执行都会作用于整个事务组`" type="info" :closable="false"/><el-tabs v-model="detailTab"><el-tab-pane label="SQL内容" name="sql"><el-input v-if="editingStatement" v-model="statementDraft" type="textarea" :autosize="{minRows:12,maxRows:22}" class="statement-editor" aria-label="SQL内容" :disabled="savingStatement"/><pre v-else class="sql-source">{{selectedStatement.sqlContent}}</pre><pre v-if="selectedStatement.executeMessage" class="execution-message">{{selectedStatement.executeMessage}}</pre><el-descriptions :column="2" border><el-descriptions-item label="SQLSTATE">{{selectedStatement.sqlState||'—'}}</el-descriptions-item><el-descriptions-item label="影响行数">{{selectedStatement.affectedRows}}</el-descriptions-item><el-descriptions-item label="耗时">{{selectedStatement.durationMs}} ms</el-descriptions-item><el-descriptions-item label="执行人">{{selectedStatement.executeUser||'—'}}</el-descriptions-item><el-descriptions-item label="数据库连接账号">{{selectedStatement.databaseUser||'—'}}</el-descriptions-item><el-descriptions-item label="文件 schema">{{currentFile?.schemaName}}</el-descriptions-item></el-descriptions></el-tab-pane><el-tab-pane label="执行记录" name="history"><SQLExecutionHistory v-if="detailTab==='history'" :statement-id="selectedStatement.id" class="detail-history" @retry-started="applyRetryResult"/></el-tab-pane></el-tabs></template><template #footer><template v-if="editingStatement&&detailTab==='sql'"><el-button :disabled="savingStatement" @click="editingStatement=false">取消编辑</el-button><el-button type="primary" :loading="savingStatement" :disabled="!canEditStatement||statementDraft===originalStatement" @click="saveStatement">保存SQL</el-button></template><template v-else><el-button @click="closeStatement">关闭</el-button><el-button v-if="canEditStatement&&detailTab==='sql'" type="primary" @click="startStatementEdit">编辑SQL</el-button><el-button v-if="selectedStatement?.executeStatus==='FAILED'&&!selectedStatement.viewDependencyTaskId&&!busy" type="primary" @click="showRetryAccount(selectedStatement)">切换账号重试</el-button></template></template></el-dialog>
  <SQLRetryAccountDialog :target="retryAccountTarget" :options="options" :disabled="busy" @close="retryAccountTarget=null" @started="applyRetryResult"/>
  <el-dialog :model-value="!!configFile" title="执行配置" width="520px" append-to-body :show-close="!savingConfig" :close-on-click-modal="false" :close-on-press-escape="!savingConfig" @close="configFile=null"><el-form label-width="140px"><el-form-item label="文件">{{configFile?.fileName}}</el-form-item><el-form-item label="执行账号/schema"><el-input v-model="schemaDraft" :disabled="savingConfig" placeholder="例如 df_shenqingdan" @keyup.enter="saveExecutionConfig"/></el-form-item></el-form><template #footer><el-button :disabled="savingConfig" @click="configFile=null">取消</el-button><el-button type="primary" :loading="savingConfig" @click="saveExecutionConfig">保存并校验</el-button></template></el-dialog>
  <el-dialog v-model="importVisible" :title="importMode==='server'?'导入服务器文件':'上传SQL文件'" width="760px" :close-on-click-modal="!importing" :show-close="!importing">
   <el-radio-group v-if="importMode!=='server'" v-model="importMode" :disabled="importing||reading"><el-radio-button value="local">SQL / ZIP 文件</el-radio-button><el-radio-button value="text">粘贴 SQL</el-radio-button></el-radio-group>
   <div v-if="importMode==='local'" class="import-body"><el-button :loading="reading" :disabled="importing" @click="fileInput?.click()">选择文件</el-button><input ref="fileInput" type="file" accept=".sql,.zip" multiple hidden :disabled="importing||reading" @change="readLocalFiles"/><span>{{reading?'正在读取文件…':`已选择 ${localFiles.length} 个 SQL 文件`}}</span><div class="import-list"><div v-for="file in localFiles" :key="file.fileName">{{file.fileName}}</div></div></div>
   <div v-else-if="importMode==='text'" class="import-body"><el-input v-model="draft.fileName" placeholder="版本__排序号__schema.sql" :disabled="importing"/><el-input v-model="draft.content" type="textarea" :rows="12" placeholder="SQL 内容" :disabled="importing"/></div><div v-else class="import-body"><el-input v-model="serverPath" placeholder="服务器 SQL / ZIP 文件路径" :disabled="importing"/></div><el-checkbox v-model="overwrite" :disabled="importing">覆盖同名文件</el-checkbox>
   <template #footer><el-button :disabled="importing" @click="importVisible=false">取消</el-button><el-button type="primary" :loading="importing" :disabled="reading" @click="importFiles">导入</el-button></template>
  </el-dialog>
 </div>
</template>
<style scoped>
.statement-file-header{flex-wrap:wrap}.statement-file-header .statement-file-context{flex:1 1 340px}
.file-handling-actions{display:flex;gap:8px;flex-wrap:wrap;align-items:center;flex-shrink:0}.file-handling-actions .el-button+.el-button{margin:0}
.sql-page{background:var(--ui-panel,#fff);border:1px solid var(--ui-border,#e2e8f0);padding:0 12px 12px;gap:12px;min-height:0}.file-tabs{flex-shrink:0}.file-toolbar{flex-shrink:0}.filters{flex-shrink:0}.file-tabs :deep(.el-tabs__header){margin:0}.file-tabs :deep(.el-tabs__nav-wrap::after){height:1px}.file-tabs :deep(.el-tabs__item){height:42px}.file-tabs :deep(.el-tabs__content){display:none}.file-toolbar{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.file-toolbar .el-button+.el-button{margin-left:0}.toolbar-spacer{flex:1}.order-note{color:#d99a39;font-size:13px}.filters{display:flex;gap:8px;flex-wrap:wrap}.filters .el-input{width:200px}.pagination-row{display:flex;justify-content:flex-end;padding-top:6px}.file-link{max-width:100%;display:block;text-align:left;overflow:hidden;text-overflow:ellipsis}.row-actions{display:flex;align-items:center;white-space:nowrap;gap:8px}.row-actions .el-button+.el-button{margin:0}
.execution-status-bar{display:flex;align-items:center;justify-content:space-between;gap:8px 16px;flex-shrink:0;min-height:36px;padding:4px 8px;margin-bottom:8px;border-bottom:1px solid var(--ui-border,#e2e8f0);background:var(--ui-panel,#fff);color:var(--el-text-color-regular);font-size:12px;line-height:24px}.execution-status-content{display:flex;align-items:center;gap:12px;flex:1;min-width:0}.execution-status-content>strong{max-width:45%;min-width:0;flex-shrink:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--el-text-color-primary);font-size:13px;font-weight:500}.execution-status-content>span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--el-text-color-secondary)}.execution-status-actions{display:flex;align-items:center;gap:12px;flex-shrink:0}.execution-status-actions .el-button+.el-button{margin:0}@media(max-width:900px){.execution-status-bar{flex-wrap:wrap}.execution-status-content{flex-basis:100%}.execution-status-content>strong{max-width:55%}.execution-status-actions{margin-left:auto}}.execution-result-details{max-height:65vh;overflow:auto;overflow-wrap:anywhere}.execution-result-details .execution-message{max-height:none}.execution-record-note{margin:0 0 12px;font-size:13px;color:var(--el-text-color-secondary);line-height:1.6}.filtered-explanation{margin:0 0 16px;color:var(--el-text-color-secondary);font-size:13px;line-height:1.7}.filtered-reason-list{display:grid;gap:12px}.filtered-reason-row{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:start;gap:8px;padding:12px;border:1px solid var(--ui-border,#e2e8f0);border-radius:6px}.filtered-reason-row>.el-button:first-child{grid-column:1/-1;font-weight:600}.filtered-reason-row .el-button{justify-content:flex-start;white-space:normal;overflow-wrap:anywhere;text-align:left;height:auto;margin:0}.filtered-reason-row span{white-space:pre-wrap;overflow-wrap:anywhere;font-size:13px;line-height:1.6}.current-file-card{flex-shrink:0;margin:0 0 8px;padding:10px;background:var(--el-color-primary-light-9);border:1px solid var(--el-color-primary-light-7);border-radius:6px}.current-file-label{display:flex;justify-content:space-between;align-items:center;gap:8px;font-size:12px;color:var(--el-text-color-secondary)}.current-file-name{display:block;width:100%;border:0;background:transparent;padding:8px 0 0;color:var(--el-color-primary);font:inherit;font-size:13px;font-weight:600;line-height:1.6;text-align:left;overflow-wrap:anywhere;cursor:pointer}.current-file-card>.el-button{margin-top:6px}.statement-file-header{display:flex;align-items:center;flex-shrink:0;gap:12px;min-height:64px;padding:8px 12px;margin-bottom:8px;border:1px solid var(--ui-border,#e2e8f0);border-radius:6px;background:var(--ui-panel-soft,#f8fafc)}.statement-file-context{display:grid;gap:3px;min-width:0;flex:1}.statement-file-context strong{font-size:13px;line-height:1.5;overflow-wrap:anywhere;color:var(--el-text-color-primary)}.statement-file-label,.statement-file-schema{font-size:12px;color:var(--el-text-color-secondary)}.statement-file-header>.el-button{flex-shrink:0;margin:0}.execution-header{display:flex;justify-content:space-between;align-items:center;width:100%}.execution-workbench{display:flex;flex:1;min-height:0;overflow:hidden;gap:8px;height:100%}.execution-files{width:320px;flex-shrink:0;display:flex;flex-direction:column;min-height:0;overflow:hidden;border-right:1px solid var(--ui-border,#dce3ed);padding-right:8px}.sidebar-section-title{font-size:12px;font-weight:600;color:var(--ui-text,#536c93);padding:8px 10px}.execution-file-list{flex:1;min-height:0;overflow:auto}.continuation-note{color:var(--el-text-color-secondary);font-size:12px}.execution-file{width:100%;background:transparent;color:var(--ui-text,#536c93);border:0;border-right:2px solid transparent;padding:9px 10px;cursor:pointer;display:flex;flex-wrap:wrap;gap:8px;align-items:center;text-align:left;font:inherit;font-size:12px;overflow-wrap:anywhere}.execution-file.active{color:var(--el-color-primary);border-right-color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.execution-file span:first-child{flex:1;min-width:0;overflow-wrap:anywhere}.statement-status-groups{flex:1;min-height:0;overflow:auto;border-top:1px solid var(--ui-border,#dce3ed)}.statement-status-group{margin:0 6px 8px;border:1px solid var(--ui-border,#e2e8f0);border-radius:6px;overflow:hidden}.statement-status-group>summary{list-style:none;cursor:pointer}.statement-status-group>summary::-webkit-details-marker{display:none}.statement-status-group>summary::after{content:"▸";margin-left:6px;color:#94a3b8}.statement-status-group[open]>summary::after{content:"▾"}.statement-group-title{display:flex;align-items:center;justify-content:space-between;padding:6px 8px;background:var(--ui-panel-soft,#f8fafc);font-size:12px;font-weight:600}.statement-group-row{display:flex;gap:7px;width:100%;padding:6px 8px;border:0;border-top:1px solid var(--ui-border,#e2e8f0);background:var(--ui-panel,#fff);text-align:left;cursor:pointer;font:inherit;font-size:12px;line-height:1.4;color:var(--ui-text,#536c93)}.statement-group-row span:first-child{flex-shrink:0;color:#6b7280}.statement-group-row span:last-child{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.statement-group-row:hover{background:var(--el-color-primary-light-9)}.statement-group-empty{padding:5px 8px;color:#9ca3af;font-size:12px}.statement-panel{flex:1;min-width:0;min-height:0;display:flex;flex-direction:column}.statement-panel .el-table{flex:1;min-height:0}.file-error{flex-shrink:0;margin-bottom:8px;max-height:130px;overflow:auto}.file-error :deep(.el-alert__title){white-space:pre-wrap;overflow-wrap:anywhere}.sql-preview{display:block;border:0;padding:0;background:transparent;color:inherit;font:inherit;text-align:left;width:100%;white-space:nowrap;text-overflow:ellipsis;overflow:hidden;cursor:pointer}.filtered-actions{display:flex;flex-wrap:wrap;gap:8px}.filtered-actions .el-button{max-width:350px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.detail-history{height:490px}.statement-error-message{display:block;width:100%;border:0;padding:0;background:transparent;text-align:left;white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;color:var(--el-color-danger);cursor:pointer}.statement-result-text{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.statement-panel :deep(.statement-error-row){--el-table-tr-bg-color:var(--el-color-danger-light-9)}.statement-panel :deep(.el-table__body tr.statement-error-row>td.el-table__cell){background:var(--el-color-danger-light-9)}.statement-panel :deep(.statement-error-row>td:first-child){box-shadow:inset 3px 0 0 var(--el-color-danger)}.execution-footer{display:flex;justify-content:center;align-items:center;flex-wrap:wrap;gap:12px}.execution-footer .el-checkbox{margin:0}.execution-footer .el-button+.el-button{margin:0}.sql-source,.execution-message{white-space:pre-wrap;overflow-wrap:anywhere;font:13px/1.7 ui-monospace,monospace;background:var(--ui-panel-soft,#f8fafc);padding:12px;max-height:50vh;overflow:auto}.statement-edit-note{margin:0 0 10px;color:var(--el-text-color-secondary);font-size:13px;line-height:1.6}.statement-editor{margin-bottom:12px}.statement-editor :deep(textarea){font:13px/1.7 ui-monospace,monospace;tab-size:4}.execution-message{color:var(--ui-text,#334155)}.import-body{display:grid;gap:12px;margin:18px 0}.import-list{max-height:180px;overflow:auto}@media(max-width:900px){.execution-files{width:240px}.execution-footer{justify-content:flex-start}.order-note{display:none}}
</style>
<style>
.sql-execution-dialog.el-dialog{height:90dvh;max-height:calc(100dvh - 32px);margin-top:16px;padding:12px;display:flex;flex-direction:column}.sql-execution-dialog.el-dialog.is-fullscreen{height:100dvh;max-height:100dvh;width:100%;max-width:100%;margin:0;border-radius:0}.sql-execution-dialog>.el-dialog__header{padding:0 4px 12px;margin:0;border-bottom:1px solid var(--ui-border,#e2e8f0)}.sql-execution-dialog>.el-dialog__body{display:flex;flex-direction:column;flex:1;min-height:0;overflow:hidden;padding-top:8px}.sql-execution-dialog>.el-dialog__footer{padding:12px 0 0;border-top:1px solid var(--ui-border,#e2e8f0)}
</style>

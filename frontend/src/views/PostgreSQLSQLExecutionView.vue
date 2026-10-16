<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue';
import { ElMessage, ElMessageBox, type TableInstance } from 'element-plus';
import JSZip from 'jszip';
import SQLExecutionHistory from '../components/SQLExecutionHistory.vue';
import { cancelSQLFile, cancelSQLFileSequence, setSQLFileExecutionConfig, deleteSQLFile, editSQLStatement, executeSelectedSQLFiles, getSQLBatch, getSQLFile, importServerSQL, listDoneSQLFiles, listTodoSQLFiles, parseSQLBatch, parseSQLFile, skipSQLFile, skipSQLStatement, type SQLExecutionFileFilter, type SQLChangeBatch, type SQLChangeFile, type SQLChangeStatement, type SQLExecuteOptions, } from '../api/postgresql';
import { getSettings } from '../api/settings';
import { formatTime } from '../utils/time';
import { compareSQLFiles } from '../utils/sql-file-order';
const files = ref<SQLChangeFile[]>([]);
const currentFile = ref<SQLChangeFile | null>(null);
const statements = ref<SQLChangeStatement[]>([]);
const statementTable = ref<TableInstance>();
let focusedError = '';
const isStatementError = (row: SQLChangeStatement) => ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(row.executeStatus);
const failedStatement = computed(() => statements.value.find(isStatementError));
const failedExecutionFile = computed(() => executionFiles.value.find(file => ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(file.executeStatus)));
const statementRowClass = ({ row }: { row: SQLChangeStatement }) => isStatementError(row) ? 'statement-error-row' : '';
async function locateStatementFailure(force = false) {
    const fileId = currentFile.value?.id;
    const failure = failedStatement.value;
    if (!failure || !fileId || !executionVisible.value) { focusedError = ''; return; }
    const key = `${fileId}:${failure.id}:${failure.executeStatus}`;
    if (!force && key === focusedError) return;
    await nextTick();
    if (disposed || currentFile.value?.id !== fileId || !executionVisible.value) return;
    const table = statementTable.value;
    const body = (table?.$el as HTMLElement | undefined)?.querySelector('.el-table__body-wrapper');
    const row = body?.querySelector<HTMLElement>('tr.statement-error-row');
    const scroll = body?.querySelector<HTMLElement>('.el-scrollbar__wrap');
    if (!row || !scroll || !table) return;
    const top = scroll.scrollTop + row.getBoundingClientRect().top - scroll.getBoundingClientRect().top - Math.max(0, (scroll.clientHeight - row.offsetHeight) / 2);
    table.setScrollTop(Math.max(0, top));
    focusedError = key;
}
async function locateExecutionFailure() {
    const file = failedStatement.value ? currentFile.value : failedExecutionFile.value;
    if (!file) return;
    if (file.id !== currentFile.value?.id) await selectExecutionFile(file);
    await locateStatementFailure(true);
}
const selectedStatement = ref<SQLChangeStatement | null>(null);
const editingStatement = ref(false);
const savingStatement = ref(false);
const statementDraft = ref('');
const originalStatement = ref('');
const group = ref<'todo' | 'done' | 'history'>('todo');
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
const fileContentVisible = ref(false);
const filteredFiles = ref<SQLExecutionFileFilter[]>([]);
const fullscreen = ref(true);
const executionFiles = ref<SQLChangeFile[]>([]);
const selectedFiles = ref<SQLChangeFile[]>([]);
const executionBatch = ref<SQLChangeBatch | null>(null);
const pageSize = ref(30);
const detailLoading = ref(false);
const filtersVisible = ref(false);
const filter = ref({ version: '', schemaName: '', fileName: '' });
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
const isActive = (file: SQLChangeFile) => ['QUEUED', 'RUNNING'].includes(file.executeStatus);
const busy = computed(() => mutating.value || savingConfig.value || submitting.value || executionBatch.value?.executeStatus === 'RUNNING' || executionFiles.value.some(isActive));
const canExecute = computed(() => !savingStatement.value && !editingStatement.value && !busy.value && executionFiles.value.some(f => !['SUCCESS', 'SKIPPED'].includes(f.executeStatus)));
const canEditStatement = computed(() => !!selectedStatement.value && !busy.value && !detailLoading.value &&
    ['PENDING', 'FAILED', 'CANCELED', 'PARTIAL_FAILED'].includes(currentFile.value?.executeStatus || '') &&
    ['PENDING', 'FAILED'].includes(selectedStatement.value.executeStatus) &&
    !statements.value.some(row => ['RUNNING', 'UNKNOWN'].includes(row.executeStatus)));
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
        const result = await editSQLStatement(row.id, statementDraft.value, originalStatement.value);
        applyFile(result.file, result.statements || []);
        executionFiles.value = executionFiles.value.map(file => file.id === result.file.id ? result.file : file);
        editingStatement.value = false;
        ElMessage.success('SQL 已保存，点击执行可重试');
        await loadFiles(true);
    } finally { savingStatement.value = false; }
}
const statusText = (s: string) => ({ PENDING: '未执行', QUEUED: '等待执行', RUNNING: '执行中', SUCCESS: '成功', FAILED: '失败', PARTIAL_FAILED: '部分失败', SKIPPED: '已跳过', CANCELED: '未完成', UNKNOWN: '结果待核实' }[s] || s);
const statusType = (s: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' => s === 'SUCCESS' ? 'success' : ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(s) ? 'danger' : ['RUNNING', 'QUEUED'].includes(s) ? 'primary' : s === 'PENDING' ? 'warning' : 'info';
const canSelect = (f: SQLChangeFile) => !isActive(f);
async function loadFiles(quiet = false) {
    if(group.value==='history') return;
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
async function searchFiles() { page.value = 1; selectedFiles.value = []; await loadFiles(); }
function clearPolling() { if (timer)
    clearTimeout(timer); timer = undefined; }
function applyFile(file: SQLChangeFile, rows: SQLChangeStatement[]) {
    currentFile.value = file;
    statements.value = rows;
    if (selectedStatement.value) selectedStatement.value = rows.find(s => s.id === selectedStatement.value?.id) || null;
    void locateStatementFailure();
}
async function selectExecutionFile(file: SQLChangeFile) {
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
    filteredFiles.value = [];
    executionFiles.value = [...rows].sort(compareSQLFiles);
    executionVisible.value = true;
    const active = rows.find(isActive);
    if (active?.executionBatchId) {
        const r = await getSQLBatch(active.executionBatchId,true);
        executionBatch.value = r.batch;
        executionFiles.value = r.files;
    }
    await selectExecutionFile(active || executionFiles.value[0]!);
    if (busy.value)
        schedulePoll();
}
async function refreshExecution() {
    const session = executionSession;
    if (executionBatch.value) {
        const wasRunning = executionBatch.value.executeStatus === 'RUNNING';
        const r = await getSQLBatch(executionBatch.value.id,true);
        if (disposed || session !== executionSession)
            return;
        executionBatch.value = r.batch;
        executionFiles.value = r.files;
        const failed = r.files.find(file => ['FAILED', 'PARTIAL_FAILED', 'UNKNOWN'].includes(file.executeStatus));
        if (wasRunning && r.batch.executeStatus !== 'RUNNING' && failed && failed.id !== currentFile.value?.id && executionVisible.value && !selectedStatement.value && !editingStatement.value) {
            await selectExecutionFile(failed);
        }
    }
    else {
        const rows = await Promise.all(executionFiles.value.map(f => getSQLFile(f.id)));
        if (disposed || session !== executionSession)
            return;
        executionFiles.value = rows.map(r => r.file);
    }
    const id = currentFile.value?.id;
    const selected = selection;
    if (id) {
        const r = await getSQLFile(id);
        if (!disposed && session === executionSession && selected === selection)
            applyFile(r.file, r.statements || []);
    }
    await loadFiles(true);
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
    catch {
        if (!disposed && session === executionSession)
            schedulePoll();
    } }, 1000);
}
async function executeFiles() {
    if (!canExecute.value)
        return;
    submitting.value = true;
    try {
        const ids = executionFiles.value.filter(f => !['SUCCESS', 'SKIPPED'].includes(f.executeStatus)).map(f => f.id);
        const r = await executeSelectedSQLFiles(ids, { ...options.value });
        executionBatch.value = r.batch;
        executionFiles.value = r.files;
        filteredFiles.value = r.batch.filteredFiles || [];
        if (filteredFiles.value.length) ElMessage.warning(`已过滤 ${filteredFiles.value.length} 个文件，其余文件按顺序执行`);
        await selectExecutionFile(r.files[0]!);
        schedulePoll();
        await loadFiles(true);
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
        ElMessage.info('停止请求已保存，当前语句结束后停止后续任务');
    } finally {stopping.value=false;}
}
function showExecutionConfig(file:SQLChangeFile){configFile.value=file;schemaDraft.value=file.schemaName||'';}
async function configureFilteredFile(id:number){
    const result=await getSQLFile(id);
    if(!result.file.schemaName) showExecutionConfig(result.file);
    else ElMessage.warning(result.file.importMessage||'请在文件列表检查并修正该文件');
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
        await ElMessageBox.confirm(`将第 ${row.lineNumber} 行标记为不执行？${row.executeStatus === 'UNKNOWN' ? '请先核实数据库执行结果。' : ''}`, '标记为不执行', { type: 'warning' });
    }
    catch {
        return;
    }
    mutating.value=true;
    try{await skipSQLStatement(row.id);await refreshExecution();}
    finally{mutating.value=false;}
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
        filter.value = { version: '', schemaName: '', fileName: '' };
        await loadFiles();
        ElMessage.success(`已上传 ${importedCount} 个 SQL 文件`);
    }
    finally {
        importing.value = false;
    }
}
onMounted(async () => { await loadFiles(); const settings = await getSettings(); options.value = { skipExistsColumn: settings.sql_execute_skip_exists_column !== 'false', skipExistsTable: settings.sql_execute_skip_exists_table !== 'false', skipUniqueConstraint: settings.sql_execute_skip_unique_constraint !== 'false' }; });
onBeforeUnmount(() => { disposed = true; selection++; clearPolling(); });
</script>
<template>
 <div class="sql-page viewport-table-page">
  <el-tabs v-model="group" class="file-tabs" @tab-change="searchFiles"><el-tab-pane label="未执行文件" name="todo"/><el-tab-pane label="已执行文件" name="done"/><el-tab-pane label="SQL记录" name="history"/></el-tabs>
  <SQLExecutionHistory v-if="group==='history'"/>
  <template v-else>
  <div class="file-toolbar">
   <el-button type="primary" @click="showImport('server')">导入服务器文件</el-button><el-button type="primary" @click="showImport('local')">上传SQL文件</el-button><el-button v-if="group==='todo'" type="danger" @click="openExecution(selectedFiles)">执行SQL文件</el-button><span v-if="group==='todo'" class="order-note">从前往后按顺序执行</span><div class="toolbar-spacer"/><el-button @click="loadFiles()">刷新</el-button><el-button :type="filtersVisible?'primary':'default'" @click="filtersVisible=!filtersVisible">筛选</el-button>
  </div>
  <div v-if="filtersVisible" class="filters"><el-input v-model="filter.version" placeholder="版本号" clearable @keyup.enter="searchFiles"/><el-input v-model="filter.schemaName" placeholder="数据库 / schema" clearable @keyup.enter="searchFiles"/><el-input v-model="filter.fileName" placeholder="文件名称" clearable @keyup.enter="searchFiles"/><el-button type="primary" @click="searchFiles">查询</el-button></div>
  <div class="table-wrapper" v-loading="loading">
   <el-table :data="files" row-key="id" height="100%" stripe @selection-change="selectedFiles=$event">
    <el-table-column v-if="group==='todo'" type="selection" width="44" :selectable="canSelect"/><el-table-column type="index" label="#" width="60" :index="(i:number)=>(page-1)*pageSize+i+1"/>
    <el-table-column prop="version" label="版本号" width="165" show-overflow-tooltip/><el-table-column label="数据库" width="140" show-overflow-tooltip><template #default="{row}">{{row.schemaName||'未识别'}}</template></el-table-column>
    <el-table-column label="文件名称" min-width="330"><template #default="{row}"><el-button link type="primary" class="file-link" @click="openExecution([row])">{{row.fileName}}</el-button></template></el-table-column>
    <el-table-column label="文件上传时间" width="170"><template #default="{row}">{{formatTime(row.createdAt)}}</template></el-table-column><el-table-column label="上传人" width="90"><template #default="{row}">{{row.createdUser||'—'}}</template></el-table-column>
    <el-table-column label="执行时间" width="170"><template #default="{row}">{{row.executeTime?formatTime(row.executeTime):'—'}}</template></el-table-column><el-table-column label="执行人" width="90"><template #default="{row}">{{row.executeUser||'—'}}</template></el-table-column>
    <el-table-column label="状态" width="105"><template #default="{row}"><el-tooltip :disabled="!row.importMessage||row.executeStatus!=='PENDING'" :content="row.importMessage"><el-tag :type="statusType(row.executeStatus)" size="small">{{row.executeStatus==='PENDING'&&row.importMessage?'待处理':statusText(row.executeStatus)}}</el-tag></el-tooltip></template></el-table-column>
    <el-table-column v-if="group==='todo'" label="操作" width="270" fixed="right"><template #default="{row}"><div class="row-actions"><el-button v-if="!row.schemaName||(row.schemaConfigured&&row.executeStatus==='PENDING')" link type="primary" :disabled="isActive(row)||mutating||savingConfig" @click="showExecutionConfig(row)">执行配置</el-button><el-button link type="primary" :disabled="mutating||savingConfig||isActive(row)||row.executeStatus==='SKIPPED'" @click="skipFile(row)">标记为不执行</el-button><el-button link type="danger" :disabled="mutating||savingConfig||isActive(row)||row.executeStatus==='UNKNOWN'" @click="removeFile(row)">删除</el-button></div></template></el-table-column>
   </el-table>
  </div>
  <div class="pagination-row"><el-pagination v-model:current-page="page" v-model:page-size="pageSize" :page-sizes="[20,30,50,100]" :total="total" layout="total, sizes, prev, pager, next, jumper" @size-change="searchFiles" @current-change="loadFiles()"/></div>
  </template>
  <el-dialog v-model="executionVisible" :fullscreen="fullscreen" width="92%" class="sql-execution-dialog" :show-close="false" :close-on-click-modal="false" destroy-on-close @opened="locateStatementFailure(true)">
   <template #header><div class="execution-header"><strong>SQL执行</strong><div><el-button v-if="failedStatement||failedExecutionFile" link type="danger" @click="locateExecutionFailure">定位错误</el-button><el-button link :disabled="!currentFile" @click="fileContentVisible=true">完整SQL</el-button><el-button link :aria-label="fullscreen?'还原窗口':'最大化窗口'" @click="fullscreen=!fullscreen">{{fullscreen?'还原':'最大化'}}</el-button><el-button link aria-label="关闭SQL执行" @click="executionVisible=false">关闭</el-button></div></div></template>
   <div class="execution-workbench">
    <aside class="execution-files"><button v-for="file in executionFiles" :key="file.id" class="execution-file" :class="{active:currentFile?.id===file.id}" @click="selectExecutionFile(file)"><span>{{file.fileName}}</span><el-tag v-if="executionFiles.length>1||isActive(file)||file.importMessage" :type="statusType(file.executeStatus)" size="small">{{file.executeStatus==='PENDING'&&file.importMessage?'待处理':statusText(file.executeStatus)}}</el-tag></button></aside>
    <main class="statement-panel" v-loading="detailLoading">
     <el-alert v-if="filteredFiles.length" :title="`${filteredFiles.length} 个文件未进入本次执行`" type="warning" :closable="false" class="file-error"><div class="filtered-actions"><el-button v-for="file in filteredFiles" :key="file.id" link type="primary" :title="file.reason" :disabled="mutating||savingConfig" @click="configureFilteredFile(file.id)">{{file.fileName}}</el-button></div></el-alert>
     <el-alert v-if="currentFile?.importMessage" :title="currentFile.importMessage" type="warning" :closable="false" class="file-error"><el-button v-if="!currentFile.schemaName" link type="primary" :disabled="savingConfig||mutating" @click="showExecutionConfig(currentFile)">配置执行账号/schema</el-button></el-alert>

     <el-alert v-if="currentFile&&!failedStatement&&['FAILED','UNKNOWN','PARTIAL_FAILED'].includes(currentFile.executeStatus)" :title="currentFile.executeMessage||'执行失败，请查看语句结果'" type="error" :closable="false" class="file-error"/>
     <el-table ref="statementTable" :data="statements" :row-class-name="statementRowClass" stripe height="100%" row-key="id" @row-dblclick="showStatement">
      <el-table-column type="index" label="#" width="60"/><el-table-column prop="lineNumber" label="line" width="55"/><el-table-column label="状态" width="110"><template #default="{row}"><el-tag :type="statusType(row.executeStatus)" size="small">{{row.executeStatus==='SKIPPED'?(row.skipKind==='MANUAL'?'手动跳过':row.skipKind==='AUTO'?'自动跳过':'已跳过'):statusText(row.executeStatus)}}</el-tag></template></el-table-column>
      <el-table-column label="sql" min-width="380" show-overflow-tooltip><template #default="{row}"><button class="sql-preview" @click="showStatement(row)">{{row.sqlContent.replace(/\s+/g,' ')}}</button></template></el-table-column><el-table-column label="执行信息" min-width="350"><template #default="{row}"><button v-if="isStatementError(row)" class="statement-error-message" :aria-label="`查看第 ${row.lineNumber} 行错误`" @click="showStatement(row)">{{row.executeMessage||'执行结果异常，请查看详情'}}</button><span v-else class="statement-result-text" :title="row.executeMessage">{{row.executeMessage}}</span></template></el-table-column>
      <el-table-column label="操作" width="170" fixed="right"><template #default="{row}"><div class="row-actions"><el-button link type="primary" @click="showStatement(row)">详情</el-button><el-button link type="primary" :disabled="busy||['SUCCESS','SKIPPED','RUNNING'].includes(row.executeStatus)||currentFile?.executeStatus==='SUCCESS'" @click="skipStatement(row)">标记为不执行</el-button></div></template></el-table-column>
     </el-table>
    </main>
   </div>
   <template #footer><div class="execution-footer"><span>跳过：</span><el-checkbox v-model="options.skipExistsTable" :disabled="busy">已存在的表 / 索引</el-checkbox><el-checkbox v-model="options.skipExistsColumn" :disabled="busy">已存在的字段</el-checkbox><el-checkbox v-model="options.skipUniqueConstraint" :disabled="busy">唯一约束冲突</el-checkbox><el-button type="primary" :disabled="!canExecute" :loading="submitting" @click="executeFiles">执行</el-button><el-button v-if="executionBatch?.executeStatus==='RUNNING'||executionFiles.some(f=>f.executeStatus==='RUNNING')" type="warning" :disabled="executionBatch?.stopRequested" :loading="stopping" @click="stopFile">停止</el-button><el-button @click="refreshExecution">刷新</el-button><el-button @click="executionVisible=false">关闭</el-button></div></template>
  </el-dialog>
  <el-dialog v-model="fileContentVisible" title="完整SQL" width="1000px" append-to-body><pre class="sql-source">{{currentFile?.fileContent}}</pre></el-dialog>
  <el-dialog :model-value="!!selectedStatement" title="SQL详情" width="900px" append-to-body :close-on-click-modal="!editingStatement&&!savingStatement" :close-on-press-escape="!savingStatement" :show-close="!savingStatement" @close="closeStatement"><template v-if="selectedStatement"><el-tabs v-model="detailTab"><el-tab-pane label="SQL内容" name="sql"><el-input v-if="editingStatement" v-model="statementDraft" type="textarea" :autosize="{minRows:12,maxRows:22}" class="statement-editor" aria-label="SQL内容" :disabled="savingStatement"/><pre v-else class="sql-source">{{selectedStatement.sqlContent}}</pre><pre v-if="selectedStatement.executeMessage" class="execution-message">{{selectedStatement.executeMessage}}</pre><el-descriptions :column="2" border><el-descriptions-item label="SQLSTATE">{{selectedStatement.sqlState||'—'}}</el-descriptions-item><el-descriptions-item label="影响行数">{{selectedStatement.affectedRows}}</el-descriptions-item><el-descriptions-item label="耗时">{{selectedStatement.durationMs}} ms</el-descriptions-item><el-descriptions-item label="执行人">{{selectedStatement.executeUser||'—'}}</el-descriptions-item></el-descriptions></el-tab-pane><el-tab-pane label="执行记录" name="history"><SQLExecutionHistory v-if="detailTab==='history'" :statement-id="selectedStatement.id" class="detail-history"/></el-tab-pane></el-tabs></template><template #footer><template v-if="editingStatement&&detailTab==='sql'"><el-button :disabled="savingStatement" @click="editingStatement=false">取消编辑</el-button><el-button type="primary" :loading="savingStatement" :disabled="!canEditStatement||statementDraft===originalStatement" @click="saveStatement">保存SQL</el-button></template><template v-else><el-button @click="closeStatement">关闭</el-button><el-button v-if="canEditStatement&&detailTab==='sql'" type="primary" @click="startStatementEdit">编辑SQL</el-button></template></template></el-dialog>
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
.sql-page{background:var(--ui-panel,#fff);border:1px solid var(--ui-border,#e2e8f0);padding:0 12px 12px;gap:12px;min-height:0}.file-tabs{flex-shrink:0}.file-toolbar{flex-shrink:0}.filters{flex-shrink:0}.file-tabs :deep(.el-tabs__header){margin:0}.file-tabs :deep(.el-tabs__nav-wrap::after){height:1px}.file-tabs :deep(.el-tabs__item){height:42px}.file-tabs :deep(.el-tabs__content){display:none}.file-toolbar{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.file-toolbar .el-button+.el-button{margin-left:0}.toolbar-spacer{flex:1}.order-note{color:#d99a39;font-size:13px}.filters{display:flex;gap:8px;flex-wrap:wrap}.filters .el-input{width:200px}.pagination-row{display:flex;justify-content:flex-end;padding-top:6px}.file-link{max-width:100%;display:block;text-align:left;overflow:hidden;text-overflow:ellipsis}.row-actions{display:flex;align-items:center;white-space:nowrap;gap:8px}.row-actions .el-button+.el-button{margin:0}
.execution-header{display:flex;justify-content:space-between;align-items:center;width:100%}.execution-workbench{display:flex;flex:1;min-height:0;overflow:hidden;gap:8px;height:100%}.execution-files{width:300px;flex-shrink:0;overflow:auto;border-right:1px solid var(--ui-border,#dce3ed);padding-right:8px}.execution-file{width:100%;background:transparent;color:var(--ui-text,#536c93);border:0;border-right:2px solid transparent;padding:14px 10px;cursor:pointer;display:flex;flex-wrap:wrap;gap:8px;align-items:center;text-align:left;font:inherit;font-size:13px;overflow-wrap:anywhere}.execution-file.active{color:var(--el-color-primary);border-right-color:var(--el-color-primary);background:var(--el-color-primary-light-9)}.statement-panel{flex:1;min-width:0;min-height:0;display:flex;flex-direction:column}.statement-panel .el-table{flex:1;min-height:0}.file-error{flex-shrink:0;margin-bottom:8px;max-height:130px;overflow:auto}.file-error :deep(.el-alert__title){white-space:pre-wrap;overflow-wrap:anywhere}.sql-preview{display:block;border:0;padding:0;background:transparent;color:inherit;font:inherit;text-align:left;width:100%;white-space:nowrap;text-overflow:ellipsis;overflow:hidden;cursor:pointer}.filtered-actions{display:flex;flex-wrap:wrap;gap:8px}.filtered-actions .el-button{max-width:350px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.detail-history{height:490px}.statement-error-message{display:block;width:100%;border:0;padding:0;background:transparent;text-align:left;white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;color:var(--el-color-danger);cursor:pointer}.statement-result-text{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.statement-panel :deep(.statement-error-row){--el-table-tr-bg-color:var(--el-color-danger-light-9)}.statement-panel :deep(.el-table__body tr.statement-error-row>td.el-table__cell){background:var(--el-color-danger-light-9)}.statement-panel :deep(.statement-error-row>td:first-child){box-shadow:inset 3px 0 0 var(--el-color-danger)}.execution-footer{display:flex;justify-content:center;align-items:center;flex-wrap:wrap;gap:12px}.execution-footer .el-checkbox{margin:0}.execution-footer .el-button+.el-button{margin:0}.sql-source,.execution-message{white-space:pre-wrap;overflow-wrap:anywhere;font:13px/1.7 ui-monospace,monospace;background:var(--ui-panel-soft,#f8fafc);padding:12px;max-height:50vh;overflow:auto}.statement-editor{margin-bottom:12px}.statement-editor :deep(textarea){font:13px/1.7 ui-monospace,monospace;tab-size:4}.execution-message{color:var(--ui-text,#334155)}.import-body{display:grid;gap:12px;margin:18px 0}.import-list{max-height:180px;overflow:auto}@media(max-width:900px){.execution-files{width:210px}.execution-footer{justify-content:flex-start}.order-note{display:none}}
</style>
<style>
.sql-execution-dialog.el-dialog{height:90dvh;max-height:calc(100dvh - 32px);margin-top:16px;padding:12px;display:flex;flex-direction:column}.sql-execution-dialog.el-dialog.is-fullscreen{height:100dvh;max-height:100dvh;width:100%;max-width:100%;margin:0;border-radius:0}.sql-execution-dialog>.el-dialog__header{padding:0 4px 12px;margin:0;border-bottom:1px solid var(--ui-border,#e2e8f0)}.sql-execution-dialog>.el-dialog__body{display:flex;flex:1;min-height:0;overflow:hidden;padding-top:8px}.sql-execution-dialog>.el-dialog__footer{padding:12px 0 0;border-top:1px solid var(--ui-border,#e2e8f0)}
</style>

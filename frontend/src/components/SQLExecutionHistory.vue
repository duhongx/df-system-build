<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { ElMessage, ElMessageBox } from 'element-plus';
import { listSQLStatementRecords, getSQLBatch, getSQLBatchFile, listSQLExecutionBatches, type SQLStatementRecord, type SQLChangeBatch, type SQLChangeFile, type SQLChangeStatement } from '../api/postgresql';
import { executeFailedSQLStatement, type SQLRetryTarget } from '../api/postgresql';
import SQLRetryAccountDialog from './SQLRetryAccountDialog.vue';
import { formatTime } from '../utils/time';
import { transactionOutcomeText } from '../utils/sql-transaction';
const props = defineProps<{statementId?: number}>();
const emit = defineEmits<{showCurrentFailures: []; retryStarted: [result: {file: SQLChangeFile; statements: SQLChangeStatement[]} ]}>();
const retryAccountTarget = ref<SQLRetryTarget | null>(null);
const mode = ref('records');
const rows = ref<SQLStatementRecord[]>([]);
const batches = ref<SQLChangeBatch[]>([]);
const page = ref(1), size = ref(30), total = ref(0), loading = ref(false);
const filter = ref({keyword:'',fileName:'',schemaName:'',action:'',status:''});
const selected = ref<SQLStatementRecord | null>(null);
const batch = ref<SQLChangeBatch | null>(null), batchFiles = ref<SQLChangeFile[]>([]);
const snapshot = ref<{file: SQLChangeFile; statements: SQLChangeStatement[]} | null>(null);
const retryingStatementId = ref<number | null>(null);
let requestId = 0;
const status = (s:string) => ({SUCCESS:'成功',FAILED:'失败',SKIPPED:'已跳过',UNKNOWN:'结果待核实',RUNNING:'执行中',QUEUED:'等待执行',PENDING:'未执行',CANCELED:'未完成',PARTIAL_FAILED:'部分失败',REMOVED:'已从当前文件移除'}[s]||s);
const action = (r:SQLStatementRecord) => r.action==='LEGACY_SNAPSHOT' ? (r.skipKind==='MANUAL'?'旧版手动跳过快照':r.skipKind==='AUTO'?'旧版自动跳过快照':'旧版状态快照') : ({EXECUTE:'执行',SESSION_RESTORE:'恢复会话配置',AUTO_SKIP:'自动跳过',AUTO_SPLIT:'自动拆分字段',RETRY_ACCOUNT:'切换账号重试',FILE_EDIT:'整文件修改',VIEW_DEPENDENCY_TRANSFER:'转入视图依赖',VIEW_DEPENDENCY_CANCEL:'撤回视图依赖',VIEW_DEPENDENCY_EXECUTE:'视图依赖任务执行',MANUAL_SKIP:'手动跳过此句',MANUAL_FAIL:'手动标记失败',FILE_FAIL:'整文件标记失败',FILE_SKIP:'手动跳过文件',EDIT:'修改SQL',CONFIGURE:'配置执行账号',INHERIT:'沿用历史结果'}[r.action]||r.action);
const legacyVisible = computed(()=>rows.value.some(r=>r.action==='LEGACY_SNAPSHOT'));
async function load(){
 const id=++requestId;loading.value=true;
 try{
  if(mode.value==='batches') {const r=await listSQLExecutionBatches(page.value,size.value);if(id===requestId){batches.value=r.list||[];total.value=r.total;}}
  else {const r=await listSQLStatementRecords(page.value,size.value,{...filter.value,statementId:props.statementId});if(id===requestId){rows.value=r.list||[];total.value=r.total;}}
 }finally{if(id===requestId)loading.value=false;}
}
async function search(){page.value=1;await load();}
async function showFailed(){mode.value='records';filter.value={...filter.value,status:'FAILED'};page.value=1;await load();}
async function retryFailed(row:SQLStatementRecord){
 if(!row.statementId||retryingStatementId.value!==null)return;
 try{await ElMessageBox.confirm(`单独重试 ${row.fileName} 第 ${row.lineNumber} 行 SQL？${row.transactionGroupId?'该语句属于显式事务，将重试整个事务组，组外 SQL 不执行。':'同一文件中的其他 SQL 不会执行；视图 DROP/CREATE 配对失败时将整组重试。'}`,'单独重试失败 SQL',{type:'warning'});}catch{return;}
 retryingStatementId.value=row.statementId;
 try{const result=await executeFailedSQLStatement(row.statementId,{skipExistsColumn:true,skipExistsTable:true,skipUniqueConstraint:true});emit('retryStarted',result);ElMessage.success(row.transactionGroupId?'已提交整个事务组重试':'已提交 SQL 重试');await load();}
 finally{retryingStatementId.value=null;}
}
function showRetryAccount(row: SQLStatementRecord) {
 if(!row.statementId||retryingStatementId.value!==null)return;
 retryAccountTarget.value={statementId:row.statementId,fileId:row.fileId,fileName:row.fileName,lineNumber:row.lineNumber,
  schemaName:row.schemaName,originalAccount:row.databaseUser||row.schemaName,transactionLabel:row.transactionGroupId?'整个事务组':undefined};
}
async function accountRetryStarted(result: {file: SQLChangeFile; statements: SQLChangeStatement[]}) {
 emit('retryStarted',result);
 await load();
}
async function openBatch(id:number){const r=await getSQLBatch(id);batch.value=r.batch;batchFiles.value=r.files||[];}
async function openSnapshot(file:SQLChangeFile){if(batch.value)snapshot.value=await getSQLBatchFile(batch.value.id,file.id);}
watch(()=>props.statementId,()=>{page.value=1;mode.value='records';void load();},{immediate:true});
</script>
<template>
 <div class="sql-history">
  <el-radio-group v-if="!statementId" v-model="mode" @change="search"><el-radio-button value="records">语句记录</el-radio-button><el-radio-button value="batches">执行批次</el-radio-button></el-radio-group>
  <div class="history-filters">
   <el-button v-if="!statementId" @click="emit('showCurrentFailures')">当前失败 SQL</el-button><el-button v-if="!statementId" @click="showFailed">历史失败记录</el-button>
   <template v-if="mode==='records'"><el-input v-model="filter.keyword" placeholder="SQL关键字" clearable @keyup.enter="search"/><el-input v-if="!statementId" v-model="filter.fileName" placeholder="文件名称" clearable @keyup.enter="search"/><el-input v-if="!statementId" v-model="filter.schemaName" placeholder="文件 schema" clearable @keyup.enter="search"/>
    <el-select v-model="filter.action" placeholder="操作类型" clearable><el-option label="执行" value="EXECUTE"/><el-option label="转入视图依赖" value="VIEW_DEPENDENCY_TRANSFER"/><el-option label="视图依赖任务执行" value="VIEW_DEPENDENCY_EXECUTE"/><el-option label="恢复会话配置" value="SESSION_RESTORE"/><el-option label="手动标记失败" value="MANUAL_FAIL"/><el-option label="手动跳过" value="MANUAL_SKIP"/><el-option label="自动跳过" value="AUTO_SKIP"/><el-option label="自动拆分字段" value="AUTO_SPLIT"/><el-option label="切换账号重试" value="RETRY_ACCOUNT"/><el-option label="修改SQL" value="EDIT"/><el-option label="整文件修改" value="FILE_EDIT"/><el-option label="沿用历史结果" value="INHERIT"/><el-option label="配置执行账号" value="CONFIGURE"/><el-option label="旧版状态快照" value="LEGACY_SNAPSHOT"/></el-select>
    <el-select v-model="filter.status" placeholder="结果状态" clearable><el-option v-for="s in ['SUCCESS','FAILED','SKIPPED','UNKNOWN','RUNNING','PENDING']" :key="s" :label="status(s)" :value="s"/></el-select><el-button type="primary" @click="search">查询</el-button>
   </template><el-button @click="load">刷新</el-button>
  </div>
  <el-alert v-if="mode==='records'&&legacyVisible" title="旧版状态快照仅保留升级时已有的最后状态，无法还原此前被覆盖的记录。" type="info" :closable="false"/>
  <div class="history-table" v-loading="loading">
   <el-table v-if="mode==='records'" :data="rows" height="100%" stripe row-key="id">
    <el-table-column label="时间" width="170"><template #default="{row}">{{formatTime(row.startedAt)}}</template></el-table-column>
    <el-table-column label="操作" width="170"><template #default="{row}">{{action(row)}}</template></el-table-column>
    <el-table-column label="状态" width="120"><template #default="{row}"><el-tag :type="['FAILED','UNKNOWN'].includes(row.status)?'danger':row.status==='SUCCESS'?'success':'info'" size="small">{{status(row.status)}}</el-tag></template></el-table-column>
    <el-table-column label="事务结果" width="130"><template #default="{row}">{{transactionOutcomeText(row.transactionOutcome)}}</template></el-table-column>
    <el-table-column prop="operator" label="操作人" width="90"/><el-table-column prop="databaseUser" label="数据库连接账号" width="150"/><el-table-column prop="schemaName" label="文件 schema" width="150"/>
    <el-table-column label="提交到数据库" width="130"><template #default="{row}">{{row.submitted===true?'已提交':row.submitted===false?'未提交':row.action==='LEGACY_SNAPSHOT'?'旧版未记录':'待核实'}}</template></el-table-column>
    <el-table-column label="文件 / 行" min-width="230"><template #default="{row}">{{row.fileName}}<span v-if="row.lineNumber"> · {{row.lineNumber}}</span></template></el-table-column>
    <el-table-column label="SQL" min-width="300"><template #default="{row}"><button class="record-sql" @click="selected=row">{{row.sqlContent.replace(/\s+/g,' ')||row.message}}</button></template></el-table-column>
    <el-table-column label="批次" width="85"><template #default="{row}"><el-button v-if="row.batchId" link type="primary" @click="openBatch(row.batchId)">{{row.batchId}}</el-button><span v-else>—</span></template></el-table-column>
    <el-table-column label="操作" width="270" fixed="right"><template #default="{row}"><el-button link type="primary" @click="selected=row">详情</el-button><el-button v-if="row.status==='FAILED'&&row.statementId" link type="primary" :loading="retryingStatementId===row.statementId" :disabled="retryingStatementId!==null" @click="retryFailed(row)">{{row.transactionGroupId?'整组重试':'单独重试'}}</el-button><el-button v-if="row.status==='FAILED'&&row.statementId" link type="primary" :disabled="retryingStatementId!==null" @click="showRetryAccount(row)">切换账号重试</el-button></template></el-table-column>
   </el-table>
   <el-table v-else :data="batches" height="100%" stripe><el-table-column prop="id" label="批次" width="90"/><el-table-column label="时间" width="180"><template #default="{row}">{{formatTime(row.createdAt)}}</template></el-table-column><el-table-column prop="executeUser" label="执行人" width="100"/><el-table-column label="状态" width="140"><template #default="{row}">{{status(row.executeStatus)}}</template></el-table-column><el-table-column prop="totalFiles" label="文件数" width="90"/><el-table-column prop="successFiles" label="成功" width="80"/><el-table-column prop="failedFiles" label="失败" width="80"/><el-table-column prop="executeMessage" label="结果" min-width="250" show-overflow-tooltip/><el-table-column label="操作" width="110"><template #default="{row}"><el-button link type="primary" @click="openBatch(row.id)">查看当次结果</el-button></template></el-table-column></el-table>
  </div>
  <el-pagination v-model:current-page="page" v-model:page-size="size" :page-sizes="[20,30,50,100]" :total="total" layout="total, sizes, prev, pager, next" @current-change="load" @size-change="search"/>
  <SQLRetryAccountDialog :target="retryAccountTarget" :options="{skipExistsColumn:true,skipExistsTable:true,skipUniqueConstraint:true}" :disabled="retryingStatementId!==null" @close="retryAccountTarget=null" @started="accountRetryStarted"/>
  <el-dialog :model-value="!!selected" title="SQL记录详情" width="1000px" append-to-body @close="selected=null"><template v-if="selected"><el-descriptions :column="2" border><el-descriptions-item label="操作">{{action(selected)}}</el-descriptions-item><el-descriptions-item label="结果">{{status(selected.status)}}</el-descriptions-item><el-descriptions-item label="操作人">{{selected.operator||'—'}}</el-descriptions-item><el-descriptions-item label="时间">{{formatTime(selected.startedAt)}}</el-descriptions-item><el-descriptions-item label="数据库">{{selected.databaseHost?`${selected.databaseHost}:${selected.databasePort}/${selected.databaseName}`:selected.action==='LEGACY_SNAPSHOT'?'旧版未记录':'未提交数据库'}}</el-descriptions-item><el-descriptions-item label="数据库账号">{{selected.databaseUser||selected.schemaName||'—'}}</el-descriptions-item><el-descriptions-item v-if="selected.transactionGroupId" label="事务结果">{{transactionOutcomeText(selected.transactionOutcome)}}</el-descriptions-item><el-descriptions-item label="SQLSTATE">{{selected.sqlState||'—'}}</el-descriptions-item><el-descriptions-item label="影响行数 / 耗时">{{selected.affectedRows}} / {{selected.durationMs}} ms</el-descriptions-item></el-descriptions><pre class="record-source">{{selected.sqlContent}}</pre><pre v-if="selected.message" class="record-source">{{selected.message}}</pre><template v-if="selected.beforeMessage"><strong>操作前的记录：{{status(selected.beforeStatus)}} · {{selected.beforeOperator||'—'}} · {{selected.beforeTime?formatTime(selected.beforeTime):'—'}}</strong><pre class="record-source">{{selected.beforeMessage}}</pre></template></template></el-dialog>
  <el-dialog :model-value="!!batch" :title="`执行批次 ${batch?.id||''}`" width="1050px" append-to-body @close="batch=null"><el-alert v-if="batch?.historyIncomplete" title="此旧版批次没有当次文件快照，无法还原历史结果。" type="warning" :closable="false"/><el-table :data="batchFiles" max-height="520"><el-table-column prop="fileName" label="文件" min-width="350"/><el-table-column prop="schemaName" label="账号/schema" width="150"/><el-table-column label="当次状态" width="140"><template #default="{row}">{{status(row.executeStatus)}}</template></el-table-column><el-table-column label="操作" width="140"><template #default="{row}"><el-button link type="primary" :disabled="batch?.historyIncomplete" @click="openSnapshot(row)">查看当次SQL</el-button></template></el-table-column></el-table></el-dialog>
  <el-dialog :model-value="!!snapshot" :title="`当次SQL · ${snapshot?.file.fileName||''}`" width="1100px" append-to-body @close="snapshot=null"><el-table :data="snapshot?.statements||[]" max-height="580"><el-table-column prop="lineNumber" label="行" width="70"/><el-table-column label="事务结果" width="130"><template #default="{row}">{{transactionOutcomeText(row.transactionOutcome)}}</template></el-table-column><el-table-column label="当次状态" width="130"><template #default="{row}">{{status(row.executeStatus)}}</template></el-table-column><el-table-column label="本次处理" width="140"><template #default="{row}">{{row.resultOrigin==='PREVIOUS'?'沿用历史结果':row.resultOrigin==='CURRENT'?'执行尝试':'未执行'}}</template></el-table-column><el-table-column prop="sqlContent" label="SQL" min-width="450" show-overflow-tooltip/><el-table-column prop="executeMessage" label="执行信息" min-width="280" show-overflow-tooltip/></el-table></el-dialog>
 </div>
</template>
<style scoped>
.sql-history{display:flex;flex-direction:column;gap:12px;min-height:0;height:100%}.history-filters{display:flex;gap:8px;flex-wrap:wrap;flex-shrink:0}.history-filters .el-input{width:190px}.history-filters .el-select{width:145px}.history-filters .el-button+.el-button{margin-left:0}.history-table{flex:1;min-height:220px}.sql-history>.el-pagination{justify-content:flex-end;flex-shrink:0}.record-sql{display:block;width:100%;text-overflow:ellipsis;overflow:hidden;white-space:nowrap;text-align:left;background:transparent;border:0;color:var(--el-color-primary);font:inherit;cursor:pointer}.record-source{font:13px/1.7 ui-monospace,monospace;white-space:pre-wrap;overflow-wrap:anywhere;max-height:40vh;overflow:auto;background:var(--ui-panel-soft,#f8fafc);padding:12px}
</style>

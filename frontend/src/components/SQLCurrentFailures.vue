<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref } from 'vue';
import { useRouter } from 'vue-router';
import { ElMessageBox, type TableInstance } from 'element-plus';
import SQLRetryAccountDialog from './SQLRetryAccountDialog.vue';
import SQLViewDependencyTransferDialog from './SQLViewDependencyTransferDialog.vue';
import { listCurrentFailedSQL, executeFailedSQLStatement, type SQLFileViewTransferTarget, type SQLViewDependencyTask, type SQLCurrentFailure, type SQLExecuteOptions, type SQLRetryTarget, type SQLChangeFile, type SQLChangeStatement } from '../api/postgresql';
const props=defineProps<{options:SQLExecuteOptions;busy?:boolean}>();
const emit=defineEmits<{openFile:[id:number];started:[result:{file:SQLChangeFile;statements:SQLChangeStatement[]}];changed:[]}>();
const router=useRouter();const rows=ref<SQLCurrentFailure[]>([]);const page=ref(1),size=ref(30),total=ref(0);const loading=ref(false),working=ref(false);const selected=ref<SQLCurrentFailure|null>(null);const retryTarget=ref<SQLRetryTarget|null>(null);
const filter=ref({keyword:'',fileName:'',schemaName:'',handling:''});let generation=0;
const fileScope=ref<{id:number;name:string}|null>(null);
const transferSelection=ref<SQLCurrentFailure[]>([]);
const failureTable=ref<TableInstance>();
const transferTarget=ref<SQLFileViewTransferTarget|null>(null);
const selectedFileID=computed(()=>new Set(transferSelection.value.map(row=>row.fileId)).size===1?transferSelection.value[0]?.fileId:undefined);
async function load(){const g=++generation;loading.value=true;failureTable.value?.clearSelection();transferSelection.value=[];try{const r=await listCurrentFailedSQL(page.value,size.value,{...filter.value,fileId:fileScope.value?.id});if(g===generation){rows.value=r.list||[];total.value=r.total}}finally{if(g===generation)loading.value=false}}
async function search(){page.value=1;await load()}
async function showFile(id:number,name:string){fileScope.value={id,name};filter.value={keyword:'',fileName:'',schemaName:'',handling:''};await search()}
async function clearFileScope(){fileScope.value=null;await search()}
const taskText=(s?:string)=>({CREATED:'待分析',ANALYZING:'分析中',ANALYZED:'待执行',PRECHECKING:'预检中',PRECHECK_PASSED:'待执行',PRECHECK_FAILED:'预检失败',EXECUTING:'执行中',FAILED:'任务失败',UNKNOWN:'结果待核实',RESTORED:'已恢复视图，变更未完成',RESTORE_FAILED:'视图恢复失败',SUCCESS:'已完成'}[s||'']||s||'待处理');
const canTransfer=(r:SQLCurrentFailure)=>!r.viewDependencyTaskId&&/^ALTER\s+TABLE\b/i.test(r.sqlContent);
function transfer(row:SQLCurrentFailure){if(props.busy||working.value)return;transferTarget.value={fileId:row.fileId,statementIds:[row.id]}}
function transferSelected(){if(props.busy||working.value||!selectedFileID.value)return;transferTarget.value={fileId:selectedFileID.value,statementIds:transferSelection.value.map(row=>row.id)}}
async function transferred(task:SQLViewDependencyTask){transferTarget.value=null;emit('changed');await router.push({name:'PostgreSQLViewDependencyChange',query:{taskId:String(task.id)}})}
async function retry(row:SQLCurrentFailure){try{await ElMessageBox.confirm(row.transactionStartLine?'将重试整个事务组，组外语句不会执行。':'只重试此语句；同名视图 DROP/CREATE 组合会一起重试。','重试失败 SQL',{type:'warning'})}catch{return}working.value=true;try{const r=await executeFailedSQLStatement(row.id,props.options);emit('started',r);await load()}finally{working.value=false}}
function account(row:SQLCurrentFailure){retryTarget.value={statementId:row.id,fileId:row.fileId,fileName:row.fileName,lineNumber:row.lineNumber,schemaName:row.schemaName,originalAccount:row.databaseUser||row.schemaName,transactionLabel:row.transactionStartLine?`${row.transactionStartLine}–${row.transactionEndLine} 行整个事务组`:undefined}}
function started(r:{file:SQLChangeFile;statements:SQLChangeStatement[]}){emit('started',r);void load()}
onMounted(load);onBeforeUnmount(()=>{generation++});defineExpose({refresh:load,showFile});
</script>
<template>
 <div class="failure-page">
  <p class="failure-note">只显示当前仍未解决的失败语句，同一语句显示一次；手动暂缓的语句也在这里。历史失败尝试请查看“执行记录”。</p>
  <div v-if="fileScope" class="failure-note">当前文件：<el-tag closable @close="clearFileScope">{{fileScope.name}}</el-tag><el-button link type="primary" @click="clearFileScope">查看全部文件</el-button></div>
  <div class="failure-filters"><el-input v-model="filter.keyword" placeholder="SQL 关键字" clearable @keyup.enter="search"/><el-input v-model="filter.fileName" placeholder="文件名称" clearable @keyup.enter="search"/><el-input v-model="filter.schemaName" placeholder="文件 schema" clearable @keyup.enter="search"/><el-select v-model="filter.handling" placeholder="处理方式" clearable><el-option label="普通重试" value="retry"/><el-option label="已转视图依赖" value="view"/></el-select><el-button type="primary" @click="search">查询</el-button><el-button @click="load">刷新</el-button></div>
  <div class="failure-filters"><el-button type="primary" :disabled="busy||working||loading||!selectedFileID" @click="transferSelected">选中 {{transferSelection.length}} 条合并转入视图依赖</el-button><el-button v-if="fileScope" :disabled="busy||working||loading" @click="transferTarget={fileId:fileScope.id,all:true}">整文件转入视图依赖</el-button><span class="failure-note">同文件可多选；不同文件分别转入。<template v-if="transferSelection.length&&!selectedFileID">当前选择跨文件，请调整。</template></span></div>
  <div class="failure-table" v-loading="loading"><el-table ref="failureTable" :data="rows" row-key="id" height="100%" stripe @selection-change="transferSelection=$event">
   <el-table-column type="selection" width="44" :selectable="(row:SQLCurrentFailure)=>!busy&&!working&&!row.viewDependencyTaskId"/>
   <el-table-column prop="fileName" label="文件" min-width="270" show-overflow-tooltip/><el-table-column prop="lineNumber" label="行" width="65"/><el-table-column prop="schemaName" label="文件 schema" width="145"/>
   <el-table-column label="处理状态" width="190"><template #default="{row}"><el-tag :type="row.viewDependencyTaskId?'warning':'danger'" size="small">{{row.viewDependencyTaskId?taskText(row.taskStatus):row.manualFailure?'已标记失败／暂缓':'执行失败'}}</el-tag></template></el-table-column>
   <el-table-column prop="sqlContent" label="SQL" min-width="300" show-overflow-tooltip/><el-table-column label="执行信息" min-width="290" show-overflow-tooltip><template #default="{row}">{{row.taskMessage||row.executeMessage}}</template></el-table-column>
   <el-table-column label="操作" width="350" fixed="right"><template #default="{row}"><div class="failure-actions"><el-button link type="primary" @click="selected=row">详情</el-button><el-button link type="primary" @click="emit('openFile',row.fileId)">定位文件</el-button><template v-if="row.viewDependencyTaskId"><el-button link type="primary" @click="router.push({name:'PostgreSQLViewDependencyChange',query:{taskId:String(row.viewDependencyTaskId)}})">查看关联任务</el-button></template><template v-else><el-button link type="primary" :disabled="busy||working" @click="retry(row)">{{row.transactionStartLine?'整组重试':'单独重试'}}</el-button><el-button link type="primary" :disabled="busy||working" @click="account(row)">切换账号</el-button><el-button v-if="canTransfer(row)" link type="warning" :disabled="busy||working" @click="transfer(row)">转入视图依赖</el-button></template></div></template></el-table-column>
  </el-table></div>
  <el-pagination v-model:current-page="page" v-model:page-size="size" :page-sizes="[20,30,50,100]" :total="total" layout="total,sizes,prev,pager,next" @current-change="load" @size-change="search"/>
  <SQLRetryAccountDialog :target="retryTarget" :options="options" :disabled="busy||working" @close="retryTarget=null" @started="started"/>
  <SQLViewDependencyTransferDialog :target="transferTarget" :busy="busy||working" @close="transferTarget=null" @created="transferred"/>
  <el-dialog :model-value="!!selected" title="当前失败 SQL" width="900px" append-to-body @close="selected=null"><template v-if="selected"><p>{{selected.fileName}} · 第 {{selected.lineNumber}} 行 · {{selected.schemaName}}</p><pre>{{selected.sqlContent}}</pre><pre>{{selected.executeMessage}}</pre><p v-if="selected.viewDependencyTaskId">关联任务 #{{selected.viewDependencyTaskId}}：{{taskText(selected.taskStatus)}}<br/>{{selected.taskMessage}}</p></template><template #footer><el-button v-if="selected" type="primary" @click="emit('openFile',selected.fileId);selected=null">定位文件／编辑</el-button><el-button @click="selected=null">关闭</el-button></template></el-dialog>
 </div>
</template>
<style scoped>
.failure-page{display:flex;flex-direction:column;gap:12px;min-height:0;flex:1}.failure-note{margin:0;color:var(--el-text-color-secondary);font-size:13px;line-height:1.6}.failure-filters{display:flex;gap:8px;flex-wrap:wrap}.failure-filters .el-input{width:190px}.failure-filters .el-select{width:170px}.failure-table{flex:1;min-height:200px}.failure-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap}.failure-actions .el-button+.el-button{margin:0}.failure-page>.el-pagination{justify-content:flex-end}pre{white-space:pre-wrap;overflow-wrap:anywhere;max-height:45vh;overflow:auto;padding:12px;background:var(--ui-panel-soft,#f8fafc);font:13px/1.7 ui-monospace,monospace}
</style>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue';
import { useRouter, useRoute } from 'vue-router';
import { ElMessage } from 'element-plus';
import { listMonitoring, getMonitoring, getMonitoringHistory, refreshMonitoring, createConfiguredInspection, type MonitoringView, type MonitoringPoint } from '../api/monitoring';
import MetricChart from './MetricChart.vue';
import ServerMonitorView from '../views/ServerMonitorView.vue';
const props=defineProps<{kind:string}>(),router=useRouter(),route=useRoute();
const rows=ref<MonitoringView[]>([]),selected=ref(0),detail=ref<MonitoringView>(),history=ref<MonitoringPoint[]>([]),hours=ref(1),step=ref(60),error=ref(''),loading=ref(false),busy=ref(false);
let timer:ReturnType<typeof setInterval>|undefined,disposed=false,epoch=0,reading=false;
const overview=computed(()=>props.kind==='overview');
const current=computed(()=>rows.value.find(r=>r.id===selected.value));
const statusLabels:Record<string,string>={NORMAL:'正常',WARNING:'需关注',CRITICAL:'异常',ERROR:'采集失败',PARTIAL:'采集不完整',STALE:'数据过期',DISABLED:'已停用',PENDING:'等待自动采集'};
const checkLabels:Record<string,string>={NORMAL:'正常',RECORDED:'已记录',WARNING:'需关注',CRITICAL:'严重',UNKNOWN:'未取得结果',NOT_APPLICABLE:'不适用'};
const phaseLabels:Record<string,string>={live:'当前运行状态',facts:'主机与文件专项',deep:'数据库深度专项'};
const statusType=(s:string)=>s==='NORMAL'?'success':s==='CRITICAL'||s==='ERROR'?'danger':s==='WARNING'||s==='STALE'||s==='PARTIAL'?'warning':'info';
const time=(s?:string|null)=>s?new Date(s).toLocaleString('zh-CN',{hour12:false}):'尚未取得';
const period=(n:number)=>n>=3600?`${n/3600} 小时`:n>=60?`${n/60} 分钟`:`${n} 秒`;
const times=computed(()=>history.value.map(p=>p.collectedAt));
const chartGroups:Record<string,Array<{title:string;unit:string;keys:Array<[string,string,number?]>}>>={
 postgresql:[{title:'连接数',unit:'个',keys:[['connections','当前连接'],['maxConnections','连接上限']]},{title:'等待与长时间运行',unit:'个',keys:[['lockWaits','锁等待'],['slowQueries','当前慢 SQL'],['longTransactions','长事务']]}],
 kubernetes:[{title:'节点与 Pod',unit:'个',keys:[['nodes','节点'],['pods','Pod']]},{title:'当前异常与重启变化',unit:'个',keys:[['unhealthyNodes','异常节点'],['unreadyPods','未就绪 Pod'],['restartIncrease','连续样本相同 Pod 新增重启']]}],
 elasticsearch:[{title:'分片状态',unit:'个',keys:[['activeShards','活跃分片'],['unassignedShards','未分配分片']]},{title:'节点资源最高使用率',unit:'%',keys:[['maxDiskPercent','磁盘'],['maxHeapPercent','JVM 堆']]}],
 redis:[{title:'连接与操作速率',unit:'',keys:[['connections','连接数'],['operationsPerSecond','操作/秒']]},{title:'内存',unit:'MiB',keys:[['memoryBytes','已用内存',1048576],['maxMemoryBytes','配置上限',1048576]]}],
 rabbitmq:[{title:'消息状态',unit:'条',keys:[['readyMessages','待消费'],['unackedMessages','未确认']]},{title:'连接与消费者',unit:'个',keys:[['connections','连接'],['consumers','消费者']]}],
};
const colors=['#3979cc','#3b9d83','#c75c68'];
const charts=computed(()=>(chartGroups[props.kind]||[]).map(g=>({...g,series:g.keys.map(([key,name,divisor],i)=>({name,color:colors[i%colors.length]!,values:history.value.map(p=>p.indicators?.[key]==null?null:p.indicators[key]!/(divisor||1))}))})));
async function load() {
 if(reading||disposed)return;reading=true;loading.value=rows.value.length===0;const ticket=epoch;
 try {
  const all=await listMonitoring();if(ticket!==epoch||disposed)return;
  rows.value=overview.value?all:all.filter(r=>r.kind===props.kind);
  if(overview.value){detail.value=undefined;error.value='';return;}
  if(!rows.value.some(r=>r.id===selected.value))selected.value=rows.value.find(r=>r.id===Number(route.query?.id))?.id||rows.value[0]?.id||0;
  const row=current.value;if(!row){detail.value=undefined;history.value=[];return;}
  const id=row.id,type=row.targetType;
  const value=await getMonitoring(type,id);if(ticket!==epoch||disposed||id!==selected.value)return;detail.value=value;
  if(props.kind!=='hosts') {
   const h=await getMonitoringHistory(type,id,hours.value);if(ticket!==epoch||disposed||id!==selected.value)return;history.value=h.samples;step.value=h.stepSeconds;
  }
  error.value='';
 }catch(e){if(ticket===epoch&&!disposed)error.value=(e as Error).message||'读取监控失败';}
 finally {reading=false;if(ticket===epoch)loading.value=false;else if(!disposed)void load();}
}
async function select(row:MonitoringView) {
 if(overview.value){await router.push({path:`/monitoring/${row.kind}`,query:{id:row.id}});return;}
 ++epoch;selected.value=row.id;detail.value=undefined;history.value=[];if(!reading)await load();
}
async function refresh() {
 const row=current.value;if(!row)return;busy.value=true;
 try{await refreshMonitoring(row.targetType,row.id);ElMessage.success('后台采集已受理，数据将自动更新');await load();}catch(e){error.value=(e as Error).message;}finally{busy.value=false;}
}
async function report() {
 const row=current.value;if(!row)return;busy.value=true;
 try{const run=await createConfiguredInspection('系统巡检报告',[{type:row.targetType,id:row.id}]);await router.push(`/inspections/${run.id}`);}catch(e){error.value=(e as Error).message;}finally{busy.value=false;}
}
watch(()=>props.kind,()=>{++epoch;selected.value=0;rows.value=[];detail.value=undefined;history.value=[];void load();},{immediate:true});
onMounted(()=>{timer=setInterval(()=>void load(),5000);});
onUnmounted(()=>{disposed=true;++epoch;if(timer)clearInterval(timer);});
</script>

<template>
 <div class="daily-monitoring" v-loading="loading">
  <div class="toolbar"><span>后台持续采集 · 页面每 5 秒自动更新 · 历史保留 48 小时</span><el-button @click="load">刷新页面数据</el-button></div>
  <el-alert v-if="error" :title="error" type="error" :closable="false"/>
  <el-table v-if="rows.length" :data="rows" highlight-current-row @row-click="select" class="instance-table">
   <el-table-column label="实例" min-width="180"><template #default="{row}"><strong>{{row.name}}</strong><div class="muted">{{row.endpoint}}</div></template></el-table-column>
   <el-table-column v-if="overview" prop="kind" label="组件" width="140"/>
   <el-table-column label="当前状态" width="145"><template #default="{row}"><el-tag :type="statusType(row.status)">{{statusLabels[row.status]||row.status}}</el-tag></template></el-table-column>
   <el-table-column label="关键情况" min-width="260"><template #default="{row}">{{row.lastError||row.summary||(row.enabled?'正在后台获取首个样本':'后台采集已停用')}}</template></el-table-column>
   <el-table-column label="最近有效数据" width="185"><template #default="{row}">{{time(row.collectedAt)}}</template></el-table-column>
   <el-table-column label="频率" width="95"><template #default="{row}">{{period(row.intervalSeconds)}}</template></el-table-column>
   <el-table-column width="90"><template #default="{row}"><el-button link type="primary" @click.stop="select(row)">查看详情</el-button></template></el-table-column>
  </el-table>
  <el-empty v-else-if="!loading" :description="kind==='hosts'?'尚未纳管 Linux 主机':'尚未登记此组件，配置连接后将自动采集'"/>
  <template v-if="!overview&&current">
   <div class="detail-heading"><div><h3>{{current.name}}</h3><span class="muted">{{current.endpoint}}</span></div><div class="actions">
    <el-button v-if="kind==='postgresql'" @click="router.push({path:'/postgresql/workbench',query:{databaseId:selected,pane:'locks'}})">锁与 SQL 诊断</el-button>
    <el-button :loading="busy" :disabled="!current.enabled" @click="refresh">立即刷新</el-button><el-button :loading="busy" @click="report">生成此对象报告</el-button>
   </div></div>
   <ServerMonitorView v-if="kind==='hosts'" :key="selected" :server-id="selected" embedded/>
   <template v-else-if="detail">
    <div class="status-strip"><el-tag :type="statusType(detail.status)">{{statusLabels[detail.status]}}</el-tag><span>最近有效数据：{{time(detail.collectedAt)}}</span><span>每 {{period(detail.intervalSeconds)}}自动采集</span><span v-if="detail.collecting">正在更新</span></div>
    <el-alert v-if="detail.lastError" :title="`${detail.status==='PARTIAL'?'本轮采集不完整':'最近采集失败'}：${detail.lastError}；后台自动重试，请核对实际采集时间。`" type="error" :closable="false"/>
    <el-alert v-else-if="detail.stale&&detail.result" title="数据已过期，以下是上次取得的数据，后台正在更新。" type="warning" :closable="false"/>
    <el-empty v-if="!detail.result" :description="current.enabled?'后台自动采集中，等待首个有效样本':'后台采集已停用'"/>
    <div v-if="charts.length" class="history-toolbar"><span>监控趋势</span><el-select v-model="hours" style="width:140px" @change="load"><el-option v-for="h in [1,6,24,48]" :key="h" :value="h" :label="`最近 ${h} 小时`"/></el-select></div>
    <div class="charts"><MetricChart v-for="chart in charts" :key="chart.title" :title="chart.title" :times="times" :series="chart.series" :unit="chart.unit" :gap-seconds="Math.max(step*2,detail.intervalSeconds*3)"/></div>
   </template>
   <template v-for="lane in detail?.lanes||[]" :key="lane.lane">
    <section v-if="!(kind==='hosts'&&lane.lane==='live')" class="phase">
     <div class="phase-heading"><h4>{{phaseLabels[lane.lane]||lane.lane}}</h4><span>每 {{period(lane.intervalSeconds)}} · 最近有效数据 {{time(lane.collectedAt)}}</span><el-tag v-if="lane.stale&&lane.result" type="warning">已过期</el-tag></div>
     <el-alert v-if="lane.lastError&&lane.lane!=='live'" :title="lane.lastError" type="warning" :closable="false"/>
     <p v-if="!lane.result" class="muted">{{lane.collecting?'正在后台采集':'等待后台自动更新'}}，无需手动执行检查。</p>
     <article v-for="(check,index) in lane.result?.reportChecks||[]" :key="`${check.code}-${index}`" class="observation">
      <div class="check-heading"><strong>{{check.name}}</strong><el-tag size="small" :type="statusType(check.status)">{{checkLabels[check.status]||check.status}}</el-tag><span v-if="check.origin" class="muted">{{check.origin.name||check.origin.endpoint}}</span></div>
      <p>{{check.value}}</p><p v-if="check.reason" class="muted">{{check.reason}}</p><p v-for="gap in check.gaps||[]" :key="gap" class="gap">{{gap}}</p>
      <div v-for="(table,i) in check.tables||[]" :key="i" class="result-table"><h5 v-if="table.title">{{table.title}}</h5><el-table :data="table.rows.slice(0,25)" size="small"><el-table-column v-for="(column,j) in table.columns" :key="j" :label="column" min-width="130"><template #default="{row}">{{row[j]}}</template></el-table-column></el-table><p v-if="table.rows.length>25" class="muted">共 {{table.rows.length}} 行，页面展示前 25 行；完整结果保留在采集明细中。</p></div>
     </article>
     <details v-if="lane.result"><summary>采集明细</summary><pre>{{JSON.stringify(lane.result,null,2)}}</pre></details>
    </section>
   </template>
  </template>
 </div>
</template>
<style scoped>
.daily-monitoring{display:flex;flex-direction:column;gap:18px}.toolbar,.detail-heading,.actions,.status-strip,.phase-heading,.check-heading,.history-toolbar{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.toolbar,.detail-heading,.phase-heading{justify-content:space-between}.toolbar,.muted,.phase-heading>span,.status-strip,.history-toolbar{font-size:12px;color:#718198}.instance-table{cursor:pointer}.instance-table strong{font-weight:500}.instance-table .muted{margin-top:6px}.detail-heading{border-top:1px solid #e3e9f1;padding-top:20px}.detail-heading h3{margin:0 0 8px}.charts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.phase{border:1px solid #e0e7f0;border-radius:8px;padding:18px}.phase h4{margin:0;font-size:15px}.observation{margin-top:18px;padding-top:14px;border-top:1px solid #eef1f6}.observation p{font-size:13px;line-height:1.8;margin:8px 0;overflow-wrap:anywhere}.gap{color:#b57425}.result-table{margin-top:12px}.result-table h5{margin:8px 0}details{margin-top:18px;font-size:12px;color:#718198}summary{cursor:pointer}pre{max-height:380px;overflow:auto;background:#f5f7fa;padding:14px;white-space:pre-wrap;overflow-wrap:anywhere}@media(max-width:1050px){.charts{grid-template-columns:1fr}.detail-heading{align-items:flex-start}}
</style>

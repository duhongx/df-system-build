<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { useRouter, useRoute } from "vue-router";
import { ElMessage } from "element-plus";
import { listMonitoring, getMonitoring, getMonitoringHistory, refreshMonitoring, createConfiguredInspection, type MonitoringView, type MonitoringPoint } from "../api/monitoring";
import { componentLabels, monitorStatusLabels, metricCards, instanceFacts, monitoringSummary, currentIssues, liveChecks, monitoringSections } from "../utils/monitoring-presentation";
import MonitoringDataTable from "./MonitoringDataTable.vue";
import MetricChart from "./MetricChart.vue";
import ServerMonitorView from "../views/ServerMonitorView.vue";
const props = defineProps<{ kind: string }>(), router = useRouter(), route = useRoute();
const rows = ref<MonitoringView[]>([]), selected = ref(0), detail = ref<MonitoringView>();
const history = ref<MonitoringPoint[]>([]), hours = ref(1), step = ref(60), error = ref("");
const loading = ref(false), busy = ref(false);
const overviewPage = ref(1), overviewPageSize = ref(25), overviewSearch = ref("");
let timer: ReturnType<typeof setInterval> | undefined, disposed = false, epoch = 0, reading = false;
const overview = computed(() => props.kind === "overview");
const current = computed(() => rows.value.find(r => r.id === selected.value));
const instanceKey = (row: MonitoringView) => `${row.targetType}:${row.id}`;
const cards = computed(() => metricCards(props.kind, detail.value));
const facts = computed(() => instanceFacts(props.kind, detail.value));
const issues = computed(() => currentIssues(detail.value));
const sections = computed(() => monitoringSections(detail.value));
const overviewRows = computed(() => rows.value.filter(row => `${row.name} ${row.endpoint} ${componentLabels[row.kind] || row.kind}`.toLowerCase().includes(overviewSearch.value.toLowerCase())));
const overviewVisible = computed(() => overviewRows.value.slice((overviewPage.value - 1) * overviewPageSize.value, overviewPage.value * overviewPageSize.value));
const hasData = computed(() => !!detail.value?.result || liveChecks(detail.value).length > 0);
const statusType = (s: string) => s === "NORMAL" ? "success" : ["CRITICAL", "ERROR"].includes(s) ? "danger" : ["WARNING", "STALE", "PARTIAL"].includes(s) ? "warning" : "info";
const time = (s?: string | null) => s ? new Date(s).toLocaleString("zh-CN", { hour12: false }) : "—";
const times = computed(() => history.value.map(p => p.collectedAt));
const groups: Record<string, Array<{ title: string; unit: string; keys: Array<[string, string, number?]> }>> = {
  postgresql: [{ title: "连接数", unit: "个", keys: [["connections", "当前连接"]] }, { title: "锁等待与长时间运行", unit: "个", keys: [["lockWaits", "锁等待"], ["slowQueries", "慢 SQL"], ["longTransactions", "长事务"]] }],
  kubernetes: [{ title: "节点与 Pod", unit: "个", keys: [["nodes", "节点"], ["pods", "Pod"]] }, { title: "当前异常", unit: "个", keys: [["unhealthyNodes", "异常节点"], ["unreadyPods", "异常 Pod"], ["restartIncrease", "新增重启"]] }],
  minio: [{ title: "磁盘状态", unit: "个", keys: [["onlineDisks", "在线"], ["offlineDisks", "离线"]] }, { title: "最高磁盘使用率", unit: "%", keys: [["maxDiskPercent", "磁盘使用率"]] }],
  elasticsearch: [{ title: "分片状态", unit: "个", keys: [["activeShards", "活跃分片"], ["unassignedShards", "未分配分片"]] }, { title: "节点资源最高使用率", unit: "%", keys: [["maxDiskPercent", "磁盘"], ["maxHeapPercent", "JVM 堆"]] }],
  redis: [{ title: "操作速率", unit: "次/s", keys: [["operationsPerSecond", "操作速率"]] }, { title: "内存", unit: "MiB", keys: [["memoryBytes", "已用内存", 1048576], ["maxMemoryBytes", "配置上限", 1048576]] }],
  rabbitmq: [{ title: "消息状态", unit: "条", keys: [["readyMessages", "待消费"], ["unackedMessages", "未确认"]] }, { title: "连接与消费者", unit: "个", keys: [["connections", "连接"], ["consumers", "消费者"]] }],
};
groups.postgresql!.push({title:"复制槽 WAL 保留",unit:"GiB",keys:[["maxSlotRetainedBytes","最大 Slot 保留 WAL",1073741824]]},{title:"归档失败累计",unit:"次",keys:[["archiveFailedCount","累计失败"]]});
groups.redis!.push({title:"连接数",unit:"个",keys:[["connections","连接数"]]},{title:"近期慢日志最大耗时",unit:"ms",keys:[["slowLogMaxMs","最近 50 条中的最大耗时"]]});
groups.rabbitmq![1]={title:"消息生产与投递速率",unit:"条/s",keys:[["publishRate","生产"],["deliverRate","投递"],["ackRate","确认"]]};
const colors = ["#3979cc", "#3b9d83", "#c75c68"];
const charts = computed(() => (groups[props.kind] || []).map(g => ({ ...g, series: g.keys.map(([key, name, divisor], i) => ({ name, color: colors[i % colors.length]!, values: history.value.map(p => p.indicators?.[key] == null ? null : p.indicators[key]! / (divisor || 1)) })) })));
async function load() {
  if (reading || disposed) return;
  reading = true; loading.value = rows.value.length === 0;
  const ticket = epoch;
  try {
    const all = await listMonitoring();
    if (ticket !== epoch || disposed) return;
    rows.value = overview.value ? all : all.filter(r => r.kind === props.kind);
    if (overview.value) { detail.value = undefined; error.value = ""; return; }
    if (!rows.value.some(r => r.id === selected.value)) selected.value = rows.value.find(r => r.id === Number(route.query?.id))?.id || rows.value[0]?.id || 0;
    const row = current.value;
    if (!row) { detail.value = undefined; history.value = []; error.value = ""; return; }
    // Linux renders its shared host monitor independently of report snapshots.
    if (props.kind === "hosts") { error.value = ""; return; }
    const id = row.id, type = row.targetType;
    const value = await getMonitoring(type, id);
    if (ticket !== epoch || disposed || id !== selected.value) return;
    detail.value = value;
    const h = await getMonitoringHistory(type, id, hours.value);
    if (ticket !== epoch || disposed || id !== selected.value) return;
    history.value = h.samples || []; step.value = h.stepSeconds; error.value = "";
  } catch (e) {
    if (ticket === epoch && !disposed) error.value = (e as Error).message || "读取监控失败";
  } finally {
    reading = false;
    if (ticket === epoch) loading.value = false;
    else if (!disposed) void load();
  }
}
async function select(row: MonitoringView) {
  if (overview.value) { await router.push({ path: `/monitoring/${row.kind}`, query: { id: row.id } }); return; }
  ++epoch; selected.value = row.id; detail.value = undefined; history.value = [];
  if (!reading) await load();
}
async function selectById(id: number) { const row = rows.value.find(r => r.id === id); if (row) await select(row); }
async function refresh() {
  const row = current.value; if (!row) return; busy.value = true;
  try { await refreshMonitoring(row.targetType, row.id); ElMessage.success("正在更新"); await load(); }
  catch (e) { error.value = (e as Error).message; } finally { busy.value = false; }
}
async function report() {
  const row = current.value; if (!row) return; busy.value = true;
  try { const run = await createConfiguredInspection("系统巡检报告", [{ type: row.targetType, id: row.id }]); await router.push(`/inspections/${run.id}`); }
  catch (e) { error.value = (e as Error).message; } finally { busy.value = false; }
}
watch(() => props.kind, () => { ++epoch; selected.value = 0; rows.value = []; detail.value = undefined; history.value = []; error.value = ""; void load(); }, { immediate: true });
watch(hours, () => { ++epoch; history.value = []; void load(); });
watch([overviewSearch, overviewPageSize], () => { overviewPage.value = 1; });
watch(() => overviewRows.value.length, n => { overviewPage.value = Math.min(overviewPage.value, Math.max(1, Math.ceil(n / overviewPageSize.value))); });
onMounted(() => { timer = setInterval(() => void load(), 5000); });
onUnmounted(() => { disposed = true; ++epoch; if (timer) clearInterval(timer); });
</script>
<template>
  <div class="daily-monitoring" v-loading="loading">
    <div class="toolbar"><span>自动更新 · 最近 48 小时</span><el-button @click="load">刷新</el-button></div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <div v-if="rows.length && !overview" class="instance-picker">
      <span>{{ kind === 'hosts' ? '主机' : '实例' }}</span>
      <el-select v-model="selected" filterable :aria-label="kind === 'hosts' ? '切换监控主机' : '切换监控实例'" style="width:min(560px,100%)" placeholder="搜索名称或地址" @change="selectById">
        <el-option v-for="row in rows" :key="row.id" :value="row.id" :label="`${row.name} · ${row.endpoint}`" />
      </el-select><span class="muted">共 {{ rows.length }} 个</span>
    </div>
    <el-input v-if="overview && rows.length" v-model="overviewSearch" clearable placeholder="搜索名称、地址或组件" style="max-width:400px" />
    <el-table v-if="overview && rows.length" :data="overviewVisible" :row-key="instanceKey" @row-click="select" class="instance-table">
      <el-table-column label="实例" min-width="180"><template #default="{ row }"><strong>{{ row.name }}</strong><div class="muted">{{ row.endpoint }}</div></template></el-table-column>
      <el-table-column v-if="overview" label="组件" width="130"><template #default="{ row }">{{ componentLabels[row.kind] || row.kind }}</template></el-table-column>
      <el-table-column label="状态" width="145"><template #default="{ row }"><el-tag :type="statusType(row.status)">{{ monitorStatusLabels[row.status] || row.status }}</el-tag></template></el-table-column>
      <el-table-column label="关键指标" min-width="280"><template #default="{ row }">{{ monitoringSummary(row) }}</template></el-table-column>
      <el-table-column label="更新时间" width="185"><template #default="{ row }">{{ time(row.collectedAt) }}</template></el-table-column>
      <el-table-column width="90"><template #default="{ row }"><el-button link type="primary" @click.stop="select(row)">查看</el-button></template></el-table-column>
    </el-table>
    <el-pagination v-if="overview && overviewRows.length > overviewPageSize" v-model:current-page="overviewPage" v-model:page-size="overviewPageSize" :total="overviewRows.length" :page-sizes="[10,25,50]" layout="total, sizes, prev, pager, next" />
    <el-empty v-if="!rows.length && !loading && !error" :description="kind === 'hosts' ? '尚未纳管 Linux 主机' : '尚未登记此组件，请先配置连接'" />
    <template v-if="!overview && current">
      <ServerMonitorView v-if="kind === 'hosts'" :key="selected" :server-id="selected" embedded />
      <template v-else>
        <div class="detail-heading"><div><h3>{{ current.name }}</h3><span class="muted">{{ current.endpoint }}</span></div><div class="actions">
          <el-button v-if="kind === 'postgresql'" @click="router.push({ path: '/postgresql/workbench', query: { databaseId: selected, pane: 'locks' } })">锁与 SQL 诊断</el-button>
          <el-button :loading="busy" :disabled="!current.enabled" @click="refresh">立即刷新</el-button><el-button :loading="busy" @click="report">生成报告</el-button>
        </div></div>
        <div v-if="detail" class="status-strip"><el-tag :type="statusType(detail.status)">{{ monitorStatusLabels[detail.status] || detail.status }}</el-tag><span>更新于 {{ time(detail.collectedAt) }}</span><span v-if="detail.collecting">正在更新</span></div>
        <el-alert v-if="detail?.lastError" :title="`最近更新失败：${detail.lastError}`" type="error" :closable="false" />
        <el-alert v-else-if="detail?.stale && hasData" title="数据已过期，正在自动重试" type="warning" :closable="false" />
        <el-empty v-if="detail && !hasData" :description="current.enabled ? '正在获取监控数据' : '已停用'" />
        <el-descriptions v-if="facts.length" :column="4" border class="instance-facts"><el-descriptions-item v-for="fact in facts" :key="fact.label" :label="fact.label">{{ fact.value }}</el-descriptions-item></el-descriptions>
        <div class="metric-cards"><div v-for="card in cards" :key="card.key" class="metric-card"><span>{{ card.label }}</span><strong>{{ card.value }}<small v-if="card.unit && card.value !== '—'">{{ card.unit }}</small></strong></div></div>
        <div class="history-toolbar"><h4>运行趋势</h4><el-select v-model="hours" style="width:140px"><el-option v-for="h in [1, 6, 24, 48]" :key="h" :value="h" :label="`最近 ${h} 小时`" /></el-select></div>
        <div class="charts"><MetricChart v-for="chart in charts" :key="chart.title" :title="chart.title" :times="times" :series="chart.series" :unit="chart.unit" :gap-seconds="Math.max(step * 2, (detail?.intervalSeconds || 60) * 3)" /></div>
        <MonitoringDataTable v-for="section in sections" :key="section.code" :check="section" />
        <section v-if="issues.length" class="issues">
          <h4>当前异常</h4>
          <article v-for="(issue, i) in issues" :key="i">
            <div class="issue-heading"><strong>{{ issue.name }}</strong><el-tag size="small" :type="issue.status === '异常' ? 'danger' : 'warning'">{{ issue.status }}</el-tag></div>
            <p>{{ issue.message }}</p>
            <div v-for="(table, n) in issue.tables" :key="n">
              <el-table :data="table.rows.slice(0, 25)" size="small"><el-table-column v-for="(column, j) in table.columns" :key="j" :label="column" min-width="130"><template #default="{ row }">{{ row[j] }}</template></el-table-column></el-table>
              <p v-if="table.rows.length > 25" class="muted">共 {{ table.rows.length }} 个异常，显示前 25 个</p>
            </div>
          </article>
        </section>
      </template>
    </template>
  </div>
</template>
<style scoped>
.daily-monitoring{display:flex;flex-direction:column;gap:18px}.instance-picker,.toolbar,.detail-heading,.actions,.status-strip,.history-toolbar,.issue-heading{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.toolbar,.detail-heading,.history-toolbar{justify-content:space-between}.toolbar,.muted,.status-strip{font-size:12px;color:#718198}.instance-table{cursor:pointer}.instance-table strong{font-weight:500}.instance-table .muted{margin-top:6px}.detail-heading{border-top:1px solid #e3e9f1;padding-top:20px}.detail-heading h3{margin:0 0 8px}.metric-cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:14px}.metric-card{border:1px solid #e0e7f0;border-radius:8px;padding:18px}.metric-card>span{font-size:12px;color:#718198}.metric-card strong{display:block;font-size:27px;color:#243e60;margin-top:12px}.metric-card small{font-size:13px;font-weight:400;margin-left:6px}.charts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.history-toolbar h4,.issues h4{font-size:14px;margin:0}.issues{border:1px solid #f1dcc2;background:#fffcf7;border-radius:8px;padding:18px}.issues article{margin-top:16px}.issues p{font-size:13px;line-height:1.7;overflow-wrap:anywhere}.issue-heading{font-size:13px}@media(max-width:1050px){.charts{grid-template-columns:1fr}.instance-facts :deep(.el-descriptions__table){table-layout:fixed}.detail-heading{align-items:flex-start}}
</style>

<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount } from 'vue';
import { getMonitoringSlowQueries, getReportSlowQueries, type SlowQueryPage, type SlowQueryRecord } from '../api/monitoring';
const props = defineProps<{ databaseId?: number; reportId?: number }>();
const data = ref<SlowQueryPage>(), page = ref(1), size = ref(20), keyword = ref(''), database = ref(''), target = ref(0);
const loading = ref(false), error = ref(''), expanded = ref<SlowQueryRecord>(), asOf = ref(0);
let epoch = 0, disposed = false, timer: ReturnType<typeof setInterval> | undefined;
const time = (value?: string) => value ? new Date(value).toLocaleString('zh-CN', {hour12:false}) : '—';
async function load(reset = false) {
  if (!props.reportId && !props.databaseId) return;
  const ticket = ++epoch;
  if (reset) { page.value = 1; asOf.value = 0; }
  loading.value = true;
  try {
    const params = {page:page.value, size:size.value, keyword:keyword.value, database:database.value, targetRunId:target.value || undefined, asOfId:asOf.value || undefined};
    const result = props.reportId ? await getReportSlowQueries(props.reportId, params) : await getMonitoringSlowQueries(props.databaseId!, params);
    if (disposed || ticket !== epoch) return;
    data.value = result; asOf.value = result.asOfId || 0; error.value = '';
  } catch (e) { if (!disposed && ticket === epoch) error.value = (e as Error).message; }
  finally { if (!disposed && ticket === epoch) loading.value = false; }
}
watch(() => [props.databaseId, props.reportId], () => { data.value = undefined; target.value = 0; expanded.value = undefined; void load(true); }, {immediate:true});
onMounted(() => { timer = setInterval(() => { if (!props.reportId && page.value === 1 && !loading.value) { asOf.value = 0; void load(); } }, 10000); });
onBeforeUnmount(() => { disposed = true; ++epoch; if (timer) clearInterval(timer); });
</script>
<template>
  <section v-if="!reportId || !data || data.targets.length" class="slow-queries" v-loading="loading">
    <div class="heading"><h4>{{ reportId ? '慢 SQL 存档' : '已发生慢 SQL' }}</h4><el-button @click="load(true)">刷新</el-button></div>
    <p class="muted">CSV 日志 · 耗时 ≥ 5 秒 · 按发生时间倒序<span v-if="data?.frozenAt"> · 快照 {{ time(data.frozenAt) }}</span><span v-else-if="data?.collectedAt"> · 日志同步 {{ time(data.collectedAt) }}</span></p>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <el-alert v-if="data?.message" :title="data.message" type="info" :closable="false" />
    <div class="filters">
      <el-select v-if="reportId && (data?.targets?.length || 0) > 1" v-model="target" aria-label="慢 SQL 实例" @change="load(true)"><el-option :value="0" label="全部实例"/><el-option v-for="item in data?.targets" :key="item.id" :value="item.id" :label="item.name"/></el-select>
      <el-input v-model="database" clearable placeholder="数据库名称（全部数据库）" @change="load(true)" />
      <el-input v-model="keyword" clearable placeholder="搜索 SQL、用户或数据库" @change="load(true)" />
    </div>
    <el-table :data="data?.list || []" row-key="id" empty-text="没有已保存的慢 SQL 记录">
      <el-table-column label="发生时间" width="175"><template #default="{row}">{{ time(row.eventTime) }}</template></el-table-column>
      <el-table-column v-if="reportId" prop="instance" label="实例" min-width="140"/>
      <el-table-column prop="database" label="数据库" min-width="120"/>
      <el-table-column label="耗时 ms" width="115"><template #default="{row}">{{ row.durationMs.toFixed(1) }}</template></el-table-column>
      <el-table-column prop="username" label="用户" width="120"/>
      <el-table-column prop="client" label="客户端" width="150"/>
      <el-table-column label="SQL" min-width="260"><template #default="{row}"><button class="sql-summary" @click="expanded = row">{{ row.sql }}</button></template></el-table-column>
    </el-table>
    <el-pagination v-model:current-page="page" v-model:page-size="size" :total="data?.total || 0" :page-sizes="[20,50,100]" layout="total, sizes, prev, pager, next" @current-change="load()" @size-change="load(true)" />
    <el-dialog :model-value="!!expanded" title="慢 SQL 记录" width="85%" @close="expanded = undefined"><template v-if="expanded"><p>{{ expanded.instance }} · {{ expanded.database }} · {{ time(expanded.eventTime) }} · {{ expanded.durationMs }} ms · {{ expanded.username }}</p><pre>{{ expanded.sql }}</pre></template></el-dialog>
  </section>
</template>
<style scoped>
.slow-queries{border:1px solid var(--ui-border,#e2e8f0);border-radius:8px;padding:16px;background:var(--ui-surface,#fff);margin-top:16px}.heading,.filters{display:flex;align-items:center;gap:12px}.heading{justify-content:space-between}h4{margin:0}.muted{font-size:12px;color:var(--ui-muted,#78899c)}.filters{margin:12px 0;flex-wrap:wrap}.filters>*{max-width:300px}.sql-summary{display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;text-align:left;background:none;border:0;cursor:pointer;color:var(--el-color-primary);font-family:monospace;white-space:pre-wrap;overflow-wrap:anywhere}.el-pagination{margin-top:14px}pre{white-space:pre-wrap;overflow-wrap:anywhere}
</style>

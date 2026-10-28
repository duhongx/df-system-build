<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { ElMessage } from "element-plus";
import {
  getSlowAnalysis,
  getStatementStatistics,
  type SlowReport,
  type SlowTemplate,
  type StatementReport,
  type StatementStat,
} from "../../api/database-workbench";
import {
  getDatabaseEvent,
  syncDatabaseLogs,
  getLogCursors,
  type OpsDatabase,
  type DatabaseEvent,
} from "../../api/operations";
import {
  formatDate as time,
  formatDuration as duration,
} from "../../utils/database-workbench.mjs";
import EventDetail from "./EventDetail.vue";
const props = defineProps<{ connection: OpsDatabase }>();
const emit = defineEmits<{
  openSql: [string, string];
  configure: [];
  synced: [];
}>();
const mode = ref("logs");
const range = ref<[Date, Date]>([new Date(Date.now() - 86400000), new Date()]);
const phase = ref("");
const keyword = ref("");
const minimum = ref(0);
const loading = ref(false);
const error = ref("");
const report = ref<SlowReport>();
const stats = ref<StatementReport>();
const selected = ref<SlowTemplate>();
const selectedStat = ref<StatementStat>();
const order = ref("total");
const metric = ref<"logCount" | "maxMs">("logCount");
const page = ref(1);
const detail = ref<DatabaseEvent>();
const detailOpen = ref(false);
const cursorOpen = ref(false);
const cursors = ref<Awaited<ReturnType<typeof getLogCursors>>>([]);
let generation = 0;
const source = computed(() => report.value?.source || props.connection);
const sorted = computed(() =>
  [...(report.value?.templates || [])].sort(
    (a, b) =>
      ({
        total: b.totalMs - a.totalMs,
        average: b.averageMs - a.averageMs,
        max: b.maxMs - a.maxMs,
        calls: b.logCount - a.logCount,
      })[order.value] || 0,
  ),
);
const pageRows = computed(() =>
  sorted.value.slice((page.value - 1) * 25, page.value * 25),
);
const trend = computed(() => {
  const data = report.value?.trend || [];
  const max = Math.max(1, ...data.map((x) => x[metric.value]));
  return {
    max,
    points: data
      .map(
        (point, i) =>
          `${(i * 1000) / Math.max(1, data.length - 1)},${110 - (point[metric.value] * 94) / max}`,
      )
      .join(" "),
    data,
  };
});
async function load() {
  const token = ++generation;
  loading.value = true;
  error.value = "";
  selected.value = undefined;
  selectedStat.value = undefined;
  try {
    if (mode.value === "logs") {
      if (!range.value?.[0] || !range.value?.[1])
        throw new Error("请选择完整的时间范围");
      const data = await getSlowAnalysis(props.connection.id, {
        from: range.value[0].toISOString(),
        to: range.value[1].toISOString(),
        keyword: keyword.value,
        phase: phase.value,
        minDuration: minimum.value,
      });
      if (token === generation) {
        report.value = data;
        page.value = 1;
      }
    } else {
      const data = await getStatementStatistics(
        props.connection.id,
        order.value,
      );
      if (token === generation) stats.value = data;
    }
  } catch (e: any) {
    if (token === generation) error.value = e.message || "加载失败";
  } finally {
    if (token === generation) loading.value = false;
  }
}
function preset(hours: number) {
  range.value = [new Date(Date.now() - hours * 3600000), new Date()];
  load();
}
async function sync() {
  await syncDatabaseLogs(props.connection.id);
  ElMessage.success("日志同步已提交，完成后刷新分析");
  emit("synced");
}
async function showCursors() {
  cursors.value = await getLogCursors(props.connection.id);
  cursorOpen.value = true;
}
async function openEvent(event: DatabaseEvent) {
  detail.value = await getDatabaseEvent(props.connection.id, event.id);
  detailOpen.value = true;
}
watch(
  () => [props.connection.id, props.connection.revision],
  () => {
    report.value = undefined;
    stats.value = undefined;
    load();
  },
  { immediate: true },
);
watch(mode, load);
watch(order, () => {
  page.value = 1;
  if (mode.value === "statistics") load();
});
</script>
<template>
  <section class="slow-panel">
    <div class="pg-toolbar">
      <div class="pg-title"><strong>慢 SQL 分析</strong></div>
      <div class="source-switch">
        <button :class="{ active: mode === 'logs' }" @click="mode = 'logs'">
          时间段慢日志</button
        ><button
          :class="{ active: mode === 'statistics' }"
          @click="mode = 'statistics'"
        >
          累计 SQL 统计
        </button>
      </div>
      <span class="toolbar-spacer" /><el-button
        size="small"
        :loading="loading"
        @click="load"
        ><el-icon><Refresh /></el-icon>刷新</el-button
      >
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <template v-if="mode === 'logs'">
      <div class="source-status">
        <span
          class="status-dot"
          :class="{ enabled: source.logEnabled && !source.logError }"
        /><strong>{{
          source.logEnabled ? "日志增量接入" : "日志接入未启用"
        }}</strong
        ><span>最近同步 {{ time(source.lastLogSyncAt) }}</span
        ><span>采集阈值 ≥ {{ source.slowThresholdMs }} ms</span
        ><span class="toolbar-spacer" /><el-button
          text
          size="small"
          @click="showCursors"
          >读取进度</el-button
        ><el-button text size="small" @click="emit('configure')"
          >接入设置</el-button
        ><el-button
          text
          type="primary"
          size="small"
          :disabled="!source.logEnabled"
          @click="sync"
          >立即同步</el-button
        >
      </div>
      <el-alert
        v-if="!source.logEnabled || source.logError"
        :title="
          source.logError ||
          '尚未启用日志接入，空结果不能说明没有慢 SQL。可配置日志接入，或查看已启用的累计 SQL 统计。'
        "
        type="warning"
        :closable="false"
      />
      <div class="pg-toolbar slow-filters">
        <el-date-picker
          v-model="range"
          type="datetimerange"
          size="small"
          start-placeholder="开始时间"
          end-placeholder="结束时间"
          style="width: 320px; flex-grow: 0"
        />
        <div class="presets">
          <button @click="preset(1)">1 h</button
          ><button @click="preset(6)">6 h</button
          ><button @click="preset(24)">24 h</button
          ><button @click="preset(168)">7 d</button>
        </div>
        <el-input
          v-model="keyword"
          size="small"
          clearable
          placeholder="SQL、库、账号、客户端"
          style="width: 180px"
          @keyup.enter="load"
        />
        <el-popover placement="bottom-end" :width="280" trigger="click"
          ><template #reference
            ><el-button size="small"
              >筛选{{ phase || minimum ? " ●" : "" }}</el-button
            ></template
          ><el-form label-position="top" size="small"
            ><el-form-item label="日志阶段"
              ><el-select v-model="phase" placeholder="全部阶段"
                ><el-option label="全部阶段" value="" /><el-option
                  v-for="p in ['statement', 'execute', 'parse', 'bind']"
                  :key="p"
                  :value="p"
                  :label="p" /></el-select></el-form-item
            ><el-form-item label="最小日志耗时（ms）"
              ><el-input-number
                v-model="minimum"
                :min="0"
                :controls="false" /></el-form-item></el-form
        ></el-popover>
        <el-button type="primary" size="small" :loading="loading" @click="load"
          >分析</el-button
        >
      </div>
      <template v-if="report"
        ><div class="slow-summary">
          <div>
            <span>慢日志记录</span><b>{{ report.logCount.toLocaleString() }}</b>
          </div>
          <div>
            <span>SQL 模板</span
            ><b>{{ report.templateCount.toLocaleString() }}</b>
          </div>
          <div>
            <span>记录总耗时</span><b>{{ duration(report.totalMs) }}</b>
          </div>
          <div>
            <span>单条最高耗时</span><b>{{ duration(report.maxMs) }}</b>
          </div>
          <p>
            统计已接入日志中的记录，按数据库与 Parse / Bind / Execute
            阶段分别聚合。<br />首次导入起点
            {{ time(source.logStartAt) }}；缺失或未记录的日志不计入。
          </p>
        </div>
        <div class="trend-panel">
          <div class="trend-heading">
            <strong
              >慢 SQL 趋势
              <small
                >{{ Math.round(report.stepSeconds / 60) }} 分钟 / 点</small
              ></strong
            >
            <div class="metric-switch">
              <button
                :class="{ active: metric === 'logCount' }"
                @click="metric = 'logCount'"
              >
                日志条数</button
              ><button
                :class="{ active: metric === 'maxMs' }"
                @click="metric = 'maxMs'"
              >
                最大耗时
              </button>
            </div>
          </div>
          <div class="trend-chart">
            <div class="trend-axis">
              <span>{{
                metric === "logCount" ? trend.max : duration(trend.max)
              }}</span
              ><span>0</span>
            </div>
            <svg
              viewBox="0 0 1000 125"
              preserveAspectRatio="none"
              role="img"
              :aria-label="
                metric === 'logCount' ? '慢日志条数趋势' : '慢日志最大耗时趋势'
              "
            >
              <path
                d="M 0 16 H 1000 M 0 63 H 1000 M 0 110 H 1000"
                stroke="var(--ui-border)"
                stroke-width="1"
                fill="none"
              />
              <polygon
                :points="`0,110 ${trend.points} 1000,110`"
                fill="var(--ui-tint)"
              />
              <polyline
                :points="trend.points"
                fill="none"
                stroke="var(--ui-primary)"
                stroke-width="2"
                vector-effect="non-scaling-stroke"
              />
              <circle
                v-for="(point, i) in trend.data"
                :key="point.time"
                :cx="(i * 1000) / Math.max(1, trend.data.length - 1)"
                :cy="110 - (point[metric] * 94) / trend.max"
                r="3"
                class="trend-point"
              >
                <title>
                  {{ time(point.time) }} · {{ point.logCount }} 条 · 最大
                  {{ duration(point.maxMs) }}
                </title>
              </circle>
            </svg>
          </div>
          <div class="trend-times">
            <span>{{ time(report.from) }}</span
            ><span>{{ time(report.to) }}</span>
          </div>
        </div>
      </template>
      <div class="template-heading">
        <strong>SQL 模板排行 <small>按日志记录聚合</small></strong
        ><el-select v-model="order" size="small" style="width: 140px"
          ><el-option label="总耗时 ↓" value="total" /><el-option
            label="平均耗时 ↓"
            value="average" /><el-option
            label="最大耗时 ↓"
            value="max" /><el-option label="日志条数 ↓" value="calls"
        /></el-select>
      </div>
      <el-table
        :data="pageRows"
        v-loading="loading"
        size="small"
        highlight-current-row
        @row-click="(row: SlowTemplate) => (selected = row)"
        empty-text="此范围没有已采集的慢 SQL 记录"
        ><el-table-column
          type="index"
          width="45"
          :index="(i: number) => (page - 1) * 25 + i + 1"
        /><el-table-column label="SQL 模板" min-width="320"
          ><template #default="{ row }"
            ><code class="template-sql" :title="row.template">{{
              row.template
            }}</code
            ><span class="template-meta"
              >{{ row.database || "库名未记录" }} ·
              {{ row.phase || "阶段未记录"
              }}<span v-if="!row.normalized"> · 原文分组</span></span
            ></template
          ></el-table-column
        ><el-table-column
          prop="logCount"
          label="日志条数"
          width="100"
          align="right"
        /><el-table-column label="总耗时 / 占比" width="160" align="right"
          ><template #default="{ row }"
            ><span>{{ duration(row.totalMs) }}</span>
            <div class="share">
              <i :style="{ width: `${row.timeShare}%` }" /><span
                >{{ row.timeShare.toFixed(1) }}%</span
              >
            </div></template
          ></el-table-column
        ><el-table-column label="平均耗时" width="110" align="right"
          ><template #default="{ row }">{{
            duration(row.averageMs)
          }}</template></el-table-column
        ><el-table-column label="最大耗时" width="110" align="right"
          ><template #default="{ row }">{{
            duration(row.maxMs)
          }}</template></el-table-column
        ><el-table-column label="最近出现" width="170"
          ><template #default="{ row }">{{
            time(row.lastSeen)
          }}</template></el-table-column
        ><el-table-column width="70"
          ><template #default="{ row }"
            ><el-button
              text
              type="primary"
              size="small"
              @click.stop="selected = row"
              >详情</el-button
            ></template
          ></el-table-column
        ></el-table
      >
      <el-pagination
        v-model:current-page="page"
        :page-size="25"
        :total="report?.templateCount || 0"
        layout="total,prev,pager,next"
      />
    </template>
    <template v-else>
      <div class="pg-toolbar">
        <span class="muted"
          >pg_stat_statements · 当前数据库 · 统计重置以来累计</span
        ><span class="toolbar-spacer" /><el-select
          v-model="order"
          size="small"
          style="width: 145px"
          ><el-option label="总耗时 ↓" value="total" /><el-option
            label="平均耗时 ↓"
            value="average" /><el-option
            label="最大耗时 ↓"
            value="max" /><el-option
            label="调用次数 ↓"
            value="calls" /><el-option label="共享块读取 ↓" value="reads"
        /></el-select>
      </div>
      <el-alert
        v-if="stats?.reason"
        :title="stats.reason"
        :type="stats.available ? 'info' : 'warning'"
        :closable="false"
      />
      <template v-if="stats?.available"
        ><div class="slow-summary">
          <div>
            <span>SQL 模板</span><b>{{ stats.totalTemplates }}</b>
          </div>
          <div>
            <span>累计调用</span><b>{{ stats.totalCalls.toLocaleString() }}</b>
          </div>
          <div>
            <span>累计执行耗时</span><b>{{ duration(stats.totalMs) }}</b>
          </div>
          <p>
            统计重置 {{ time(stats.statsReset) }}<br />显示排序后的 Top
            100；本表不受慢日志时间筛选影响。
          </p>
        </div>
        <el-table
          :data="stats.items"
          size="small"
          v-loading="loading"
          @row-click="(row: StatementStat) => (selectedStat = row)"
          ><el-table-column type="index" width="45" /><el-table-column
            label="SQL 模板"
            min-width="350"
            ><template #default="{ row }"
              ><code class="template-sql" :title="row.sql">{{ row.sql }}</code
              ><span class="template-meta"
                >{{ row.username }} · Query ID {{ row.queryId }}</span
              ></template
            ></el-table-column
          ><el-table-column
            prop="calls"
            label="调用次数"
            width="110"
            align="right"
          /><el-table-column label="总耗时" width="115" align="right"
            ><template #default="{ row }">{{
              duration(row.totalMs)
            }}</template></el-table-column
          ><el-table-column label="平均耗时" width="110" align="right"
            ><template #default="{ row }">{{
              duration(row.averageMs)
            }}</template></el-table-column
          ><el-table-column label="最大耗时" width="110" align="right"
            ><template #default="{ row }">{{
              duration(row.maxMs)
            }}</template></el-table-column
          ><el-table-column
            prop="sharedReads"
            label="共享块读取"
            width="115"
            align="right"
          /><el-table-column width="70"
            ><template #default="{ row }"
              ><el-button
                text
                type="primary"
                size="small"
                @click="selectedStat = row"
                >详情</el-button
              ></template
            ></el-table-column
          ></el-table
        ></template
      >
      <div v-else-if="!loading && !error" class="capability-empty">
        <el-icon><DataAnalysis /></el-icon>
        <h4>累计 SQL 统计暂不可用</h4>
        <p>
          需要目标数据库已配置 pg_stat_statements 及相应读取权限。<br />平台不会自动安装扩展或更改数据库参数；可先使用时间段慢日志分析。
        </p>
        <el-button size="small" @click="mode = 'logs'">查看慢日志</el-button>
      </div>
    </template>
    <el-drawer
      :model-value="!!selected"
      title="SQL 模板分析"
      size="min(780px,90vw)"
      @close="selected = undefined"
      ><template v-if="selected"
        ><div class="drawer-meta">
          <el-tag size="small">{{ selected.database || "库名未记录" }}</el-tag
          ><el-tag size="small" type="info">{{
            selected.phase || "阶段未记录"
          }}</el-tag
          ><span>{{ selected.logCount }} 条慢日志</span>
        </div>
        <pre class="sql-code">{{ selected.template }}</pre>
        <el-descriptions :column="3" border size="small"
          ><el-descriptions-item label="总耗时">{{
            duration(selected.totalMs)
          }}</el-descriptions-item
          ><el-descriptions-item label="平均">{{
            duration(selected.averageMs)
          }}</el-descriptions-item
          ><el-descriptions-item label="最大">{{
            duration(selected.maxMs)
          }}</el-descriptions-item
          ><el-descriptions-item label="首次出现">{{
            time(selected.firstSeen)
          }}</el-descriptions-item
          ><el-descriptions-item label="最近出现" :span="2">{{
            time(selected.lastSeen)
          }}</el-descriptions-item
          ><el-descriptions-item label="账号" :span="3">{{
            selected.users.join(", ") || "未记录"
          }}</el-descriptions-item
          ><el-descriptions-item label="客户端" :span="3">{{
            selected.clients.join(", ") || "未记录"
          }}</el-descriptions-item></el-descriptions
        >
        <h4>最近样本 <small>最多 5 条真实日志，参数值以样本为准</small></h4>
        <article
          v-for="sample in selected.samples"
          :key="sample.id"
          class="sample"
        >
          <header>
            <span
              >{{ time(sample.eventTime) }} · {{ sample.username }} ·
              {{ duration(sample.durationMs) }}</span
            ><el-button
              text
              type="primary"
              size="small"
              @click="openEvent(sample)"
              >原始日志</el-button
            >
          </header>
          <pre class="sql-code">{{ sample.sql }}</pre>
          <el-button
            size="small"
            @click="emit('openSql', sample.sql, sample.dbName)"
            >在 SQL 窗口打开</el-button
          >
        </article></template
      ></el-drawer
    >
    <el-drawer
      :model-value="!!selectedStat"
      title="累计 SQL 详情"
      size="min(720px,90vw)"
      @close="selectedStat = undefined"
      ><template v-if="selectedStat"
        ><p class="muted">
          Query ID {{ selectedStat.queryId }} · {{ selectedStat.username }}
        </p>
        <pre class="sql-code">{{ selectedStat.sql }}</pre>
        <el-descriptions :column="2" border
          ><el-descriptions-item label="调用次数">{{
            selectedStat.calls
          }}</el-descriptions-item
          ><el-descriptions-item label="返回行数">{{
            selectedStat.rows
          }}</el-descriptions-item
          ><el-descriptions-item label="共享块命中">{{
            selectedStat.sharedHits
          }}</el-descriptions-item
          ><el-descriptions-item label="共享块读取">{{
            selectedStat.sharedReads
          }}</el-descriptions-item
          ><el-descriptions-item label="临时块写入">{{
            selectedStat.tempWrites
          }}</el-descriptions-item
          ><el-descriptions-item label="总耗时占比"
            >{{ selectedStat.timeShare.toFixed(2) }}%</el-descriptions-item
          ></el-descriptions
        >
        <p class="pg-footnote">
          这是参数化模板。打开后请填写 $1、$2
          等绑定参数，再查询或查看预估执行计划。
        </p>
        <el-button
          type="primary"
          size="small"
          @click="emit('openSql', selectedStat!.sql, connection.database)"
          >在 SQL 窗口打开</el-button
        ></template
      ></el-drawer
    >
    <el-dialog v-model="cursorOpen" title="日志读取进度" width="850px"
      ><el-table :data="cursors" size="small"
        ><el-table-column
          prop="path"
          label="文件"
          min-width="260"
        /><el-table-column label="已读 / 文件字节" width="180"
          ><template #default="{ row }"
            >{{ row.offset }} / {{ row.lastSize }}</template
          ></el-table-column
        ><el-table-column
          prop="lastError"
          label="读取错误"
          show-overflow-tooltip
        /><el-table-column label="更新于" width="175"
          ><template #default="{ row }">{{
            time(row.updatedAt)
          }}</template></el-table-column
        ></el-table
      >
      <p class="pg-footnote">
        读取位置不代表日志内容完整覆盖查询时间范围；轮转、缺失文件和采集错误会影响结果。
      </p></el-dialog
    >
    <EventDetail v-model="detailOpen" :event="detail" />
  </section>
</template>
<style scoped>
.slow-panel {
  flex: 1;
  min-height: 0;
  overflow: auto;
}
.source-switch,
.metric-switch {
  display: flex;
  background: var(--ui-panel-soft);
  border: 1px solid var(--ui-border);
  border-radius: 4px;
  padding: 2px;
}
.source-switch button,
.metric-switch button,
.presets button {
  border: 0;
  background: none;
  color: var(--ui-muted);
  font-size: 11px;
  cursor: pointer;
  padding: 5px 12px;
  border-radius: 3px;
}
.source-switch button.active,
.metric-switch button.active {
  background: var(--ui-panel);
  color: var(--ui-primary);
  box-shadow: 0 1px 3px #0001;
}
.source-status {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 11px;
  color: var(--ui-muted);
  padding: 8px 18px;
  background: var(--ui-panel-soft);
  border-bottom: 1px solid var(--ui-border);
  flex-wrap: wrap;
}
.source-status strong {
  font-size: 11px;
  font-weight: 500;
  color: var(--ui-text);
}
.status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #9ca9b8;
}
.status-dot.enabled {
  background: #27a77f;
}
.presets {
  display: flex;
  gap: 1px;
}
.presets button {
  padding: 4px 6px;
}
.presets button:hover {
  color: var(--ui-primary);
  background: var(--ui-tint);
}
.slow-summary {
  display: flex;
  align-items: center;
  gap: 36px;
  padding: 14px 20px;
  border-bottom: 1px solid var(--ui-border);
  flex-wrap: wrap;
}
.slow-summary > div {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.slow-summary span {
  font-size: 11px;
  color: var(--ui-muted);
}
.slow-summary b {
  font-size: 22px;
  font-weight: 550;
  letter-spacing: -0.5px;
  font-variant-numeric: tabular-nums;
}
.slow-summary p {
  margin: 0 0 0 auto;
  max-width: 430px;
  font-size: 10px;
  line-height: 1.9;
  color: var(--ui-muted);
}
.trend-panel {
  padding: 12px 20px;
  border-bottom: 1px solid var(--ui-border);
}
.trend-heading,
.template-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.trend-heading strong,
.template-heading strong {
  font-size: 12px;
  font-weight: 550;
}
.trend-heading small,
.template-heading small {
  font-size: 10px;
  font-weight: 400;
  color: var(--ui-muted);
  margin-left: 8px;
}
.trend-chart {
  display: flex;
  height: 112px;
  gap: 16px;
}
.trend-chart svg {
  flex: 1;
  min-width: 0;
}
.trend-axis {
  width: 58px;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  padding: 13px 0 16px;
  color: var(--ui-muted);
  font-size: 10px;
}
.trend-times {
  display: flex;
  justify-content: space-between;
  padding-left: 74px;
  font-size: 10px;
  color: var(--ui-muted);
}
.trend-point {
  fill: transparent;
  stroke: transparent;
}
.trend-point:hover {
  fill: var(--ui-primary);
  stroke: var(--ui-panel);
  stroke-width: 2;
}
.template-heading {
  padding: 12px 18px;
}
.template-sql {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: var(--ui-heading);
  max-width: 100%;
  cursor: pointer;
}
.template-meta {
  font-size: 10px;
  color: var(--ui-muted);
  display: block;
  margin-top: 4px;
}
.share {
  height: 14px;
  position: relative;
  background: var(--ui-panel-soft);
  margin-top: 4px;
  border-radius: 2px;
  overflow: hidden;
  font-size: 9px;
}
.share i {
  height: 100%;
  display: block;
  background: var(--el-color-primary-light-8);
  position: absolute;
  left: 0;
}
.share span {
  position: relative;
  padding-right: 4px;
  color: var(--ui-primary);
}
.el-pagination {
  justify-content: flex-end;
  padding: 14px;
}
.drawer-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  color: var(--ui-muted);
}
.sample {
  border-top: 1px solid var(--ui-border);
  padding: 12px 0;
}
.sample header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 11px;
  color: var(--ui-muted);
}
h4 {
  font-size: 13px;
}
h4 small {
  font-size: 11px;
  color: var(--ui-muted);
  font-weight: 400;
}
.capability-empty {
  text-align: center;
  padding: 64px 20px;
}
.capability-empty > .el-icon {
  font-size: 36px;
  color: var(--ui-muted);
}
.capability-empty h4 {
  margin: 14px 0 8px;
}
.capability-empty p {
  font-size: 12px;
  line-height: 1.9;
  color: var(--ui-muted);
  margin-bottom: 18px;
}
@media (max-width: 1150px) {
  .slow-summary {
    gap: 24px;
  }
  .slow-summary p {
    margin-left: 0;
    max-width: none;
    width: 100%;
  }
}
</style>

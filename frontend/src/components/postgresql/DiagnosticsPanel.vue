<script setup lang="ts">
import {
  computed,
  ref,
  watch,
  onActivated,
  onDeactivated,
  onBeforeUnmount,
} from "vue";
import {
  getSnapshot,
  type Snapshot,
  type Activity,
} from "../../api/database-workbench";
import {
  listDatabaseEvents,
  getDatabaseEvent,
  type DatabaseEvent,
  type OpsDatabase,
} from "../../api/operations";
import {
  formatDate as time,
  formatDuration as duration,
} from "../../utils/database-workbench.mjs";
import LockGraph from "./LockGraph.vue";
import EventDetail from "./EventDetail.vue";
const props = defineProps<{
  connection: OpsDatabase;
  pane: "sessions" | "locks";
}>();
const emit = defineEmits<{ openSql: [string, string] }>();
const snapshot = ref<Snapshot>();
const loading = ref(false);
const error = ref("");
const keyword = ref("");
const state = ref("");
const minSeconds = ref(0);
const refresh = ref(0);
const root = ref<number>();
const selected = ref<number>();
const tab = ref("current");
const events = ref<DatabaseEvent[]>([]);
const eventTotal = ref(0);
const eventPage = ref(1);
const eventType = ref("deadlock");
const range = ref<[Date, Date]>([new Date(Date.now() - 86400000), new Date()]);
const detail = ref<DatabaseEvent>();
const detailOpen = ref(false);
let generation = 0;
let active = true;
const sessions = computed(() => {
  const term = keyword.value.toLowerCase();
  return (snapshot.value?.activities || []).filter(
    (a) =>
      (props.pane === "locks"
        ? a.blockingPids.length > 0 ||
          snapshot.value?.blockingEdges.some((e) => e.blockerPid === a.pid)
        : a.dbName === props.connection.database &&
          a.backendType === "client backend") &&
      (!state.value ||
        (state.value === "long"
          ? a.transactionSeconds >= 300
          : a.state === state.value)) &&
      (!minSeconds.value ||
        (a.state === "active" && a.elapsedSeconds >= minSeconds.value)) &&
      (!term ||
        [a.pid, a.username, a.application, a.client, a.query].some((v) =>
          String(v).toLowerCase().includes(term),
        )),
  );
});
const currentSession = computed(() =>
  snapshot.value?.activities.find((a) => a.pid === selected.value),
);
const selectedLocks = computed(() =>
  (snapshot.value?.locks || []).filter(
    (l) => selected.value === undefined || l.pid === selected.value,
  ),
);
async function load() {
  const token = ++generation;
  loading.value = true;
  error.value = "";
  try {
    const data = await getSnapshot(props.connection.id);
    if (token === generation) {
      snapshot.value = data;
      if (!data.blockingGroups.some((g) => g.rootPid === root.value))
        root.value = data.blockingGroups[0]?.rootPid;
    }
  } catch (e: any) {
    if (token === generation) error.value = e.message || "诊断失败";
  } finally {
    if (token === generation) loading.value = false;
  }
}
async function loadEvents() {
  const token = ++generation;
  const id = props.connection.id;
  loading.value = true;
  error.value = "";
  try {
    const result = await listDatabaseEvents(id, {
      type: eventType.value,
      page: eventPage.value,
      pageSize: 25,
      from: range.value?.[0].toISOString(),
      to: range.value?.[1].toISOString(),
      keyword: keyword.value,
    });
    if (token === generation && id === props.connection.id) {
      events.value = result.list;
      eventTotal.value = result.total;
    }
  } catch (e: any) {
    if (token === generation) error.value = e.message;
  } finally {
    if (token === generation) loading.value = false;
  }
}
async function openEvent(event: DatabaseEvent) {
  detail.value = await getDatabaseEvent(props.connection.id, event.id);
  detailOpen.value = true;
}
function selectSession(a: Activity) {
  selected.value = a.pid;
}
watch(
  () => [props.connection.id, props.connection.revision],
  () => {
    snapshot.value = undefined;
    selected.value = undefined;
    root.value = undefined;
    events.value = [];
    eventTotal.value = 0;
    error.value = "";
    load();
    if (tab.value === "events") loadEvents();
  },
  { immediate: true },
);
watch(tab, () => {
  if (tab.value === "events") loadEvents();
  else load();
});
let lastRefresh = Date.now();
const timer = setInterval(() => {
  if (
    active &&
    tab.value === "current" &&
    refresh.value &&
    !loading.value &&
    Date.now() - lastRefresh >= refresh.value * 1000
  ) {
    lastRefresh = Date.now();
    load();
  }
}, 1000);
onActivated(() => {
  active = true;
});
onDeactivated(() => {
  active = false;
});
onBeforeUnmount(() => {
  generation++;
  clearInterval(timer);
});
</script>
<template>
  <section class="diagnostic-panel">
    <div class="pg-toolbar">
      <div class="pg-title">
        <strong>{{ pane === "locks" ? "锁诊断" : "会话管理" }}</strong
        ><span>{{ time(snapshot?.checkedAt) }} 快照</span>
      </div>
      <el-select v-model="refresh" size="small" style="width: 112px"
        ><el-option label="手动刷新" :value="0" /><el-option
          label="每 10 秒"
          :value="10" /><el-option label="每 30 秒" :value="30" /></el-select
      ><el-button
        size="small"
        :loading="loading"
        @click="tab === 'events' ? loadEvents() : load()"
        ><el-icon><Refresh /></el-icon>刷新</el-button
      >
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <el-alert
      v-for="warning in snapshot?.warnings || []"
      :key="warning"
      :title="warning"
      type="warning"
      :closable="false"
    />
    <div class="session-summary" v-if="snapshot">
      <div>
        <span>当前库会话</span><b>{{ snapshot.sessionSummary.total }}</b>
      </div>
      <div>
        <span>活跃</span
        ><b class="primary">{{ snapshot.sessionSummary.active }}</b>
      </div>
      <div>
        <span>空闲事务</span
        ><b :class="{ warning: snapshot.sessionSummary.idleInTransaction }">{{
          snapshot.sessionSummary.idleInTransaction
        }}</b>
      </div>
      <div>
        <span>锁等待</span
        ><b :class="{ warning: snapshot.sessionSummary.waiting }">{{
          snapshot.sessionSummary.waiting
        }}</b>
      </div>
      <div>
        <span>长事务 ≥ 5 min</span
        ><b>{{ snapshot.sessionSummary.longTransactions }}</b>
      </div>
      <div>
        <span>累计死锁</span><b>{{ snapshot.deadlocks }}</b
        ><small :title="time(snapshot.statsReset)">自统计重置起</small>
      </div>
    </div>
    <div v-if="pane === 'locks'" class="pg-subtabs">
      <button :class="{ active: tab === 'current' }" @click="tab = 'current'">
        当前阻塞链</button
      ><button :class="{ active: tab === 'events' }" @click="tab = 'events'">
        死锁与锁等待日志
      </button>
    </div>
    <template v-if="tab === 'current' || pane === 'sessions'">
      <template v-if="pane === 'locks' && snapshot">
        <div class="graph-heading">
          <div>
            <strong>阻塞关系</strong
            ><span>阻塞者 → 等待者 · 点击节点查看会话</span>
          </div>
          <el-select
            v-if="snapshot.blockingGroups.length"
            v-model="root"
            size="small"
            style="width: 230px"
            ><el-option
              v-for="g in snapshot.blockingGroups"
              :key="g.rootPid"
              :value="g.rootPid"
              :label="`${g.rootPid === 0 ? '预备事务' : `根 PID ${g.rootPid}`} · 影响 ${g.waitingPids.length} 个会话${g.cycle ? ' · 环路' : ''}`"
          /></el-select>
        </div>
        <template v-if="snapshot.blockingEdges.length"
          ><el-alert
            v-for="g in snapshot.blockingGroups.filter(
              (g) =>
                g.rootPid === root &&
                (g.missingSession || g.cycle || g.preparedTransaction),
            )"
            :key="g.rootPid"
            :title="
              g.preparedTransaction
                ? 'PID 0 表示预备事务，请结合下方事务 GID 与业务核对。'
                : g.cycle
                  ? '快照发现阻塞环路，请结合死锁日志确认；快照本身不能证明发生死锁。'
                  : '阻塞者会话信息不可见或在采样期间已结束。'
            "
            type="warning"
            :closable="false" /><LockGraph
            :snapshot="snapshot"
            :root="root"
            :selected="selected"
            @select="selected = $event"
        /></template>
        <div v-else class="graph-empty">
          <el-icon><Connection /></el-icon><strong>本次快照未发现阻塞链</strong
          ><span
            >历史锁事件请查看日志；{{
              snapshot.statsVisible
                ? "已具备全局统计读取权限"
                : "部分会话信息可能因权限不可见"
            }}</span
          >
        </div>
      </template>
      <div class="pg-toolbar filters">
        <el-input
          v-model="keyword"
          size="small"
          clearable
          placeholder="PID、账号、客户端、SQL"
          style="width: 240px"
          ><template #prefix
            ><el-icon><Search /></el-icon></template></el-input
        ><el-select
          v-model="state"
          placeholder="全部状态"
          size="small"
          style="width: 150px"
          ><el-option label="全部状态" value="" /><el-option
            label="活跃 active"
            value="active" /><el-option
            label="空闲 idle"
            value="idle" /><el-option
            label="空闲事务"
            value="idle in transaction" /><el-option
            label="长事务 ≥ 5 min"
            value="long" /></el-select
        ><template v-if="pane === 'sessions'"
          ><el-input-number
            v-model="minSeconds"
            size="small"
            :min="0"
            :controls="false"
            style="width: 90px"
          /><span class="muted">活跃耗时 ≥ 秒</span></template
        ><span class="row-count">{{ sessions.length }} 个会话</span>
      </div>
      <div
        class="session-grid"
        :class="{ 'has-detail': selected !== undefined }"
      >
        <div class="session-table">
          <el-table
            :data="sessions"
            size="small"
            height="100%"
            highlight-current-row
            row-key="pid"
            @row-click="selectSession"
            empty-text="当前筛选下没有会话"
            ><el-table-column
              prop="pid"
              label="PID"
              width="85" /><el-table-column
              prop="state"
              label="状态"
              width="160" /><el-table-column
              label="SQL 耗时"
              width="100"
              sortable
              :sort-method="
                (a: Activity, b: Activity) =>
                  a.elapsedSeconds - b.elapsedSeconds
              "
              ><template #default="{ row }">{{
                row.state === "active"
                  ? duration(row.elapsedSeconds * 1000)
                  : "—"
              }}</template></el-table-column
            ><el-table-column label="事务时长" width="100"
              ><template #default="{ row }">{{
                row.transactionStart
                  ? duration(row.transactionSeconds * 1000)
                  : "—"
              }}</template></el-table-column
            ><el-table-column v-if="pane === 'locks'" label="锁等待" width="100"
              ><template #default="{ row }">{{
                duration(
                  row.lockWaitSeconds == null
                    ? null
                    : row.lockWaitSeconds * 1000,
                )
              }}</template></el-table-column
            ><el-table-column label="等待事件" min-width="145"
              ><template #default="{ row }">{{
                [row.waitEventType, row.waitEvent]
                  .filter(Boolean)
                  .join(" / ") || "—"
              }}</template></el-table-column
            ><el-table-column
              prop="username"
              label="账号"
              width="115" /><el-table-column
              prop="client"
              label="客户端"
              width="140" /><el-table-column
              prop="application"
              label="应用"
              width="150" /><el-table-column label="阻塞 PID" width="120"
              ><template #default="{ row }">{{
                row.blockingPids.join(", ") || "—"
              }}</template></el-table-column
            ><el-table-column
              prop="query"
              label="SQL（非活跃会话为最后一条）"
              min-width="300"
              show-overflow-tooltip
          /></el-table>
        </div>
        <aside v-if="selected !== undefined" class="session-detail">
          <header>
            <strong>{{
              selected === 0 ? "预备事务" : `PID ${selected}`
            }}</strong
            ><el-button text size="small" @click="selected = undefined"
              ><el-icon><Close /></el-icon
            ></el-button>
          </header>
          <template v-if="currentSession"
            ><dl>
              <dt>数据库 / 账号</dt>
              <dd>
                {{ currentSession.dbName }} / {{ currentSession.username }}
              </dd>
              <dt>状态 / 等待</dt>
              <dd>
                {{ currentSession.state }} /
                {{ currentSession.waitEvent || "—" }}
              </dd>
              <dt>应用 / 客户端</dt>
              <dd>
                {{ currentSession.application || "—" }} /
                {{ currentSession.client || "—" }}
              </dd>
              <dt>事务开始</dt>
              <dd>{{ time(currentSession.transactionStart) }}</dd>
              <dt>SQL 开始</dt>
              <dd>{{ time(currentSession.queryStart) }}</dd>
              <dt>连接开始</dt>
              <dd>{{ time(currentSession.backendStart) }}</dd>
              <dt>根阻塞者</dt>
              <dd>{{ currentSession.rootBlockingPids.join(", ") || "—" }}</dd>
            </dl>
            <pre class="sql-code">{{
              currentSession.query || "SQL 文本不可见"
            }}</pre>
            <el-button
              v-if="currentSession.query"
              size="small"
              @click="
                emit('openSql', currentSession.query, currentSession.dbName)
              "
              >在 SQL 窗口打开</el-button
            ></template
          >
          <p v-else class="muted">
            {{
              selected === 0
                ? "预备事务不对应运行中的后端进程，请查看锁记录的 GID。"
                : "会话已结束或信息不可见。"
            }}
          </p>
          <h5>关联锁（{{ selectedLocks.length }}）</h5>
          <div v-for="(lock, i) in selectedLocks" :key="i" class="lock-record">
            <b :class="{ warning: !lock.granted }"
              >{{ lock.granted ? "持有" : "等待" }} · {{ lock.mode }}</b
            ><span>{{ lock.object }} · {{ lock.lockType }}</span
            ><span v-if="(lock as any).preparedGid"
              >GID {{ (lock as any).preparedGid }}</span
            >
          </div>
        </aside>
      </div>
      <div class="pg-footnote">
        SQL 活跃耗时包含等待；锁等待时长仅在 PostgreSQL 提供 waitstart
        时显示。SQL 文本受 track_activity_query_size 限制。
      </div>
    </template>
    <template v-else
      ><div class="pg-toolbar">
        <el-select v-model="eventType" size="small" style="width: 130px"
          ><el-option label="死锁日志" value="deadlock" /><el-option
            label="锁等待日志"
            value="lock_wait" /></el-select
        ><el-date-picker
          v-model="range"
          type="datetimerange"
          size="small"
          start-placeholder="开始时间"
          end-placeholder="结束时间"
          style="width: 340px"
        /><el-input
          v-model="keyword"
          size="small"
          clearable
          placeholder="SQL 或消息关键词"
          style="width: 200px"
        /><el-button
          size="small"
          type="primary"
          @click="
            eventPage = 1;
            loadEvents();
          "
          >查询</el-button
        >
      </div>
      <el-alert
        v-if="!connection.logEnabled || connection.logError"
        :title="
          connection.logError ||
          '日志接入未启用，空列表不能说明没有历史锁事件。请在连接设置中配置日志。'
        "
        type="warning"
        :closable="false"
      /><el-table
        :data="events"
        size="small"
        v-loading="loading"
        @row-dblclick="openEvent"
        ><el-table-column label="时间" width="175"
          ><template #default="{ row }">{{
            time(row.eventTime)
          }}</template></el-table-column
        ><el-table-column
          prop="dbName"
          label="数据库"
          width="120"
        /><el-table-column prop="pid" label="PID" width="85" /><el-table-column
          prop="username"
          label="账号"
          width="120"
        /><el-table-column
          prop="message"
          label="日志消息"
          min-width="300"
          show-overflow-tooltip
        /><el-table-column width="70"
          ><template #default="{ row }"
            ><el-button text size="small" type="primary" @click="openEvent(row)"
              >详情</el-button
            ></template
          ></el-table-column
        ></el-table
      ><el-pagination
        v-model:current-page="eventPage"
        :page-size="25"
        :total="eventTotal"
        layout="total,prev,pager,next"
        @current-change="loadEvents"
      />
      <p class="pg-footnote">
        死锁明细来自原始日志；历史锁等待需要数据库已开启
        log_lock_waits。最近同步 {{ time(connection.lastLogSyncAt) }}
      </p></template
    >
    <EventDetail v-model="detailOpen" :event="detail" />
  </section>
</template>
<style scoped>
.diagnostic-panel {
  display: flex;
  flex-direction: column;
  min-height: 0;
  flex: 1;
  overflow: auto;
}
.session-summary {
  display: flex;
  padding: 16px 20px;
  border-bottom: 1px solid var(--ui-border);
  gap: 28px;
  flex-wrap: wrap;
}
.session-summary > div {
  display: flex;
  align-items: baseline;
  gap: 9px;
}
.session-summary span,
.session-summary small {
  font-size: 11px;
  color: var(--ui-muted);
}
.session-summary b {
  font-size: 22px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.session-summary small {
  font-size: 10px;
}
.primary {
  color: var(--ui-primary);
}
.warning {
  color: #d28c23;
}
.graph-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  gap: 10px;
}
.graph-heading strong {
  font-size: 12px;
}
.graph-heading span {
  font-size: 11px;
  color: var(--ui-muted);
  margin-left: 12px;
}
.graph-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 8px;
  min-height: 150px;
  background: var(--ui-panel-soft);
  border-block: 1px solid var(--ui-border);
}
.graph-empty > .el-icon {
  font-size: 27px;
  color: var(--ui-muted);
}
.graph-empty strong {
  font-size: 13px;
  font-weight: 500;
}
.graph-empty span {
  font-size: 11px;
  color: var(--ui-muted);
}
.session-grid {
  display: flex;
  flex: 1;
  min-height: 240px;
}
.session-table {
  flex: 1;
  min-width: 0;
  min-height: 240px;
}
.session-table :deep(.cell) {
  white-space: nowrap;
}
.session-detail {
  width: 320px;
  flex-shrink: 0;
  border-left: 1px solid var(--ui-border);
  padding: 0 16px 16px;
  overflow: auto;
  max-height: 530px;
}
.session-detail header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  position: sticky;
  top: 0;
  background: var(--ui-panel);
  padding: 8px 0;
  z-index: 1;
}
.session-detail dl {
  font-size: 11px;
  margin: 6px 0;
}
.session-detail dt {
  color: var(--ui-muted);
  margin-top: 10px;
}
.session-detail dd {
  margin: 3px 0;
  overflow-wrap: anywhere;
}
.lock-record {
  font-size: 10px;
  padding: 9px 0;
  border-top: 1px solid var(--ui-border);
}
.lock-record > * {
  display: block;
  overflow-wrap: anywhere;
  margin-bottom: 3px;
}
.lock-record span {
  color: var(--ui-muted);
}
.row-count {
  margin-left: auto;
  color: var(--ui-muted);
  font-size: 11px;
}
.el-pagination {
  justify-content: flex-end;
  padding: 12px;
}
.sql-code {
  max-height: 260px;
}
.session-detail h5 {
  margin: 18px 0 8px;
}
@media (max-width: 1200px) {
  .session-detail {
    width: 270px;
  }
  .session-summary {
    gap: 18px;
  }
}
@media (max-width: 900px) {
  .session-grid.has-detail {
    flex-direction: column;
  }
  .session-detail {
    width: auto;
    border-left: 0;
    border-top: 1px solid var(--ui-border);
  }
}
</style>

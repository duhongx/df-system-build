<script setup lang="ts">
import { ref, computed, watch } from "vue";
import { useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import {
  listComponents,
  getCollection,
  collectComponent,
  collectDatabaseInspection,
  createConfiguredInspection,
  type ComponentConnection,
} from "../api/monitoring";
import { listOpsDatabases, type InspectionResult } from "../api/operations";
import { listManagedServers } from "../api/server-mgmt";
const props = defineProps<{ kind: string }>(),
  router = useRouter();
type Source = {
  id: number;
  name: string;
  endpoint: string;
  revision: number;
  enabled: boolean;
};
type Observation = {
  code: string;
  name: string;
  status: string;
  value: string;
  reason?: string;
  origin?: { id: number; name: string; endpoint: string };
  error?: string;
  gaps?: string[];
  tables?: Array<{
    title: string;
    columns: string[];
    rows: string[][];
    note?: string;
  }>;
};
const sources = ref<Source[]>([]),
  selected = ref(0),
  result = ref<InspectionResult | null>(null),
  at = ref(""),
  loading = ref(false),
  busy = ref(false),
  stale = ref(false),
  error = ref(""),
  raw = ref(false);
const sourceType = computed(() =>
  props.kind === "postgresql"
    ? "database"
    : props.kind === "hosts"
      ? "host"
      : "component",
);
const current = computed(() =>
  sources.value.find((v) => v.id === selected.value),
);
const checks = computed<Observation[]>(() =>
  result.value
    ? [
        ...(result.value.reportChecks || []),
        ...(result.value.checks || []).filter(
          (v) =>
            !(result.value?.reportChecks || []).some((o) => o.code === v.code),
        ),
      ]
    : [],
);
const counts = computed(() => ({
  attention: result.value?.coverage
    ? result.value.coverage.warning + result.value.coverage.critical
    : checks.value.filter((v) => ["WARNING", "CRITICAL"].includes(v.status))
        .length,
  missing: result.value?.coverage
    ? result.value.coverage.planned - result.value.coverage.completed
    : new Set(
        checks.value.filter((v) => v.status === "UNKNOWN").map((v) => v.code),
      ).size,
}));
const labels: Record<string, string> = {
  NORMAL: "正常",
  RECORDED: "已记录",
  WARNING: "需关注",
  CRITICAL: "严重",
  UNKNOWN: "未取得结果",
  NOT_APPLICABLE: "不适用",
};
function statusType(s: string) {
  return s === "NORMAL"
    ? "success"
    : s === "WARNING"
      ? "warning"
      : s === "CRITICAL"
        ? "danger"
        : "info";
}
let epoch = 0;
async function loadSources() {
  const ticket = ++epoch;
  loading.value = true;
  result.value = null;
  error.value = "";
  try {
    let rows: Source[] = [];
    if (props.kind === "postgresql") {
      rows = (await listOpsDatabases()).map((v) => ({
        id: v.id,
        name: v.name,
        endpoint: `${v.host}:${v.port}/${v.database}`,
        revision: v.revision,
        enabled: true,
      }));
    } else if (props.kind === "hosts") {
      rows = (await listManagedServers()).map((v) => ({
        id: v.id,
        name: v.remark || v.host,
        endpoint: v.host,
        revision: 0,
        enabled: true,
      }));
    } else {
      rows = (await listComponents())
        .filter((v: ComponentConnection) => v.kind === props.kind)
        .map((v) => ({
          ...v,
          endpoint:
            v.source === "environment"
              ? "复用环境配置"
              : v.endpoint || "主机 mc 别名",
        }));
    }
    if (ticket !== epoch) return;
    sources.value = rows;
    selected.value = rows[0]?.id || 0;
    if (!rows.length) {
      loading.value = false;
      return;
    }
    await loadResult();
  } catch (e) {
    if (ticket === epoch) error.value = (e as Error).message;
  } finally {
    if (ticket === epoch) loading.value = false;
  }
}
async function loadResult() {
  const ticket = ++epoch;
  result.value = null;
  at.value = "";
  stale.value = false;
  error.value = "";
  if (!selected.value) {
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const sample = await getCollection(sourceType.value, selected.value);
    if (ticket !== epoch) return;
    if (sample) {
      result.value = JSON.parse(sample.resultJSON);
      at.value = sample.collectedAt;
      stale.value =
        !!current.value?.revision && sample.revision !== current.value.revision;
    }
  } catch (e) {
    if (ticket === epoch) error.value = (e as Error).message;
  } finally {
    if (ticket === epoch) loading.value = false;
  }
}
async function collect() {
  if (!selected.value) return;
  const kind = sourceType.value,
    id = selected.value,
    ticket = epoch;
  busy.value = true;
  try {
    if (kind === "host") {
      const run = await createConfiguredInspection(
        `${current.value?.name} 巡检报告`,
        [{ type: kind, id }],
      );
      router.push(`/inspections/${run.id}`);
      return;
    }
    const value =
      kind === "database"
        ? await collectDatabaseInspection(id)
        : await collectComponent(id);
    if (ticket !== epoch) return;
    result.value = value;
    at.value = value.finishedAt;
    stale.value = false;
    error.value = "";
    ElMessage.success("本次检查结果已更新");
  } catch (e) {
    if (ticket === epoch) error.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
async function report() {
  if (!selected.value) return;
  busy.value = true;
  try {
    const run = await createConfiguredInspection("系统巡检报告", [
      { type: sourceType.value, id: selected.value },
    ]);
    router.push(`/inspections/${run.id}`);
  } finally {
    busy.value = false;
  }
}
watch(() => props.kind, loadSources, { immediate: true });
</script>
<template>
  <div class="component-inspection">
    <div class="source-toolbar">
      <el-select
        v-model="selected"
        @change="loadResult"
        filterable
        placeholder="选择已登记实例"
        style="width: 360px"
        ><el-option
          v-for="s in sources"
          :key="s.id"
          :label="`${s.name} · ${s.endpoint}`"
          :value="s.id" /></el-select
      ><el-button :disabled="!selected || busy" @click="loadResult"
        >读取最近结果</el-button
      ><el-button
        :loading="busy"
        :disabled="!selected"
        type="primary"
        @click="collect"
        >{{ kind === "hosts" ? "执行主机巡检" : "立即检查" }}</el-button
      ><el-button :disabled="!selected || busy" @click="report"
        >生成此对象报告</el-button
      ><el-button
        text
        @click="
          router.push(
            kind === 'postgresql'
              ? '/postgresql/workbench'
              : '/settings/environment#inspection-components',
          )
        "
        >维护连接</el-button
      >
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <el-alert
      v-if="stale"
      title="连接已修改，以下为修改前结果；请重新检查。"
      type="warning"
      :closable="false"
    />
    <el-empty
      v-if="!loading && !sources.length"
      description="尚未登记此组件，先在环境配置中维护连接"
    />
    <div v-else v-loading="loading">
      <div v-if="result" class="result-overview">
        <div>
          <strong>{{ current?.name }}</strong>
          <p>
            采集于
            {{ new Date(at).toLocaleString("zh-CN", { hour12: false }) }} ·
            结果保留 48 小时
          </p>
        </div>
        <div class="counts">
          <span
            >应查项目
            <b>{{
              result?.coverage?.planned ??
              new Set(checks.map((v) => v.code)).size
            }}</b></span
          ><span
            >需关注 <b>{{ counts.attention }}</b></span
          ><span
            >结果缺口 <b>{{ counts.missing }}</b></span
          >
        </div>
        <el-button text @click="raw = true">查看原始结果</el-button>
      </div>
      <el-empty
        v-else-if="!loading"
        description="近 48 小时没有检查结果；生成报告时将现场采集，也可立即检查"
      />
      <el-table
        v-if="result"
        :data="checks"
        :row-key="
          (row: Observation) =>
            `${row.code}:${row.origin?.id || 0}:${row.origin?.endpoint || row.reason || ''}`
        "
        class="checks"
        empty-text="未取得任何检查项目"
      >
        <el-table-column type="expand"
          ><template #default="{ row }"
            ><div class="expanded">
              <p v-if="row.reason || row.error">
                {{ row.reason || row.error }}
              </p>
              <p v-for="(gap, i) in row.gaps" :key="i">采集缺口：{{ gap }}</p>
              <template v-for="(t, i) in row.tables" :key="i"
                ><h4>{{ t.title }}</h4>
                <div class="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th v-for="(c, j) in t.columns" :key="j">{{ c }}</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr v-for="(r, j) in t.rows.slice(0, 20)" :key="j">
                        <td v-for="(v, k) in r" :key="k">{{ v }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <small v-if="t.rows.length > 20"
                  >共 {{ t.rows.length }} 行，此处展示前 20
                  行，完整内容见原始结果。</small
                ><small>{{ t.note }}</small></template
              >
            </div></template
          ></el-table-column
        >
        <el-table-column label="采集对象" min-width="150"
          ><template #default="{ row }">{{
            row.origin?.name || row.origin?.endpoint || current?.name
          }}</template></el-table-column
        >
        <el-table-column
          prop="name"
          label="检查内容"
          min-width="190"
        /><el-table-column label="结论" width="120"
          ><template #default="{ row }"
            ><el-tag :type="statusType(row.status)">{{
              labels[row.status] || row.status
            }}</el-tag></template
          ></el-table-column
        ><el-table-column label="关键结果" min-width="350"
          ><template #default="{ row }"
            ><span>{{
              row.status === "UNKNOWN"
                ? row.reason ||
                  row.error ||
                  row.gaps?.[0] ||
                  row.value ||
                  "未取得结果"
                : row.value || row.reason || row.error || "未提供结论"
            }}</span></template
          ></el-table-column
        >
      </el-table>
      <div v-if="kind === 'postgresql'" class="diagnostics">
        <strong>日常排障</strong
        ><el-button
          @click="
            router.push({
              path: '/postgresql/workbench',
              query: { databaseId: selected, pane: 'sessions' },
            })
          "
          >连接与会话</el-button
        ><el-button
          @click="
            router.push({
              path: '/postgresql/workbench',
              query: { databaseId: selected, pane: 'locks' },
            })
          "
          >锁与阻塞</el-button
        ><el-button
          @click="
            router.push({
              path: '/postgresql/workbench',
              query: { databaseId: selected, pane: 'slow' },
            })
          "
          >慢 SQL</el-button
        >
      </div>
    </div>
    <el-dialog v-model="raw" title="本次完整采集结果" width="900px">
      <pre>{{ JSON.stringify(result, null, 2) }}</pre>
    </el-dialog>
  </div>
</template>
<style scoped>
.source-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  margin-bottom: 20px;
}
.result-overview {
  display: flex;
  align-items: center;
  gap: 28px;
  background: #f5f8fc;
  padding: 20px;
  margin: 18px 0;
}
.result-overview p,
small {
  font-size: 12px;
  color: #748297;
}
.counts {
  display: flex;
  gap: 25px;
  margin-left: auto;
}
.counts span {
  color: #738095;
  font-size: 12px;
}
.counts b {
  display: block;
  font-size: 23px;
  color: #243750;
  margin-top: 6px;
}
.expanded {
  padding: 10px 30px 20px;
  line-height: 1.8;
}
.table-wrap {
  max-height: 420px;
  overflow: auto;
}
table {
  border-collapse: collapse;
  width: 100%;
  font-size: 12px;
  table-layout: fixed;
}
th,
td {
  padding: 8px 12px;
  border: 1px solid #e3e8ef;
  text-align: left;
  overflow-wrap: anywhere;
}
th {
  background: #f3f6fa;
}
pre {
  max-height: 65vh;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  background: #f7f9fc;
  padding: 15px;
  font-size: 12px;
}
.diagnostics {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 22px 0;
  margin-top: 20px;
  border-top: 1px solid #e5e9ef;
}
.diagnostics strong {
  font-size: 13px;
  margin-right: 15px;
}
</style>

<script setup lang="ts">
import { computed, ref, watch, onMounted, onBeforeUnmount } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import type { SQLNamespace } from "@codemirror/lang-sql";
import {
  listOpsDatabases,
  importOpsDatabase,
  type OpsDatabase,
} from "../api/operations";
import {
  getSchemas,
  getRelations,
  getRelation,
  queryDatabase,
  exportQuery,
  type SchemaInfo,
  type RelationInfo,
  type RelationDetail,
  type QueryResult,
  type QueryRequest,
  type QueryRecord,
  getRoutines,
  getRoutine,
  type RoutineInfo,
  type RoutineDetail,
  type WorkspaceDraft,
  type WorkspaceState,
  type SavedQuery,
} from "../api/database-workbench";
import {
  relationQuery,
  targetLabel,
  sampleTarget,
  formatDuration as duration,
  formatDate as time,
} from "../utils/database-workbench.mjs";
import SqlEditor from "../components/postgresql/SqlEditor.vue";
import ConnectionDialog from "../components/postgresql/ConnectionDialog.vue";
import DiagnosticsPanel from "../components/postgresql/DiagnosticsPanel.vue";
import SlowSQLPanel from "../components/postgresql/SlowSQLPanel.vue";
import QueryPlan from "../components/postgresql/QueryPlan.vue";
import QueryHistory from "../components/postgresql/QueryHistory.vue";
import DatabaseAccessPanel from "../components/postgresql/DatabaseAccessPanel.vue";
import QueryResultGrid from "../components/postgresql/QueryResultGrid.vue";
import ObjectDataBrowser from "../components/postgresql/ObjectDataBrowser.vue";
import SavedQueries from "../components/postgresql/SavedQueries.vue";
import { useDatabaseWorkspace } from "../composables/useDatabaseWorkspace";
import { quoteIdentifier } from "../utils/database-workbench.mjs";
defineOptions({ name: "PostgreSQLWorkbenchView" });
type Pane = "sql" | "objects" | "access" | "sessions" | "locks" | "slow" | "records";
interface QueryTab {
  id: number;
  name: string;
  databaseId: number;
  revision: number;
  target: string;
  schema: string;
  sql: string;
  parameters: string;
  maxRows: number;
  timeoutSeconds: number;
  running: boolean;
  result?: QueryResult;
  lastRequest?: QueryRequest;
  error: string;
  resultPane: "rows" | "plan" | "message";
  controller?: AbortController;
}
const route = useRoute();
const router = useRouter();
const databases = ref<OpsDatabase[]>([]);
const databaseId = ref<number>();
const databaseSearch = ref("");
const objectSearch = ref("");
const navigationOpen = ref(true);
const connectionsLoading = ref(false);
const connectionsError = ref("");
const schemas = ref<Record<number, SchemaInfo[]>>({});
const relations = ref<Record<string, RelationInfo[]>>({});
const expanded = ref<string[]>([]);
const metadataLoading = ref(false);
const metadataError = ref("");
const catalogGeneration = ref(0);
const object = ref<RelationDetail>();
const objectLoading = ref(false);
const objectError = ref("");
const objectTab = ref("columns");
const editorDialog = ref(false);
const editingConnection = ref<OpsDatabase>();
const pane = ref<Pane>("sql");
const queryTabs = ref<QueryTab[]>([]);
const tabId = ref<number>();
const editors = new Map<number, InstanceType<typeof SqlEditor>>();
function bindEditor(id: number, value: unknown) {
  if (value) editors.set(id, value as InstanceType<typeof SqlEditor>);
  else editors.delete(id);
}
const closedTabs = ref<WorkspaceDraft[]>([]),
  savedQueriesOpen = ref(false),
  favorites = ref<InstanceType<typeof SavedQueries>>();
const routines = ref<Record<string, RoutineInfo[]>>({}),
  expandedRoutines = ref<string[]>([]),
  routine = ref<RoutineDetail>();
const menu = ref<{ x: number; y: number; row: RelationInfo }>();
let objectRequest = 0;
function hideMenu() {
  menu.value = undefined;
}
const columnsCache = ref<Record<string, string[]>>({});
const queryArea = ref<HTMLElement>();
const editorHeight = ref(245);
const parametersOpen = ref(false);
let sequence = 0;
const panes: Array<{ id: Pane; label: string; icon: string }> = [
  { id: "sql", label: "SQL 窗口", icon: "EditPen" },
  { id: "objects", label: "对象管理", icon: "Grid" },
  { id: "access", label: "用户与权限", icon: "Key" },
  { id: "sessions", label: "会话", icon: "User" },
  { id: "locks", label: "锁诊断", icon: "Connection" },
  { id: "slow", label: "慢 SQL", icon: "DataAnalysis" },
  { id: "records", label: "执行记录", icon: "Clock" },
];
const connection = computed(() =>
  databases.value.find((d) => d.id === databaseId.value),
);
const currentTab = computed(() =>
  queryTabs.value.find((t) => t.id === tabId.value),
);
const tabConnection = computed(() =>
  databases.value.find((d) => d.id === currentTab.value?.databaseId),
);
const staleTarget = computed(
  () =>
    !!currentTab.value &&
    (!tabConnection.value ||
      tabConnection.value.revision !== currentTab.value.revision),
);
const visibleConnections = computed(() =>
  databases.value.filter((d) =>
    [d.name, d.host, d.database].some((v) =>
      v.toLowerCase().includes(databaseSearch.value.toLowerCase()),
    ),
  ),
);
const key = (id: number, schema: string) => `${id}:${schema}`;
const currentSchemas = computed(
  () => schemas.value[databaseId.value || 0] || [],
);
const allRelations = computed(() =>
  currentSchemas.value.flatMap(
    (s) => relations.value[key(databaseId.value!, s.name)] || [],
  ),
);
const visibleRelations = (schema: string) =>
  (relations.value[key(databaseId.value!, schema)] || []).filter((r) =>
    [r.name, r.comment].some((v) =>
      v.toLowerCase().includes(objectSearch.value.toLowerCase()),
    ),
  );
const objectRows = computed(() =>
  allRelations.value.filter((r) =>
    [r.name, r.schema, r.comment].some((v) =>
      v.toLowerCase().includes(objectSearch.value.toLowerCase()),
    ),
  ),
);
const completions = computed<Record<number, SQLNamespace>>(() => {
  const out: Record<number, SQLNamespace> = {};
  for (const [id, items] of Object.entries(schemas.value)) {
    const ns: Record<string, Record<string, string[]>> = {};
    for (const schema of items) {
      ns[schema.name] = {};
      for (const relation of relations.value[key(Number(id), schema.name)] ||
        [])
        ns[schema.name]![relation.name] =
          columnsCache.value[`${id}:${relation.oid}`] || [];
    }
    out[Number(id)] = ns;
  }
  return out;
});
function draftOf(tab: QueryTab): WorkspaceDraft {
  return {
    id: tab.id,
    name: tab.name,
    databaseId: tab.databaseId,
    revision: tab.revision,
    target: tab.target,
    schema: tab.schema,
    sql: tab.sql,
    parameters: tab.parameters,
    maxRows: tab.maxRows,
    timeoutSeconds: tab.timeoutSeconds,
  };
}
function snapshot(): WorkspaceState {
  return {
    version: 1,
    tabs: queryTabs.value.map(draftOf),
    closedTabs: closedTabs.value,
    activeTabId: tabId.value || 0,
    databaseId: databaseId.value || 0,
    pane: pane.value,
    editorHeight: editorHeight.value,
    navigationOpen: navigationOpen.value,
  };
}
function restoredTab(d: WorkspaceDraft): QueryTab {
  return {
    id: d.id,
    name: d.name || `查询 ${d.id}`,
    databaseId: d.databaseId,
    revision: d.revision,
    target: d.target,
    schema: d.schema || "",
    sql: d.sql || "",
    parameters: d.parameters || "[]",
    maxRows: d.maxRows > 0 && d.maxRows <= 10000 ? d.maxRows : 1000,
    timeoutSeconds:
      d.timeoutSeconds > 0 && d.timeoutSeconds <= 30 ? d.timeoutSeconds : 15,
    running: false,
    error: "",
    resultPane: "rows",
  };
}
function applyWorkspace(state: WorkspaceState) {
  queryTabs.value = (state.tabs || [])
    .filter((t) => Number.isSafeInteger(t.id) && t.id > 0)
    .map(restoredTab);
  closedTabs.value = (state.closedTabs || []).filter(
    (t) => Number.isSafeInteger(t.id) && t.id > 0,
  );
  sequence = Math.max(
    0,
    ...queryTabs.value.map((t) => t.id),
    ...closedTabs.value.map((t) => t.id),
  );
  tabId.value = queryTabs.value.some((t) => t.id === state.activeTabId)
    ? state.activeTabId
    : queryTabs.value[0]?.id;
  databaseId.value =
    state.databaseId ||
    queryTabs.value[0]?.databaseId ||
    databases.value[0]?.id;
  if (panes.some((p) => p.id === state.pane)) pane.value = state.pane as Pane;
  editorHeight.value = Math.max(160, Math.min(600, state.editorHeight || 245));
  navigationOpen.value = state.navigationOpen !== false;
}
const workspace = useDatabaseWorkspace(snapshot, applyWorkspace);
async function reloadWorkspace() {
  if (workspace.dirty.value) {
    try {
      await ElMessageBox.confirm(
        "载入服务器版本将替换本页工作区，请先收藏需要保留的 SQL。",
        "载入工作区",
        { confirmButtonText: "载入", cancelButtonText: "保留本页" },
      );
    } catch {
      return;
    }
  }
  try {
    await workspace.initialize();
  } catch {}
}
async function overwriteWorkspace() {
  try {
    await workspace.overwrite();
  } catch (e: any) {
    ElMessage.error(e.message);
  }
}
async function renameTab(tab: QueryTab) {
  try {
    const value = await ElMessageBox.prompt("输入窗口名称", "重命名 SQL 窗口", {
      inputValue: tab.name,
      inputValidator: (v) =>
        (!!v?.trim() && v.trim().length <= 128) || "请输入 1～128 个字符",
    });
    tab.name = value.value.trim();
  } catch {}
}
function restoreClosed(draft: WorkspaceDraft) {
  const tab = restoredTab({ ...draft, id: ++sequence });
  queryTabs.value.push(tab);
  closedTabs.value = closedTabs.value.filter((t) => t.id !== draft.id);
  selectQuery(tab);
  setPane("sql");
  savedQueriesOpen.value = false;
}
async function discardClosed(draft: WorkspaceDraft) {
  try {
    await ElMessageBox.confirm(`删除草稿“${draft.name}”？`, "删除窗口草稿", {
      confirmButtonText: "删除",
      type: "warning",
    });
  } catch {
    return;
  }
  closedTabs.value = closedTabs.value.filter((t) => t.id !== draft.id);
}
function openFavorite(row: SavedQuery) {
  const db = row.databaseId
    ? databases.value.find((d) => d.id === row.databaseId)
    : connection.value;
  if (!db) {
    ElMessage.warning("请选择可用的连接后再打开此 SQL");
    return;
  }
  newQuery(db, row.sql, row.schema, row.parametersJSON);
  if (currentTab.value) currentTab.value.name = row.name;
  savedQueriesOpen.value = false;
}
async function copyText(text: string) {
  try {
    if (navigator.clipboard) await navigator.clipboard.writeText(text);
    else {
      const el = document.createElement("textarea");
      el.value = text;
      document.body.append(el);
      el.select();
      document.execCommand("copy");
      el.remove();
    }
    ElMessage.success("已复制");
  } catch {
    ElMessage.error("复制失败");
  }
}
async function toggleRoutines(schema: string) {
  if (expandedRoutines.value.includes(schema)) {
    expandedRoutines.value = expandedRoutines.value.filter((s) => s !== schema);
    return;
  }
  expandedRoutines.value.push(schema);
  const id = databaseId.value!;
  if (routines.value[key(id, schema)]) return;
  metadataLoading.value = true;
  try {
    routines.value[key(id, schema)] = await getRoutines(id, schema);
  } catch (e: any) {
    metadataError.value = e.message;
  } finally {
    metadataLoading.value = false;
  }
}
async function openRoutine(row: RoutineInfo) {
  const id = databaseId.value!,
    token = ++objectRequest;
  object.value = undefined;
  routine.value = undefined;
  objectError.value = "";
  objectLoading.value = true;
  setPane("objects");
  try {
    const result = await getRoutine(id, row.oid);
    if (token === objectRequest && id === databaseId.value)
      routine.value = result;
  } catch (e: any) {
    if (token === objectRequest) objectError.value = e.message;
  } finally {
    if (token === objectRequest) objectLoading.value = false;
  }
}
function objectMenu(event: MouseEvent, row: RelationInfo) {
  menu.value = {
    x: Math.min(event.clientX, window.innerWidth - 210),
    y: Math.min(event.clientY, window.innerHeight - 190),
    row,
  };
}
async function objectAction(action: string) {
  const row = menu.value?.row;
  hideMenu();
  if (!row || !connection.value) return;
  if (action === "data") await openRelation(row, "data");
  else if (action === "details") await openRelation(row);
  else if (action === "query")
    newQuery(connection.value, relationQuery(row), row.schema);
  else if (action === "name")
    await copyText(
      `${quoteIdentifier(row.schema)}.${quoteIdentifier(row.name)}`,
    );
  else if (action === "sql") await copyText(relationQuery(row));
}
const relationKind = (kind: string) =>
  (
    ({
      r: "表",
      p: "分区表",
      v: "视图",
      m: "物化视图",
      f: "外部表",
      S: "序列",
    }) as Record<string, string>
  )[kind] || kind;
const rowCount = computed(() => currentTab.value?.result?.rowCount || 0);
async function loadConnections() {
  connectionsLoading.value = true;
  connectionsError.value = "";
  try {
    databases.value = await listOpsDatabases();
  } catch (e: any) {
    connectionsError.value = e.message;
  } finally {
    connectionsLoading.value = false;
  }
}
async function loadCatalog(id: number) {
  const token = ++catalogGeneration.value;
  const revision = databases.value.find((d) => d.id === id)?.revision;
  metadataLoading.value = true;
  metadataError.value = "";
  try {
    const values = await getSchemas(id);
    if (
      token !== catalogGeneration.value ||
      databases.value.find((d) => d.id === id)?.revision !== revision
    )
      return;
    schemas.value[id] = values;
    if (token === catalogGeneration.value) {
      const initial = values.find((s) => s.name === "public") || values[0];
      expanded.value = initial ? [initial.name] : [];
      if (initial) await loadSchema(id, initial.name);
    }
  } catch (e: any) {
    if (token === catalogGeneration.value) metadataError.value = e.message;
  } finally {
    if (token === catalogGeneration.value) metadataLoading.value = false;
  }
}
async function loadSchema(id: number, schema: string) {
  const revision = databases.value.find((d) => d.id === id)?.revision;
  const values = await getRelations(id, schema);
  if (databases.value.find((d) => d.id === id)?.revision === revision)
    relations.value[key(id, schema)] = values;
}
async function toggleSchema(schema: string) {
  if (expanded.value.includes(schema)) {
    expanded.value = expanded.value.filter((s) => s !== schema);
    return;
  }
  expanded.value.push(schema);
  if (!relations.value[key(databaseId.value!, schema)]) {
    metadataLoading.value = true;
    try {
      await loadSchema(databaseId.value!, schema);
    } catch (e: any) {
      metadataError.value = e.message;
    } finally {
      metadataLoading.value = false;
    }
  }
}
function setPane(value: Pane) {
  pane.value = value;
  router.replace({
    path: route.path,
    query: { ...route.query, pane: value, databaseId: databaseId.value },
  });
}
function newQuery(
  db = connection.value,
  sql = "-- Ctrl / ⌘ + Enter 执行选中 SQL，未选中时执行全文\nSELECT current_database(), current_user, now();",
  schema?: string,
  parameters = "[]",
) {
  if (!db) return;
  const tab: QueryTab = {
    id: ++sequence,
    name: `查询 ${sequence}`,
    databaseId: db.id,
    revision: db.revision,
    target: targetLabel(db),
    schema: schema ?? "",
    sql,
    parameters,
    maxRows: 1000,
    timeoutSeconds: 15,
    running: false,
    error: "",
    resultPane: "rows",
  };
  queryTabs.value.push(tab);
  tabId.value = tab.id;
  databaseId.value = db.id;
  setPane("sql");
}
function selectConnection(db: OpsDatabase) {
  databaseId.value = db.id;
  if (pane.value === "sql") {
    const existing = queryTabs.value.find(
      (t) => t.databaseId === db.id && t.revision === db.revision,
    );
    if (existing) tabId.value = existing.id;
    else newQuery(db);
  }
  router.replace({
    path: route.path,
    query: { ...route.query, databaseId: db.id },
  });
}
function selectQuery(tab: QueryTab) {
  tabId.value = tab.id;
  databaseId.value = tab.databaseId;
}
async function closeQuery(tab: QueryTab) {
  if (tab.running) {
    ElMessage.warning("请先停止查询");
    return;
  }
  if (tab.sql.trim()) {
    try {
      await ElMessageBox.confirm(
        "关闭后保留 SQL 草稿，可从“我的 SQL → 关闭的窗口”恢复。执行中的结果集不保留。",
        "关闭窗口",
        { confirmButtonText: "关闭并保留草稿", cancelButtonText: "取消" },
      );
    } catch {
      return;
    }
  }
  const index = queryTabs.value.indexOf(tab);
  closedTabs.value.push(draftOf(tab));
  queryTabs.value.splice(index, 1);
  if (tab.id === tabId.value) {
    const next = queryTabs.value[Math.min(index, queryTabs.value.length - 1)];
    tabId.value = next?.id;
    if (next) databaseId.value = next.databaseId;
  }
}
async function openRelation(row: RelationInfo, initialTab = "columns") {
  const token = ++objectRequest;
  routine.value = undefined;
  hideMenu();
  const id = databaseId.value!;
  objectLoading.value = true;
  objectError.value = "";
  object.value = undefined;
  setPane("objects");
  try {
    const value = await getRelation(id, row.oid);
    if (id === databaseId.value && token === objectRequest) {
      object.value = value;
      columnsCache.value[`${id}:${row.oid}`] = value.columns.map((c) => c.name);
      objectTab.value = row.kind === "S" ? "sequence" : initialTab;
    }
  } catch (e: any) {
    if (id === databaseId.value && token === objectRequest)
      objectError.value = e.message;
  } finally {
    if (token === objectRequest) objectLoading.value = false;
  }
}
function openSQL(sql: string, database: string) {
  if (!connection.value) return;
  const target = sampleTarget(connection.value, database, databases.value);
  if (!target) {
    ElMessage.warning(
      `请先登记 ${connection.value.host}:${connection.value.port} / ${database || "日志未记录数据库名"} 的连接，再打开此 SQL，避免目标不一致`,
    );
    return;
  }
  newQuery(target, sql);
}
function openRecord(record: QueryRecord) {
  const db = databases.value.find((d) => d.id === record.databaseId);
  if (!db || db.revision !== record.revision) {
    ElMessage.warning(
      "连接已删除或配置发生变化。请展开记录核对原执行目标，再复制 SQL 到正确连接。",
    );
    return;
  }
  newQuery(db, record.sql, record.schema, record.parametersJSON || "[]");
}
function requestFor(
  tab: QueryTab,
  mode: QueryRequest["mode"],
  sql: string,
): QueryRequest {
  const params = JSON.parse(tab.parameters || "[]");
  if (!Array.isArray(params))
    throw new Error('绑定参数须为 JSON 数组，如 [123,"名称"]');
  return {
    sql,
    schema: tab.schema,
    parameters: params,
    mode,
    maxRows: tab.maxRows,
    timeoutSeconds: tab.timeoutSeconds,
    revision: tab.revision,
  };
}
async function run(mode: "query" | "explain", sql?: string) {
  const tab = currentTab.value;
  if (!tab || tab.running || staleTarget.value) return;
  tab.error = "";
  let req: QueryRequest;
  try {
    req = requestFor(
      tab,
      mode,
      sql ?? editors.get(tab.id)?.selected() ?? tab.sql,
    );
    if (!req.sql.trim()) throw new Error("请先输入 SQL");
  } catch (e: any) {
    tab.error = e.message;
    return;
  }
  tab.running = true;
  tab.result = undefined;
  tab.lastRequest = req;
  tab.controller = new AbortController();
  try {
    tab.result = await queryDatabase(
      tab.databaseId,
      req,
      tab.controller.signal,
    );
    tab.resultPane =
      tab.result.status === "FAILED"
        ? "message"
        : mode === "explain"
          ? "plan"
          : "rows";
  } catch (e: any) {
    tab.error =
      e.code === "ERR_CANCELED"
        ? "查询已停止，执行状态可在记录中核对"
        : e.message;
  } finally {
    tab.running = false;
    tab.controller = undefined;
  }
}
async function download() {
  const tab = currentTab.value;
  if (!tab || tab.running || staleTarget.value) return;
  tab.error = "";
  tab.running = true;
  tab.controller = new AbortController();
  try {
    const req = tab.lastRequest
      ? { ...tab.lastRequest, mode: "export" as const }
      : requestFor(tab, "export", editors.get(tab.id)?.selected() || tab.sql);
    await exportQuery(tab.databaseId, req, tab.controller.signal);
    ElMessage.success("完整查询结果已导出，CSV 中 \\N 表示 NULL");
  } catch (e: any) {
    tab.error = e.name === "AbortError" ? "导出已停止" : e.message;
  } finally {
    tab.running = false;
    tab.controller = undefined;
  }
}
function manage(db?: OpsDatabase) {
  editingConnection.value = db;
  editorDialog.value = true;
}
async function saved(row: OpsDatabase) {
 object.value=undefined;routine.value=undefined;objectRequest++;
 for(const cache of [routines.value,columnsCache.value])Object.keys(cache).filter(k=>k.startsWith(`${row.id}:`)).forEach(k=>delete cache[k]);
  await loadConnections();
  delete schemas.value[row.id];
  Object.keys(relations.value)
    .filter((k) => k.startsWith(`${row.id}:`))
    .forEach((k) => delete relations.value[k]);
  databaseId.value = row.id;
  await loadCatalog(row.id);
}
async function deleted(id: number) {
  await loadConnections();
  delete schemas.value[id];
  if (databaseId.value === id) databaseId.value = databases.value[0]?.id;
  object.value = undefined;
}
async function importConnection() {
  const row = await importOpsDatabase();
  await loadConnections();
  selectConnection(row);
  ElMessage.success("已导入环境配置中的 PostgreSQL 连接，日志接入默认关闭");
}
let stopResize: (() => void) | undefined;
function resize(event: PointerEvent) {
  event.preventDefault();
  const start = event.clientY;
  const height = editorHeight.value;
  const move = (e: PointerEvent) => {
    const max = Math.max(180, (queryArea.value?.clientHeight || 600) - 190);
    editorHeight.value = Math.max(
      160,
      Math.min(max, height + e.clientY - start),
    );
  };
  const stop = () => {
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", stop);
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
    stopResize = undefined;
  };
  stopResize?.();
  document.body.style.cursor = "row-resize";
  document.body.style.userSelect = "none";
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", stop);
  stopResize = stop;
}
watch(databaseId, (id) => {
  metadataError.value = "";
  object.value = undefined;
  routine.value = undefined;
  hideMenu();
  objectRequest++;
  objectError.value = "";
  expanded.value = [];
  if (id) {
    loadCatalog(id);
  } else catalogGeneration.value++;
});
watch(
  () => route.query.pane,
  (value) => {
    if (panes.some((p) => p.id === value)) pane.value = value as Pane;
  },
  { immediate: true },
);
watch(
  () => route.query.databaseId,
  (value) => {
    const id = Number(value);
    if (databases.value.some((d) => d.id === id) && id !== databaseId.value)
      databaseId.value = id;
  },
);
onMounted(async () => {
  window.addEventListener("click", hideMenu);
  window.addEventListener("blur", hideMenu);
  await loadConnections();
  try {
    await workspace.initialize();
  } catch {}
  const requested = Number(route.query.databaseId);
  if (panes.some((p) => p.id === route.query.pane))
    pane.value = route.query.pane as Pane;
  if (databases.value.some((d) => d.id === requested)) {
    databaseId.value = requested;
    if (pane.value === "sql") {
      const tab = queryTabs.value.find((t) => t.databaseId === requested);
      if (tab) tabId.value = tab.id;
      else newQuery(connection.value);
    }
  } else if (!databaseId.value) databaseId.value = databases.value[0]?.id;
  if (!queryTabs.value.length && connection.value && pane.value === "sql")
    newQuery(connection.value);
});
onBeforeUnmount(() => {
  window.removeEventListener("click", hideMenu);
  window.removeEventListener("blur", hideMenu);
  stopResize?.();
  queryTabs.value.forEach((t) => t.controller?.abort());
});
</script>
<template>
  <div class="pg-workbench" v-loading="workspace.initializing.value">
    <aside v-if="navigationOpen" class="object-navigator">
      <div class="navigator-heading">
        <strong>数据库连接</strong
        ><el-tooltip content="导入环境配置中的 PostgreSQL 连接"
          ><el-button
            text
            size="small"
            aria-label="导入连接"
            @click="importConnection"
            ><el-icon><Download /></el-icon></el-button></el-tooltip
        ><el-button text size="small" aria-label="新建连接" @click="manage()"
          ><el-icon><Plus /></el-icon></el-button
        ><el-button
          text
          size="small"
          aria-label="收起导航"
          @click="navigationOpen = false"
          ><el-icon><Fold /></el-icon
        ></el-button>
      </div>
      <div class="navigator-search">
        <el-input
          v-model="databaseSearch"
          size="small"
          clearable
          placeholder="搜索名称、地址、数据库"
          ><template #prefix
            ><el-icon><Search /></el-icon></template
        ></el-input>
      </div>
      <div class="connection-list" v-loading="connectionsLoading">
        <button
          v-for="db in visibleConnections"
          :key="db.id"
          class="connection-item"
          :class="{ selected: databaseId === db.id }"
          :title="targetLabel(db)"
          @click="selectConnection(db)"
        >
          <span class="connection-symbol"
            ><el-icon><Coin /></el-icon></span
          ><span class="connection-text"
            ><b>{{ db.name }}</b
            ><small
              >{{ db.host }}:{{ db.port }} / {{ db.database }}</small
            ></span
          ><span
            class="connection-settings"
            role="button"
            tabindex="0"
            title="管理连接"
            @click.stop="manage(db)"
            @keydown.enter.stop="manage(db)"
            ><el-icon><Setting /></el-icon
          ></span>
        </button>
        <div
          v-if="!visibleConnections.length && !connectionsLoading"
          class="navigator-empty"
        >
          {{ databaseSearch ? "没有匹配的连接" : "尚未登记连接"
          }}<button v-if="!databaseSearch" @click="manage()">新建连接</button>
        </div>
      </div>
      <div v-if="connectionsError" class="navigator-error">
        {{ connectionsError
        }}<el-button text size="small" @click="loadConnections">重试</el-button>
      </div>
      <template v-if="connection"
        ><div class="navigator-heading objects-heading">
          <strong>对象资源</strong><span>{{ connection.database }}</span
          ><el-button
            text
            size="small"
            :loading="metadataLoading"
            aria-label="刷新对象"
            @click="loadCatalog(connection.id)"
            ><el-icon><Refresh /></el-icon
          ></el-button>
        </div>
        <div class="navigator-search">
          <el-input
            v-model="objectSearch"
            size="small"
            clearable
            placeholder="筛选已加载对象"
            ><template #prefix
              ><el-icon><Search /></el-icon></template
          ></el-input>
        </div>
        <div class="schema-tree" v-loading="metadataLoading">
          <div v-if="metadataError" class="navigator-error">
            {{ metadataError
            }}<el-button text size="small" @click="loadCatalog(connection.id)"
              >重试</el-button
            >
          </div>
          <div
            v-for="schema in currentSchemas"
            :key="schema.name"
            class="schema"
          >
            <button class="schema-row" @click="toggleSchema(schema.name)">
              <el-icon
                ><ArrowDown v-if="expanded.includes(schema.name)" /><ArrowRight
                  v-else /></el-icon
              ><el-icon class="schema-symbol"><FolderOpened /></el-icon
              ><span>{{ schema.name }}</span
              ><small>{{ schema.objects }}</small>
            </button>
            <div v-if="expanded.includes(schema.name)">
              <button
                v-for="relation in visibleRelations(schema.name)"
                :key="relation.oid"
                class="relation-row"
                :class="{ selected: object?.relation.oid === relation.oid }"
                :title="`${relationKind(relation.kind)} · ${relation.comment || relation.name}`"
                @click="openRelation(relation)"
                @dblclick="
                  openRelation(
                    relation,
                    relation.kind === 'S' ? 'sequence' : 'data',
                  )
                "
                @contextmenu.prevent.stop="objectMenu($event, relation)"
              >
                <el-icon
                  ><View v-if="['v', 'm'].includes(relation.kind)" /><Clock
                    v-else-if="relation.kind === 'S'" /><Grid v-else /></el-icon
                ><span>{{ relation.name }}</span
                ><small>{{ relationKind(relation.kind) }}</small>
              </button>
              <button
                class="schema-row routine-group"
                @click="toggleRoutines(schema.name)"
              >
                <el-icon
                  ><ArrowDown
                    v-if="expandedRoutines.includes(schema.name)" /><ArrowRight
                    v-else /></el-icon
                ><el-icon><Connection /></el-icon><span>函数与过程</span
                ><small>{{
                  routines[key(connection.id, schema.name)]?.length ?? "…"
                }}</small></button
              ><template v-if="expandedRoutines.includes(schema.name)"
                ><button
                  v-for="item in (
                    routines[key(connection.id, schema.name)] || []
                  ).filter((r) =>
                    [r.name, r.arguments].some((t) =>
                      t.toLowerCase().includes(objectSearch.toLowerCase()),
                    ),
                  )"
                  :key="item.oid"
                  class="relation-row routine-row"
                  :class="{ selected: routine?.routine.oid === item.oid }"
                  :title="`${item.name}(${item.arguments})`"
                  @click="openRoutine(item)"
                >
                  <el-icon><Connection /></el-icon
                  ><span>{{ item.name }}({{ item.arguments }})</span
                  ><small>{{ item.kind === "p" ? "过程" : "函数" }}</small>
                </button></template
              >
              <p
                v-if="relations[key(connection.id, schema.name)]?.length === 0"
                class="tree-empty"
              >
                没有可见的表、视图或序列
              </p>
            </div>
          </div>
        </div>
        <div class="navigator-bottom">
          <span>账号 {{ connection.username }}</span
          ><el-button text size="small" @click="manage(connection)"
            >连接设置</el-button
          >
        </div></template
      >
    </aside>
    <main class="workbench-main">
      <div class="workspace-topbar">
        <el-button
          v-if="!navigationOpen"
          text
          size="small"
          aria-label="展开导航"
          @click="navigationOpen = true"
          ><el-icon><Expand /></el-icon
        ></el-button>
        <div class="context-path">
          <el-icon><Coin /></el-icon
          ><strong>{{ connection?.name || "数据库工作台" }}</strong
          ><span v-if="connection">/</span
          ><span>{{ connection?.database }}</span
          ><el-tag v-if="connection" size="small" type="info">{{
            connection.username
          }}</el-tag>
        </div>
        <span class="toolbar-spacer" /><span
          class="draft-save-state"
          :class="{ 'error-text': workspace.error.value }"
          :title="workspace.error.value || time(workspace.updatedAt.value)"
          >{{
            workspace.error.value
              ? "草稿保存异常"
              : workspace.saving.value
                ? "保存中…"
                : workspace.dirty.value
                  ? "有未保存修改"
                  : workspace.loaded.value
                    ? "草稿已保存"
                    : "草稿未连接"
          }}</span
        ><el-button size="small" text @click="savedQueriesOpen = true"
          >我的 SQL
          <small v-if="closedTabs.length">{{
            closedTabs.length
          }}</small></el-button
        ><el-button
          v-if="connection"
          size="small"
          type="primary"
          plain
          @click="newQuery()"
          ><el-icon><Plus /></el-icon>新建 SQL 窗口</el-button
        >
      </div>
      <div class="workspace-nav">
        <button
          v-for="item in panes"
          :key="item.id"
          :class="{ active: pane === item.id }"
          @click="setPane(item.id)"
        >
          <el-icon><component :is="item.icon" /></el-icon>{{ item.label
          }}<span
            v-if="item.id === 'sql' && queryTabs.length"
            class="tab-count"
            >{{ queryTabs.length }}</span
          >
        </button>
      </div>
      <div v-if="workspace.error.value" class="workspace-save-error">
        <span>{{ workspace.error.value }}</span
        ><template v-if="workspace.conflict.value"
          ><el-button text size="small" @click="reloadWorkspace"
            >载入服务器版本</el-button
          ><el-button
            text
            type="primary"
            size="small"
            @click="overwriteWorkspace"
            >用本页草稿覆盖</el-button
          ></template
        ><el-button
          v-else
          text
          size="small"
          @click="
            workspace.loaded.value ? workspace.flush() : reloadWorkspace()
          "
          >重试{{ workspace.loaded.value ? "保存" : "读取" }}</el-button
        >
      </div>
      <div v-if="!connection && pane !== 'sql'" class="workspace-empty">
        <el-icon><Coin /></el-icon>
        <h3>接入数据库，开始管理</h3>
        <p>登记 PostgreSQL 连接后，即可浏览对象、查询数据并诊断运行情况。</p>
        <el-button type="primary" @click="manage()">新建连接</el-button>
      </div>
      <div class="sql-workspace" v-show="pane === 'sql'">
        <div class="query-tabs" role="tablist" aria-label="SQL 查询窗口">
          <div
            v-for="tab in queryTabs"
            :key="tab.id"
            class="query-tab"
            role="tab"
            :aria-selected="tab.id === tabId"
            :tabindex="tab.id === tabId ? 0 : -1"
            :class="{ active: tab.id === tabId }"
            @click="selectQuery(tab)"
            @dblclick="renameTab(tab)"
            @keydown.enter="selectQuery(tab)"
          >
            <span
              class="query-tab-dot"
              :class="{ running: tab.running }"
            /><span>{{ tab.name }}</span
            ><small>{{
              databases.find((d) => d.id === tab.databaseId)?.database ||
              "已移除连接"
            }}</small
            ><button aria-label="关闭 SQL 窗口" @click.stop="closeQuery(tab)">
              <el-icon><Close /></el-icon>
            </button>
          </div>
          <el-button
            text
            size="small"
            :disabled="!connection"
            aria-label="新建 SQL 窗口"
            @click="newQuery()"
            ><el-icon><Plus /></el-icon
          ></el-button>
        </div>
        <div v-if="!currentTab" class="workspace-empty">
          <el-icon><EditPen /></el-icon>
          <h3>SQL 查询工作区</h3>
          <p>
            选择左侧连接，新建 SQL 窗口。写入与结构变更可进入 SQL 变更发布。
          </p>
          <el-button v-if="connection" type="primary" @click="newQuery()"
            >新建 SQL 窗口</el-button
          ><el-button v-else type="primary" @click="manage()"
            >新建连接</el-button
          >
        </div>
        <template v-else
          ><el-alert
            v-if="staleTarget"
            title="此窗口绑定的连接配置已变更或移除，请核对目标并新建 SQL 窗口。已有 SQL 与结果仍保留。"
            type="warning"
            :closable="false"
          />
          <div class="query-target">
            <span class="target-badge">固定目标</span
            ><code :title="currentTab.target">{{ currentTab.target }}</code
            ><span>Schema</span
            ><el-select
              v-model="currentTab.schema"
              placeholder="连接默认"
              size="small"
              style="width: 150px"
              :disabled="currentTab.running || staleTarget"
              ><el-option label="连接默认" value="" /><el-option
                v-for="schema in schemas[currentTab.databaseId] || []"
                :key="schema.name"
                :value="schema.name"
                :label="schema.name" /></el-select
            ><span class="toolbar-spacer" /><span class="read-only-label"
              ><el-icon><Lock /></el-icon>只读查询</span
            >
          </div>
          <div class="pg-toolbar query-actions">
            <el-button
              size="small"
              type="primary"
              :disabled="currentTab.running || staleTarget"
              @click="run('query')"
              ><el-icon><CaretRight /></el-icon>执行</el-button
            ><el-button
              size="small"
              :disabled="currentTab.running || staleTarget"
              @click="run('explain')"
              >预估计划</el-button
            ><el-button
              size="small"
              :disabled="currentTab.running"
              @click="editors.get(currentTab.id)?.formatSelection()"
              title="Ctrl / ⌘ + Shift + F"
              >格式化</el-button
            ><el-button text size="small" @click="favorites?.saveCurrent()"
              >收藏 SQL</el-button
            ><el-button
              v-if="currentTab.running"
              size="small"
              type="danger"
              plain
              @click="currentTab.controller?.abort()"
              >停止</el-button
            ><span class="shortcut">Ctrl / ⌘ + Enter</span
            ><span class="toolbar-spacer" /><el-button
              text
              size="small"
              @click="parametersOpen = !parametersOpen"
              >绑定参数{{
                currentTab.parameters && currentTab.parameters !== "[]"
                  ? " ●"
                  : ""
              }}</el-button
            ><span class="muted">预览</span
            ><el-select
              v-model="currentTab.maxRows"
              size="small"
              style="width: 92px"
              :disabled="currentTab.running"
              ><el-option
                v-for="count in [100, 1000, 5000, 10000]"
                :key="count"
                :value="count"
                :label="`${count} 行`" /></el-select
            ><el-select
              v-model="currentTab.timeoutSeconds"
              size="small"
              style="width: 95px"
              :disabled="currentTab.running"
              ><el-option
                v-for="seconds in [5, 15, 30]"
                :key="seconds"
                :value="seconds"
                :label="`${seconds} s 超时`"
            /></el-select>
          </div>
          <div v-if="parametersOpen" class="parameter-bar">
            <span>$1, $2 …</span
            ><el-input
              v-model="currentTab.parameters"
              size="small"
              placeholder='JSON 数组，例如 [123,"名称"]'
              :disabled="currentTab.running"
            /><small>按顺序绑定参数，不替换 SQL 文本</small>
          </div>
          <div
            ref="queryArea"
            class="query-area"
            :style="{
              gridTemplateRows: `${editorHeight}px 5px minmax(180px,1fr)`,
            }"
          >
            <SqlEditor
              v-for="tab in queryTabs"
              :key="tab.id"
              :ref="(value) => bindEditor(tab.id, value)"
              v-show="tab.id === tabId"
              v-model="tab.sql"
              :schema="completions[tab.databaseId]"
              :default-schema="tab.schema"
              :active="tab.id === tabId && pane === 'sql'"
              @run="run('query', $event)"
              @explain="run('explain', $event)"
              @error="ElMessage.error($event)"
              @save="workspace.flush()"
            />
            <div
              class="query-resizer"
              role="separator"
              aria-label="调整编辑器与结果区高度"
              tabindex="0"
              @pointerdown="resize"
              @keydown.up.prevent="
                editorHeight = Math.max(160, editorHeight - 20)
              "
              @keydown.down.prevent="editorHeight += 20"
            />
            <section class="result-pane">
              <div class="result-heading">
                <div class="result-tabs">
                  <button
                    :class="{ active: currentTab.resultPane === 'rows' }"
                    @click="currentTab.resultPane = 'rows'"
                  >
                    结果集
                    <small v-if="currentTab.result?.status === 'SUCCESS'">{{
                      rowCount
                    }}</small></button
                  ><button
                    :class="{ active: currentTab.resultPane === 'plan' }"
                    @click="currentTab.resultPane = 'plan'"
                  >
                    执行计划</button
                  ><button
                    :class="{ active: currentTab.resultPane === 'message' }"
                    @click="currentTab.resultPane = 'message'"
                  >
                    消息
                    <span
                      v-if="currentTab.error || currentTab.result?.error"
                      class="error-dot"
                    />
                  </button>
                </div>
                <span class="toolbar-spacer" /><el-button
                  text
                  size="small"
                  :disabled="currentTab.running || staleTarget"
                  title="使用上次执行的查询 SQL 和参数重新查询，导出完整结果"
                  @click="download"
                  ><el-icon><Download /></el-icon>重新执行并导出 CSV</el-button
                >
              </div>
              <div v-if="currentTab.error" class="result-error">
                {{ currentTab.error }}
              </div>
              <div v-if="currentTab.running" class="query-progress">
                <el-icon class="is-loading"><Loading /></el-icon>正在
                {{
                  currentTab.lastRequest?.mode === "explain"
                    ? "获取预估计划"
                    : "执行查询"
                }}
                · {{ currentTab.target }}
              </div>
              <template v-if="currentTab.resultPane === 'rows'"
                ><QueryResultGrid
                  v-if="currentTab.result?.columns.length"
                  :result="currentTab.result"
                />
                <div v-else class="result-empty">
                  {{
                    currentTab.result?.status === "FAILED"
                      ? "查询失败，请查看消息"
                      : currentTab.result?.status === "SUCCESS"
                        ? "查询完成，暂无结果列"
                        : "执行 SQL 后在这里查看结果"
                  }}
                </div></template
              >
              <template v-else-if="currentTab.resultPane === 'plan'"
                ><QueryPlan
                  v-if="currentTab.result?.plan"
                  :plan="currentTab.result.plan"
                />
                <div v-else class="result-empty">
                  选择 SQL，点击“预估计划”；支持查询及 INSERT / UPDATE / DELETE
                  / MERGE
                </div></template
              >
              <div v-else class="result-messages">
                <template v-if="currentTab.result"
                  ><p
                    :class="
                      currentTab.result.status === 'SUCCESS'
                        ? 'success-text'
                        : 'error-text'
                    "
                  >
                    {{
                      currentTab.result.status === "SUCCESS"
                        ? "执行成功"
                        : "执行失败"
                    }}
                    · {{ duration(currentTab.result.durationMs) }} ·
                    {{ time(currentTab.result.finishedAt) }}
                  </p>
                  <p>
                    {{ currentTab.result.target }} · Schema
                    {{ currentTab.lastRequest?.schema || "连接默认" }} · 记录
                    #{{ currentTab.result.recordId || "未保存" }}
                  </p>
                  <pre
                    v-if="currentTab.result.error"
                    class="sql-code error-text"
                    >{{
                      currentTab.result.sqlState
                        ? `SQLSTATE ${currentTab.result.sqlState}\n`
                        : ""
                    }}{{ currentTab.result.error }}</pre>
                  <pre class="sql-code">{{ currentTab.lastRequest?.sql }}</pre>
                  <p v-if="currentTab.result.warning" class="warning-text">
                    {{ currentTab.result.warning }}
                  </p></template
                ><span v-else class="muted"
                  >每次执行一条 SELECT / WITH 或 SHOW。变更请使用 SQL
                  变更发布。</span
                >
              </div>
              <footer v-if="currentTab.result" class="result-status">
                <span
                  :class="
                    currentTab.result.status === 'SUCCESS'
                      ? 'success-text'
                      : 'error-text'
                  "
                  >{{
                    currentTab.result.status === "SUCCESS"
                      ? "✓ 执行成功"
                      : "× 执行失败"
                  }}</span
                ><span>{{ duration(currentTab.result.durationMs) }}</span
                ><span v-if="currentTab.lastRequest?.mode === 'explain'"
                  >仅获取预估计划</span
                ><span v-else
                  >{{ currentTab.result.rowCount }} 行{{
                    currentTab.result.hasMore
                      ? " · 仅预览，完整结果可导出 CSV"
                      : ""
                  }}</span
                ><code :title="currentTab.result.target">{{
                  currentTab.result.target
                }}</code>
              </footer>
            </section>
          </div></template
        >
      </div>
      <template v-if="connection && pane === 'objects'"
        ><div class="pg-toolbar">
          <div class="pg-title">
            <strong>{{
              object
                ? `${object.relation.schema}.${object.relation.name}`
                : routine
                  ? `${routine.routine.schema}.${routine.routine.name}(${routine.routine.arguments})`
                  : "对象管理"
            }}</strong
            ><span>{{
              object
                ? relationKind(object.relation.kind)
                : routine
                  ? routine.routine.kind === "p"
                    ? "过程"
                    : "函数"
                  : "选择左侧数据库对象"
            }}</span>
          </div>
          <span class="toolbar-spacer" /><template
            v-if="object && object.relation.kind !== 'S'"
            ><el-button
              type="primary"
              plain
              size="small"
              @click="objectTab = 'data'"
              >浏览数据</el-button
            ><el-button
              size="small"
              @click="
                newQuery(
                  connection,
                  relationQuery(object.relation),
                  object.relation.schema,
                )
              "
              >新建查询</el-button
            ></template
          ><el-button
            v-if="object || routine"
            size="small"
            @click="
              copyText(
                object
                  ? `${quoteIdentifier(object.relation.schema)}.${quoteIdentifier(object.relation.name)}`
                  : routine
                    ? `${quoteIdentifier(routine.routine.schema)}.${quoteIdentifier(routine.routine.name)}`
                    : '',
              )
            "
            >复制对象名</el-button
          ><el-button
            v-if="object || routine"
            text
            size="small"
            @click="
              object = undefined;
              routine = undefined;
            "
            >对象列表</el-button
          >
        </div>
        <el-alert
          v-if="objectError"
          :title="objectError"
          type="error"
          :closable="false"
        />
        <div
          class="object-content"
          :class="{ 'object-content--data': objectTab === 'data' && !!object }"
          v-loading="objectLoading"
        >
          <template v-if="routine"
            ><div class="object-summary">
              <span>OID {{ routine.routine.oid }}</span
              ><span>所有者 {{ routine.owner }}</span
              ><span>语言 {{ routine.language }}</span
              ><span>返回 {{ routine.routine.result || "无返回值" }}</span
              ><span>{{
                routine.securityDefiner
                  ? "SECURITY DEFINER"
                  : "SECURITY INVOKER"
              }}</span>
            </div>
            <p class="object-comment">{{ routine.routine.comment }}</p>
            <div class="pg-toolbar">
              <strong>函数 / 过程定义</strong
              ><span class="toolbar-spacer" /><el-button
                size="small"
                @click="copyText(routine.definition)"
                >复制定义</el-button
              ><el-button
                size="small"
                @click="
                  newQuery(
                    connection,
                    routine.definition,
                    routine.routine.schema,
                  )
                "
                >在 SQL 窗口查看</el-button
              >
            </div>
            <pre class="sql-code object-definition">{{
              routine.definition
            }}</pre>
          </template>
          <template v-else-if="object"
            ><div class="object-summary">
              <span>OID {{ object.relation.oid }}</span
              ><template v-if="object.relation.kind !== 'S'"
                ><span
                  >估算行数
                  {{
                    object.relation.estimatedRows.toLocaleString()
                  }}（统计值）</span
                ><span
                  >{{ object.columns.length }} 列 ·
                  {{ object.indexes.length }} 个索引 ·
                  {{ object.constraints.length }} 个约束</span
                ></template
              ><span>{{ object.relation.comment }}</span>
            </div>
            <template v-if="object.sequence"
              ><el-descriptions :column="2" border class="sequence-properties"
                ><el-descriptions-item label="数据类型">{{
                  object.sequence.type
                }}</el-descriptions-item
                ><el-descriptions-item label="起始值">{{
                  object.sequence.start
                }}</el-descriptions-item
                ><el-descriptions-item label="最小值">{{
                  object.sequence.min
                }}</el-descriptions-item
                ><el-descriptions-item label="最大值">{{
                  object.sequence.max
                }}</el-descriptions-item
                ><el-descriptions-item label="步长">{{
                  object.sequence.increment
                }}</el-descriptions-item
                ><el-descriptions-item label="缓存">{{
                  object.sequence.cache
                }}</el-descriptions-item
                ><el-descriptions-item label="循环">{{
                  object.sequence.cycle ? "是" : "否"
                }}</el-descriptions-item
                ><el-descriptions-item label="磁盘记录值">{{
                  object.sequence.last ?? "尚未使用或账号无读取权限"
                }}</el-descriptions-item></el-descriptions
              >
              <p class="pg-footnote">
                磁盘记录值受序列缓存影响，不代表最后一次实际发放的值；浏览序列不会调用
                nextval。
              </p></template
            >
            <template v-else
              ><div class="pg-subtabs">
                <button
                  v-for="item in [
                    { id: 'data', label: '数据' },
                    { id: 'columns', label: '列结构' },
                    { id: 'indexes', label: '索引' },
                    { id: 'constraints', label: '约束' },
                    { id: 'triggers', label: '触发器' },
                    { id: 'definition', label: '视图定义' },
                  ].filter(
                    (item) => item.id !== 'definition' || object?.definition,
                  )"
                  :key="item.id"
                  :class="{ active: objectTab === item.id }"
                  @click="objectTab = item.id"
                >
                  {{ item.label }}
                </button>
              </div>
              <ObjectDataBrowser
                v-if="objectTab === 'data'"
                :key="`${connection.id}:${object.relation.oid}`"
                :connection="connection"
                :object="object"
                @open="
                  (sql, schema, params) =>
                    newQuery(connection, sql, schema, JSON.stringify(params))
                "
              />
              <el-table
                v-else-if="objectTab === 'columns'"
                :data="object.columns"
                size="small"
                ><el-table-column
                  prop="position"
                  label="#"
                  width="55" /><el-table-column label="列名" min-width="180"
                  ><template #default="{ row }"
                    ><span>{{ row.name }}</span
                    ><el-tag
                      v-if="row.primaryKey"
                      size="small"
                      class="primary-key-tag"
                      >PK</el-tag
                    ></template
                  ></el-table-column
                ><el-table-column
                  prop="type"
                  label="类型"
                  min-width="160" /><el-table-column
                  label="允许 NULL"
                  width="110"
                  ><template #default="{ row }">{{
                    row.nullable ? "是" : "否"
                  }}</template></el-table-column
                ><el-table-column
                  prop="default"
                  label="默认值"
                  min-width="180"
                  show-overflow-tooltip /><el-table-column
                  prop="comment"
                  label="注释"
                  min-width="200"
                  show-overflow-tooltip
              /></el-table>
              <el-table
                v-else-if="objectTab === 'indexes'"
                :data="object.indexes"
                size="small"
                ><el-table-column
                  prop="name"
                  label="索引名"
                  width="210"
                /><el-table-column label="属性" width="140"
                  ><template #default="{ row }">{{
                    [
                      row.primary ? "主键" : "",
                      row.unique ? "唯一" : "",
                      !row.valid ? "无效" : "",
                    ]
                      .filter(Boolean)
                      .join(" · ") || "普通"
                  }}</template></el-table-column
                ><el-table-column
                  prop="definition"
                  label="定义"
                  min-width="350"
                /><el-table-column width="70"
                  ><template #default="{ row }"
                    ><el-button
                      text
                      size="small"
                      @click="copyText(row.definition)"
                      >复制</el-button
                    ></template
                  ></el-table-column
                ></el-table
              >
              <el-table
                v-else-if="objectTab === 'constraints'"
                :data="object.constraints"
                size="small"
                ><el-table-column
                  prop="name"
                  label="约束名"
                  width="210" /><el-table-column label="类型" width="110"
                  ><template #default="{ row }">{{
                    (
                      {
                        p: "主键",
                        u: "唯一",
                        f: "外键",
                        c: "检查",n:"非空",
                        x: "排除",
                      } as any
                    )[row.type] || row.type
                  }}</template></el-table-column
                ><el-table-column label="已验证" width="90"
                  ><template #default="{ row }">{{
                    row.validated ? "是" : "否"
                  }}</template></el-table-column
                ><el-table-column
                  prop="definition"
                  label="定义"
                  min-width="350"
              /></el-table>
              <el-table
                v-else-if="objectTab === 'triggers'"
                :data="object.triggers"
                size="small"
                ><el-table-column
                  prop="name"
                  label="触发器"
                  width="210" /><el-table-column label="启用模式" width="120"
                  ><template #default="{ row }">{{
                    ({ O: "普通", D: "禁用", R: "副本", A: "始终" } as any)[
                      row.enabled
                    ] || row.enabled
                  }}</template></el-table-column
                ><el-table-column
                  prop="definition"
                  label="定义"
                  min-width="400"
              /></el-table>
              <template v-else
                ><div class="pg-toolbar">
                  <span class="muted">PostgreSQL 返回的视图查询定义</span
                  ><el-button
                    text
                    size="small"
                    @click="copyText(object.definition)"
                    >复制定义</el-button
                  >
                </div>
                <pre class="sql-code object-definition">{{
                  object.definition
                }}</pre>
              </template></template
            ></template
          >
          <template v-else
            ><div class="pg-toolbar">
              <el-input
                v-model="objectSearch"
                size="small"
                placeholder="筛选已加载对象"
                clearable
                style="width: 250px"
              /><span class="muted"
                >双击表或视图浏览数据，右键显示对象操作</span
              >
            </div>
            <el-table
              :data="objectRows"
              size="small"
              @row-dblclick="
                (row: RelationInfo) =>
                  openRelation(row, row.kind === 'S' ? 'sequence' : 'data')
              "
              ><el-table-column
                prop="schema"
                label="Schema"
                width="160"
              /><el-table-column
                prop="name"
                label="名称"
                min-width="250"
              /><el-table-column label="类型" width="120"
                ><template #default="{ row }">{{
                  relationKind(row.kind)
                }}</template></el-table-column
              ><el-table-column
                prop="comment"
                label="注释"
                min-width="240"
              /><el-table-column width="75"
                ><template #default="{ row }"
                  ><el-button text size="small" @click="openRelation(row)"
                    >查看</el-button
                  ></template
                ></el-table-column
              ></el-table
            ></template
          >
        </div></template
      >
      <DatabaseAccessPanel
        v-else-if="connection && pane === 'access'"
        :connection="connection"
        @changed="loadCatalog(connection.id)"
      />
      <DiagnosticsPanel
        v-else-if="connection && (pane === 'sessions' || pane === 'locks')"
        :connection="connection"
        :pane="pane"
        :key="pane"
        @open-sql="openSQL"
      />
      <SlowSQLPanel
        v-else-if="connection && pane === 'slow'"
        :connection="connection"
        @configure="manage(connection)"
        @synced="loadConnections"
        @open-sql="openSQL"
      />
      <QueryHistory
        v-else-if="connection && pane === 'records'"
        :database-id="connection.id"
        @open="openRecord"
      />
    </main>
    <SavedQueries
      ref="favorites"
      v-model="savedQueriesOpen"
      :connections="databases"
      :connection-id="databaseId"
      :draft="currentTab ? draftOf(currentTab) : undefined"
      :closed="closedTabs"
      @open="openFavorite"
      @restore="restoreClosed"
      @discard="discardClosed"
    />
    <Teleport to="body"
      ><div
        v-if="menu"
        class="database-object-menu"
        :style="{ left: `${menu.x}px`, top: `${menu.y}px` }"
        role="menu"
        @click.stop
      >
        <button
          v-if="menu.row.kind !== 'S'"
          role="menuitem"
          @click="objectAction('data')"
        >
          浏览数据</button
        ><button role="menuitem" @click="objectAction('details')">
          查看对象详情</button
        ><button
          v-if="menu.row.kind !== 'S'"
          role="menuitem"
          @click="objectAction('query')"
        >
          新建 SQL 查询
        </button>
        <hr />
        <button role="menuitem" @click="objectAction('name')">
          复制限定对象名</button
        ><button
          v-if="menu.row.kind !== 'S'"
          role="menuitem"
          @click="objectAction('sql')"
        >
          复制 SELECT 语句
        </button>
      </div></Teleport
    >
    <ConnectionDialog
      v-model="editorDialog"
      :connection="editingConnection"
      @saved="saved"
      @deleted="deleted"
    />
  </div>
</template>
<style src="../styles/postgresql-workbench.css"></style>

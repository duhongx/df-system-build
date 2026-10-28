<script setup lang="ts">
import { ref, watch, computed, onBeforeUnmount } from "vue";
import type { OpsDatabase } from "../../api/operations";
import {
  queryDatabase,
  type RelationDetail,
  type QueryResult,
} from "../../api/database-workbench";
import { tableQuery } from "../../utils/database-table-query.mjs";
import { formatDuration } from "../../utils/database-workbench.mjs";
import QueryResultGrid from "./QueryResultGrid.vue";
const props = defineProps<{
  connection: OpsDatabase;
  object: RelationDetail;
}>();
const emit = defineEmits<{ open: [string, string, unknown[]] }>();
const selectedColumns = ref<string[]>([]),
  filterColumn = ref(""),
  operator = ref("="),
  value = ref(""),
  sortColumn = ref(""),
  desc = ref(false),
  page = ref(1),
  pageSize = ref(100),
  loading = ref(false),
  result = ref<QueryResult>(),
  error = ref(""),
  showSQL = ref(false);
let controller: AbortController | undefined;
let generation = 0;
const query = computed(() => {
  try {
    return tableQuery(props.object.relation, props.object.columns, {
      columns: selectedColumns.value,
      filterColumn: filterColumn.value,
      operator: operator.value,
      value: value.value,
      sortColumn: sortColumn.value,
      desc: desc.value,
      page: page.value,
      pageSize: pageSize.value,
    });
  } catch {
    return undefined;
  }
});
async function load(reset = false) {
  if (reset) page.value = 1;
  if (!query.value) {
    error.value = "请检查所选列和查询条件";
    return;
  }
  const token = ++generation;
  controller?.abort();
  controller = new AbortController();
  loading.value = true;
  error.value = "";
  try {
    const data = await queryDatabase(
      props.connection.id,
      {
        ...query.value,
        schema: props.object.relation.schema,
        revision: props.connection.revision,
        mode: "query",
        maxRows: pageSize.value,
        timeoutSeconds: 15,
      },
      controller.signal,
    );
    if (token === generation) {
      result.value = data;
      if (data.status !== "SUCCESS") error.value = data.error;
    }
  } catch (e: any) {
    if (token === generation && e.code !== "ERR_CANCELED")
      error.value = e.message;
  } finally {
    if (token === generation) loading.value = false;
  }
}
function turn(delta: number) {
  page.value += delta;
  load();
}
watch(
  () => [
    props.connection.id,
    props.connection.revision,
    props.object.relation.oid,
  ],
  () => {
    selectedColumns.value = props.object.columns.map((c) => c.name);
    filterColumn.value = "";
    sortColumn.value = "";
    page.value = 1;
    result.value = undefined;
    load();
  },
  { immediate: true },
);
onBeforeUnmount(() => {
  generation++;
  controller?.abort();
});
</script>
<template>
  <section class="object-data-browser">
    <div class="pg-toolbar">
      <el-select
        v-model="filterColumn"
        placeholder="筛选列"
        clearable
        size="small"
        style="width: 150px"
        ><el-option
          v-for="c in object.columns"
          :key="c.name"
          :value="c.name"
          :label="c.name" /></el-select
      ><el-select v-model="operator" size="small" style="width: 125px"
        ><el-option
          v-for="op in [
            '=',
            '<>',
            '<',
            '<=',
            '>',
            '>=',
            'contains',
            'IS NULL',
            'IS NOT NULL',
          ]"
          :key="op"
          :value="op"
          :label="op === 'contains' ? '包含文本' : op" /></el-select
      ><el-input
        v-if="!operator.includes('NULL')"
        v-model="value"
        size="small"
        placeholder="筛选值（绑定参数）"
        style="width: 185px"
        @keyup.enter="load(true)"
      /><el-button
        type="primary"
        size="small"
        :loading="loading"
        @click="load(true)"
        >查询</el-button
      ><span class="toolbar-spacer" /><el-button
        text
        size="small"
        @click="showSQL = !showSQL"
        >查看 SQL</el-button
      ><el-button
        text
        size="small"
        :disabled="!query"
        @click="
          query &&
          emit('open', query.sql, object.relation.schema, query.parameters)
        "
        >在 SQL 窗口打开</el-button
      >
    </div>
    <div class="pg-toolbar data-options">
      <el-popover trigger="click" :width="260"
        ><template #reference
          ><el-button text size="small"
            >查询列 {{ selectedColumns.length }} /
            {{ object.columns.length }}</el-button
          ></template
        >
        <div class="column-options">
          <el-checkbox-group v-model="selectedColumns"
            ><el-checkbox
              v-for="c in object.columns"
              :key="c.name"
              :value="c.name"
              >{{ c.name }}</el-checkbox
            ></el-checkbox-group
          >
        </div></el-popover
      ><span class="muted">排序</span
      ><el-select
        v-model="sortColumn"
        clearable
        placeholder="主键顺序／未指定"
        size="small"
        style="width: 180px"
        ><el-option
          v-for="c in object.columns"
          :key="c.name"
          :value="c.name"
          :label="c.name" /></el-select
      ><el-switch v-model="desc" size="small" active-text="降序" /><span
        class="toolbar-spacer"
      /><el-select
        v-model="pageSize"
        size="small"
        style="width: 95px"
        @change="load(true)"
        ><el-option
          v-for="n in [50, 100, 200]"
          :key="n"
          :value="n"
          :label="`${n} 行/页`" /></el-select
      ><el-button
        size="small"
        :disabled="page <= 1 || loading"
        @click="turn(-1)"
        >上一页</el-button
      ><span class="muted">第 {{ page }} 页</span
      ><el-button
        size="small"
        :disabled="!result?.hasMore || loading"
        @click="turn(1)"
        >下一页</el-button
      >
    </div>
    <div v-if="showSQL" class="browser-sql">
      <pre class="sql-code">{{ query?.sql }}</pre>
      <span class="muted"
        >绑定参数 {{ JSON.stringify(query?.parameters) }}</span
      >
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <div v-loading="loading" class="browser-results">
      <QueryResultGrid v-if="result?.columns.length" :result="result" compact />
      <div v-else class="result-empty">
        {{ loading ? "正在查询表数据" : "暂无可展示的数据" }}
      </div>
    </div>
    <footer class="pg-footnote">
      当前页 {{ result?.rowCount || 0 }} 行 ·
      {{ formatDuration(result?.durationMs) }} ·
      每次翻页重新查询，业务数据变化可能影响翻页结果。{{
        !object.columns.some((c) => c.primaryKey) && !sortColumn
          ? "当前对象没有主键，建议选择排序列。"
          : ""
      }}
    </footer>
  </section>
</template>
<style scoped>
.object-data-browser {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 360px;
  overflow: hidden;
}
.data-options {
  padding-block: 5px !important;
  background: var(--ui-panel-soft);
}
.browser-results {
  display: flex;
  flex: 1;
  min-height: 230px;
}
.browser-sql {
  padding: 4px 14px 10px;
  border-bottom: 1px solid var(--ui-border);
}
.browser-sql pre {
  margin-bottom: 3px;
  max-height: 160px;
}
.column-options {
  max-height: 340px;
  overflow: auto;
}
.column-options :deep(.el-checkbox-group) {
  display: flex;
  flex-direction: column;
}
</style>

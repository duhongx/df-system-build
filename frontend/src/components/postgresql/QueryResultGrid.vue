<script setup lang="ts">
import { computed, ref, watch, nextTick } from "vue";
import { ElMessage } from "element-plus";
import type { QueryResult } from "../../api/database-workbench";
import {
  resultRows,
  delimited,
  textParts,
  type ResultRow,
} from "../../utils/database-results.mjs";
const props = defineProps<{ result: QueryResult; compact?: boolean }>();
const keyword = ref(""),
  filter = ref(false),
  page = ref(1),
  pageSize = ref(100),
  sortIndex = ref(-1),
  sortOrder = ref("ascending"),
  visible = ref<number[]>([]),
  selected = ref<ResultRow[]>([]),
  cell = ref<{ row: ResultRow; column: number }>(),
  detail = ref<ResultRow>(),
  detailOpen = ref(false),
  table = ref(),
  host = ref<HTMLElement>();
const rows = computed(() =>
  resultRows(
    props.result,
    keyword.value,
    filter.value,
    sortIndex.value,
    sortOrder.value,
  ),
);
const paged = computed(() =>
  props.compact
    ? rows.value
    : rows.value.slice(
        (page.value - 1) * pageSize.value,
        page.value * pageSize.value,
      ),
);
const columns = computed(() =>
  props.result.columns
    .map((value, index) => ({ ...value, index }))
    .filter((c) => visible.value.includes(c.index)),
);
const matches = computed(() =>
  !keyword.value ? 0 : resultRows(props.result, keyword.value, true).length,
);
watch(
  () => props.result,
  () => {
    visible.value = props.result.columns.map((_, i) => i);
    sortIndex.value = -1;
    keyword.value = "";
    selected.value = [];
    cell.value = undefined;
    page.value = 1;
  },
  { immediate: true },
);
watch([keyword, filter, pageSize], () => {
  page.value = 1;
  selected.value = [];
  table.value?.clearSelection();
});
watch(visible, () => {
  cell.value = undefined;
});
function sort(value: { prop: string; order: string | null }) {
  sortIndex.value = value.order ? Number(value.prop.slice(2)) : -1;
  sortOrder.value = value.order || "ascending";
  page.value = 1;
}
async function copy(text: string) {
  try {
    if (navigator.clipboard) await navigator.clipboard.writeText(text);
    else {
      const input = document.createElement("textarea");
      input.value = text;
      document.body.append(input);
      input.select();
      document.execCommand("copy");
      input.remove();
    }
    ElMessage.success("已复制");
  } catch {
    ElMessage.error("复制失败，请手动选择文本");
  }
}
function cellClass({
  row,
  column,
}: {
  row: ResultRow;
  column: { property: string };
}) {
  const value = cell.value;
  return value &&
    value.row.index === row.index &&
    column.property === `c_${value.column}`
    ? "selected-result-cell"
    : "";
}
function copyRows(chosen: ResultRow[]) {
  void copy(
    delimited(
      columns.value,
      chosen.map((r) => ({
        index: r.index,
        values: columns.value.map((c) => r.values[c.index] ?? null),
      })),
      "\t",
    ),
  );
}
function clickCell(row: ResultRow, column: { property: string }) {
  if (!column.property?.startsWith("c_")) return;
  cell.value = { row, column: Number(column.property.slice(2)) };
  nextTick(() => host.value?.focus({ preventScroll: true }));
}
function keyboard(event: KeyboardEvent) {
  if (
    (event.ctrlKey || event.metaKey) &&
    event.key.toLowerCase() === "c" &&
    cell.value &&
    !window.getSelection()?.toString() &&
    !(event.target instanceof HTMLInputElement)
  ) {
    event.preventDefault();
    void copy(cell.value.row.values[cell.value.column] ?? "NULL");
  }
}
function download() {
  const text = delimited(
    columns.value,
    rows.value.map((r) => ({
      index: r.index,
      values: columns.value.map((c) => r.values[c.index] ?? null),
    })),
  );
  const url = URL.createObjectURL(
    new Blob(["\uFEFF", text], { type: "text/csv;charset=utf-8" }),
  );
  const a = document.createElement("a");
  a.href = url;
  a.download = `result-${props.result.recordId || Date.now()}-loaded.csv`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 60000);
}
</script>
<template>
  <div ref="host" class="data-result-grid" tabindex="0" @keydown="keyboard">
    <div class="result-grid-tools">
      <el-input
        v-model="keyword"
        clearable
        size="small"
        placeholder="搜索已加载结果"
        style="width: 195px"
        ><template #prefix
          ><el-icon><Search /></el-icon></template></el-input
      ><el-checkbox v-model="filter" size="small">仅显示匹配行</el-checkbox
      ><span v-if="keyword" class="grid-hint">{{ matches }} 行匹配</span
      ><span class="toolbar-spacer" /><el-popover
        trigger="click"
        placement="bottom-end"
        :width="250"
        ><template #reference
          ><el-button size="small" text
            >显示列 {{ visible.length }} /
            {{ result.columns.length }}</el-button
          ></template
        >
        <div class="column-picker">
          <el-checkbox-group v-model="visible" :min="1"
            ><el-checkbox
              v-for="(column, index) in result.columns"
              :key="index"
              :value="index"
              >{{ column.name }} <small>{{ column.type }}</small></el-checkbox
            ></el-checkbox-group
          >
        </div></el-popover
      ><el-button
        text
        size="small"
        :disabled="!selected.length"
        @click="copyRows(selected)"
        >复制所选 {{ selected.length || "" }}</el-button
      ><el-dropdown
        trigger="click"
        @command="
          (command: string) =>
            command === 'download' ? download() : copyRows(paged)
        "
        ><el-button text size="small"
          >更多 <el-icon><ArrowDown /></el-icon></el-button
        ><template #dropdown
          ><el-dropdown-menu
            ><el-dropdown-item command="copy"
              >复制当前页（含列名）</el-dropdown-item
            ><el-dropdown-item command="download"
              >导出已加载结果 CSV（{{ rows.length }} 行）</el-dropdown-item
            ></el-dropdown-menu
          ></template
        ></el-dropdown
      >
    </div>
    <el-table
      ref="table"
      :data="paged"
      border
      size="small"
      height="100%"
      class="result-table"
      row-key="index"
      :cell-class-name="cellClass"
      @sort-change="sort"
      @selection-change="(value: ResultRow[]) => (selected = value)"
      @cell-click="clickCell"
      @row-dblclick="
        (row: ResultRow) => {
          detail = row;
          detailOpen = true;
        }
      "
      ><el-table-column
        type="selection"
        width="38"
        reserve-selection
      /><el-table-column
        prop="index"
        label="#"
        width="58"
        fixed
      /><el-table-column
        v-for="column in columns"
        :key="column.index"
        :prop="`c_${column.index}`"
        min-width="180"
        sortable="custom"
        show-overflow-tooltip
        ><template #header
          ><span class="column-heading"
            ><span>{{ column.name }}</span
            ><small class="column-type">{{ column.type }}</small></span
          ></template
        ><template #default="{ row }"
          ><span v-if="row.values[column.index] === null" class="null-value"
            >NULL</span
          ><template v-else
            ><template
              v-for="(part, i) in textParts(row.values[column.index], keyword)"
              :key="i"
              ><mark v-if="part.match">{{ part.text }}</mark
              ><span v-else>{{ part.text }}</span></template
            ></template
          ></template
        ></el-table-column
      ><el-table-column fixed="right" label="" width="70"
        ><template #default="{ row }"
          ><el-button
            text
            size="small"
            title="查看单行详情"
            @click.stop="
              detail = row;
              detailOpen = true;
            "
            >详情</el-button
          ></template
        ></el-table-column
      ></el-table
    >
    <div class="result-grid-footer">
      <span class="grid-hint"
        >已加载 {{ result.rows.length }} 行{{
          result.hasMore
            ? compact
              ? " · 后面还有数据，请翻页"
              : " · 后续结果可重新执行并完整导出"
            : ""
        }}<template v-if="cell">
          · 当前 {{ result.columns[cell.column]?.name }}
          <el-button
            text
            size="small"
            @click="copy(cell.row.values[cell.column] ?? 'NULL')"
            >复制单元格</el-button
          ></template
        ></span
      ><el-pagination
        v-if="!compact"
        v-model:current-page="page"
        v-model:page-size="pageSize"
        small
        :page-sizes="[50, 100, 200]"
        :total="rows.length"
        layout="sizes,prev,pager,next"
      />
    </div>
    <el-drawer
      v-model="detailOpen"
      :title="`第 ${detail?.index || ''} 行详情`"
      size="min(650px,90vw)"
      append-to-body
      ><template v-if="detail"
        ><div
          v-for="(column, index) in result.columns"
          :key="index"
          class="cell-detail"
        >
          <header>
            <strong>{{ column.name }}</strong
            ><small>{{ column.type }}</small
            ><el-button
              text
              size="small"
              @click="copy(detail!.values[index] ?? 'NULL')"
              >复制</el-button
            >
          </header>
          <pre :class="{ 'null-value': detail.values[index] === null }">{{
            detail.values[index] === null ? "NULL" : detail.values[index]
          }}</pre>
        </div></template
      ></el-drawer
    >
  </div>
</template>
<style scoped>
.data-result-grid {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  outline: none;
  overflow: hidden;
}
.result-grid-tools {
  display: flex;
  gap: 10px;
  align-items: center;
  padding: 6px 12px;
  border-bottom: 1px solid var(--ui-border);
  flex-shrink: 0;
  flex-wrap: wrap;
}
.grid-hint {
  font-size: 10px;
  color: var(--ui-muted);
}
.result-grid-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 4px 10px;
  border-top: 1px solid var(--ui-border);
  flex-shrink: 0;
}
.result-grid-footer > .grid-hint {
  display: flex;
  align-items: center;
  gap: 5px;
}
.result-table {
  flex: 1;
  min-height: 0;
}
.result-table :deep(.cell) {
  white-space: nowrap;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
}
.result-table :deep(th.is-sortable .cell) {
  display: flex;
  align-items: center;
  gap: 6px;
}
.column-heading {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 18px;
}
.column-heading > span {
  overflow: hidden;
  text-overflow: ellipsis;
}
.result-table :deep(.selected-result-cell) {
  outline: 1px solid var(--ui-primary);
  outline-offset: -1px;
  background: var(--ui-tint);
}
mark {
  background: #ffe6a0;
  color: #563f0b;
  border-radius: 2px;
}
.column-picker {
  max-height: 360px;
  overflow: auto;
}
.column-picker :deep(.el-checkbox-group) {
  display: flex;
  flex-direction: column;
}
.column-picker small {
  color: var(--ui-muted);
  font-size: 9px;
}
.cell-detail {
  border-bottom: 1px solid var(--ui-border);
  padding: 12px 0;
}
.cell-detail header {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
}
.cell-detail header small {
  font-size: 10px;
  color: var(--ui-muted);
  flex: 1;
}
.cell-detail pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 350px;
  overflow: auto;
  background: var(--ui-panel-soft);
  padding: 12px;
  border-radius: 4px;
  font:
    12px/1.8 ui-monospace,
    SFMono-Regular,
    Consolas,
    monospace;
  color: var(--ui-heading);
}
@media (max-width: 1100px) {
  .result-grid-tools {
    gap: 5px;
  }
  .result-grid-footer {
    flex-wrap: wrap;
  }
}
</style>

<script setup lang="ts">
import { computed, ref } from "vue";
import { planRows } from "../../utils/database-workbench.mjs";
const props = defineProps<{ plan: Array<{ Plan: Record<string, any> }> }>();
const raw = ref(false);
const selected = ref<Record<string, any>>();
const rows = computed(() => planRows(props.plan[0]?.Plan));
</script>
<template>
  <div class="query-plan">
    <div class="plan-note">
      <span
        >预估执行计划 · 未执行 ANALYZE · Cost 为优化器成本，Rows
        为预计输出行数</span
      ><el-switch v-model="raw" size="small" active-text="JSON" />
    </div>
    <pre v-if="raw" class="sql-code">{{ JSON.stringify(plan, null, 2) }}</pre>
    <template v-else
      ><el-table
        :data="rows"
        size="small"
        @row-click="(row: any) => (selected = row.node)"
        ><el-table-column label="执行节点" min-width="300"
          ><template #default="{ row }"
            ><div
              :style="{ paddingLeft: `${row.depth * 22}px` }"
              class="plan-node"
            >
              <span v-if="row.depth" class="tree-line">└</span
              ><b>{{ row.node["Node Type"] }}</b
              ><span>{{
                row.node["Relation Name"] || row.node["Index Name"] || ""
              }}</span>
            </div></template
          ></el-table-column
        ><el-table-column label="启动 Cost" width="120" align="right"
          ><template #default="{ row }">{{
            row.node["Startup Cost"]
          }}</template></el-table-column
        ><el-table-column label="总 Cost" width="120" align="right"
          ><template #default="{ row }">{{
            row.node["Total Cost"]
          }}</template></el-table-column
        ><el-table-column label="Rows" width="110" align="right"
          ><template #default="{ row }">{{
            row.node["Plan Rows"]
          }}</template></el-table-column
        ><el-table-column label="Width" width="90" align="right"
          ><template #default="{ row }">{{
            row.node["Plan Width"]
          }}</template></el-table-column
        ><el-table-column label="条件" min-width="250" show-overflow-tooltip
          ><template #default="{ row }">{{
            row.node["Index Cond"] ||
            row.node["Hash Cond"] ||
            row.node["Join Filter"] ||
            row.node["Filter"] ||
            "—"
          }}</template></el-table-column
        ></el-table
      >
      <div v-if="selected" class="node-detail">
        <strong>{{ selected["Node Type"] }} 节点详情</strong>
        <pre class="sql-code">{{
          JSON.stringify(
            Object.fromEntries(
              Object.entries(selected).filter(([key]) => key !== "Plans"),
            ),
            null,
            2,
          )
        }}</pre>
      </div></template
    >
  </div>
</template>
<style scoped>
.query-plan {
  overflow: auto;
  height: 100%;
}
.plan-note {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 9px 16px;
  color: var(--ui-muted);
  font-size: 11px;
  gap: 16px;
  border-bottom: 1px solid var(--ui-border);
}
.plan-node {
  display: flex;
  gap: 8px;
  font-size: 11px;
  white-space: nowrap;
}
.plan-node b {
  font-weight: 550;
  color: var(--ui-primary);
}
.plan-node span {
  color: var(--ui-muted);
}
.tree-line {
  font-family: monospace;
}
.node-detail {
  padding: 14px;
  font-size: 12px;
}
.query-plan > pre {
  margin: 12px;
}
</style>

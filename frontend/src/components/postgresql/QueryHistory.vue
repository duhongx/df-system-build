<script setup lang="ts">
import { ref, watch } from "vue";
import {
  getQueryRecords,
  type QueryRecord,
} from "../../api/database-workbench";
import {
  formatDate as time,
  formatDuration as duration,
} from "../../utils/database-workbench.mjs";
const props = defineProps<{ databaseId: number }>();
const emit = defineEmits<{ open: [QueryRecord] }>();
const rows = ref<QueryRecord[]>([]);
const page = ref(1);
const total = ref(0);
const loading = ref(false);
const error = ref("");
let generation = 0;
async function load() {
  const token = ++generation;
  loading.value = true;
  error.value = "";
  try {
    const result = await getQueryRecords(props.databaseId, page.value);
    if (token === generation) {
      rows.value = result.list;
      total.value = result.total;
    }
  } catch (e: any) {
    if (token === generation) error.value = e.message;
  } finally {
    if (token === generation) loading.value = false;
  }
}
watch(
  () => props.databaseId,
  () => {
    page.value = 1;
    rows.value = [];
    load();
  },
  { immediate: true },
);
</script>
<template>
  <section class="query-history">
    <div class="pg-toolbar">
      <div class="pg-title">
        <strong>执行记录</strong
        ><span>保留当时的连接目标、SQL、参数与执行结果状态</span>
      </div>
      <span class="toolbar-spacer" /><el-button
        size="small"
        :loading="loading"
        @click="load"
        >刷新</el-button
      >
    </div>
    <el-alert
      v-if="error"
      :title="error"
      type="error"
      :closable="false"
    /><el-table :data="rows" size="small" v-loading="loading"
      ><el-table-column type="expand"
        ><template #default="{ row }"
          ><div class="history-expanded">
            <p>
              {{ row.target }} ·
              {{
                row.kind === "access"
                  ? "用户权限变更"
                  : `Schema ${row.schema || "默认"}`
              }}
              · 连接版本
              {{ row.revision }}
            </p>
            <pre class="sql-code">{{ row.sql }}</pre>
            <pre
              v-if="row.parametersJSON && row.parametersJSON !== 'null'"
              class="sql-code"
              >{{
                row.kind === "access" ? "变更参数（不含密码）" : "绑定参数"
              }}：{{ row.parametersJSON }}</pre
            >
            <el-alert
              v-if="row.error"
              :title="row.error"
              type="error"
              :closable="false"
            /></div></template></el-table-column
      ><el-table-column label="开始时间" width="175"
        ><template #default="{ row }">{{
          time(row.startedAt)
        }}</template></el-table-column
      ><el-table-column label="类型" width="95"
        ><template #default="{ row }">{{
          (
            {
              query: "查询",
              explain: "预估计划",
              export: "CSV 导出",
              access: "用户权限",
            } as any
          )[row.kind] || row.kind
        }}</template></el-table-column
      ><el-table-column label="状态" width="85"
        ><template #default="{ row }"
          ><el-tag
            size="small"
            :type="
              row.status === 'SUCCESS'
                ? 'success'
                : row.status === 'FAILED'
                  ? 'danger'
                  : 'warning'
            "
            >{{
              (
                {
                  SUCCESS: "成功",
                  FAILED: "失败",
                  RUNNING: "执行中",
                  UNKNOWN: "待核实",
                } as any
              )[row.status] || row.status
            }}</el-tag
          ></template
        ></el-table-column
      ><el-table-column label="耗时" width="110"
        ><template #default="{ row }">{{
          duration(row.durationMs)
        }}</template></el-table-column
      ><el-table-column label="返回行数" width="110"
        ><template #default="{ row }"
          >{{ row.rows }}{{ row.hasMore ? "+" : "" }}</template
        ></el-table-column
      ><el-table-column
        prop="operator"
        label="操作人"
        width="100"
      /><el-table-column
        prop="sql"
        label="SQL"
        min-width="300"
        show-overflow-tooltip
      /><el-table-column label="操作" width="100"
        ><template #default="{ row }"
          ><el-button
            v-if="row.kind !== 'access'"
            text
            size="small"
            type="primary"
            @click="emit('open', row)"
            >新窗口打开</el-button
          ></template
        ></el-table-column
      ></el-table
    ><el-pagination
      v-model:current-page="page"
      :page-size="25"
      :total="total"
      layout="total,prev,pager,next"
      @current-change="load"
    />
  </section>
</template>
<style scoped>
.query-history {
  overflow: auto;
  flex: 1;
  min-height: 0;
}
.history-expanded {
  padding: 8px 24px 16px;
}
.history-expanded p {
  font-size: 11px;
  color: var(--ui-muted);
}
.el-pagination {
  padding: 14px;
  justify-content: flex-end;
}
</style>

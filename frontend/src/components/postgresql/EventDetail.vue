<script setup lang="ts">
import type { DatabaseEvent } from "../../api/operations";
import {
  formatDate as time,
  formatDuration as duration,
} from "../../utils/database-workbench.mjs";
defineProps<{ modelValue: boolean; event?: DatabaseEvent }>();
const emit = defineEmits<{ "update:modelValue": [boolean] }>();
</script>
<template>
  <el-drawer
    :model-value="modelValue"
    title="原始日志详情"
    size="min(800px,90vw)"
    @update:model-value="emit('update:modelValue', $event)"
    ><template v-if="event"
      ><el-descriptions :column="2" border size="small"
        ><el-descriptions-item label="发生时间">{{
          time(event.eventTime)
        }}</el-descriptions-item
        ><el-descriptions-item label="数据库">{{
          event.dbName || "未记录"
        }}</el-descriptions-item
        ><el-descriptions-item label="账号 / PID"
          >{{ event.username }} / {{ event.pid }}</el-descriptions-item
        ><el-descriptions-item label="阶段 / 耗时"
          >{{ event.phase }} /
          {{ duration(event.durationMs) }}</el-descriptions-item
        ><el-descriptions-item label="客户端">{{
          event.client || "未记录"
        }}</el-descriptions-item
        ><el-descriptions-item label="SQLSTATE">{{
          event.sqlState || "—"
        }}</el-descriptions-item></el-descriptions
      >
      <h4>SQL</h4>
      <pre class="sql-code">{{ event.sql || "日志未提供 SQL" }}</pre>
      <template
        v-for="field in ['message', 'detail', 'hint', 'context'] as const"
        :key="field"
        ><h4 v-if="event[field]">{{ field }}</h4>
        <pre v-if="event[field]" class="sql-code">{{ event[field] }}</pre>
      </template>
      <h4>原始记录 · {{ event.sourcePath }}:{{ event.sourceOffset }}</h4>
      <pre class="sql-code">{{ event.raw }}</pre>
    </template></el-drawer
  >
</template>

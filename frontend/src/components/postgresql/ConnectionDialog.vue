<script setup lang="ts">
import { reactive, ref, watch } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  saveOpsDatabase,
  deleteOpsDatabase,
  detectDatabaseLogs,
  type OpsDatabase,
} from "../../api/operations";
import { listManagedServers, type ManagedServer } from "../../api/server-mgmt";
const props = defineProps<{ modelValue: boolean; connection?: OpsDatabase }>();
const emit = defineEmits<{
  "update:modelValue": [boolean];
  saved: [OpsDatabase];
  deleted: [number];
}>();
const draft = reactive<Partial<OpsDatabase>>({});
const busy = ref(false);
const detecting = ref(false);
const advanced = ref<string[]>([]);
const servers = ref<ManagedServer[]>([]);
const capability = ref<Record<string, string>>();
watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return;
    Object.keys(draft).forEach((key) => delete (draft as any)[key]);
    Object.assign(
      draft,
      props.connection
        ? { ...props.connection, password: "" }
        : {
            name: "",
            host: "",
            port: 5432,
            database: "",
            username: "",
            password: "",
            sslMode: "require",
            serverId: 0,
            logEnabled: false,
            logDirectory: "",
            logFormat: "csv",
            logTimezone: "Asia/Shanghai",
            slowThresholdMs: 5000,
            lookbackHours: 24,
          },
    );
    advanced.value = draft.logEnabled ? ["logs"] : [];
    capability.value = undefined;
    try {
      servers.value = await listManagedServers();
    } catch {
      servers.value = [];
    }
  },
);
async function save() {
  if (
    !draft.name?.trim() ||
    !draft.host?.trim() ||
    !draft.database?.trim() ||
    !draft.username?.trim()
  ) {
    ElMessage.warning("请填写连接名称、地址、数据库和账号");
    return;
  }
  busy.value = true;
  try {
    const row = await saveOpsDatabase(draft);
    emit("saved", row);
    emit("update:modelValue", false);
    ElMessage.success("连接已保存");
  } finally {
    busy.value = false;
  }
}
async function detect() {
  if (!draft.id) return;
  detecting.value = true;
  try {
    const c = await detectDatabaseLogs(draft.id);
    capability.value = c;
    Object.assign(draft, {
      logDirectory: c.resolved_directory,
      logFormat: c.format,
      logTimezone: c.log_timezone || "Asia/Shanghai",
      logPrefix: c.log_line_prefix,
    });
  } finally {
    detecting.value = false;
  }
}
async function remove() {
  if (!draft.id) return;
  try {
    await ElMessageBox.confirm(
      `删除连接“${draft.name}”？已有日志、执行记录和巡检报告会保留。`,
      "删除连接",
      { type: "warning", confirmButtonText: "删除" },
    );
  } catch {
    return;
  }
  busy.value = true;
  try {
    await deleteOpsDatabase(draft.id);
    emit("deleted", draft.id);
    emit("update:modelValue", false);
  } finally {
    busy.value = false;
  }
}
</script>
<template>
  <el-dialog
    :model-value="modelValue"
    :title="connection ? '管理连接' : '新建 PostgreSQL 连接'"
    width="700px"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form
      label-position="top"
      size="default"
      class="connection-form"
      @submit.prevent="save"
    >
      <el-form-item label="连接名称" required
        ><el-input
          v-model="draft.name"
          placeholder="例如：客户生产库 · 业务查询"
      /></el-form-item>
      <div class="form-columns">
        <el-form-item label="数据库地址" required
          ><el-input
            v-model="draft.host"
            placeholder="IP 或域名" /></el-form-item
        ><el-form-item label="端口"
          ><el-input-number v-model="draft.port" :min="1" :max="65535"
        /></el-form-item>
      </div>
      <div class="form-columns">
        <el-form-item label="数据库" required
          ><el-input v-model="draft.database" /></el-form-item
        ><el-form-item label="SSL 模式"
          ><el-select v-model="draft.sslMode"
            ><el-option
              v-for="mode in ['require', 'verify-ca', 'verify-full', 'disable']"
              :key="mode"
              :value="mode"
              :label="mode" /></el-select
        ></el-form-item>
      </div>
      <div class="form-columns">
        <el-form-item label="账号" required
          ><el-input
            v-model="draft.username"
            autocomplete="off" /></el-form-item
        ><el-form-item label="密码"
          ><el-input
            v-model="draft.password"
            type="password"
            show-password
            autocomplete="new-password"
            :placeholder="draft.id ? '留空保留已保存密码' : '数据库密码'"
        /></el-form-item>
      </div>
      <p class="connection-hint">
        每个连接对应一个明确的数据库。SQL 查询、对象浏览与诊断均使用此连接；SQL
        变更发布使用发布任务自己的目标配置。
      </p>
      <el-collapse v-model="advanced"
        ><el-collapse-item name="logs" title="慢 SQL 与锁事件的日志接入">
          <div class="log-heading">
            <el-switch
              v-model="draft.logEnabled"
              active-text="启用增量接入"
            /><el-button
              v-if="draft.id"
              size="small"
              :loading="detecting"
              @click="detect"
              >检测已保存连接的日志配置</el-button
            >
          </div>
          <el-form-item label="日志所在服务器"
            ><el-select v-model="draft.serverId" placeholder="选择已登记服务器"
              ><el-option
                v-for="s in servers"
                :key="s.id"
                :value="s.id"
                :label="`${s.remark || s.host} · ${s.host}`" /></el-select
          ></el-form-item>
          <el-form-item label="日志目录"
            ><el-input
              v-model="draft.logDirectory"
              placeholder="服务器上的 PostgreSQL 日志绝对路径"
          /></el-form-item>
          <div class="form-columns">
            <el-form-item label="日志格式"
              ><el-select v-model="draft.logFormat"
                ><el-option value="csv" label="CSV 日志" /><el-option
                  value="text"
                  label="文本日志" /></el-select></el-form-item
            ><el-form-item label="日志时区"
              ><el-input v-model="draft.logTimezone" /></el-form-item
            ><el-form-item label="平台采集阈值（ms）"
              ><el-input-number
                v-model="draft.slowThresholdMs"
                :min="1"
                :max="3600000" /></el-form-item
            ><el-form-item label="首次回溯（小时）"
              ><el-input-number
                v-model="draft.lookbackHours"
                :min="1"
                :max="720"
            /></el-form-item>
          </div>
          <el-form-item
            v-if="draft.logFormat === 'text'"
            label="log_line_prefix"
            ><el-input v-model="draft.logPrefix"
          /></el-form-item>
          <p class="connection-hint">
            沿用数据库已有日志配置，平台不修改 PostgreSQL 参数。日志约每 30
            秒同步、保留 30 天；修改首次回溯不会重置已有读取位置。
          </p>
          <el-alert
            v-if="capability"
            :title="`logging_collector=${capability.logging_collector} · log_destination=${capability.log_destination} · log_min_duration_statement=${capability.log_min_duration_statement} ms · log_lock_waits=${capability.log_lock_waits}`"
            type="info"
            :closable="false"
          /> </el-collapse-item
      ></el-collapse>
    </el-form>
    <template #footer
      ><div class="connection-footer">
        <el-button
          v-if="connection"
          text
          type="danger"
          :disabled="busy"
          @click="remove"
          >删除连接</el-button
        ><span /><el-button @click="emit('update:modelValue', false)"
          >取消</el-button
        ><el-button type="primary" :loading="busy" @click="save"
          >保存连接</el-button
        >
      </div></template
    >
  </el-dialog>
</template>
<style scoped>
.form-columns {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0 20px;
}
.connection-form :deep(.el-select),
.connection-form :deep(.el-input-number) {
  width: 100%;
}
.connection-hint {
  font-size: 12px;
  color: var(--ui-muted);
  line-height: 1.8;
  margin: 0 0 18px;
}
.connection-footer,
.log-heading {
  display: flex;
  align-items: center;
  gap: 12px;
}
.connection-footer > span {
  flex: 1;
}
.log-heading {
  justify-content: space-between;
  margin: 4px 0 16px;
}
.connection-form :deep(.el-collapse) {
  border-bottom: 0;
}
</style>

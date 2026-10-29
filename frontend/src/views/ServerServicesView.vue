<script setup lang="ts">
import { ref, reactive, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import { getManagedServer, type ManagedServer } from "../api/server-mgmt";
import {
  listServices,
  saveService,
  deleteService,
  getServiceStatus,
  getServiceLogs,
  serviceAction,
  getServiceOperations,
  type ManagedService,
  type ManagedServiceStatus,
  type ServiceOperation,
} from "../api/server-services";
const route = useRoute(),
  router = useRouter(),
  hostID = Number(route.params.id),
  host = ref<ManagedServer>(),
  rows = ref<ManagedService[]>([]),
  canCreate = ref(false),
  loading = ref(false),
  saving = ref(false),
  dialog = ref(false),
  logs = ref(false),
  logText = ref(""),
  drawer = ref(false),
  selected = ref<ManagedService>(),
  operations = ref<ServiceOperation[]>([]);
const statuses = reactive<Record<number, ManagedServiceStatus>>({}),
  pending = reactive<Record<number, boolean>>({});
const empty = () => ({
  id: 0,
  name: "",
  mode: "script" as "script" | "systemd",
  unit: "",
  runUser: "",
  directory: "",
  startScript: "",
  identity: "",
  logPath: "",
  operators: [] as string[],
});
const form = ref(empty()),
  operatorsText = ref("");
const statusLabel = (s: string) =>
  (
    ({
      RUNNING: "运行中",
      STOPPED: "未运行",
      UNKNOWN: "状态待核实",
      QUEUED: "等待操作",
      SUCCEEDED: "操作成功",
      FAILED: "操作失败",
    }) as Record<string, string>
  )[s] || s;
async function load() {
  loading.value = true;
  try {
    const [h, r] = await Promise.all([
      getManagedServer(hostID),
      listServices(hostID),
    ]);
    host.value = h;
    rows.value = r.services;
    canCreate.value = r.canCreate;
  } finally {
    loading.value = false;
  }
}
function edit(row?: ManagedService) {
  form.value = row
    ? { ...empty(), ...row, operators: JSON.parse(row.operatorsJSON || "[]") }
    : empty();
  operatorsText.value = form.value.operators.join(", ");
  dialog.value = true;
}
async function save() {
  saving.value = true;
  try {
    form.value.operators = operatorsText.value
      .split(/[,，\n]/)
      .map((v) => v.trim())
      .filter(Boolean);
    await saveService(hostID, form.value);
    dialog.value = false;
    await load();
    ElMessage.success("服务配置已保存");
  } finally {
    saving.value = false;
  }
}
async function refreshStatus(row: ManagedService) {
  pending[row.id] = true;
  try {
    statuses[row.id] = await getServiceStatus(hostID, row.id);
  } finally {
    pending[row.id] = false;
  }
}
async function remove(row: ManagedService) {
  await ElMessageBox.confirm(`删除 ${row.name} 的纳管配置？`, "删除配置", {
    type: "warning",
  });
  await deleteService(hostID, row.id);
  await load();
}
async function showLogs(row: ManagedService) {
  pending[row.id] = true;
  try {
    logText.value = (await getServiceLogs(hostID, row.id)).text;
    logs.value = true;
  } finally {
    pending[row.id] = false;
  }
}
async function showOperations(row: ManagedService) {
  selected.value = row;
  operations.value = await getServiceOperations(hostID, row.id);
  drawer.value = true;
}
async function act(row: ManagedService, action: "start" | "restart") {
  if (action === "restart")
    await ElMessageBox.confirm(
      `重启 ${host.value?.host} 上的 ${row.name}？现有进程会先正常退出，再按已保存的配置启动。`,
      "重启服务",
      { type: "warning" },
    );
  pending[row.id] = true;
  try {
    const op = await serviceAction(hostID, row.id, action);
    selected.value = row;
    operations.value = [op];
    drawer.value = true;
    ElMessage.success("操作已提交，后台执行并核实服务状态");
  } finally {
    pending[row.id] = false;
  }
}
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(async () => {
  await load();
  timer = setInterval(async () => {
    const row = selected.value;
    if (
      !row ||
      !operations.value.some((o) => ["QUEUED", "RUNNING"].includes(o.status))
    )
      return;
    try {
      operations.value = await getServiceOperations(hostID, row.id);
      if (
        !operations.value.some((o) => ["QUEUED", "RUNNING"].includes(o.status))
      )
        await refreshStatus(row);
    } catch {
      /* next refresh retains the accepted operation */
    }
  }, 2500);
});
onUnmounted(() => {
  if (timer) clearInterval(timer);
});
</script>
<template>
  <div class="services-page">
    <header>
      <div>
        <el-button text @click="router.push('/servers')"
          >← 服务器管理</el-button
        >
        <h2>{{ host?.remark || host?.host }} · 服务管理</h2>
        <p>提供已纳管进程的启动、重启和日志入口，按需核实状态。</p>
      </div>
      <div>
        <el-button @click="load">刷新列表</el-button
        ><el-button v-if="canCreate" type="primary" @click="edit()"
          >纳管服务</el-button
        >
      </div>
    </header>
    <el-table
      :data="rows"
      v-loading="loading"
      empty-text="尚未纳管服务，可为 SkyWalking OAP、UI 等分别配置启动方式"
    >
      <el-table-column
        prop="name"
        label="服务"
        min-width="160"
      /><el-table-column label="运行方式" min-width="250"
        ><template #default="{ row }"
          ><div>{{ row.mode === "systemd" ? row.unit : row.startScript }}</div>
          <small>{{
            row.mode === "script"
              ? `${row.runUser} · ${row.directory}`
              : "systemd 服务管理"
          }}</small></template
        ></el-table-column
      >
      <el-table-column label="实际状态" min-width="150"
        ><template #default="{ row }"
          ><el-tag
            :type="
              statuses[row.id]?.status === 'RUNNING'
                ? 'success'
                : statuses[row.id]?.status === 'STOPPED'
                  ? 'warning'
                  : 'info'
            "
            >{{
              statuses[row.id]
                ? statusLabel(statuses[row.id]!.status)
                : "尚未检查"
            }}</el-tag
          ><small v-if="statuses[row.id]">{{
            new Date(statuses[row.id]!.checkedAt).toLocaleTimeString()
          }}</small></template
        ></el-table-column
      >
      <el-table-column label="操作" min-width="370"
        ><template #default="{ row }"
          ><el-button
            text
            :loading="pending[row.id]"
            @click="refreshStatus(row)"
            >核实状态</el-button
          ><el-button
            text
            type="primary"
            :disabled="!row.canOperate || pending[row.id]"
            @click="act(row, 'start')"
            >启动</el-button
          ><el-button
            text
            type="warning"
            :disabled="!row.canOperate || pending[row.id]"
            @click="act(row, 'restart')"
            >重启</el-button
          ><el-button
            text
            :disabled="!row.canOperate || pending[row.id]"
            @click="showLogs(row)"
            >日志</el-button
          ><el-button text @click="showOperations(row)">操作记录</el-button
          ><el-button v-if="row.canConfigure" text @click="edit(row)"
            >配置</el-button
          ><el-button
            v-if="row.canConfigure"
            text
            type="danger"
            @click="remove(row)"
            >删除</el-button
          ></template
        ></el-table-column
      >
    </el-table>
    <div
      v-for="r in rows.filter((v) => statuses[v.id]?.status === 'UNKNOWN')"
      :key="r.id"
      class="state-note"
    >
      {{ r.name }}：{{ statuses[r.id]?.message }}
    </div>
    <el-dialog
      v-model="dialog"
      :title="form.id ? '编辑纳管服务' : '纳管服务'"
      width="700px"
      ><el-form label-width="120px"
        ><el-form-item label="服务名称"
          ><el-input
            v-model="form.name"
            placeholder="例如：SkyWalking OAP"
            maxlength="128" /></el-form-item
        ><el-form-item label="管理方式"
          ><el-radio-group v-model="form.mode"
            ><el-radio-button value="script">已有启动脚本</el-radio-button
            ><el-radio-button value="systemd"
              >systemd</el-radio-button
            ></el-radio-group
          ></el-form-item
        >
        <el-form-item v-if="form.mode === 'systemd'" label="服务单元"
          ><el-input v-model="form.unit" placeholder="准确的 xxx.service"
        /></el-form-item>
        <template v-else
          ><el-form-item label="运行账号"
            ><el-input
              v-model="form.runUser"
              placeholder="该服务实际使用的 Linux 账号" /></el-form-item
          ><el-form-item label="工作目录"
            ><el-input
              v-model="form.directory"
              placeholder="例如 /opt/skywalking" /></el-form-item
          ><el-form-item label="启动脚本"
            ><el-input
              v-model="form.startScript"
              placeholder="该进程专用启动脚本的绝对路径" /></el-form-item
          ><el-form-item label="进程识别参数"
            ><el-input
              v-model="form.identity"
              placeholder="命令行中唯一、完整的主类名或绝对 jar 路径"
            /><small
              >按完整参数、运行账号、工作目录共同识别。OAP 与 UI
              是独立进程时分别纳管，使用各自的启动脚本。</small
            ></el-form-item
          ><el-form-item label="日志文件"
            ><el-input
              v-model="form.logPath"
              placeholder="服务日志的绝对路径" /></el-form-item
        ></template>
        <el-form-item label="可操作维护人员"
          ><el-input
            v-model="operatorsText"
            placeholder="填写平台登录账号，逗号分隔"
          /><small
            >配置人和管理员始终可操作；其他人员需在此授权。</small
          ></el-form-item
        ></el-form
      ><template #footer
        ><el-button @click="dialog = false">取消</el-button
        ><el-button type="primary" :loading="saving" @click="save"
          >保存服务</el-button
        ></template
      ></el-dialog
    >
    <el-dialog v-model="logs" title="最近 100 行服务日志" width="900px">
      <pre>{{ logText || "没有日志输出" }}</pre>
    </el-dialog>
    <el-drawer
      v-model="drawer"
      :title="`${selected?.name || ''} · 操作记录`"
      size="580px"
      ><div v-for="o in operations" :key="o.id" class="operation">
        <div>
          <strong
            >{{ o.action === "start" ? "启动" : "重启" }} ·
            {{ statusLabel(o.status) }}</strong
          ><small
            >{{ o.operator }} ·
            {{
              new Date(o.startedAt).toLocaleString("zh-CN", { hour12: false })
            }}</small
          >
        </div>
        <p>{{ o.message }}</p>
      </div>
      <el-empty v-if="!operations.length" description="暂无操作记录"
    /></el-drawer>
  </div>
</template>
<style scoped>
.services-page {
  background: #fff;
  padding: 24px;
  border: 1px solid #e1e7ee;
  border-radius: 8px;
}
.services-page header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 25px;
}
.services-page h2 {
  font-size: 20px;
  margin: 10px 0;
}
.services-page header p,
small {
  color: #8090a3;
  font-size: 12px;
  line-height: 1.8;
}
small {
  display: block;
}
.state-note {
  margin: 15px 0;
  color: #a46f2e;
  font-size: 13px;
}
pre {
  max-height: 65vh;
  overflow: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  background: #f5f7fb;
  padding: 18px;
  font: 12px/1.8 monospace;
}
.operation {
  border-bottom: 1px solid #e4e9ef;
  padding: 20px 0;
}
.operation p {
  font-size: 13px;
  line-height: 1.8;
  color: #60748e;
}
</style>

<script setup lang="ts">
import { ref, onMounted } from "vue";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  componentKinds,
  listComponents,
  saveComponent,
  deleteComponent,
  type ComponentConnection,
  type ComponentInput,
} from "../api/monitoring";
import { listManagedServers, type ManagedServer } from "../api/server-mgmt";
const rows = ref<ComponentConnection[]>([]),
  hosts = ref<ManagedServer[]>([]),
  loading = ref(false),
  saving = ref(false),
  dialog = ref(false);
const empty = (): ComponentInput => ({
  kind: "minio",
  name: "",
  endpoint: "",
  username: "",
  secret: "",
  clearSecret: false,
  serverId: 0,
  enabled: true,
  options: { alias: "prod" },
});
const form = ref<ComponentInput>(empty());
const label = (v: string) =>
  componentKinds.find((k) => k.value === v)?.label || v;
async function load() {
  loading.value = true;
  try {
    [rows.value, hosts.value] = await Promise.all([
      listComponents(),
      listManagedServers(),
    ]);
  } finally {
    loading.value = false;
  }
}
function edit(row?: ComponentConnection) {
  form.value = row
    ? {
        ...empty(),
        ...row,
        secret: "",
        options: JSON.parse(row.optionsJSON || "{}"),
      }
    : empty();
  dialog.value = true;
}
async function save() {
  saving.value = true;
  try {
    await saveComponent(form.value);
    dialog.value = false;
    form.value.secret = "";
    await load();
    ElMessage.success("组件连接已保存");
  } finally {
    saving.value = false;
  }
}
async function remove(row: ComponentConnection) {
  await ElMessageBox.confirm(
    `删除 ${row.name} 的连接配置？已生成报告继续保留。`,
    "删除连接",
    { type: "warning" },
  );
  await deleteComponent(row.id);
  await load();
}
onMounted(load);
</script>
<template>
  <section class="component-config">
    <div class="heading">
      <div>
        <h3>组件连接与监控</h3>
        <p>
          维护连接后按周期自动采集，监控页面和报告共用数据。PostgreSQL
          复用数据库工作台连接，环境 Kubernetes 自动接入。
        </p>
      </div>
      <el-button type="primary" @click="edit()">新增组件连接</el-button>
    </div>
    <el-table
      :data="rows"
      v-loading="loading"
      empty-text="暂无额外组件连接，可新增 MinIO、Elasticsearch、Redis 或 RabbitMQ"
    >
      <el-table-column label="组件" width="155"
        ><template #default="{ row }">{{
          label(row.kind)
        }}</template></el-table-column
      ><el-table-column prop="name" label="名称" min-width="150" />
      <el-table-column label="连接来源" min-width="220"
        ><template #default="{ row }">{{
          row.source === "environment"
            ? "复用现有环境配置"
            : row.endpoint || `主机 #${row.serverId} 上的 mc 别名`
        }}</template></el-table-column
      >
      <el-table-column label="纳入报告" width="100"
        ><template #default="{ row }"
          ><el-tag :type="row.enabled ? 'success' : 'info'">{{
            row.enabled ? "是" : "否"
          }}</el-tag></template
        ></el-table-column
      >
      <el-table-column label="维护" width="150"
        ><template #default="{ row }"
          ><span v-if="row.source === 'environment'" class="muted"
            >在上方环境配置维护</span
          ><template v-else
            ><el-button text @click="edit(row)">编辑</el-button
            ><el-button text type="danger" @click="remove(row)"
              >删除</el-button
            ></template
          ></template
        ></el-table-column
      >
    </el-table>
    <el-dialog
      v-model="dialog"
      :title="form.id ? '编辑组件连接' : '新增组件连接'"
      width="660px"
      @closed="form.secret = ''"
    >
      <el-form label-width="125px" @submit.prevent>
        <el-form-item label="组件类型"
          ><el-select v-model="form.kind" :disabled="!!form.id"
            ><el-option
              v-for="k in componentKinds.filter(
                (v) => v.value !== 'postgresql',
              )"
              :key="k.value"
              :value="k.value"
              :label="k.label" /></el-select
        ></el-form-item>
        <el-form-item label="名称"
          ><el-input
            v-model="form.name"
            maxlength="128"
            placeholder="例如：生产 Redis / 预生产 MinIO"
        /></el-form-item>
        <template v-if="form.kind === 'kubernetes'"
          ><el-form-item label="kubeconfig"
            ><el-input
              v-model="form.secret"
              type="textarea"
              :rows="6"
              :placeholder="
                form.id
                  ? '留空保留原 kubeconfig'
                  : '填写额外集群的 kubeconfig 内容'
              "
              autocomplete="off" /></el-form-item
        ></template>
        <template v-else>
          <el-form-item label="连接地址"
            ><el-input
              v-model="form.endpoint"
              :placeholder="
                form.kind === 'redis'
                  ? 'redis://192.168.1.10:6379'
                  : form.kind === 'rabbitmq'
                    ? 'http://192.168.1.10:15672'
                    : form.kind === 'elasticsearch'
                      ? 'http://192.168.1.10:9200'
                      : '可留空复用主机已有 mc 别名'
              "
          /></el-form-item>
          <el-form-item :label="form.kind === 'minio' ? 'Access Key' : '账号'"
            ><el-input v-model="form.username" autocomplete="off"
          /></el-form-item>
          <el-form-item :label="form.kind === 'minio' ? 'Secret Key' : '密码'"
            ><el-input
              v-model="form.secret"
              type="password"
              show-password
              autocomplete="new-password"
              :placeholder="
                form.id ? '留空保留已保存凭据' : '可选，按实际认证配置填写'
              "
          /></el-form-item>
          <el-form-item v-if="form.id" label="清除已存凭据"
            ><el-switch v-model="form.clearSecret"
          /></el-form-item>
        </template>
        <el-form-item label="关联主机"
          ><el-select v-model="form.serverId" filterable style="width: 100%"
            ><el-option label="不关联主机" :value="0" /><el-option
              v-for="h in hosts"
              :key="h.id"
              :value="h.id"
              :label="`${h.remark || h.host} · ${h.host}`" /></el-select
          ><small
            >MinIO 需关联可执行 mc 的主机；其他组件用于关联主机资源和低频专项。</small
          ></el-form-item
        >
        <template v-if="form.kind === 'minio'"
          ><el-form-item v-if="!form.endpoint" label="已有 mc 别名"
            ><el-input
              v-model="form.options.alias"
              placeholder="prod" /></el-form-item
          ><el-form-item label="已有每日记录"
            ><el-input
              v-model="form.options.dailyFile"
              placeholder="可选，主机上的采样文件绝对路径" /></el-form-item
          ><el-form-item label="备份目录"
            ><el-input
              v-model="form.options.backupDirectory"
              placeholder="可选，主机上的实际备份目录" /></el-form-item
        ></template>
        <el-form-item label="生成报告时纳入"
          ><el-switch v-model="form.enabled"
        /></el-form-item> </el-form
      ><template #footer
        ><el-button @click="dialog = false">取消</el-button
        ><el-button type="primary" :loading="saving" @click="save"
          >保存连接</el-button
        ></template
      >
    </el-dialog>
  </section>
</template>
<style scoped>
.component-config {
  padding: 22px;
  background: var(--el-bg-color, #fff);
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  margin-top: 20px;
}
.heading {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 16px;
}
.heading h3 {
  margin: 0 0 8px;
  font-size: 16px;
}
.heading p,
small,
.muted {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.7;
}
small {
  display: block;
}
</style>

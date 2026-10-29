<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  listInspectionRuns,
  deleteInspection,
  downloadInspection,
  type InspectionRun,
  listInspectionPlans,
  deleteInspectionPlan,
  type InspectionPlan,
} from "../api/operations";
import request from "../api/request";
import { createConfiguredInspection } from "../api/monitoring";
import {
  inspectionExecutionLabel,
  inspectionCoverageText,
  inspectionFindings,
} from "../utils/inspection-status";
const route = useRoute(),
  router = useRouter(),
  runs = ref<InspectionRun[]>([]),
  loading = ref(false),
  saving = ref(false),
  error = ref(""),
  dialog = ref(false),
  name = ref("系统巡检报告"),
  page = ref(1),
  total = ref(0),
  status = ref("");
const plans = ref<InspectionPlan[]>([]),
  plansDialog = ref(false),
  planBusy = ref(0);
async function loadPlans() {
  plans.value = await listInspectionPlans();
}
async function togglePlan(row: InspectionPlan) {
  planBusy.value = row.id;
  try {
    await request.patch(`/operations/inspection-plans/${row.id}/enabled`, {
      enabled: !row.enabled,
    });
    await loadPlans();
  } finally {
    planBusy.value = 0;
  }
}
async function removePlan(row: InspectionPlan) {
  await ElMessageBox.confirm(
    `删除定时任务「${row.name}」？历史报告保留。`,
    "删除定时任务",
    { type: "warning" },
  );
  await deleteInspectionPlan(row.id);
  await loadPlans();
}
const time = (v: string) =>
  v ? new Date(v).toLocaleString("zh-CN", { hour12: false }) : "—";
async function load() {
  if (loading.value) return;
  loading.value = true;
  try {
    const r = await listInspectionRuns(page.value, 20, status.value);
    runs.value = r.list;
    total.value = r.total;
    error.value = "";
  } catch (e) {
    error.value = (e as Error).message;
  } finally {
    loading.value = false;
  }
}
async function create() {
  saving.value = true;
  try {
    const targets = route.query.host
      ? [{ type: "host", id: Number(route.query.host) }]
      : undefined;
    const r = await createConfiguredInspection(name.value, targets);
    dialog.value = false;
    router.push(`/inspections/${r.id}`);
  } finally {
    saving.value = false;
  }
}
async function remove(row: InspectionRun) {
  await ElMessageBox.confirm(
    `删除报告「${row.name}」及其冻结结果？`,
    "删除报告",
    { type: "warning" },
  );
  await deleteInspection(row.id);
  await load();
}
async function download(id: number) {
  try {
    await downloadInspection(id);
  } catch (e) {
    ElMessage.error((e as Error).message);
  }
}
let timer: ReturnType<typeof setInterval> | undefined;
onMounted(async () => {
  await load();
  await loadPlans();
  if (route.query.host) dialog.value = true;
  timer = setInterval(() => {
    if (runs.value.some((v) => ["QUEUED", "RUNNING"].includes(v.status)))
      void load();
  }, 5000);
});
onUnmounted(() => {
  if (timer) clearInterval(timer);
});
</script>
<template>
  <div class="inspection-reports">
    <div class="report-heading">
      <div>
        <h3>巡检报告</h3>
        <p>生成时现场采集已纳管服务器与已启用组件；以本次结果形成独立报告。</p>
      </div>
      <div>
        <el-button v-if="plans.length" @click="plansDialog = true"
          >已有定时任务（{{ plans.length }}）</el-button
        ><el-button type="primary" @click="dialog = true"
          >生成巡检报告</el-button
        >
      </div>
    </div>
    <el-alert v-if="error" :title="error" type="error" :closable="false" />
    <div class="filter">
      <el-select
        v-model="status"
        placeholder="全部状态"
        clearable
        style="width: 160px"
        @change="
          page = 1;
          load();
        "
        ><el-option
          v-for="s in [
            'QUEUED',
            'RUNNING',
            'ENDED',
            'FAILED',
            'CANCELED',
            'UNKNOWN',
          ]"
          :key="s"
          :value="s"
          :label="inspectionExecutionLabel(s)" /></el-select
      ><el-button @click="load">刷新列表</el-button
      ><span>临时检查数据保留 48 小时；正式报告独立存档</span>
    </div>
    <el-table
      :data="runs"
      v-loading="loading"
      empty-text="暂无巡检报告，点击上方生成巡检报告"
    >
      <el-table-column label="报告名称" min-width="190"
        ><template #default="{ row }"
          ><el-link
            type="primary"
            @click="router.push(`/inspections/${row.id}`)"
            >{{ row.name }}</el-link
          ><small>#{{ row.id }} · {{ row.operator }}</small></template
        ></el-table-column
      >
      <el-table-column label="执行状态" width="150"
        ><template #default="{ row }"
          >{{ row.executionLabel || inspectionExecutionLabel(row.status)
          }}<small v-if="['RUNNING', 'QUEUED'].includes(row.status)"
            >{{ row.completedTargets }} / {{ row.totalTargets }} 个对象</small
          ></template
        ></el-table-column
      >
      <el-table-column label="检查结论" min-width="250"
        ><template #default="{ row }"
          ><div class="result-counts">
            <span>需关注 {{ inspectionFindings(row).warning }}</span
            ><span>严重 {{ inspectionFindings(row).critical }}</span>
          </div>
          <el-link
            type="primary"
            @click="router.push(`/inspections/${row.id}`)"
            >{{ inspectionCoverageText(row) }}</el-link
          ></template
        ></el-table-column
      >
      <el-table-column label="生成时间" width="180"
        ><template #default="{ row }">{{
          time(row.createdAt)
        }}</template></el-table-column
      >
      <el-table-column label="报告" width="220"
        ><template #default="{ row }"
          ><el-button
            text
            :disabled="row.htmlStatus !== 'READY'"
            @click="router.push(`/inspections/${row.id}/report`)"
            >在线查看</el-button
          ><el-button
            text
            :disabled="row.pdfStatus !== 'READY'"
            @click="download(row.id)"
            >下载 PDF</el-button
          ><small v-if="row.pdfStatus === 'FAILED'" class="danger"
            >PDF 生成失败，详情可重试</small
          ></template
        ></el-table-column
      >
      <el-table-column width="65"
        ><template #default="{ row }"
          ><el-button
            v-if="!['QUEUED', 'RUNNING'].includes(row.status)"
            text
            type="danger"
            @click="remove(row)"
            >删除</el-button
          ></template
        ></el-table-column
      > </el-table
    ><el-pagination
      v-model:current-page="page"
      :total="total"
      :page-size="20"
      layout="total,prev,pager,next"
      @current-change="load"
    />
    <el-dialog v-model="plansDialog" title="已有定时任务" width="800px">
      <p>
        显示此前建立的定时任务。停用后不再产生新任务，已执行的任务和报告继续保留。
      </p>
      <el-table :data="plans"
        ><el-table-column prop="name" label="任务" /><el-table-column
          label="执行时间"
          ><template #default="{ row }"
            >每天 {{ row.dailyTime }} · {{ row.timezone }}</template
          ></el-table-column
        ><el-table-column prop="lastError" label="最近提示" /><el-table-column
          label="状态"
          ><template #default="{ row }">{{
            row.enabled ? "启用" : "停用"
          }}</template></el-table-column
        ><el-table-column label="操作"
          ><template #default="{ row }"
            ><el-button
              text
              :loading="planBusy === row.id"
              @click="togglePlan(row)"
              >{{ row.enabled ? "停用" : "启用" }}</el-button
            ><el-button text type="danger" @click="removePlan(row)"
              >删除</el-button
            ></template
          ></el-table-column
        ></el-table
      >
    </el-dialog>
    <el-dialog v-model="dialog" title="生成巡检报告" width="570px"
      ><el-form label-position="top"
        ><el-form-item label="报告名称"
          ><el-input v-model="name" maxlength="128"
        /></el-form-item>
        <div class="scope">
          <strong>本次采集范围</strong>
          <p v-if="route.query.host">
            服务器 #{{ route.query.host }}，自动执行 Linux
            基础检查并识别本机部署的组件。
          </p>
          <p v-else>
            全部已纳管服务器、数据库工作台已登记的
            PostgreSQL，以及环境配置中已启用的组件连接。
          </p>
          <p>
            自动采集当前状态，正常项简要汇总，异常及缺少结果的原因单独展示。已生成的历史报告保持原有结果。
          </p>
        </div></el-form
      ><template #footer
        ><el-button @click="dialog = false">取消</el-button
        ><el-button type="primary" :loading="saving" @click="create"
          >开始采集并生成</el-button
        ></template
      ></el-dialog
    >
  </div>
</template>
<style scoped>
.report-heading {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 24px;
}
.report-heading h3 {
  font-size: 17px;
  margin: 0;
}
.report-heading p {
  font-size: 12px;
  color: #7b8ba1;
  margin-bottom: 0;
}
.filter {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-bottom: 20px;
}
.filter span {
  font-size: 12px;
  color: #8794a8;
  margin-left: auto;
}
small {
  display: block;
  color: #8a97a8;
  font-size: 12px;
  line-height: 1.9;
}
.result-counts {
  display: flex;
  gap: 18px;
  font-size: 12px;
  margin-bottom: 5px;
}
.el-pagination {
  justify-content: flex-end;
  margin-top: 20px;
}
.scope {
  background: #f4f7fb;
  padding: 20px;
  color: #64758b;
  font-size: 13px;
  line-height: 1.9;
}
.scope strong {
  color: #354a65;
}
.danger {
  color: #cc5050;
}
</style>

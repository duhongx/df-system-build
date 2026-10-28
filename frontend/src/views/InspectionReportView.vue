<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import {
  getInspection,
  getInspectionReportHTML,
  downloadInspection,
  type InspectionRun,
  type InspectionCoverage,
} from "../api/operations";
import InspectionCoveragePanel from "../components/InspectionCoveragePanel.vue";
const route = useRoute(),
  router = useRouter(),
  id = computed(() => Number(route.params.id));
const run = ref<InspectionRun>(),
  coverage = ref<InspectionCoverage>(),
  html = ref(""),
  error = ref(""),
  loading = ref(false),
  downloading = ref(false);
let controller: AbortController | undefined;
async function load() {
  controller?.abort();
  const current = new AbortController();
  controller = current;
  loading.value = true;
  error.value = "";
  html.value = "";
  run.value = undefined;
  coverage.value = undefined;
  try {
    const data = await getInspection(id.value);
    if (current.signal.aborted) return;
    run.value = data.run;
    coverage.value = data.coverage;
    if (data.run.htmlStatus !== "READY")
      throw new Error(
        data.run.reportError || "在线报告尚未生成，请在任务详情查看进度",
      );
    const content = await getInspectionReportHTML(id.value, current.signal);
    if (!current.signal.aborted) html.value = content;
  } catch (e: any) {
    if (!current.signal.aborted) error.value = e.message;
  } finally {
    if (!current.signal.aborted) loading.value = false;
  }
}
async function download() {
  downloading.value = true;
  try {
    await downloadInspection(id.value);
  } catch (e: any) {
    ElMessage.error(e.message);
  } finally {
    downloading.value = false;
  }
}
watch(() => route.params.id, load, { immediate: true });
onBeforeUnmount(() => controller?.abort());
</script>
<template>
  <div class="inspection-report-view">
    <header class="report-heading">
      <div>
        <el-button text @click="router.push('/inspections')"
          >← 巡检报告</el-button
        >
        <h3>
          系统巡检报告 <small>#{{ id }}</small>
        </h3>
        <p v-if="run">
          任务名称：{{ run.name }} ·
          {{ coverage?.executionLabel || "正在读取执行状态" }}
        </p>
      </div>
      <div class="report-actions">
        <el-button @click="router.push(`/inspections/${id}`)"
          >任务详情</el-button
        ><el-button :loading="loading" @click="load">刷新</el-button
        ><el-button
          type="primary"
          :disabled="run?.pdfStatus !== 'READY'"
          :loading="downloading"
          @click="download"
          >下载 PDF</el-button
        >
      </div>
    </header>
    <el-alert
      v-if="error"
      type="error"
      :title="error"
      :closable="false"
      show-icon
    />
    <InspectionCoveragePanel v-if="run" :run="run" :coverage="coverage" />
    <div v-loading="loading" class="report-frame">
      <iframe
        v-if="html"
        title="系统巡检报告正文"
        :srcdoc="html"
        sandbox=""
        referrerpolicy="no-referrer"
      /><el-empty
        v-else-if="!loading"
        description="报告内容暂不可用，请查看任务详情或刷新重试"
      />
    </div>
  </div>
</template>
<style scoped>
.inspection-report-view {
  display: flex;
  flex-direction: column;
  gap: 16px;
  min-height: calc(100vh - 160px);
}
.report-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.report-heading h3 {
  margin: 5px 0;
  font-size: 18px;
}
.report-heading small,
.report-heading p {
  font-size: 12px;
  color: var(--ui-muted, #78899c);
}
.report-heading p {
  margin: 8px 0;
}
.report-actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.report-actions .el-button + .el-button {
  margin-left: 0;
}
.report-frame {
  min-height: 500px;
  background: white;
  border: 1px solid var(--ui-border, #e2e8f0);
  border-radius: 7px;
  overflow: hidden;
}
.report-frame iframe {
  display: block;
  width: 100%;
  height: 80vh;
  min-height: 600px;
  border: 0;
}
</style>

<script setup lang="ts">
import type { InspectionCoverage, InspectionRun } from "../api/operations";
import { inspectionCoverageText } from "../utils/inspection-status";
defineProps<{ run: InspectionRun; coverage?: InspectionCoverage }>();
</script>
<template>
  <section class="coverage-panel">
    <div class="coverage-heading">
      <strong>检查覆盖情况</strong
      ><span>{{
        inspectionCoverageText(run, coverage?.summary || undefined)
      }}</span>
    </div>
    <p v-if="coverage?.legacy" class="coverage-note">
      此历史任务未保存逐项报告统计，以下列出已保存的采集缺口。
    </p>
    <p v-if="coverage?.uncertainTargets" class="coverage-note warning">
      {{
        coverage.uncertainTargets
      }}
      个对象的远端执行状态尚未核实，不能认定检查已经完成。
    </p>
    <p v-if="coverage?.failedTargets" class="coverage-note warning">
      {{ coverage.failedTargets }} 个对象执行失败，详见下方原因。
    </p>
    <template v-if="coverage?.issues.length">
      <p class="coverage-note">
        下面逐项列出未取得结果和未执行的内容；它们与已发现的运行告警分别统计。
      </p>
      <el-table :data="coverage.issues" size="small" border>
        <el-table-column prop="code" label="项目编号" width="125" />
        <el-table-column prop="name" label="检查项目" min-width="175" />
        <el-table-column prop="object" label="检查对象" min-width="140"
          ><template #default="{ row }">{{
            row.object || "本次未分配对象"
          }}</template></el-table-column
        >
        <el-table-column label="结果" width="130"
          ><template #default="{ row }"
            ><el-tag type="warning" size="small">{{
              row.status === "NOT_RUN" ? "未执行" : "未取得完整结果"
            }}</el-tag></template
          ></el-table-column
        >
        <el-table-column label="具体原因" min-width="320"
          ><template #default="{ row }"
            ><span class="issue-reason">{{ row.reason }}</span></template
          ></el-table-column
        >
      </el-table>
    </template>
    <p
      v-else-if="
        coverage?.summary &&
        coverage.summary.completed === coverage.summary.planned &&
        !coverage.uncertainTargets &&
        !['QUEUED', 'RUNNING'].includes(run.status)
      "
      class="coverage-note"
    >
      本次范围内的检查结果已完整取得。运行情况请查看报告结论与异常项目。
    </p>
    <p
      v-else-if="coverage?.legacy && run.errorCount"
      class="coverage-note warning"
    >
      历史任务记录了采集缺口，但没有保存可还原的逐项原因，请查看对象详情。
    </p>
    <p v-else-if="!coverage" class="coverage-note">
      检查覆盖情况暂未取得，请刷新重试。
    </p>
  </section>
</template>
<style scoped>
.coverage-panel {
  padding: 18px 20px;
  background: var(--ui-panel, #fff);
  border: 1px solid var(--ui-border, #e2e8f0);
  border-radius: 7px;
}
.coverage-heading {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 13px;
}
.coverage-heading > span {
  font-size: 12px;
  color: var(--ui-muted, #66758b);
}
.coverage-note {
  font-size: 12px;
  line-height: 1.8;
  color: var(--ui-muted, #66758b);
  margin: 10px 0;
}
.issue-reason {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  line-height: 1.7;
}
.warning {
  color: var(--el-color-warning);
}
</style>

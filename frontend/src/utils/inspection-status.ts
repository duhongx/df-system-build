import type { InspectionRun, InspectionSummary } from "../api/operations";

export function inspectionExecutionLabel(status: string) {
  return (
    (
      {
        QUEUED: "等待执行",
        RUNNING: "正在检查",
        ENDED: "检查已结束",
        COMPLETED: "检查已结束",
        PARTIAL: "检查已结束",
        FAILED: "执行失败",
        CANCELED: "已取消",
        UNKNOWN: "远端执行状态待核实",
      } as Record<string, string>
    )[status] || status
  );
}

export function inspectionCoverageText(
  run: InspectionRun,
  summary = run.reportSummary,
) {
  if (summary) {
    const missing = Math.max(0, summary.planned - summary.completed);
    return (
      `已取得 ${summary.completed}/${summary.planned} 项结果` +
      (missing ? `，${missing} 项未完整取得` : "") +
      (summary.notRun ? `；另有 ${summary.notRun} 项未执行` : "")
    );
  }
  if (run.status === "QUEUED" || run.status === "RUNNING")
    return "检查结果持续汇总中";
  return run.errorCount
    ? `${run.errorCount} 项采集缺口，查看具体原因`
    : "历史任务，查看已保存的检查结果";
}

export function inspectionFindings(
  run: InspectionRun,
  summary?: InspectionSummary | null,
) {
  const value = summary || run.reportSummary;
  return {
    warning: value?.warning ?? run.warningCount,
    critical: value?.critical ?? run.criticalCount,
    recorded: value?.recorded ?? 0,
  };
}

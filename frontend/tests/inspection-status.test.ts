import { describe, expect, it } from "vitest";
import type { InspectionRun, InspectionSummary } from "../src/api/operations";
import {
  inspectionCoverageText,
  inspectionExecutionLabel,
  inspectionFindings,
} from "../src/utils/inspection-status";
const summary = {
  planned: 38,
  completed: 27,
  unknown: 11,
  notRun: 1,
  notApplicable: 0,
  recorded: 19,
  normal: 2,
  warning: 6,
  critical: 0,
} satisfies InspectionSummary;
const run = {
  status: "PARTIAL",
  errorCount: 99,
  warningCount: 7,
  criticalCount: 0,
  reportSummary: summary,
} as InspectionRun;
describe("inspection status meaning", () => {
  it("separates the end of execution from missing results and report failures", () => {
    expect(inspectionExecutionLabel("PARTIAL")).toBe("检查已结束");
    expect(inspectionExecutionLabel("UNKNOWN")).toBe("远端执行状态待核实");
    expect(inspectionCoverageText(run)).toBe(
      "已取得 27/38 项结果，11 项未完整取得；另有 1 项未执行",
    );
    expect(
      inspectionCoverageText({
        ...run,
        errorCount: 0,
        pdfStatus: "FAILED",
        reportSummary: { ...summary, completed: 38, notRun: 0, unknown: 0 },
      }),
    ).toBe("已取得 38/38 项结果");
  });
  it("uses frozen report statistics and labels old tasks without inventing coverage", () => {
    expect(inspectionFindings(run)).toEqual({
      warning: 6,
      critical: 0,
      recorded: 19,
    });
    expect(
      inspectionCoverageText({
        ...run,
        reportSummary: undefined,
        errorCount: 1,
      }),
    ).toBe("1 项采集缺口，查看具体原因");
    expect(
      inspectionCoverageText({
        ...run,
        reportSummary: undefined,
        errorCount: 0,
      }),
    ).toContain("历史任务");
  });
});

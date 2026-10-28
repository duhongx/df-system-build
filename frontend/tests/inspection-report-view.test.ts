import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createApp, h, type App } from "vue";
import InspectionReportView from "../src/views/InspectionReportView.vue";

const mocks = vi.hoisted(() => ({
  getInspection: vi.fn(),
  getInspectionReportHTML: vi.fn(),
  downloadInspection: vi.fn(),
  push: vi.fn(),
}));
vi.mock("../src/api/operations", () => mocks);
vi.mock("vue-router", () => ({
  useRoute: () => ({ params: { id: "3" } }),
  useRouter: () => ({ push: mocks.push }),
}));
vi.mock("../src/components/InspectionCoveragePanel.vue", () => ({
  default: { render: () => null },
}));
vi.mock("element-plus", () => ({ ElMessage: { error: vi.fn() } }));

let app: App | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  mocks.getInspection.mockResolvedValue({
    run: {
      id: 3,
      name: "保存的任务名称",
      htmlStatus: "READY",
      pdfStatus: "READY",
    },
    coverage: { executionLabel: "检查已结束" },
  });
  mocks.getInspectionReportHTML.mockResolvedValue(
    "<!doctype html><h1>真实报告内容</h1>",
  );
});
afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.replaceChildren();
});
function mount() {
  const host = document.createElement("div");
  document.body.append(host);
  app = createApp(InspectionReportView);
  for (const name of ["ElButton", "ElAlert", "ElEmpty"])
    app.component(name, {
      setup(_, { slots }) {
        return () => h("div", slots.default?.());
      },
    });
  app.directive("loading", () => {});
  app.mount(host);
  return host;
}
it("opens authenticated report content inside an isolated frame without a download or token URL", async () => {
  const host = mount();
  await vi.waitFor(() => expect(host.querySelector("iframe")).not.toBeNull());
  const frame = host.querySelector("iframe")!;
  expect(mocks.getInspectionReportHTML).toHaveBeenCalledWith(
    3,
    expect.any(AbortSignal),
  );
  expect(frame.getAttribute("sandbox")).toBe("");
  expect(frame.getAttribute("referrerpolicy")).toBe("no-referrer");
  expect(frame.getAttribute("srcdoc")).toContain("真实报告内容");
  expect(frame.hasAttribute("src")).toBe(false);
  expect(mocks.downloadInspection).not.toHaveBeenCalled();
});
it("keeps report retrieval errors visible instead of displaying a failed response as report content", async () => {
  mocks.getInspectionReportHTML.mockRejectedValue(
    new Error("在线报告读取失败"),
  );
  const host = mount();
  await vi.waitFor(() =>
    expect(host.querySelector('[title="在线报告读取失败"]')).not.toBeNull(),
  );
  expect(host.querySelector("iframe")).toBeNull();
  expect(mocks.downloadInspection).not.toHaveBeenCalled();
});

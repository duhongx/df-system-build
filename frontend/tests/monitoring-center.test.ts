import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createApp, h, reactive, type App } from "vue";
import ElementPlus from "element-plus";
import InspectionsView from "../src/views/InspectionsView.vue";
import ComponentInspectionPanel from "../src/components/ComponentInspectionPanel.vue";

const api = vi.hoisted(() => ({
  listInspectionRuns: vi.fn(),
  listInspectionPlans: vi.fn(),
  deleteInspectionPlan: vi.fn(),
  deleteInspection: vi.fn(),
  downloadInspection: vi.fn(),
  createConfiguredInspection: vi.fn(),
  listComponents: vi.fn(),
  getCollection: vi.fn(),
  collectComponent: vi.fn(),
  collectDatabaseInspection: vi.fn(),
  listOpsDatabases: vi.fn(),
  push: vi.fn(),
}));
vi.mock("../src/api/monitoring", () => api);
vi.mock("../src/api/operations", () => api);
vi.mock("vue-router", () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: api.push }),
}));
vi.mock("../src/api/server-mgmt", () => ({
  listManagedServers: vi.fn().mockResolvedValue([]),
}));
let app: App | undefined;
beforeEach(() => {
  vi.clearAllMocks();
  api.listInspectionRuns.mockResolvedValue({ list: [], total: 0 });
  api.listInspectionPlans.mockResolvedValue([]);
  api.createConfiguredInspection.mockResolvedValue({ id: 91 });
  api.listComponents.mockResolvedValue([
    {
      id: 7,
      kind: "redis",
      name: "生产 Redis",
      endpoint: "redis://redis:6379",
      revision: 2,
      enabled: true,
    },
    {
      id: 8,
      kind: "minio",
      name: "生产 MinIO",
      endpoint: "",
      revision: 1,
      enabled: true,
      source: "",
    },
  ]);
  api.getCollection.mockResolvedValue(null);
});
afterEach(() => {
  app?.unmount();
  app = undefined;
  document.body.replaceChildren();
});
function mount(render: () => ReturnType<typeof h>) {
  const el = document.createElement("div");
  document.body.append(el);
  app = createApp({ render });
  app.use(ElementPlus);
  app.mount(el);
  return el;
}
function button(root: Element, label: string) {
  const b = Array.from(root.querySelectorAll("button")).find(
    (v) => v.textContent?.trim() === label,
  );
  expect(b, `button ${label}`).toBeTruthy();
  b!.click();
}
it("generates an all-configured-sources report without selecting command profiles", async () => {
  const el = mount(() => h(InspectionsView));
  await vi.waitFor(() => expect(api.listInspectionRuns).toHaveBeenCalled());
  button(el, "生成巡检报告");
  await vi.waitFor(() =>
    expect(document.body.textContent).toContain("本次采集范围"),
  );
  button(document.body, "开始采集并生成");
  await vi.waitFor(() =>
    expect(api.createConfiguredInspection).toHaveBeenCalledWith(
      "系统巡检报告",
      undefined,
    ),
  );
  expect(api.push).toHaveBeenCalledWith("/inspections/91");
  expect(document.body.textContent).not.toContain("执行范围");
});
it("shows no recent data honestly and does not automatically collect metrics", async () => {
  const el = mount(() => h(ComponentInspectionPanel, { kind: "redis" }));
  await vi.waitFor(() =>
    expect(api.getCollection).toHaveBeenCalledWith("component", 7),
  );
  expect(el.textContent).toContain("近 48 小时没有检查结果");
  expect(api.collectComponent).not.toHaveBeenCalled();
  expect(api.collectDatabaseInspection).not.toHaveBeenCalled();
});
it("leaves loading state when no component connections exist", async () => {
  api.listComponents.mockResolvedValue([]);
  const el = mount(() => h(ComponentInspectionPanel, { kind: "redis" }));
  await vi.waitFor(() => expect(el.textContent).toContain("尚未登记此组件"));
  expect(api.getCollection).not.toHaveBeenCalled();
});
it("discards late results after switching component kinds", async () => {
  let resolveOld: (v: unknown) => void = () => {};
  api.getCollection.mockImplementation((_kind: string, id: number) =>
    id === 7
      ? new Promise((resolve) => {
          resolveOld = resolve;
        })
      : Promise.resolve({
          revision: 1,
          collectedAt: new Date().toISOString(),
          resultJSON: JSON.stringify({
            reportChecks: [
              {
                code: "minio",
                name: "MinIO 磁盘",
                status: "NORMAL",
                value: "1 个磁盘在线",
              },
            ],
          }),
        }),
  );
  const state = reactive({ kind: "redis" });
  const el = mount(() => h(ComponentInspectionPanel, { kind: state.kind }));
  await vi.waitFor(() =>
    expect(api.getCollection).toHaveBeenCalledWith("component", 7),
  );
  state.kind = "minio";
  await vi.waitFor(() => expect(el.textContent).toContain("1 个磁盘在线"));
  resolveOld({
    revision: 2,
    collectedAt: new Date().toISOString(),
    resultJSON: JSON.stringify({
      reportChecks: [
        {
          code: "old",
          name: "过期 Redis 结果",
          status: "NORMAL",
          value: "old",
        },
      ],
    }),
  });
  await new Promise((resolve) => setTimeout(resolve, 20));
  expect(el.textContent).not.toContain("过期 Redis 结果");
  expect(el.textContent).toContain("1 个磁盘在线");
});

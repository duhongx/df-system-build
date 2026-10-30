import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createApp, h, type App } from "vue";
import ElementPlus from "element-plus";
import ServerMonitorView from "../src/views/ServerMonitorView.vue";
import ComponentInspectionPanel from "../src/components/ComponentInspectionPanel.vue";

const api = vi.hoisted(() => ({ getManagedServer: vi.fn(), getHostMonitor: vi.fn(), getHostHistory: vi.fn(), configureHostMonitor: vi.fn(), collectHostMetric: vi.fn(), push: vi.fn(), listMonitoring: vi.fn(),getMonitoring:vi.fn(),getMonitoringHistory:vi.fn(),refreshMonitoring:vi.fn(),createConfiguredInspection:vi.fn() }));
vi.mock("../src/api/server-mgmt", () => api);
vi.mock("../src/api/operations", () => api);
vi.mock("../src/api/monitoring", () => api);
vi.mock("vue-router", () => ({ useRoute: () => ({ params: { kind: "hosts" }, query: {} }), useRouter: () => ({ push: api.push }) }));

const metric = () => ({ collectedAt: new Date().toISOString(), hostname: "fixture", os: "Linux", kernel: "6.1", cpuUsagePercent: 12, cpuWaitPercent: 1, cpuCores: 4, cpuPerCore: [10, 12, 15, 11], load1: 0.5, load5: 0.4, load15: 0.3, memUsagePercent: 34, memAvailableBytes: 4000000000, memTotalBytes: 8000000000, swapUsedBytes: 0, swapTotalBytes: 0, uptimeSeconds: 86400, networks: [{ name: "eth0", receivePerSecond: 1024, transmitPerSecond: 2048 }], disks: [], diskIO: [], warnings: [] });
let app: App | undefined;
const errors: unknown[] = [];
function mount(panel = false) {
  const root = document.createElement("div"); document.body.append(root);
  app = createApp({ render: () => panel ? h(ComponentInspectionPanel, {kind:"hosts"}) : h(ServerMonitorView, { serverId: 3, embedded: true }) });
  app.config.errorHandler = error => errors.push(error);
  app.use(ElementPlus); app.mount(root); return root;
}
beforeEach(() => {
  vi.clearAllMocks(); errors.length = 0;
  api.getManagedServer.mockResolvedValue({ id: 3, host: "192.0.2.3", remark: "测试主机" });
  api.getHostMonitor.mockResolvedValue({ config: { enabled: true, intervalSeconds: 60 }, metrics: metric(), collecting: false });
  api.getHostHistory.mockResolvedValue({ samples: [metric()], stepSeconds: 60 });
  api.listMonitoring.mockResolvedValue([{id:3,targetType:"host",kind:"hosts",name:"测试主机",endpoint:"192.0.2.3",enabled:true,status:"NORMAL",intervalSeconds:60,indicators:{cpuPercent:12}}]);
});
afterEach(() => { app?.unmount(); app = undefined; document.body.replaceChildren(); });

it("renders the actual Linux monitor with current metrics and charts without a manual collection", async () => {
  const root = mount();
  await vi.waitFor(() => expect(root.textContent).toContain("CPU 使用率"));
  expect(root.querySelector('svg[aria-label="CPU 使用与等待"]')).toBeTruthy();
  expect(root.textContent).toContain("34.0");
  expect(api.collectHostMetric).not.toHaveBeenCalled();
  expect(errors).toEqual([]);
  expect(root.textContent).not.toContain("指标地址");
  expect(root.textContent).not.toContain("采样区间");
});

it("still displays current metrics when host metadata cannot be read", async () => {
  api.getManagedServer.mockRejectedValue(new Error("主机信息读取失败"));
  const root = mount();
  await vi.waitFor(() => expect(root.textContent).toContain("CPU 使用率"));
  expect(api.getHostMonitor).toHaveBeenCalledWith(3);
  expect(errors).toEqual([]);
});

it("renders the full monitoring center host panel independently of component snapshot APIs", async () => {
  api.getMonitoring.mockRejectedValue(new Error("组件快照读取失败"));
  const root = mount(true);
  await vi.waitFor(() => expect(root.querySelector('svg[aria-label="CPU 使用与等待"]')).toBeTruthy());
  expect(root.textContent).toContain("CPU 使用率");
  expect(root.textContent).not.toContain("采集明细");
  expect(api.getMonitoring).not.toHaveBeenCalled();
  expect(api.collectHostMetric).not.toHaveBeenCalled();
  expect(errors).toEqual([]);
});

it("keeps current metrics when history fails and recovers automatically", async () => {
  vi.useFakeTimers();
  try {
    api.getHostHistory.mockRejectedValueOnce(new Error("历史数据读取失败"));
    const root = mount();
    await vi.waitFor(() => expect(root.textContent).toContain("历史数据读取失败"));
    expect(root.textContent).toContain("CPU 使用率");
    await vi.advanceTimersByTimeAsync(3000);
    expect(api.getHostHistory).toHaveBeenCalledTimes(2);
    expect(root.textContent).not.toContain("历史数据读取失败");
    expect(root.querySelector('svg[aria-label="CPU 使用与等待"]')).toBeTruthy();
    expect(errors).toEqual([]);
  } finally { vi.useRealTimers(); }
});

it("tolerates absent optional network and disk inventories", async () => {
  api.getHostMonitor.mockResolvedValue({ config: { enabled: true, intervalSeconds: 60 }, metrics: { ...metric(), networks: null, disks: null, diskIO: null, cpuPerCore: null }, collecting: false });
  const root = mount();
  await vi.waitFor(() => expect(root.textContent).toContain("CPU 使用率"));
  expect(errors).toEqual([]);
});

it("shows the actual disk used by I/O charts and allows switching devices", async () => {
  const m = { ...metric(), diskIO: ["sda", "sdb"].map((device, i) => ({ device, kind: "disk", mountpoints: i ? ["/data"] : ["/"], readPerSecond: 1024, writePerSecond: 2048, readAwaitMs: 1, writeAwaitMs: 2 })) };
  api.getHostMonitor.mockResolvedValue({ config: { enabled: true, intervalSeconds: 60 }, metrics: m, collecting: false });
  api.getHostHistory.mockResolvedValue({ samples: [m], stepSeconds: 60 });
  const root = mount();
  await vi.waitFor(() => expect(root.textContent).toContain("磁盘读写吞吐 · sda"));
  (root.querySelector('[aria-label="监控磁盘设备"]') as HTMLElement).click();
  await vi.waitFor(() => expect(document.querySelectorAll(".el-select-dropdown__item").length).toBeGreaterThan(0));
  const option = Array.from(document.querySelectorAll(".el-select-dropdown__item")).find(item => item.textContent?.includes("sdb")) as HTMLElement;
  expect(option).toBeTruthy(); option.click();
  await vi.waitFor(() => expect(root.textContent).toContain("磁盘读写吞吐 · sdb"));
  expect(root.textContent).toContain("磁盘读写时延 · sdb");
  expect(api.collectHostMetric).not.toHaveBeenCalled();
  expect(errors).toEqual([]);
});
